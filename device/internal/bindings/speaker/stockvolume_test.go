package speaker

import (
	"math"
	"testing"
)

// Held to em_volume.stock_level_db, which the controller's tests hold to
// stock's table: the two must give the same dB for every level.
func TestStockVolumeMatchesTheController(t *testing.T) {
	want := map[int]float64{1: -77.33543307086615, 13: -26.72913385826772, 25: -21.988976377952756,
		38: -16.823622047244097, 51: -12.705511811023623, 63: -8.918110236220473, 64: -8.681889763779528,
		76: -5.8, 88: -4.8, 89: -1.3, 90: -1.3, 101: -0.30000000000000004, 102: 3.7, 114: 3.7,
		115: 10.865354330708659, 126: 13.463779527559055, 127: 13.7}
	for level, db := range want {
		got, ok := StockVolumeDb(level)
		if !ok || math.Abs(got-db) > 1e-9 {
			t.Errorf("level %d: %v dB, want %v", level, got, db)
		}
	}
	if _, ok := StockVolumeDb(0); ok {
		t.Error("level 0 must be silence")
	}
	if StockVolumeGain(0) != 0 {
		t.Error("level 0 gain must be 0")
	}
}

func TestStockVolumeNeverFallsAsLevelRises(t *testing.T) {
	prev := math.Inf(-1)
	for level := 1; level <= 127; level++ {
		db, _ := StockVolumeDb(level)
		if db < prev {
			t.Fatalf("level %d: %v dB after %v", level, db, prev)
		}
		prev = db
	}
}

// The response boost never cuts: under the stock law the volume exceeds 1
// above 70%, and 1/volume there would turn the voice DOWN under the music.
func TestResponseCapNeverCuts(t *testing.T) {
	if got := cappedResponseGain(4, 4.8); got != 1 {
		t.Errorf("volume 4.8, requested 4: got %v, want 1", got)
	}
	if got := cappedResponseGain(4, 0.5); got != 2 {
		t.Errorf("volume 0.5, requested 4: got %v, want 2", got)
	}
}
