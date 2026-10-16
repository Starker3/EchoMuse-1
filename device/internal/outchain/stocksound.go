package outchain

import "math"

// The stock-sound profile (#229, #247): a driver correction after the user's
// EQ, and three gentle compressor bands above the bass guard. A port of
// em_eq.correction_sos and em_mbc.BassGuard(upper_bands=True), held to the
// same vectors as the rest of the chain.

// Driver correction: four peaks and an S=1 high shelf fitted to the inverse
// of this speaker's response (em_eq.CORRECTION_PEAKS / CORRECTION_SHELF).
var correctionPeaks = [4][3]float64{ // Hz, dB, Q
	{175.0, 29.66, 0.281},
	{590.5, -13.84, 0.725},
	{2618.1, -20.00, 0.674},
	{2094.4, 5.24, 0.561},
}

var correctionShelf = [2]float64{9861.7, 12.0} // Hz, dB

// An LR4 high-pass and a 65Hz notch under the peaks, which otherwise boost
// 30-80Hz by 11-28dB where stock's curve is near 0dB, and the driver
// distorted on that (em_eq.CORRECTION_HIGHPASS_HZ / CORRECTION_NOTCH).
const correctionHighpassHz = 80.0

var correctionNotch = [3]float64{65.0, -25.0, 4.0} // Hz, dB, Q

type correction struct{ s [8]biquad }

func newCorrection(fs float64) correction {
	var c correction
	for i, p := range correctionPeaks {
		c.s[i] = peaking(p[0], p[1], p[2], fs)
	}
	c.s[4] = highShelf(correctionShelf[0], correctionShelf[1], fs)
	c.s[5] = peaking(correctionNotch[0], correctionNotch[1], correctionNotch[2], fs)
	hp := butter2(correctionHighpassHz, fs, true)
	c.s[6], c.s[7] = hp, hp
	return c
}

func (c *correction) step(x float64) float64 {
	for i := range c.s {
		x = c.s[i].step(x)
	}
	return x
}

func (c *correction) reset() {
	for i := range c.s {
		c.s[i].reset()
	}
}

// Upper bands (em_mbc.UPPER_*): stock's MBCL crossovers and law.
const (
	upperRatio       = 2.0
	upperThresholdDb = -10.0
	upperFloorDb     = -40.0
	upperReleaseMs   = 100.0
	// stockBassFloorDb: stock's band 1 floor; the profile uses the deeper of
	// it and GuardDb (em_mbc.STOCK_BASS_FLOOR_DB).
	stockBassFloorDb = -40.0
	// Hold before release (em_mbc.STOCK_BASS_HOLD_MS / STOCK_UPPER_HOLD_MS):
	// without it the gain recovers between a waveform's peaks and is cut
	// again on the next, 1% THD at 150-200Hz.
	stockBassHoldMs  = 20.0
	stockUpperHoldMs = 10.0
)

// holdMin is the minimum of the last n values pushed, by a monotonic deque:
// em_mbc._BandGain's trailing minimum_filter1d, one sample at a time. Its
// empty state reads as no reduction, as Python's zero-filled tail does.
type holdMin struct {
	n          int
	t          int
	idx        []int
	val        []float64
	head, size int
}

func newHoldMin(ms, fs float64) *holdMin {
	n := max(1, int(math.Round(ms*fs/1000)))
	return &holdMin{n: n, idx: make([]int, n), val: make([]float64, n)}
}

func (h *holdMin) push(v float64) float64 {
	c := len(h.val)
	for h.size > 0 && h.val[(h.head+h.size-1)%c] >= v {
		h.size--
	}
	h.idx[(h.head+h.size)%c], h.val[(h.head+h.size)%c] = h.t, v
	h.size++
	for h.idx[h.head] <= h.t-h.n {
		h.head = (h.head + 1) % c
		h.size--
	}
	h.t++
	return min(h.val[h.head], 0)
}

func (h *holdMin) reset() { h.t, h.head, h.size = 0, 0, 0 }

