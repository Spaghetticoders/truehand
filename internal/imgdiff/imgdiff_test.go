package imgdiff

import (
	"image"
	"image/color"
	"testing"
)

func solid(w, h int, c color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func paint(img *image.NRGBA, r Rect, c color.Color) {
	for y := r.Y; y < r.Y+r.H; y++ {
		for x := r.X; x < r.X+r.W; x++ {
			img.Set(x, y, c)
		}
	}
}

var white = color.NRGBA{255, 255, 255, 255}
var red = color.NRGBA{255, 0, 0, 255}

func TestIdentical(t *testing.T) {
	a := solid(120, 200, white)
	r := Compare(a, solid(120, 200, white), Options{})
	if r.Ratio != 0 || len(r.Regions) != 0 {
		t.Fatalf("identical images: ratio %v regions %v", r.Ratio, r.Regions)
	}
}

func TestDifferenceIsLocated(t *testing.T) {
	a := solid(120, 200, white)
	paint(a, Rect{48, 96, 24, 24}, red)
	r := Compare(a, solid(120, 200, white), Options{Cell: 24})
	want := float64(24*24) / float64(120*200)
	if r.Ratio < want*0.99 || r.Ratio > want*1.01 {
		t.Fatalf("ratio %v, want %v", r.Ratio, want)
	}
	if len(r.Regions) != 1 || r.Regions[0] != (Rect{48, 96, 24, 24}) {
		t.Fatalf("regions %+v", r.Regions)
	}
}

func TestMaskHidesDifference(t *testing.T) {
	a := solid(120, 200, white)
	paint(a, Rect{0, 0, 120, 30}, red)
	r := Compare(a, solid(120, 200, white), Options{Masks: []Rect{{0, 0, 120, 30}}})
	if r.Differing != 0 || r.Compared != 120*170 {
		t.Fatalf("mask ignored: differing %d compared %d", r.Differing, r.Compared)
	}
}

func TestDesignScaledToWidth(t *testing.T) {
	a := solid(300, 600, white)
	paint(a, Rect{0, 300, 300, 300}, red)
	d := solid(100, 200, white)
	paint(d, Rect{0, 100, 100, 100}, red)
	r := Compare(a, d, Options{})
	if r.Ratio > 0.01 || r.HeightMismatch != 0 {
		t.Fatalf("scaled design should match: ratio %v height %d", r.Ratio, r.HeightMismatch)
	}
}

func TestThresholdToleratesAntialiasing(t *testing.T) {
	a := solid(50, 50, color.NRGBA{200, 200, 200, 255})
	r := Compare(a, solid(50, 50, color.NRGBA{220, 220, 220, 255}), Options{})
	if r.Differing != 0 {
		t.Fatalf("20-level difference must be tolerated, got %d", r.Differing)
	}
}
