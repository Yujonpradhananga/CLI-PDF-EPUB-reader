package viewer

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"

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

// hasPage reports whether the given 0-indexed PDF page is among the pages the
// current clickMap covers, i.e. is displayed on screen right now.
func (m *clickMap) hasPage(page0 int) bool {
	for _, t := range m.targets {
		if t.page0 == page0 {
			return true
		}
	}
	return false
}

// pdfToCell maps (page0, x, y) — PDF points, top-left origin — to the 1-based
// terminal cell showing that point: the inverse of cellToPDF, walking the same
// targets, crop fractions, and cell box. ok is false when the map is empty
// (text page), the page is not among the displayed targets, or the point lies
// outside the visible band (cropped away / other half in half-page mode).
func (m *clickMap) pdfToCell(page0 int, x, y float64) (col, row int, ok bool) {
	if m.cols <= 0 || m.rows <= 0 || m.pxW <= 0 || m.pxH <= 0 {
		return 0, 0, false
	}
	for _, t := range m.targets {
		if t.page0 != page0 || t.pageW <= 0 || t.pageH <= 0 {
			continue
		}
		if t.fx1 <= t.fx0 || t.fy1 <= t.fy0 {
			continue
		}
		gx := x / t.pageW
		gy := y / t.pageH
		if gx < t.fx0 || gx > t.fx1 || gy < t.fy0 || gy > t.fy1 {
			continue
		}
		px := t.x0 + (gx-t.fx0)/(t.fx1-t.fx0)*(t.x1-t.x0)
		py := t.y0 + (gy-t.fy0)/(t.fy1-t.fy0)*(t.y1-t.y0)
		col = m.originCol + int(px/m.pxW*float64(m.cols))
		row = m.originRow + int(py/m.pxH*float64(m.rows))
		// A point exactly on the band's far edge computes one cell past the
		// box; clamp back inside.
		col = min(col, m.originCol+m.cols-1)
		row = min(row, m.originRow+m.rows-1)
		return col, row, true
	}
	return 0, 0, false
}

// markerCell places a margin marker for a point at height y (PDF points,
// top-left origin) on page page0: the row showing that point, and the column
// just outside the image on the side that page sits on — the left margin for
// the left page of a side-by-side spread, the right margin for anything else,
// since the page image covers the cells it occupies (see drawFlash). ok is
// false when the page is not on screen, or when y is hidden — cropped away, or
// in the half a half-page view is not showing.
func (m *clickMap) markerCell(page0 int, y float64, termWidth int) (col, row int, ok bool) {
	for _, t := range m.targets {
		if t.page0 != page0 {
			continue
		}
		// Any x inside the visible band gives the same row; take its middle.
		_, row, ok = m.pdfToCell(page0, (t.fx0+t.fx1)/2*t.pageW, y)
		if !ok {
			return 0, 0, false
		}
		col = m.originCol + m.cols
		if t.x1 <= m.pxW/2 {
			col = m.originCol - 1
		}
		return min(max(col, 1), termWidth), row, true
	}
	return 0, 0, false
}

