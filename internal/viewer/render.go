package viewer

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"

	"github.com/blacktop/go-termimg"

	"pdf-cli/internal/imgutil"
)

func (d *DocumentViewer) renderPageImage(pageNum, maxWidth, maxHeight int) int {
	if maxHeight <= 0 {
		return 0
	}

	termType := d.detectTerminalType()
	imagePath, actualHeight, imageWidthInChars, err := d.savePageAsImage(pageNum, maxWidth, maxHeight, termType)
	if err != nil {
		return 0
	}
	defer os.Remove(imagePath)

	horizontalOffset := (maxWidth - imageWidthInChars) / 2
	if horizontalOffset < 0 {
		horizontalOffset = 0
	}

	return d.renderWithTermImg(imagePath, actualHeight, horizontalOffset, imageWidthInChars, termType)
}

func (d *DocumentViewer) savePageAsImage(pageNum, termWidth, termHeight int, termType string) (string, int, int, error) {
	if err := os.MkdirAll(d.tempDir, 0o755); err != nil {
		return "", 0, 0, err
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

	// Get page dimensions at 72 DPI to calculate proper render DPI
	testImg, err := d.doc.ImageDPI(pageNum, 72.0)
	if err != nil {
		return "", 0, 0, err
	}
	testBounds := testImg.Bounds()
	pageWidthAt72 := testBounds.Dx()
	pageHeightAt72 := testBounds.Dy()
	aspectRatio := float64(pageHeightAt72) / float64(pageWidthAt72)

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
	// Kitty handles high-res images natively; Sixel terminals need lower DPI
	maxDPI := 300.0
	if termType != "kitty" {
		maxDPI = 150.0
	}
	if dpi > maxDPI {
		dpi = maxDPI
	}

	// Render at calculated DPI - no resizing needed
	img, err := d.doc.ImageDPI(pageNum, dpi)
	if err != nil {
		return "", 0, 0, err
	}

	bounds := img.Bounds()
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
		return "", 0, 0, err
	}
	defer file.Close()

	err = png.Encode(file, img)
	if err != nil {
		os.Remove(imagePath)
		return "", 0, 0, err
	}

	return imagePath, actualLines, imageWidthInChars, nil
}

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

	testImg, err := d.doc.ImageDPI(pageNum, 72.0)
	if err != nil {
		return nil, err
	}
	testBounds := testImg.Bounds()
	pageWidthAt72 := testBounds.Dx()
	pageHeightAt72 := testBounds.Dy()
	aspectRatio := float64(pageHeightAt72) / float64(pageWidthAt72)

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

	img, err := d.doc.ImageDPI(pageNum, dpi)
	if err != nil {
		return nil, err
	}

	switch d.darkMode {
	case "smart":
		return imgutil.SmartInvert(img), nil
	case "invert":
		return imgutil.SimpleInvert(img), nil
	}
	return img, nil
}

