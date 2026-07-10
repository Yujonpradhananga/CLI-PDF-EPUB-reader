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

// fastPNG encodes rendered pages with the fastest compression. These PNGs are
// short-lived local temp files handed straight to the terminal, so encode speed
// matters far more than file size (BestSpeed is ~3-5x faster than default).
var fastPNG = png.Encoder{CompressionLevel: png.BestSpeed}

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

// saveEphemeralPNG writes a per-frame PNG for the dual/half display paths and
// deletes the previous frame's file. With t=f transmission the terminal opens
// the file only when it processes the escape sequence, so the current frame's
// file must outlive renderWithTermImg (no immediate delete); the previous
// frame's file has certainly been consumed by then.
func (d *DocumentViewer) saveEphemeralPNG(prefix string, img image.Image) (string, error) {
	if err := os.MkdirAll(d.tempDir, 0o755); err != nil {
		return "", err
	}
	d.ephemeralSeq++
	imagePath := filepath.Join(d.tempDir, fmt.Sprintf("%s_%d.png", prefix, d.ephemeralSeq))
	file, err := os.Create(imagePath)
	if err != nil {
		return "", err
	}
	if err := fastPNG.Encode(file, img); err != nil {
		file.Close()
		os.Remove(imagePath)
		return "", err
	}
	file.Close()
	if d.lastEphemeralPath != "" {
		os.Remove(d.lastEphemeralPath)
	}
	d.lastEphemeralPath = imagePath
	return imagePath, nil
}

// clickTarget maps a pixel rectangle of the transmitted image back to a page
// sub-region: pixels [x0,x1)x[y0,y1) of the image show the fraction band
// [fx0,fx1]x[fy0,fy1] of a page whose box is pageW x pageH PDF points.
type clickTarget struct {
	page0              int // 0-indexed PDF page
	x0, y0, x1, y1     float64
	pageW, pageH       float64
	fx0, fx1, fy0, fy1 float64
}

// clickMap records where the last-drawn image sits on screen (cells) and which
// page regions its pixels show, so an Opt+click cell can be mapped back to PDF
// points for synctex (see handleAltClick). Rebuilt on every image display and
// zeroed at the top of displayCurrentPage, so text pages have no map.
type clickMap struct {
	originCol, originRow int // 1-based cell of the image's top-left corner
	cols, rows           int // cell box the terminal scales the image into
	pxW, pxH             float64
	targets              []clickTarget
}

// cellToPDF maps a 1-based terminal cell to (page, x, y) in PDF points with
// top-left origin. The kitty graphics protocol stretches the transmitted image
// to exactly cols x rows cells, so a fraction of the cell box equals the same
// fraction of the image pixels; the cell center stands in for the unknowable
// sub-cell click position. ok is false outside the displayed image (or inside
// the gap between dual-mode pages).
func (m *clickMap) cellToPDF(col, row int) (page0 int, x, y float64, ok bool) {
	if m.cols <= 0 || m.rows <= 0 || m.pxW <= 0 || m.pxH <= 0 {
		return 0, 0, 0, false
	}
	fx := (float64(col-m.originCol) + 0.5) / float64(m.cols)
	fy := (float64(row-m.originRow) + 0.5) / float64(m.rows)
	if fx < 0 || fx >= 1 || fy < 0 || fy >= 1 {
		return 0, 0, 0, false
	}
	px := fx * m.pxW
	py := fy * m.pxH
	for _, t := range m.targets {
		if px < t.x0 || px >= t.x1 || py < t.y0 || py >= t.y1 {
			continue
		}
		gx := t.fx0 + (px-t.x0)/(t.x1-t.x0)*(t.fx1-t.fx0)
		gy := t.fy0 + (py-t.y0)/(t.y1-t.y0)*(t.fy1-t.fy0)
		x = math.Min(math.Max(gx*t.pageW, 0), t.pageW)
		y = math.Min(math.Max(gy*t.pageH, 0), t.pageH)
		return t.page0, x, y, true
	}
	return 0, 0, 0, false
}

// setPageClickMap records a whole-image single-page render for Opt+click:
// the image's pixels show the fraction band [fx0,fx1]x[fy0,fy1] of pageNum.
// Standalone images have no source to sync to, so the map stays cleared.
func (d *DocumentViewer) setPageClickMap(pageNum, originCol, originRow, rows, cols, pxW, pxH int, fx0, fx1, fy0, fy1 float64) {
	d.clickMap = clickMap{}
	if d.isImage || d.doc == nil {
		return
	}
	r, err := d.doc.Bound(pageNum)
	if err != nil || r.Dx() <= 0 || r.Dy() <= 0 {
		return
	}
	d.clickMap = clickMap{
		originCol: originCol, originRow: originRow,
		cols: cols, rows: rows,
		pxW: float64(pxW), pxH: float64(pxH),
		targets: []clickTarget{{
			page0: pageNum,
			x1:    float64(pxW), y1: float64(pxH),
			pageW: float64(r.Dx()), pageH: float64(r.Dy()),
			fx0: fx0, fx1: fx1, fy0: fy0, fy1: fy1,
		}},
	}
}

