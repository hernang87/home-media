// Command genicon renders the Home Media app icon as a 1024x1024 PNG.
//
// Usage: go run ./cmd/genicon <output.png>
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const (
	size = 1024 // output edge length in pixels
	ss   = 4    // supersampling factor per axis (ss*ss samples per pixel)
)

// Gradient endpoints (top and bottom of the tile).
var (
	topColor    = [3]float64{0x1b, 0x2a, 0x49}
	bottomColor = [3]float64{0x0e, 0x7c, 0x86}
)

// Cast-style waves: all geometry is in normalised units (0..1 of the edge).
const (
	originU, originV = 0.18, 0.82 // wave origin, bottom-left
	ringWidth        = 0.035
	cornerRadius     = 0.22
)

type wave struct {
	radius, alpha float64
}

var waves = []wave{
	{radius: 0.10, alpha: 0.55},
	{radius: 0.155, alpha: 0.40},
	{radius: 0.21, alpha: 0.25},
}

// Play triangle vertices (normalised), centre-left of the tile.
var triangle = [3][2]float64{
	{0.30, 0.28},
	{0.30, 0.60},
	{0.66, 0.44},
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/genicon <output.png>")
		os.Exit(2)
	}
	if err := writePNG(os.Args[1], render()); err != nil {
		fmt.Fprintln(os.Stderr, "genicon:", err)
		os.Exit(1)
	}
}

func writePNG(path string, img image.Image) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	return png.Encode(f, img)
}

// render supersamples the tile. Colour is averaged over the samples that fall
// inside the rounded square, and alpha is the covered fraction, which gives
// anti-aliased edges and a transparent background.
func render() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	total := float64(ss * ss)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var sum [3]float64
			inside := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / size
					y := (float64(py) + (float64(sy)+0.5)/ss) / size
					if !inRoundedSquare(x, y) {
						continue
					}
					inside++
					c := shade(x, y)
					for i := range sum {
						sum[i] += c[i]
					}
				}
			}
			if inside == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(math.Round(sum[0] / float64(inside))),
				G: uint8(math.Round(sum[1] / float64(inside))),
				B: uint8(math.Round(sum[2] / float64(inside))),
				A: uint8(math.Round(255 * float64(inside) / total)),
			})
		}
	}
	return img
}

// inRoundedSquare reports whether the normalised point lies inside the
// full-tile rounded square.
func inRoundedSquare(u, v float64) bool {
	const half = 0.5
	inner := half - cornerRadius
	dx := math.Max(math.Abs(u-half)-inner, 0)
	dy := math.Max(math.Abs(v-half)-inner, 0)
	return dx*dx+dy*dy <= cornerRadius*cornerRadius
}

// shade returns the RGB colour at a point inside the tile: the vertical
// gradient with the white play triangle and cast waves composited on top.
func shade(u, v float64) [3]float64 {
	c := [3]float64{
		topColor[0] + (bottomColor[0]-topColor[0])*v,
		topColor[1] + (bottomColor[1]-topColor[1])*v,
		topColor[2] + (bottomColor[2]-topColor[2])*v,
	}
	if inTriangle(u, v) {
		return mix(c, 1)
	}
	dx, dy := u-originU, originV-v // y grows upward from the origin
	dist := math.Hypot(dx, dy)
	angle := math.Atan2(dy, dx)
	if angle >= 0 && angle <= math.Pi/2 {
		for _, w := range waves {
			if math.Abs(dist-w.radius) <= ringWidth/2 {
				return mix(c, w.alpha)
			}
		}
	}
	return c
}

// mix composites white over c with the given alpha.
func mix(c [3]float64, a float64) [3]float64 {
	for i := range c {
		c[i] = c[i]*(1-a) + 255*a
	}
	return c
}

// inTriangle reports whether (u, v) lies inside the play triangle, using the
// sign of the cross product against each edge.
func inTriangle(u, v float64) bool {
	side := func(a, b [2]float64) float64 {
		return (b[0]-a[0])*(v-a[1]) - (b[1]-a[1])*(u-a[0])
	}
	d1 := side(triangle[0], triangle[1])
	d2 := side(triangle[1], triangle[2])
	d3 := side(triangle[2], triangle[0])
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}
