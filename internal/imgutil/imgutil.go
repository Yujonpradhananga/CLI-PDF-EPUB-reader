package imgutil

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

func CropImage(img image.Image, top, bottom, left, right float64) image.Image {
	if top == 0 && bottom == 0 && left == 0 && right == 0 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	x0 := b.Min.X + int(float64(w)*left)
	x1 := b.Max.X - int(float64(w)*right)
	y0 := b.Min.Y + int(float64(h)*top)
	y1 := b.Max.Y - int(float64(h)*bottom)
	if x1 <= x0 || y1 <= y0 {
		return img
	}
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := img.(subImager); ok {
		return si.SubImage(image.Rect(x0, y0, x1, y1))
	}
	dst := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	draw.Draw(dst, dst.Bounds(), img, image.Pt(x0, y0), draw.Src)
	return dst
}

// SmartInvert inverts lightness while preserving hue and saturation.
// White backgrounds become black, black text becomes white, colors keep their hue.
func SmartInvert(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			r8 := float64(r>>8) / 255.0
			g8 := float64(g>>8) / 255.0
			b8 := float64(b>>8) / 255.0

			h, s, l := RGBToHSL(r8, g8, b8)
			l = 0.12 + (1.0-l)*0.88
			nr, ng, nb := HSLToRGB(h, s, l)

			dst.Set(x, y, color.RGBA{
				R: uint8(nr * 255),
				G: uint8(ng * 255),
				B: uint8(nb * 255),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}

// SimpleInvert does a full RGB color inversion with the same gray background shift.
func SimpleInvert(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			nr := 30 + (255-r>>8)*225/255
			ng := 30 + (255-g>>8)*225/255
			nb := 30 + (255-b>>8)*225/255
			dst.Set(x, y, color.RGBA{
				R: uint8(nr),
				G: uint8(ng),
				B: uint8(nb),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}

// RGBToHSL converts RGB values (0-1 range) to HSL.
func RGBToHSL(r, g, b float64) (h, s, l float64) {
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	l = (max + min) / 2

	if max == min {
		return 0, 0, l
	}

	d := max - min
	if l > 0.5 {
		s = d / (2.0 - max - min)
	} else {
		s = d / (max + min)
	}

	switch max {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	case b:
		h = (r-g)/d + 4
	}
	h /= 6
	return
}

// HSLToRGB converts HSL values to RGB (0-1 range).
func HSLToRGB(h, s, l float64) (r, g, b float64) {
	if s == 0 {
		return l, l, l
	}

	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q

	r = HueToRGB(p, q, h+1.0/3.0)
	g = HueToRGB(p, q, h)
	b = HueToRGB(p, q, h-1.0/3.0)
	return
}

// HueToRGB is a helper for HSL to RGB conversion.
func HueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	if t < 1.0/6.0 {
		return p + (q-p)*6*t
	}
	if t < 1.0/2.0 {
		return q
	}
	if t < 2.0/3.0 {
		return p + (q-p)*(2.0/3.0-t)*6
	}
	return p
}