func (d *DocumentViewer) renderPageImage(pageNum, maxWidth, maxHeight int) int {
	return d.renderPageImageAligned(pageNum, maxWidth, maxHeight, "center")
}

func (d *DocumentViewer) renderPageImageAligned(pageNum, maxWidth, maxHeight int, align string) int {
	if maxHeight <= 0 {
		return 0
	}

	termType := d.detectTerminalType()
	rp := d.snapshotParams() // main thread: safe to read viewer state
	imagePath, actualHeight, imageWidthInChars, actualPixelWidth, actualPixelHeight, err := d.savePageAsImage(pageNum, maxWidth, maxHeight, termType, rp)
	if err != nil {
		return 0
	}
	// Note: imagePath is owned by the render cache (evicted later); do not delete here.

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

	// Opt+click map. Both callers (displayImagePage, displayMixedPage) position
	// the cursor at row 2, column 1 before rendering, so the image's top-left
	// cell is (row 2, col 1+offset). The visible band of the page is the whole
	// page minus the user crop fractions (cropImage in savePageAsImage).
	d.setPageClickMap(pageNum, 1+horizontalOffset, 2, actualHeight, imageWidthInChars,
		actualPixelWidth, actualPixelHeight,
		rp.cropLeft, 1-rp.cropRight, rp.cropTop, 1-rp.cropBottom)

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
			i := (y * dst.Stride) + x*4
			dst.Pix[i+0] = uint8(r >> 8)
			dst.Pix[i+1] = uint8(g >> 8)
			dst.Pix[i+2] = uint8(b >> 8)
			dst.Pix[i+3] = uint8(a >> 8)
		}
	}
	return dst
}

func (d *DocumentViewer) savePageAsImage(pageNum, termWidth, termHeight int, termType string, rp renderParams) (string, int, int, int, int, error) {
	if err := os.MkdirAll(d.cacheDir, 0o755); err != nil {
		return "", 0, 0, 0, 0, err
	}

	// Serve from the render cache when nothing affecting the output changed.
	sig := renderSig(pageNum, termWidth, termHeight, termType, rp)
	if c, ok := d.cacheGet(sig); ok {
		return c.path, c.lines, c.widthChars, c.pxW, c.pxH, nil
	}

	pixelsPerChar, pixelsPerLine := rp.pixelsPerChar, rp.pixelsPerLine

	// Supersample for the kitty graphics protocol (kitty / ghostty / agterm). agterm and
	// ghostty report logical (1x) pixels via TIOCGWINSZ, so a page rendered to match that
	// size is upscaled by the terminal on a retina display -> blurry. Render at 2x and keep
	// the cell footprint logical (divide it back out below); the terminal scales the hi-res
	// image into the same cells -> crisp. No dependence on DOCVIEWER_CELL_SIZE.
	superSample := 1.0
	if termType == "kitty" {
		superSample = 2.0
	}

	// Calculate target pixel dimensions based on terminal size
	horizontalPadding := 4
	verticalPadding := 3
	effectiveWidth := termWidth - horizontalPadding
	effectiveHeight := termHeight - verticalPadding

	// Apply user scale factor
	scale := rp.scaleFactor
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
		// Page box in points == pixels at 72 DPI. Bound() just queries geometry;
		// it avoids a full throwaway render of the page on every switch.
		r, err := d.doc.Bound(pageNum)
		if err != nil {
			return "", 0, 0, 0, 0, err
		}
		pageWidthAt72 = r.Dx()
		pageHeightAt72 = r.Dy()
		if pageWidthAt72 <= 0 || pageHeightAt72 <= 0 {
			return "", 0, 0, 0, 0, fmt.Errorf("invalid page bounds for page %d", pageNum)
		}
		aspectRatio = float64(pageHeightAt72) / float64(pageWidthAt72)
	}

	// Calculate final dimensions based on fit mode
	var finalWidth, finalHeight int
	switch rp.fitMode {
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
		dpi *= superSample // render hi-res; footprint stays logical (divided back out below)

		// Clamp DPI to reasonable range
		if dpi < 36 {
			dpi = 36
		}
		maxDPI := 300.0
		if termType != "kitty" {
			maxDPI = 100.0
		}
		maxDPI *= superSample
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
	switch rp.darkMode {
	case "smart":
		finalImg = smartInvert(img)
	case "invert":
		finalImg = simpleInvert(img)
	}

	// Apply user crop
	finalImg = cropImage(finalImg, rp.cropTop, rp.cropBottom, rp.cropLeft, rp.cropRight)

	bounds := finalImg.Bounds()
	actualWidth := bounds.Dx()
	actualHeight := bounds.Dy()

	// Divide the supersample factor back out so the cell footprint stays logical
	// (the image has superSample x more pixels than its on-screen cell area).
	actualLines := int(float64(actualHeight)/pixelsPerLine/superSample) + 1
	if actualLines > termHeight {
		actualLines = termHeight
	}

	imageWidthInChars := int(float64(actualWidth)/pixelsPerChar/superSample) + 1

	// Per-signature filename in the shared persistent cache dir. Write via
	// tmp+rename so a concurrent viewer process rendering the same signature
	// can't interleave writes into a torn PNG.
	imagePath := d.cachePath(sig)
	tmpPath := fmt.Sprintf("%s.tmp%d", imagePath, os.Getpid())

	file, err := os.Create(tmpPath)
	if err != nil {
		return "", 0, 0, 0, 0, err
	}

	if err = fastPNG.Encode(file, finalImg); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return "", 0, 0, 0, 0, err
	}
	file.Close()

	c := cachedRender{
		path:       imagePath,
		lines:      actualLines,
		widthChars: imageWidthInChars,
		pxW:        actualWidth,
		pxH:        actualHeight,
	}
	// Meta before rename: readers require the PNG to exist first, so a sidecar
	// without its PNG is just a miss (and swept by the GC), never a bad read.
	writeMeta(imagePath, c)
	if err := os.Rename(tmpPath, imagePath); err != nil {
		os.Remove(tmpPath)
		return "", 0, 0, 0, 0, err
	}

	d.cacheStore(sig, c)

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
		// Page box in points == pixels at 72 DPI; avoids a throwaway full render.
		r, err := d.doc.Bound(pageNum)
		if err != nil {
			return nil, err
		}
		pageWidthAt72 = r.Dx()
		pageHeightAt72 = r.Dy()
		if pageWidthAt72 <= 0 || pageHeightAt72 <= 0 {
			return nil, fmt.Errorf("invalid page bounds for page %d", pageNum)
		}
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