var (
	upperCrossovers  = [2]float64{500, 7500}
	upperThresholdLn = fullScale * math.Pow(10, upperThresholdDb/20)
)

// allpass2 is the second-order allpass an LR4 crossover's halves sum to
// (em_mbc._allpass2), for a band that skips that crossover.
func allpass2(fc, fs float64) biquad {
	w0 := 2 * math.Pi * fc / fs
	alpha := math.Sin(w0) / (2 * (1 / math.Sqrt(2)))
	a0, a1, a2 := 1+alpha, -2*math.Cos(w0), 1-alpha
	return biquad{b0: a2 / a0, b1: a1 / a0, b2: 1.0, a1: a1 / a0, a2: a2 / a0}
}

type upperBands struct {
	lp2, hp2, lp3, hp3 [2]biquad // LR4 at 500Hz and 7.5kHz
	loAP               [2]biquad // the bass band's allpasses (500Hz, 7.5kHz)
	b2AP               biquad    // the 115-500Hz band's (7.5kHz)
	gainDb             [3]float64
	gain               [3]gainCache
	hold               [3]*holdMin
	slew               float64
	maxReductionDb     float64
}

func newUpperBands(fs float64) *upperBands {
	u := &upperBands{slew: releaseReferenceDb / (math.Max(0.1, upperReleaseMs) / 1000) / fs}
	f2, f3 := upperCrossovers[0], upperCrossovers[1]
	lo2, hi2 := butter2(f2, fs, false), butter2(f2, fs, true)
	lo3, hi3 := butter2(f3, fs, false), butter2(f3, fs, true)
	u.lp2, u.hp2 = [2]biquad{lo2, lo2}, [2]biquad{hi2, hi2}
	u.lp3, u.hp3 = [2]biquad{lo3, lo3}, [2]biquad{hi3, hi3}
	u.loAP = [2]biquad{allpass2(f2, fs), allpass2(f3, fs)}
	u.b2AP = allpass2(f3, fs)
	for i := range u.hold {
		u.hold[i] = newHoldMin(stockUpperHoldMs, fs)
	}
	return u
}

// bandGain is em_mbc._BandGain for one sample: the 2:1 static law with an
// instant attack and a slew-limited release.
func (u *upperBands) bandGain(i int, x float64) float64 {
	target := 0.0
	if a := math.Abs(x); a > upperThresholdLn {
		levelDb := 20 * math.Log10(a/fullScale)
		target = max(-(levelDb-upperThresholdDb)*(1-1/upperRatio), upperFloorDb)
	}
	target = u.hold[i].push(target)
	u.gainDb[i] = min(target, u.gainDb[i]+u.slew)
	if r := -u.gainDb[i]; r > u.maxReductionDb {
		u.maxReductionDb = r
	}
	return u.gain[i].of(u.gainDb[i])
}

// step takes the guard's two halves and returns the four-band sum. lowGain
// is the guard's gain for the bass band, or nil when the guard is bypassed
// (then no band is compressed, but all are still split and summed).
func (u *upperBands) step(low, high float64, lowGain func(float64) float64) float64 {
	b2 := u.b2AP.step(u.lp2[1].step(u.lp2[0].step(high)))
	rest := u.hp2[1].step(u.hp2[0].step(high))
	b3 := u.lp3[1].step(u.lp3[0].step(rest))
	b4 := u.hp3[1].step(u.hp3[0].step(rest))
	low = u.loAP[1].step(u.loAP[0].step(low))
	if lowGain == nil {
		return low + ((b2 + b3) + b4)
	}
	out := low * lowGain(low)
	out = out + b2*u.bandGain(0, b2)
	out = out + b3*u.bandGain(1, b3)
	out = out + b4*u.bandGain(2, b4)
	return out
}

func (u *upperBands) reset() {
	for _, f := range []*[2]biquad{&u.lp2, &u.hp2, &u.lp3, &u.hp3, &u.loAP} {
		f[0].reset()
		f[1].reset()
	}
	u.b2AP.reset()
	u.gainDb = [3]float64{}
	for _, h := range u.hold {
		h.reset()
	}
}