func (d *DocumentViewer) renderDualComposite(page1, page2 int, hasPage2 bool, termWidth, termHeight int, layout string, gap int) int {
	if termHeight <= 0 {
		return 0
	}

	termType := d.detectTerminalType()
	pixelsPerChar, pixelsPerLine := d.getTerminalCellSize()

	var img1W, img2W int
	var img1H, img2H int

	if layout == "vertical" {
		img1H = termHeight / 2
		img2H = img1H
		img1W = termWidth
		img2W = termWidth
	} else {
		img1W = termWidth / 2
		img2W = termWidth - img1W
		img1H = termHeight
		img2H = termHeight
	}

	page1Img, err := d.renderPageToImage(page1, img1W, img1H, termType)
	if err != nil {
		return 0
	}
	page1Img = imgutil.CropImage(page1Img, d.cropTop, d.cropBottom, d.cropLeft, d.cropRight)

	var page2Img image.Image
	if hasPage2 {
		page2Img, err = d.renderPageToImage(page2, img2W, img2H, termType)
		if err != nil {
			return 0
		}
		page2Img = imgutil.CropImage(page2Img, d.cropTop, d.cropBottom, d.cropLeft, d.cropRight)
	}

	b1 := page1Img.Bounds()

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

		bgColor := color.RGBA{255, 255, 255, 255}
		if d.darkMode != "" {
			bgColor = color.RGBA{30, 30, 30, 255}
		}
		composite = image.NewRGBA(image.Rect(0, 0, compositeW, compositeH))
		draw.Draw(composite, composite.Bounds(), &image.Uniform{bgColor}, image.Point{}, draw.Src)

		x1 := (compositeW - b1.Dx()) / 2
		draw.Draw(composite, image.Rect(x1, 0, x1+b1.Dx(), b1.Dy()), page1Img, b1.Min, draw.Over)

		if page2Img != nil {
			b2 := page2Img.Bounds()
			x2 := (compositeW - b2.Dx()) / 2
			y2 := b1.Dy() + gap
			draw.Draw(composite, image.Rect(x2, y2, x2+b2.Dx(), y2+b2.Dy()), page2Img, b2.Min, draw.Over)
		}
	} else {
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

		y1 := (compositeH - b1.Dy()) / 2
		draw.Draw(composite, image.Rect(0, y1, b1.Dx(), y1+b1.Dy()), page1Img, b1.Min, draw.Over)

		if page2Img != nil {
			b2 := page2Img.Bounds()
			x2 := b1.Dx() + gap
			y2 := (compositeH - b2.Dy()) / 2
			draw.Draw(composite, image.Rect(x2, y2, x2+b2.Dx(), y2+b2.Dy()), page2Img, b2.Min, draw.Over)
		}
	}

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

	return d.renderWithTermImg(imagePath, actualLines, horizontalOffset, imageWidthInChars, termType)
}

func (d *DocumentViewer) renderHalfPage(pageNum, termWidth, termHeight int, isBottom bool) int {
	if termHeight <= 0 {
		return 0
	}

	termType := d.detectTerminalType()
	pixelsPerChar, pixelsPerLine := d.getTerminalCellSize()

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
	if dpi > 300 {
		dpi = 300
	}

	rawImg, err := d.doc.ImageDPI(pageNum, dpi)
	if err != nil {
		return 0
	}

	var img image.Image = rawImg
	switch d.darkMode {
	case "smart":
		img = imgutil.SmartInvert(rawImg)
	case "invert":
		img = imgutil.SimpleInvert(rawImg)
	}

	bounds := img.Bounds()
	fullH := bounds.Dy()
	fullW := bounds.Dx()

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

	cropped := image.NewRGBA(image.Rect(0, 0, fullW, cropH))
	draw.Draw(cropped, cropped.Bounds(), img, image.Pt(bounds.Min.X, bounds.Min.Y+cropY), draw.Src)

	var userTop, userBottom float64
	if isBottom {
		userBottom = d.cropBottom
	} else {
		userTop = d.cropTop
	}
	var croppedImg image.Image = cropped
	croppedImg = imgutil.CropImage(croppedImg, userTop, userBottom, d.cropLeft, d.cropRight)

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

	return d.renderWithTermImg(imagePath, actualLines, horizontalOffset, imageWidthInChars, termType)
}

func (d *DocumentViewer) renderWithTermImg(imagePath string, estimatedLines int, horizontalOffset int, widthChars int, termType string) int {
	if horizontalOffset > 0 {
		fmt.Printf("\033[%dC", horizontalOffset) // Move cursor right
	}

	img, err := termimg.Open(imagePath)
	if err != nil {
		return 0
	}

	if termType == "kitty" {
		// Kitty protocol handles Width/Height in cells natively
		err = img.Width(widthChars).Height(estimatedLines).Scale(termimg.ScaleNone).Print()
	} else {
		// For Sixel/iTerm2: don't set Width/Height — the Sixel encoder
		// would resize using its own font metrics, causing misalignment.
		// The image is already at the correct pixel size from our DPI calculation.
		err = img.Scale(termimg.ScaleNone).Print()
	}
	if err != nil {
		return 0
	}

	return estimatedLines
}
