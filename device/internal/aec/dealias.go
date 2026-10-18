package aec

import (
	"encoding/binary"
	"log"
	"math"
	"sync"
)

// dealias rebuilds the hardware reference without its aliases.
//
// ch8 is the playback looped back into the 16kHz mic capture by keeping
// every third sample of the 48kHz output, with NO anti-aliasing filter:
// tones at 9, 12, 15 and 20kHz arrive in ch8 at 7, 4, 1 and 4kHz at full
// level, while the microphones (filtered by the codec) show nothing there
// (VVV, 2026-10-18). Whatever the output carries above 8kHz is therefore
// in-band in the reference and absent from the echo, and the canceller
// chases it: pink noise through the firmware measured 1dB of cancellation,
// the same capture with the reference rebuilt from the output 25.7dB.
// Music always has content up there, so this is what held barge-in back.
//
// The cure is the output itself. WriteFar sees every period written to
// ALSA; ch8 is an exact copy of every third sample of it (to 1 LSB), so
// matching the two gives the reference's position in the output to the
// sample, with none of the drift the software tap had to be steered for.
// The reference handed to the canceller is then the same samples
// decimated through a proper low-pass. When no match is found (the tap
// is not being fed, or the board's ch8 is something else) ch8 is used
// unchanged, as before.
type dealias struct {
	mu   sync.Mutex
	ring [dealiasRing]int16 // the output's left channel, 48kHz
	head int64              // samples ever written; ring index is head % dealiasRing

	locked bool
	pos    int64 // output index of the next ch8 sample, when locked
	locks  int   // times a lock was found, for the log
	fir    [dealiasTaps]float64
}

const (
	dealiasRing = 1 << 16 // 1.37s: the ALSA write-ahead is 171ms
	dealiasTaps = 95      // odd, so the filter's centre is a whole sample
	dealiasHalf = dealiasTaps / 2
	// dealiasTol: ch8 is the S16 output shifted into 24 bits; extracted
	// back at unity it differs by at most 1.
	dealiasTol = 2
	// A lock needs this many non-silent samples to agree, so a quiet
	// stretch cannot match by chance.
	dealiasMinMatch = 64
)

func newDealias() *dealias {
	d := &dealias{}
	// Windowed sinc (Blackman), cut at 7.2kHz: 0.15 of 48kHz.
	const fc = 7200.0 / 48000.0
	sum := 0.0
	for i := range d.fir {
		n := float64(i - dealiasHalf)
		h := 2 * fc
		if n != 0 {
			h = math.Sin(2*math.Pi*fc*n) / (math.Pi * n)
		}
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/(dealiasTaps-1)) +
			0.08*math.Cos(4*math.Pi*float64(i)/(dealiasTaps-1))
		d.fir[i] = h * w
		sum += d.fir[i]
	}
	for i := range d.fir {
		d.fir[i] /= sum
	}
	return d
}

// feed appends one output period (48kHz stereo S16LE). Speaker goroutine.
func (d *dealias) feed(period []byte) {
	if d == nil {
		return
	}
	n := len(period) / 4
	d.mu.Lock()
	for i := 0; i < n; i++ {
		d.ring[(d.head+int64(i))%dealiasRing] = int16(binary.LittleEndian.Uint16(period[i*4:]))
	}
	d.head += int64(n)
	d.mu.Unlock()
}

func (d *dealias) at(i int64) int16 { return d.ring[i%dealiasRing] }

// matches reports whether ref (16kHz S16LE) is every third output sample
// from output index p, with at least need non-silent samples agreeing. The
// filter's reach either side must be in the ring too.
func (d *dealias) matches(ref []byte, p int64, need int) bool {
	n := len(ref) / 2
	if p < d.head-dealiasRing+dealiasHalf || p+3*int64(n-1)+dealiasHalf >= d.head {
		return false
	}
	agree := 0
	for i := 0; i < n; i++ {
		r := int16(binary.LittleEndian.Uint16(ref[i*2:]))
		o := d.at(p + 3*int64(i))
		if diff := int(r) - int(o); diff > dealiasTol || diff < -dealiasTol {
			return false
		}
		if r > 64 || r < -64 {
			agree++
		}
	}
	return agree >= need
}

// search finds the output index ref starts at, newest first.
func (d *dealias) search(ref []byte) (int64, bool) {
	n := len(ref) / 2
	i0 := -1
	for i := 0; i < n; i++ {
		if r := int16(binary.LittleEndian.Uint16(ref[i*2:])); r > 64 || r < -64 {
			i0 = i
			break
		}
	}
	if i0 < 0 {
		return 0, false
	}
	r0 := int(int16(binary.LittleEndian.Uint16(ref[i0*2:])))
	lo := d.head - dealiasRing + dealiasHalf + 3*int64(i0)
	if lo < 3*int64(i0) {
		lo = 3 * int64(i0)
	}
	for j := d.head - 1; j >= lo; j-- {
		if diff := int(d.at(j)) - r0; diff > dealiasTol || diff < -dealiasTol {
			continue
		}
		if p := j - 3*int64(i0); d.matches(ref, p, dealiasMinMatch) {
			return p, true
		}
	}
	return 0, false
}

// clean returns ref rebuilt from the output, or ref itself when it cannot
// be placed. Mic goroutine.
func (d *dealias) clean(ref []byte) []byte {
	if d == nil {
		return ref
	}
	n := len(ref) / 2
	silent := true
	for i := 0; i < n; i++ {
		if r := int16(binary.LittleEndian.Uint16(ref[i*2:])); r != 0 {
			silent = false
			break
		}
	}
	span := d.place(ref, silent)
	if span == nil {
		return ref
	}
	// Filtered outside the lock: the speaker's write loop feeds the ring
	// and must not wait on this (#866).
	out := make([]byte, len(ref))
	for i := 0; i < n; i++ {
		acc := 0.0
		for k, h := range d.fir {
			acc += h * float64(span[3*i+k])
		}
		s := int16(max(-32768, min(32767, math.Round(acc))))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(s))
	}
	return out
}

// place finds ref in the output and returns the output samples the filter
// needs for it (from dealiasHalf before its first sample to dealiasHalf
// after its last), or nil to use ref as it is.
func (d *dealias) place(ref []byte, silent bool) []int16 {
	n := len(ref) / 2
	d.mu.Lock()
	defer d.mu.Unlock()
	if silent {
		// Nothing playing: an all-zero reference is already clean, and a
		// held lock carries on through it.
		if d.locked {
			d.pos += 3 * int64(n)
		}
		return nil
	}
	// Following a lock needs every sample to agree, not the loud ones a
	// fresh lock needs: a quiet passage would otherwise drop it.
	if !d.locked || !d.matches(ref, d.pos, 0) {
		was := d.locked
		p, ok := d.search(ref)
		if !ok {
			if was {
				log.Printf("[aec] de-aliased reference lost; using ch8 as captured")
			}
			d.locked = false
			return nil
		}
		if was {
			// A capture overrun or a stream restart; the size says which.
			log.Printf("[aec] de-aliased reference moved %+d output samples", p-d.pos)
		} else {
			log.Printf("[aec] de-aliased reference locked to the output (lock %d)", d.locks+1)
		}
		d.locked, d.pos = true, p
		d.locks++
	}
	start, end := d.pos-dealiasHalf, d.pos+3*int64(n-1)+dealiasHalf
	d.pos += 3 * int64(n)
	span := make([]int16, end-start+1)
	for i := range span {
		span[i] = d.at(start + int64(i))
	}
	return span
}
