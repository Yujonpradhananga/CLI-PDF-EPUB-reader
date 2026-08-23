package imgutil

import (
	"image"
	"image/draw"
	"math"
)

// CropImage trims fractions of each edge from an image.
// Uses SubImage (zero-copy) when the image type supports it.
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

// toRGBACopy returns a fresh *image.RGBA copy of src with zero-origin bounds.
// Always copies: the invert functions mutate the returned pixels in place, and
// src may be a shared image that must stay pristine.
func toRGBACopy(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// SmartInvert inverts lightness while preserving hue and saturation.
// White backgrounds become black, black text becomes white, colors keep their hue.
// Grayscale pixels (r==g==b — virtually all of a rendered PDF page) go through a
// 256-entry LUT; only colored pixels pay the float HSL round trip. Direct Pix
// access instead of At()/Set() keeps this fast on ~12 MP supersampled pages.
func SmartInvert(src image.Image) image.Image {
	rgba := toRGBACopy(src)

	// LUT for the grayscale case: s=0, so hslToRGB returns (l,l,l) with
	// l' = 0.12 + (1-l)*0.88 (dark gray bg instead of pure black).
	var lut [256]uint8
	for v := 0; v < 256; v++ {
		l := 0.12 + (1.0-float64(v)/255.0)*0.88
		lut[v] = uint8(l * 255)
	}

	pix := rgba.Pix
	for i := 0; i < len(pix); i += 4 {
		r, g, b := pix[i], pix[i+1], pix[i+2]
		if r == g && g == b {
			v := lut[r]
			pix[i], pix[i+1], pix[i+2] = v, v, v
			continue
		}
		h, s, l := RGBToHSL(float64(r)/255.0, float64(g)/255.0, float64(b)/255.0)
		l = 0.12 + (1.0-l)*0.88
		nr, ng, nb := HSLToRGB(h, s, l)
		pix[i] = uint8(nr * 255)
		pix[i+1] = uint8(ng * 255)
		pix[i+2] = uint8(nb * 255)
	}
	return rgba
}

// SimpleInvert does a full RGB color inversion with the same gray background shift.
func SimpleInvert(src image.Image) image.Image {
	rgba := toRGBACopy(src)

	// Invert and remap to gray bg range: 255→30, 0→255
	var lut [256]uint8
	for v := 0; v < 256; v++ {
		lut[v] = uint8(30 + (255-v)*225/255)
	}

	pix := rgba.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = lut[pix[i]]
		pix[i+1] = lut[pix[i+1]]
		pix[i+2] = lut[pix[i+2]]
	}
	return rgba
}

// dimPageWhite is what 255 (paper white) maps to in "dim" dark mode, and
// dimPageGamma is the curve applied before that scaling.
const (
	DimPageWhite = 120
	DimPageGamma = 2.2
)

// DimPage darkens the page without inverting it: white paper becomes mid gray,
// dark ink stays dark. Linear per channel, so hues are preserved.
func DimPage(src image.Image) image.Image {
	rgba := toRGBACopy(src)

	var lut [256]uint8
	for v := 0; v < 256; v++ {
		lut[v] = uint8(DimPageWhite * math.Pow(float64(v)/255.0, DimPageGamma))
	}

	pix := rgba.Pix
	for i := 0; i < len(pix); i += 4 {
		pix[i] = lut[pix[i]]
		pix[i+1] = lut[pix[i+1]]
		pix[i+2] = lut[pix[i+2]]
	}
	return rgba
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