// cellSizePDF reports the page-point footprint of one terminal cell for the
// target showing page0. Click-resolution tolerances (findLinkAt) must track
// it: dual and half layouts change how much page a cell covers, and a
// tolerance tuned for single-page cells makes thin links unhittable there.
func (m *clickMap) cellSizePDF(page0 int) (w, h float64, ok bool) {
	if m.cols <= 0 || m.rows <= 0 || m.pxW <= 0 || m.pxH <= 0 {
		return 0, 0, false
	}
	for _, t := range m.targets {
		if t.page0 != page0 || t.x1 <= t.x0 || t.y1 <= t.y0 {
			continue
		}
		w = m.pxW / float64(m.cols) / (t.x1 - t.x0) * (t.fx1 - t.fx0) * t.pageW
		h = m.pxH / float64(m.rows) / (t.y1 - t.y0) * (t.fy1 - t.fy0) * t.pageH
		return w, h, true
	}
	return 0, 0, false
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

// maxRenderPixels bounds a single rasterized page. The supersample factor is
// backed off rather than exceeding it, so a fullscreen window can't turn one
// page into hundreds of megabytes of RGBA plus the PNG encode that follows.
const maxRenderPixels = 24e6

// defaultSuperSample is how many times larger than its on-screen cell footprint
// a page is rasterized for the kitty graphics protocol (kitty / ghostty /
// agterm). The terminal scales the result back into the same cells, so the extra
// pixels cost nothing on screen but everything in render and PNG-encode time,
// which grows with the square of this. agterm and ghostty report logical (1x)
// pixels via TIOCGWINSZ, so on a retina panel the cell box is twice the pixels
// we size the render against and a factor of 1 leaves the terminal upscaling —
// that is what reads as soft. 1.6 clears the box with enough margin to absorb
// the cell footprint being rounded up a cell in each axis; past that the gain is
// invisible and only the wait grows.
const defaultSuperSample = 1.6

// superSampleCeiling is the factor before the maxRenderPixels backoff.
// renderSig includes it, so changing DOCVIEWER_SUPERSAMPLE re-renders instead of
// serving pages the persistent cache rasterized at the old factor.
func superSampleCeiling() float64 {
	if env := os.Getenv("DOCVIEWER_SUPERSAMPLE"); env != "" {
		if v, err := strconv.ParseFloat(env, 64); err == nil && v >= 1 && v <= 8 {
			return v
		}
	}
	return defaultSuperSample
}

// superSampleFactor returns the factor to rasterize with. targetW/targetH are
// the 1x pixel dimensions, used only for the maxRenderPixels backoff.
func superSampleFactor(termType string, targetW, targetH int) float64 {
	if termType != "kitty" {
		return 1.0
	}
	ss := superSampleCeiling()
	if px := float64(targetW) * float64(targetH); px > 0 {
		if lim := math.Sqrt(maxRenderPixels / px); lim < ss {
			ss = lim
		}
	}
	if ss < 1 {
		ss = 1
	}
	return ss
}

// imageSuperSample caps the factor to the detail a standalone image file
// actually holds: scaling a photo past its own resolution burns pixels without
// adding sharpness, and scaleImage samples nearest-neighbour.
func imageSuperSample(src image.Image, targetW int, ss float64) float64 {
	if src == nil || targetW <= 0 {
		return 1
	}
	if avail := float64(src.Bounds().Dx()) / float64(targetW); avail < ss {
		ss = avail
	}
	if ss < 1 {
		ss = 1
	}
	return ss
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

	// Rasterize above the cell footprint; the footprint stays logical (the factor
	// is divided back out below) and the terminal scales the hi-res image down.
	superSample := superSampleFactor(termType, finalWidth, finalHeight)

	// Get the image at final dimensions
	var img image.Image
	if d.isImage {
		superSample = imageSuperSample(d.sourceImage, finalWidth, superSample)
		img = scaleImage(d.sourceImage, int(float64(finalWidth)*superSample), int(float64(finalHeight)*superSample))
	} else {
		// Calculate DPI needed to render at exactly the right size
		dpiForWidth := float64(finalWidth) / float64(pageWidthAt72) * 72.0
		dpiForHeight := float64(finalHeight) / float64(pageHeightAt72) * 72.0
		dpi := dpiForWidth
		if dpiForHeight < dpi {
			dpi = dpiForHeight
		}
		dpi *= superSample

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
	case "dim":
		finalImg = dimPage(img)
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

// renderPageToImage renders a page to an in-memory image at the given terminal
// dimensions, rasterized superSample times larger. The caller composites the
// result and divides the factor back out of the cell footprint, so it passes the
// same factor for every page of one composite.
func (d *DocumentViewer) renderPageToImage(pageNum, termWidth, termHeight int, termType string, superSample float64) (image.Image, error) {
	pixelsPerChar, pixelsPerLine := d.getTerminalCellSize()

	horizontalPadding := 4
	verticalPadding := 3
	effectiveWidth := termWidth - horizontalPadding
	effectiveHeight := termHeight - verticalPadding

	scale := d.zoom()

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
		// Not capped to the source resolution here (unlike the single-page path):
		// the caller divides one shared factor back out of the composite, so every
		// page of it has to come back at exactly that scale.
		img = scaleImage(d.sourceImage, int(float64(finalWidth)*superSample), int(float64(finalHeight)*superSample))
	} else {
		dpiForWidth := float64(finalWidth) / float64(pageWidthAt72) * 72.0
		dpiForHeight := float64(finalHeight) / float64(pageHeightAt72) * 72.0
		dpi := dpiForWidth
		if dpiForHeight < dpi {
			dpi = dpiForHeight
		}
		dpi *= superSample

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
	case "dim":
		return dimPage(img), nil
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

	// One factor for the whole composite, sized against the full terminal area:
	// the two pages must come back at the same scale to be composited, and the
	// footprint below divides it back out once.
	superSample := superSampleFactor(termType,
		int(float64(termWidth)*pixelsPerChar), int(float64(termHeight)*pixelsPerLine))
	gap = int(float64(gap) * superSample) // keep the separator its old on-screen width

	page1Img, err := d.renderPageToImage(page1, img1W, img1H, termType, superSample)
	if err != nil {
		return 0
	}
	page1Img = cropImage(page1Img, d.cropTop, d.cropBottom, d.cropLeft, d.cropRight)

	var page2Img image.Image
	if hasPage2 {
		page2Img, err = d.renderPageToImage(page2, img2W, img2H, termType, superSample)
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
		bgColor := d.pageBackground()
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

		bgColor := d.pageBackground()
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

	actualLines := int(float64(compositeH)/pixelsPerLine/superSample) + 1
	if actualLines > termHeight {
		actualLines = termHeight
	}
	imageWidthInChars := int(float64(compositeW)/pixelsPerChar/superSample) + 1

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

// halfPageDPI is the render DPI for half-page mode: chosen so that 55% of the
// page height fills termHeight exactly, then clamped to what the terminal can
// usefully display. The page box in points equals pixels at 72 DPI, so this
// avoids a throwaway full render just to measure. It also returns the
// supersample factor baked into that DPI, which callers must divide back out of
// every pixel count they compare against the terminal's own cell geometry.
func (d *DocumentViewer) halfPageDPI(pageWidthAt72, pageHeightAt72 float64, termHeight int, termType string, pixelsPerLine float64) (float64, float64) {
	targetFullPixels := float64(termHeight) * pixelsPerLine / 0.55 * d.zoom()

	superSample := superSampleFactor(termType,
		int(targetFullPixels*pageWidthAt72/pageHeightAt72), int(targetFullPixels))

	dpi := targetFullPixels / pageHeightAt72 * 72.0 * superSample

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
	return dpi, superSample
}

// halfPageBands returns the vertical bands of page pageNum that the two halves
// actually show, as fractions of the full page height, mirroring renderHalfPage:
// each half covers cropFrac of the page (55%, or more when a clamped DPI leaves
// the render too short to fill termHeight), anchored to its outer edge, with the
// user's outer-edge crop trimmed off. rollHalfToSyncPoint needs these to decide
// which half a synctex point lands in.
func (d *DocumentViewer) halfPageBands(pageNum, termHeight int) (topY0, topY1, botY0, botY1 float64, ok bool) {
	if termHeight <= 0 || d.doc == nil {
		return 0, 0, 0, 0, false
	}
	r, err := d.doc.Bound(pageNum)
	if err != nil || r.Dy() <= 0 {
		return 0, 0, 0, 0, false
	}
	_, pixelsPerLine := d.getTerminalCellSize()
	dpi, superSample := d.halfPageDPI(float64(r.Dx()), float64(r.Dy()), termHeight, d.detectTerminalType(), pixelsPerLine)
	fullH := float64(r.Dy()) * dpi / 72.0
	if fullH <= 0 {
		return 0, 0, 0, 0, false
	}

	cropFrac := 0.55
	if want := float64(termHeight) * pixelsPerLine * superSample / fullH; want > cropFrac && want <= 1 {
		cropFrac = want
	}

	return cropFrac * d.cropTop, cropFrac,
		1 - cropFrac, 1 - cropFrac*d.cropBottom, true
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
	superSample := 1.0

	if d.isImage {
		// For standalone images, scale so that 55% of the height fills the terminal
		srcBounds := d.sourceImage.Bounds()
		srcW, srcH := srcBounds.Dx(), srcBounds.Dy()
		aspectRatio := float64(srcW) / float64(srcH)

		targetCropPixels := float64(termHeight) * pixelsPerLine
		targetFullH := int(targetCropPixels / 0.55)

		targetFullH = int(float64(targetFullH) * d.zoom())
		targetFullW := int(float64(targetFullH) * aspectRatio)

		superSample = imageSuperSample(d.sourceImage, targetFullW,
			superSampleFactor(termType, targetFullW, targetFullH))
		targetFullW = int(float64(targetFullW) * superSample)
		targetFullH = int(float64(targetFullH) * superSample)

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

		var dpi float64
		dpi, superSample = d.halfPageDPI(float64(r.Dx()), float64(pageHeightAt72), termHeight, termType, pixelsPerLine)

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
	case "dim":
		img = dimPage(img)
	}

	bounds := img.Bounds()
	fullH := bounds.Dy()
	fullW := bounds.Dx()

	// Crop enough to fill termHeight; ideally 55%, but more if DPI was clamped.
	targetCropPixels := float64(termHeight) * pixelsPerLine * superSample
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

	actualLines := int(float64(finalH)/pixelsPerLine/superSample) + 1
	if actualLines > termHeight {
		actualLines = termHeight
	}
	imageWidthInChars := int(float64(finalW)/pixelsPerChar/superSample) + 1

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
		if err := kittySendPNG(imagePath, newID, widthChars, estimatedLines, 0); err != nil {
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

// dimPageWhite is what 255 (paper white) maps to in "dim" dark mode, and
// dimPageGamma is the curve applied before that scaling. A plain linear scale
// would drag ink and its antialiased edges into the same gray band as the
// paper; the gamma pushes everything below white back down so glyphs stay
// black and the page keeps its contrast.
const (
	dimPageWhite = 120
	dimPageGamma = 2.2
)

// pageBackground is the color to fill around page images: it must match what
// the active dark mode turns paper white into, or the padding shows as a halo.
func (d *DocumentViewer) pageBackground() color.RGBA {
	switch d.darkMode {
	case "smart", "invert":
		return color.RGBA{30, 30, 30, 255}
	case "dim":
		return color.RGBA{dimPageWhite, dimPageWhite, dimPageWhite, 255}
	}
	return color.RGBA{255, 255, 255, 255}
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

// dimPage darkens the page without inverting it: white paper becomes mid gray,
// dark ink stays dark. Linear per channel, so hues are preserved.
func dimPage(src image.Image) image.Image {
	rgba := toRGBACopy(src)

	var lut [256]uint8
	for v := 0; v < 256; v++ {
		lut[v] = uint8(dimPageWhite * math.Pow(float64(v)/255.0, dimPageGamma))
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
