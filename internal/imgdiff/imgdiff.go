// Package imgdiff compares a screenshot with a design render.
package imgdiff

import (
	"image"
	"image/color"
	"image/draw"
	"sort"
)

type Rect struct {
	X int `json:"x" yaml:"x"`
	Y int `json:"y" yaml:"y"`
	W int `json:"w" yaml:"w"`
	H int `json:"h" yaml:"h"`
}

func (r Rect) contains(x, y int) bool { return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H }

type Options struct {
	// Threshold is the per-channel difference (0–255) above which a pixel differs.
	// Font rasterisation alone produces differences around 20–30.
	Threshold int
	// Masks are ignored regions, in pixels of the actual image.
	Masks []Rect
	// Cell is the side of the grid cells used to group differences into regions.
	Cell int
}

type Result struct {
	Width, Height  int         `json:"-"`
	Compared       int         `json:"compared_pixels"`
	Differing      int         `json:"differing_pixels"`
	Ratio          float64     `json:"ratio"`
	HeightMismatch int         `json:"height_mismatch_px"`
	Regions        []Rect      `json:"regions"`
	Diff           image.Image `json:"-"`
}

// Compare compares actual with design. Design is scaled to actual's width first; if heights
// still differ, only the overlapping top part is compared and the gap is reported.
func Compare(actual, design image.Image, o Options) Result {
	if o.Threshold <= 0 {
		o.Threshold = 40
	}
	if o.Cell <= 0 {
		o.Cell = 24
	}
	ab := actual.Bounds()
	w, h := ab.Dx(), ab.Dy()
	db := design.Bounds()
	if db.Dx() != w {
		nh := int(float64(db.Dy())*float64(w)/float64(db.Dx()) + 0.5)
		design = Resize(design, w, nh)
		db = design.Bounds()
	}
	ch := min(h, db.Dy())
	res := Result{Width: w, Height: h, HeightMismatch: db.Dy() - h}

	out := image.NewRGBA(image.Rect(0, 0, w, h))
	cols, rows := (w+o.Cell-1)/o.Cell, (ch+o.Cell-1)/o.Cell
	cells := make([]int, cols*rows)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			ar, ag, ab_, _ := rgb(actual.At(ab.Min.X+x, ab.Min.Y+y))
			gray := uint8((299*int(ar) + 587*int(ag) + 114*int(ab_)) / 1000)
			faded := uint8(200 + int(gray)/5)
			if y >= ch {
				out.Set(x, y, color.RGBA{255, 200, 0, 255})
				continue
			}
			if masked(o.Masks, x, y) {
				out.Set(x, y, color.RGBA{faded - 40, faded - 10, 255, 255})
				continue
			}
			res.Compared++
			dr, dg, db_, _ := rgb(design.At(db.Min.X+x, db.Min.Y+y))
			if absDiff(ar, dr) > o.Threshold || absDiff(ag, dg) > o.Threshold || absDiff(ab_, db_) > o.Threshold {
				res.Differing++
				cells[(y/o.Cell)*cols+x/o.Cell]++
				out.Set(x, y, color.RGBA{255, 0, 64, 255})
			} else {
				out.Set(x, y, color.RGBA{faded, faded, faded, 255})
			}
		}
	}
	if res.Compared > 0 {
		res.Ratio = float64(res.Differing) / float64(res.Compared)
	}
	res.Regions = regions(cells, cols, rows, o.Cell, w, ch)
	res.Diff = out
	return res
}

// regions merges grid cells with meaningful differences into connected bounding boxes,
// largest first.
func regions(cells []int, cols, rows, cell, w, h int) []Rect {
	minPx := cell * cell / 20
	seen := make([]bool, len(cells))
	var out []Rect
	for i, n := range cells {
		if n < minPx || seen[i] {
			continue
		}
		x0, y0, x1, y1 := cols, rows, -1, -1
		stack := []int{i}
		seen[i] = true
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			cx, cy := c%cols, c/cols
			x0, y0, x1, y1 = min(x0, cx), min(y0, cy), max(x1, cx), max(y1, cy)
			for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := cx+d[0], cy+d[1]
				if nx < 0 || ny < 0 || nx >= cols || ny >= rows {
					continue
				}
				j := ny*cols + nx
				if !seen[j] && cells[j] >= minPx {
					seen[j] = true
					stack = append(stack, j)
				}
			}
		}
		r := Rect{X: x0 * cell, Y: y0 * cell, W: (x1 - x0 + 1) * cell, H: (y1 - y0 + 1) * cell}
		r.W = min(r.W, w-r.X)
		r.H = min(r.H, h-r.Y)
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].W*out[i].H > out[j].W*out[j].H })
	return out
}

func masked(ms []Rect, x, y int) bool {
	for _, m := range ms {
		if m.contains(x, y) {
			return true
		}
	}
	return false
}

func rgb(c color.Color) (uint8, uint8, uint8, uint8) {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	// Composite transparent design pixels on white, as Figma frames usually are.
	a := int(n.A)
	blend := func(v uint8) uint8 { return uint8((int(v)*a + 255*(255-a)) / 255) }
	return blend(n.R), blend(n.G), blend(n.B), 255
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// Resize scales img to w×h with bilinear filtering.
func Resize(img image.Image, w, h int) image.Image {
	src := image.NewNRGBA(img.Bounds())
	draw.Draw(src, src.Bounds(), img, img.Bounds().Min, draw.Src)
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		fy := (float64(y)+0.5)*float64(sh)/float64(h) - 0.5
		y0 := clamp(int(fy), 0, sh-1)
		y1 := clamp(y0+1, 0, sh-1)
		ty := fy - float64(y0)
		if ty < 0 {
			ty = 0
		}
		for x := 0; x < w; x++ {
			fx := (float64(x)+0.5)*float64(sw)/float64(w) - 0.5
			x0 := clamp(int(fx), 0, sw-1)
			x1 := clamp(x0+1, 0, sw-1)
			tx := fx - float64(x0)
			if tx < 0 {
				tx = 0
			}
			i00 := src.PixOffset(x0, y0)
			i10 := src.PixOffset(x1, y0)
			i01 := src.PixOffset(x0, y1)
			i11 := src.PixOffset(x1, y1)
			o := dst.PixOffset(x, y)
			for k := 0; k < 4; k++ {
				top := float64(src.Pix[i00+k])*(1-tx) + float64(src.Pix[i10+k])*tx
				bot := float64(src.Pix[i01+k])*(1-tx) + float64(src.Pix[i11+k])*tx
				dst.Pix[o+k] = uint8(top*(1-ty) + bot*ty + 0.5)
			}
		}
	}
	return dst
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