// dualClickTarget builds the Opt+click target for one page of a dual-mode
// composite: the page image occupies pixel rect [x0,x1)x[y0,y1) of the
// composite and shows the page minus the user crop fractions (cropImage is
// applied per page in renderDualComposite).
func (d *DocumentViewer) dualClickTarget(pageNum, x0, y0, x1, y1 int) (clickTarget, bool) {
	if d.isImage || d.doc == nil {
		return clickTarget{}, false
	}
	r, err := d.doc.Bound(pageNum)
	if err != nil || r.Dx() <= 0 || r.Dy() <= 0 {
		return clickTarget{}, false
	}
	return clickTarget{
		page0: pageNum,
		x0:    float64(x0), y0: float64(y0), x1: float64(x1), y1: float64(y1),
		pageW: float64(r.Dx()), pageH: float64(r.Dy()),
		fx0: d.cropLeft, fx1: 1 - d.cropRight,
		fy0: d.cropTop, fy1: 1 - d.cropBottom,
	}, true
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
	var targets []clickTarget

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
		if t, ok := d.dualClickTarget(page1, x1, 0, x1+b1.Dx(), b1.Dy()); ok {
			targets = append(targets, t)
		}

		if page2Img != nil {
			b2 := page2Img.Bounds()
			x2 := (compositeW - b2.Dx()) / 2
			y2 := b1.Dy() + gap
			draw.Draw(composite, image.Rect(x2, y2, x2+b2.Dx(), y2+b2.Dy()), page2Img, b2.Min, draw.Over)
			if t, ok := d.dualClickTarget(page2, x2, y2, x2+b2.Dx(), y2+b2.Dy()); ok {
				targets = append(targets, t)
			}
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
		if t, ok := d.dualClickTarget(page1, 0, y1, b1.Dx(), y1+b1.Dy()); ok {
			targets = append(targets, t)
		}

		if page2Img != nil {
			b2 := page2Img.Bounds()
			x2 := b1.Dx() + gap
			y2 := (compositeH - b2.Dy()) / 2
			draw.Draw(composite, image.Rect(x2, y2, x2+b2.Dx(), y2+b2.Dy()), page2Img, b2.Min, draw.Over)
			if t, ok := d.dualClickTarget(page2, x2, y2, x2+b2.Dx(), y2+b2.Dy()); ok {
				targets = append(targets, t)
			}
		}
	}

	// Save composite
	imagePath, err := d.saveEphemeralPNG("dual", composite)
	if err != nil {
		return 0
	}

	actualLines := int(float64(compositeH)/pixelsPerLine) + 1
	if actualLines > termHeight {
		actualLines = termHeight
	}
	imageWidthInChars := int(float64(compositeW)/pixelsPerChar) + 1

	horizontalOffset := (termWidth - imageWidthInChars) / 2
	if horizontalOffset < 0 {
		horizontalOffset = 0
	}

	// Opt+click map: displayDualVertical/Horizontal position the cursor at
	// row 1, column 1 before rendering. Clicks in the gap between the two
	// page targets fall through to "no target" and are ignored.
	d.clickMap = clickMap{
		originCol: 1 + horizontalOffset, originRow: 1,
		cols: imageWidthInChars, rows: actualLines,
		pxW: float64(compositeW), pxH: float64(compositeH),
		targets: targets,
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
		// Page box in points == pixels at 72 DPI; avoids a throwaway full render.
		r, err := d.doc.Bound(pageNum)
		if err != nil {
			return 0
		}
		pageHeightAt72 := r.Dy()
		if pageHeightAt72 <= 0 {
			return 0
		}

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
	imagePath, err := d.saveEphemeralPNG("half", croppedImg)
	if err != nil {
		return 0
	}

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

	// Opt+click map: displayHalfPage positions the cursor at row 1, column 1.
	// The displayed image shows the vertical band [cropY, cropY+cropH] of the
	// full-page render (fullH px tall), narrowed by the user's outer-edge crop
	// — that band expressed as page-height fractions is [fy0, fy1].
	fy0 := (float64(cropY) + float64(cropH)*userTop) / float64(fullH)
	fy1 := (float64(cropY) + float64(cropH)*(1-userBottom)) / float64(fullH)
	d.setPageClickMap(pageNum, 1+horizontalOffset, 1, actualLines, imageWidthInChars,
		finalW, finalH, d.cropLeft, 1-d.cropRight, fy0, fy1)

	return d.renderWithTermImg(imagePath, actualLines, horizontalOffset, imageWidthInChars, finalW, finalH, termType)
}

func (d *DocumentViewer) renderWithTermImg(imagePath string, estimatedLines int, horizontalOffset int, widthChars int, pixelWidth int, pixelHeight int, termType string) int {
	if horizontalOffset > 0 {
		fmt.Printf("\033[%dC", horizontalOffset) // Move cursor right
	}

	// Kitty path: hand the terminal the PNG already on disk (see kitty.go) instead
	// of going through termimg, which decodes the PNG and retransmits it as raw
	// RGBA base64 — tens of MB through the PTY per page display in agterm/ghostty.
	if termType == "kitty" {
		newID := nextKittyImageID()
		if err := kittySendPNG(imagePath, newID, widthChars, estimatedLines); err != nil {
			return 0
		}
		// Flicker-free swap: the new image was just placed over the old one, so
		// now delete the PREVIOUS image by id. Drawing-then-deleting (rather than
		// the old delete-all-then-draw) means a reload never blanks the screen
		// while the new page transmits. d=I also frees the old image's data, so a
		// long LaTeX session doesn't accumulate images in terminal memory.
		if d.lastKittyImageID != 0 && d.lastKittyImageID != newID {
			fmt.Printf("\033_Ga=d,d=I,i=%d\033\\", d.lastKittyImageID)
		}
		d.lastKittyImageID = newID
		return estimatedLines
	}

	// Sixel terminals (Foot, xterm, etc.): pixel-based dimensions with ScaleFit
	img, err := termimg.Open(imagePath)
	if err != nil {
		return 0
	}
	if err := img.WidthPixels(pixelWidth).HeightPixels(pixelHeight).Scale(termimg.ScaleFit).Print(); err != nil {
		return 0
	}
	return estimatedLines
}

// toRGBACopy returns a fresh *image.RGBA copy of src with zero-origin bounds.
// Always copies: the invert functions mutate the returned pixels in place, and
// src may be a shared image (d.sourceImage) that must stay pristine.
func toRGBACopy(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// smartInvert inverts lightness while preserving hue and saturation.
// White backgrounds become black, black text becomes white, colors keep their hue.
// Grayscale pixels (r==g==b — virtually all of a rendered PDF page) go through a
// 256-entry LUT; only colored pixels pay the float HSL round trip. Direct Pix
// access instead of At()/Set() keeps this fast on ~12 MP supersampled pages.
func smartInvert(src image.Image) image.Image {
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
		h, s, l := rgbToHSL(float64(r)/255.0, float64(g)/255.0, float64(b)/255.0)
		l = 0.12 + (1.0-l)*0.88
		nr, ng, nb := hslToRGB(h, s, l)
		pix[i] = uint8(nr * 255)
		pix[i+1] = uint8(ng * 255)
		pix[i+2] = uint8(nb * 255)
	}
	return rgba
}

// simpleInvert does a full RGB color inversion with the same gray background shift.
func simpleInvert(src image.Image) image.Image {
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
