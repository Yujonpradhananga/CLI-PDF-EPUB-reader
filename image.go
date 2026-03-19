package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/blacktop/go-termimg"
)

// cropImage trims fractions of each edge from an image.
// Uses SubImage (zero-copy) when the image type supports it.
func cropImage(img image.Image, top, bottom, left, right float64) image.Image {
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

func (d *DocumentViewer) renderPageImage(pageNum, maxWidth, maxHeight int) int {
	return d.renderPageImageAligned(pageNum, maxWidth, maxHeight, "center")
}

func (d *DocumentViewer) renderPageImageAligned(pageNum, maxWidth, maxHeight int, align string) int {
	if maxHeight <= 0 {
		return 0
	}

	termType := d.detectTerminalType()
	imagePath, actualHeight, imageWidthInChars, actualPixelWidth, actualPixelHeight, err := d.savePageAsImage(pageNum, maxWidth, maxHeight, termType)
	if err != nil {
		return 0
	}
	defer os.Remove(imagePath)

	var horizontalOffset int
	switch align {
	case "right":
		horizontalOffset = maxWidth - imageWidthInChars
	case "left":
		horizontalOffset = 0
	default: // "center"
		horizontalOffset = (maxWidth - imageWidthInChars) / 2
	}
	if horizontalOffset < 0 {
		horizontalOffset = 0
	}

	return d.renderWithTermImg(imagePath, actualHeight, horizontalOffset, imageWidthInChars, actualPixelWidth, actualPixelHeight, termType)
}

// scaleImage performs nearest-neighbor scaling of an image to the target dimensions.
func scaleImage(src image.Image, targetW, targetH int) image.Image {
	if targetW <= 0 || targetH <= 0 {
		return src
	}
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW == targetW && srcH == targetH {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	for y := 0; y < targetH; y++ {
		srcY := bounds.Min.Y + y*srcH/targetH
		for x := 0; x < targetW; x++ {
			srcX := bounds.Min.X + x*srcW/targetW
			r, g, b, a := src.At(srcX, srcY).RGBA()
			i := (y*dst.Stride) + x*4
			dst.Pix[i+0] = uint8(r >> 8)
			dst.Pix[i+1] = uint8(g >> 8)
			dst.Pix[i+2] = uint8(b >> 8)
			dst.Pix[i+3] = uint8(a >> 8)
		}
	}
	return dst
}

func (d *DocumentViewer) savePageAsImage(pageNum, termWidth, termHeight int, termType string) (string, int, int, int, int, error) {
	if err := os.MkdirAll(d.tempDir, 0o755); err != nil {
		return "", 0, 0, 0, 0, err
	}

	pixelsPerChar, pixelsPerLine := d.getTerminalCellSize()

	// Calculate target pixel dimensions based on terminal size
	horizontalPadding := 4
	verticalPadding := 3
	effectiveWidth := termWidth - horizontalPadding
	effectiveHeight := termHeight - verticalPadding

	// Apply user scale factor
	scale := d.scaleFactor
	if scale == 0 {
		scale = 1.0
	}

	targetPixelWidth := int(float64(effectiveWidth) * pixelsPerChar * scale)
	targetPixelHeight := int(float64(effectiveHeight) * pixelsPerLine * scale)

	// Get aspect ratio from source
	var aspectRatio float64
	var pageWidthAt72, pageHeightAt72 int

	if d.isImage {
		srcBounds := d.sourceImage.Bounds()
		aspectRatio = float64(srcBounds.Dy()) / float64(srcBounds.Dx())
	} else {
		// Get page dimensions at 72 DPI to calculate proper render DPI
		testImg, err := d.doc.ImageDPI(pageNum, 72.0)
		if err != nil {
			return "", 0, 0, 0, 0, err
		}
		testBounds := testImg.Bounds()
		pageWidthAt72 = testBounds.Dx()
		pageHeightAt72 = testBounds.Dy()
		aspectRatio = float64(pageHeightAt72) / float64(pageWidthAt72)
	}

	// Calculate final dimensions based on fit mode
	var finalWidth, finalHeight int
	switch d.fitMode {
	case "height":
		finalHeight = targetPixelHeight
		finalWidth = int(float64(finalHeight) / aspectRatio)
		if finalWidth > targetPixelWidth {
			finalWidth = targetPixelWidth
			finalHeight = int(float64(finalWidth) * aspectRatio)
		}
	case "width":
		finalWidth = targetPixelWidth
		finalHeight = int(float64(finalWidth) * aspectRatio)
	default: // "auto"
		finalWidth = targetPixelWidth
		finalHeight = int(float64(finalWidth) * aspectRatio)
		if finalHeight > targetPixelHeight {
			finalHeight = targetPixelHeight
			finalWidth = int(float64(finalHeight) / aspectRatio)
		}
	}

	// Get the image at final dimensions
	var img image.Image
	if d.isImage {
		img = scaleImage(d.sourceImage, finalWidth, finalHeight)
	} else {
		// Calculate DPI needed to render at exactly the right size
		dpiForWidth := float64(finalWidth) / float64(pageWidthAt72) * 72.0
		dpiForHeight := float64(finalHeight) / float64(pageHeightAt72) * 72.0
		dpi := dpiForWidth
		if dpiForHeight < dpi {
			dpi = dpiForHeight
		}

		// Clamp DPI to reasonable range
		if dpi < 36 {
			dpi = 36
		}
		maxDPI := 300.0
		if termType != "kitty" {
			maxDPI = 100.0
		}
		if dpi > maxDPI {
			dpi = maxDPI
		}

		// Render at calculated DPI - no resizing needed
		var err error
		img, err = d.doc.ImageDPI(pageNum, dpi)
		if err != nil {
			return "", 0, 0, 0, 0, err
		}
	}

	// Apply dark mode
	var finalImg image.Image = img
	switch d.darkMode {
	case "smart":
		finalImg = smartInvert(img)
	case "invert":
		finalImg = simpleInvert(img)
	}

	// Apply user crop
	finalImg = cropImage(finalImg, d.cropTop, d.cropBottom, d.cropLeft, d.cropRight)

	bounds := finalImg.Bounds()
	actualWidth := bounds.Dx()
	actualHeight := bounds.Dy()

	actualLines := int(float64(actualHeight)/pixelsPerLine) + 1
	if actualLines > termHeight {
		actualLines = termHeight
	}

	imageWidthInChars := int(float64(actualWidth)/pixelsPerChar) + 1

	filename := fmt.Sprintf("page_%d.png", pageNum)
	imagePath := filepath.Join(d.tempDir, filename)

	file, err := os.Create(imagePath)
	if err != nil {
		return "", 0, 0, 0, 0, err
	}
	defer file.Close()

	err = png.Encode(file, finalImg)
	if err != nil {
		os.Remove(imagePath)
		return "", 0, 0, 0, 0, err
	}

	return imagePath, actualLines, imageWidthInChars, actualWidth, actualHeight, nil
}

// renderPageToImage renders a page to an in-memory image at the given terminal dimensions.
func (d *DocumentViewer) renderPageToImage(pageNum, termWidth, termHeight int, termType string) (image.Image, error) {
	pixelsPerChar, pixelsPerLine := d.getTerminalCellSize()

	horizontalPadding := 4
	verticalPadding := 3
	effectiveWidth := termWidth - horizontalPadding
	effectiveHeight := termHeight - verticalPadding

	scale := d.scaleFactor
	if scale == 0 {
		scale = 1.0
	}

	targetPixelWidth := int(float64(effectiveWidth) * pixelsPerChar * scale)
	targetPixelHeight := int(float64(effectiveHeight) * pixelsPerLine * scale)

	var aspectRatio float64
	var pageWidthAt72, pageHeightAt72 int

	if d.isImage {
		srcBounds := d.sourceImage.Bounds()
		aspectRatio = float64(srcBounds.Dy()) / float64(srcBounds.Dx())
	} else {
		testImg, err := d.doc.ImageDPI(pageNum, 72.0)
		if err != nil {
			return nil, err
		}
		testBounds := testImg.Bounds()
		pageWidthAt72 = testBounds.Dx()
		pageHeightAt72 = testBounds.Dy()
		aspectRatio = float64(pageHeightAt72) / float64(pageWidthAt72)
	}

	var finalWidth, finalHeight int
	switch d.fitMode {
	case "height":
		finalHeight = targetPixelHeight
		finalWidth = int(float64(finalHeight) / aspectRatio)
		if finalWidth > targetPixelWidth {
			finalWidth = targetPixelWidth
			finalHeight = int(float64(finalWidth) * aspectRatio)
		}
	case "width":
		finalWidth = targetPixelWidth
		finalHeight = int(float64(finalWidth) * aspectRatio)
	default:
		finalWidth = targetPixelWidth
		finalHeight = int(float64(finalWidth) * aspectRatio)
		if finalHeight > targetPixelHeight {
			finalHeight = targetPixelHeight
			finalWidth = int(float64(finalHeight) / aspectRatio)
		}
	}

	var img image.Image
	if d.isImage {
		img = scaleImage(d.sourceImage, finalWidth, finalHeight)
	} else {
		dpiForWidth := float64(finalWidth) / float64(pageWidthAt72) * 72.0
		dpiForHeight := float64(finalHeight) / float64(pageHeightAt72) * 72.0
		dpi := dpiForWidth
		if dpiForHeight < dpi {
			dpi = dpiForHeight
		}

		if dpi < 36 {
			dpi = 36
		}
		maxDPI := 300.0
		if termType != "kitty" {
			maxDPI = 100.0
		}
		if dpi > maxDPI {
			dpi = maxDPI
		}

		var err error
		img, err = d.doc.ImageDPI(pageNum, dpi)
		if err != nil {
			return nil, err
		}
	}

	switch d.darkMode {
	case "smart":
		return smartInvert(img), nil
	case "invert":
		return simpleInvert(img), nil
	}
	return img, nil
}

// renderDualComposite renders two pages as a single composited image.
// layout is "vertical" (stacked) or "horizontal" (side-by-side).
// gap is the pixel gap between pages.
func (d *DocumentViewer) renderDualComposite(page1, page2 int, hasPage2 bool, termWidth, termHeight int, layout string, gap int) int {
	if termHeight <= 0 {
		return 0
	}

	termType := d.detectTerminalType()
	pixelsPerChar, pixelsPerLine := d.getTerminalCellSize()

	var img1W, img2W int
	var img1H, img2H int

	if layout == "vertical" {
		// Each page gets half the terminal height
		img1H = termHeight / 2
		img2H = img1H
		img1W = termWidth
		img2W = termWidth
	} else {
		// Each page gets half the terminal width
		img1W = termWidth / 2
		img2W = termWidth - img1W
		img1H = termHeight
		img2H = termHeight
	}

	page1Img, err := d.renderPageToImage(page1, img1W, img1H, termType)
	if err != nil {
		return 0
	}
	page1Img = cropImage(page1Img, d.cropTop, d.cropBottom, d.cropLeft, d.cropRight)

	var page2Img image.Image
	if hasPage2 {
		page2Img, err = d.renderPageToImage(page2, img2W, img2H, termType)
		if err != nil {
			return 0
		}
		page2Img = cropImage(page2Img, d.cropTop, d.cropBottom, d.cropLeft, d.cropRight)
	}

	b1 := page1Img.Bounds()

	// Build composite image
	var composite *image.RGBA
	var compositeW, compositeH int

	if layout == "vertical" {
		compositeW = b1.Dx()
		compositeH = b1.Dy() + gap
		if page2Img != nil {
			b2 := page2Img.Bounds()
			if b2.Dx() > compositeW {
				compositeW = b2.Dx()
			}
			compositeH += b2.Dy()
		}

		// Use white background (or dark if dark mode)
		bgColor := color.RGBA{255, 255, 255, 255}
		if d.darkMode != "" {
			bgColor = color.RGBA{30, 30, 30, 255}
		}
		composite = image.NewRGBA(image.Rect(0, 0, compositeW, compositeH))
		draw.Draw(composite, composite.Bounds(), &image.Uniform{bgColor}, image.Point{}, draw.Src)

		// Center page 1 horizontally
		x1 := (compositeW - b1.Dx()) / 2
		draw.Draw(composite, image.Rect(x1, 0, x1+b1.Dx(), b1.Dy()), page1Img, b1.Min, draw.Over)

		if page2Img != nil {
			b2 := page2Img.Bounds()
			x2 := (compositeW - b2.Dx()) / 2
			y2 := b1.Dy() + gap
			draw.Draw(composite, image.Rect(x2, y2, x2+b2.Dx(), y2+b2.Dy()), page2Img, b2.Min, draw.Over)
		}
	} else {
		// Horizontal
		compositeH = b1.Dy()
		compositeW = b1.Dx() + gap
		if page2Img != nil {
			b2 := page2Img.Bounds()
			if b2.Dy() > compositeH {
				compositeH = b2.Dy()
			}
			compositeW += b2.Dx()
		}

		bgColor := color.RGBA{255, 255, 255, 255}
		if d.darkMode != "" {
			bgColor = color.RGBA{30, 30, 30, 255}
		}
		composite = image.NewRGBA(image.Rect(0, 0, compositeW, compositeH))
		draw.Draw(composite, composite.Bounds(), &image.Uniform{bgColor}, image.Point{}, draw.Src)

		// Center page 1 vertically
		y1 := (compositeH - b1.Dy()) / 2
		draw.Draw(composite, image.Rect(0, y1, b1.Dx(), y1+b1.Dy()), page1Img, b1.Min, draw.Over)

		if page2Img != nil {
			b2 := page2Img.Bounds()
			x2 := b1.Dx() + gap
			y2 := (compositeH - b2.Dy()) / 2
			draw.Draw(composite, image.Rect(x2, y2, x2+b2.Dx(), y2+b2.Dy()), page2Img, b2.Min, draw.Over)
		}
	}

	// Save composite
	if err := os.MkdirAll(d.tempDir, 0o755); err != nil {
		return 0
	}
	imagePath := filepath.Join(d.tempDir, "dual.png")
	file, err := os.Create(imagePath)
	if err != nil {
		return 0
	}
	if err := png.Encode(file, composite); err != nil {
		file.Close()
		os.Remove(imagePath)
		return 0
	}
	file.Close()
	defer os.Remove(imagePath)

	actualLines := int(float64(compositeH)/pixelsPerLine) + 1
	if actualLines > termHeight {
		actualLines = termHeight
	}
	imageWidthInChars := int(float64(compositeW)/pixelsPerChar) + 1

	horizontalOffset := (termWidth - imageWidthInChars) / 2
	if horizontalOffset < 0 {
		horizontalOffset = 0
	}

	return d.renderWithTermImg(imagePath, actualLines, horizontalOffset, imageWidthInChars, compositeW, compositeH, termType)
}

// renderHalfPage renders only the top or bottom 55% of a page, scaled to fill the terminal.
// isBottom=false shows top 55%, isBottom=true shows bottom 55%.
func (d *DocumentViewer) renderHalfPage(pageNum, termWidth, termHeight int, isBottom bool) int {
	if termHeight <= 0 {
		return 0
	}

	termType := d.detectTerminalType()
	pixelsPerChar, pixelsPerLine := d.getTerminalCellSize()

	var img image.Image

	if d.isImage {
		// For standalone images, scale so that 55% of the height fills the terminal
		srcBounds := d.sourceImage.Bounds()
		srcW, srcH := srcBounds.Dx(), srcBounds.Dy()
		aspectRatio := float64(srcW) / float64(srcH)

		targetCropPixels := float64(termHeight) * pixelsPerLine
		targetFullH := int(targetCropPixels / 0.55)

		scale := d.scaleFactor
		if scale == 0 {
			scale = 1.0
		}
		targetFullH = int(float64(targetFullH) * scale)
		targetFullW := int(float64(targetFullH) * aspectRatio)

		if targetFullW != srcW || targetFullH != srcH {
			img = scaleImage(d.sourceImage, targetFullW, targetFullH)
		} else {
			img = d.sourceImage
		}
	} else {
		// Compute DPI so that 55% of the page height fills termHeight exactly.
		testImg, err := d.doc.ImageDPI(pageNum, 72.0)
		if err != nil {
			return 0
		}
		pageHeightAt72 := testImg.Bounds().Dy()

		targetCropPixels := float64(termHeight) * pixelsPerLine
		targetFullPixels := targetCropPixels / 0.55

		scale := d.scaleFactor
		if scale == 0 {
			scale = 1.0
		}
		dpi := targetFullPixels / float64(pageHeightAt72) * 72.0 * scale

		if dpi < 36 {
			dpi = 36
		}
		maxDPI := 300.0
		if termType != "kitty" {
			maxDPI = 100.0
		}
		if dpi > maxDPI {
			dpi = maxDPI
		}

		rawImg, err := d.doc.ImageDPI(pageNum, dpi)
		if err != nil {
			return 0
		}
		img = rawImg
	}
	switch d.darkMode {
	case "smart":
		img = smartInvert(img)
	case "invert":
		img = simpleInvert(img)
	}

	bounds := img.Bounds()
	fullH := bounds.Dy()
	fullW := bounds.Dx()

	// Crop enough to fill termHeight; ideally 55%, but more if DPI was clamped.
	targetCropPixels := float64(termHeight) * pixelsPerLine
	targetCropH := int(targetCropPixels)
	cropH := fullH * 55 / 100
	if cropH < targetCropH && targetCropH <= fullH {
		cropH = targetCropH
	}
	if cropH <= 0 || cropH > fullH {
		cropH = fullH
	}

	var cropY int
	if isBottom {
		cropY = fullH - cropH
	}

	// Crop
	cropped := image.NewRGBA(image.Rect(0, 0, fullW, cropH))
	draw.Draw(cropped, cropped.Bounds(), img, image.Pt(bounds.Min.X, bounds.Min.Y+cropY), draw.Src)

	// Apply user crops: only outer edge (not the inner split)
	var userTop, userBottom float64
	if isBottom {
		userBottom = d.cropBottom // outer edge for bottom half
	} else {
		userTop = d.cropTop // outer edge for top half
	}
	var croppedImg image.Image = cropped
	croppedImg = cropImage(croppedImg, userTop, userBottom, d.cropLeft, d.cropRight)

	// Save
	if err := os.MkdirAll(d.tempDir, 0o755); err != nil {
		return 0
	}
	imagePath := filepath.Join(d.tempDir, "half.png")
	file, err := os.Create(imagePath)
	if err != nil {
		return 0
	}
	if err := png.Encode(file, croppedImg); err != nil {
		file.Close()
		os.Remove(imagePath)
		return 0
	}
	file.Close()
	defer os.Remove(imagePath)

	cb := croppedImg.Bounds()
	finalW := cb.Dx()
	finalH := cb.Dy()

	actualLines := int(float64(finalH)/pixelsPerLine) + 1
	if actualLines > termHeight {
		actualLines = termHeight
	}
	imageWidthInChars := int(float64(finalW)/pixelsPerChar) + 1

	horizontalOffset := (termWidth - imageWidthInChars) / 2
	if horizontalOffset < 0 {
		horizontalOffset = 0
	}

	return d.renderWithTermImg(imagePath, actualLines, horizontalOffset, imageWidthInChars, finalW, finalH, termType)
}

func (d *DocumentViewer) renderWithTermImg(imagePath string, estimatedLines int, horizontalOffset int, widthChars int, pixelWidth int, pixelHeight int, termType string) int {
	if horizontalOffset > 0 {
		fmt.Printf("\033[%dC", horizontalOffset) // Move cursor right
	}

	// Use termimg fluent API to control size in terminal cells
	img, err := termimg.Open(imagePath)
	if err != nil {
		return 0
	}

	// Choose rendering strategy based on terminal type:
	// - Kitty uses native graphics protocol with character-based dimensions
	// - Sixel terminals need pixel-based dimensions for proper scaling
	if termType == "kitty" {
		// Kitty: use character-based dimensions with ScaleNone
		err = img.Width(widthChars).Height(estimatedLines).Scale(termimg.ScaleNone).Print()
	} else {
		// Sixel terminals (Foot, xterm, etc.): use pixel-based dimensions with ScaleFit
		err = img.WidthPixels(pixelWidth).HeightPixels(pixelHeight).Scale(termimg.ScaleFit).Print()
	}

	if err != nil {
		return 0
	}

	return estimatedLines
}

// smartInvert inverts lightness while preserving hue and saturation.
// White backgrounds become black, black text becomes white, colors keep their hue.
func smartInvert(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			r8 := float64(r>>8) / 255.0
			g8 := float64(g>>8) / 255.0
			b8 := float64(b>>8) / 255.0

			h, s, l := rgbToHSL(r8, g8, b8)
			l = 0.12 + (1.0-l)*0.88 // invert lightness; dark gray bg instead of pure black
			nr, ng, nb := hslToRGB(h, s, l)

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

// simpleInvert does a full RGB color inversion with the same gray background shift.
func simpleInvert(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := src.At(x, y).RGBA()
			// Invert and remap to gray bg range: 255→30, 0→255
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

func rgbToHSL(r, g, b float64) (h, s, l float64) {
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

func hslToRGB(h, s, l float64) (r, g, b float64) {
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

	r = hueToRGB(p, q, h+1.0/3.0)
	g = hueToRGB(p, q, h)
	b = hueToRGB(p, q, h-1.0/3.0)
	return
}

func hueToRGB(p, q, t float64) float64 {
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
