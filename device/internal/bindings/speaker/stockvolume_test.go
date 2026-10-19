package speaker

import (
	"math"
	"testing"
)

// Held to em_volume.stock_level_db: the two must give the same dB for every
// level.
func TestStockVolumeMatchesTheController(t *testing.T) {
	want := map[int]float64{1: -53, 13: -47, 47: -30, 64: -21.5, 89: -9, 114: 3.5, 126: 9.5, 127: 10}
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

// The buttons step 8 levels from 47 (internal/server volume.go); every press
// must be the same 4dB, all the way to the top.
func TestEveryButtonPressIsFourDb(t *testing.T) {
	for level := 47; level+8 <= 127; level += 8 {
		a, _ := StockVolumeDb(level)
		b, _ := StockVolumeDb(level + 8)
		if math.Abs(b-a-4) > 1e-9 {
			t.Errorf("press %d -> %d: %v dB, want 4", level, level+8, b-a)
		}
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

// The response boost never cuts: under the stock law the volume exceeds 1 from
// level 107, and 1/volume there would turn the voice DOWN under the music.
func TestResponseCapNeverCuts(t *testing.T) {
	if got := cappedResponseGain(4, 4.8); got != 1 {
		t.Errorf("volume 4.8, requested 4: got %v, want 1", got)
	}
	if got := cappedResponseGain(4, 0.5); got != 2 {
		t.Errorf("volume 0.5, requested 4: got %v, want 2", got)
	}
}
