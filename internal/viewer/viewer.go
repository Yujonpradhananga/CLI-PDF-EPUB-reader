package viewer

import (
	"crypto/md5"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gen2brain/go-fitz"

	"pdf-cli/internal/config"
	"pdf-cli/internal/layout"
	"pdf-cli/internal/terminal"
)

// Chapter represents a document chapter/section from the ToC.
type Chapter struct {
	Title string
	Page  int // 0-indexed page number in the document
	Level int // nesting level (1 = top-level)
}

// DocumentViewer is the main document viewing engine.
type DocumentViewer struct {
	doc         *fitz.Document
	currentPage int
	textPages   []int
	path        string
	fileType    string // "pdf" or "epub"
	tempDir     string // for storing temporary image files
	cacheDir    string // persistent cross-session render cache
	forceMode   string // "", "text", or "image" - override auto-detection
	fitMode      string  // "auto", "height", "width"
	wantBack     bool    // signal to go back to file picker
	searchQuery  string  // current search query
	searchHits   []int     // pages with matches
	searchHitIdx int       // current index in searchHits
	scaleFactor  float64   // image scale adjustment (1.0 = default)
	lastModTime  time.Time // for auto-reload detection
	cellWidth    float64   // cached cell width in pixels
	cellHeight   float64   // cached cell height in pixels
	lastTermCols int       // last known terminal columns (for change detection)
	lastTermRows int       // last known terminal rows (for change detection)
	fifoPath      string // path to FIFO for external page jump commands
	skipClear     bool   // skip screen clear on next display (for smooth reload)
	htmlPageWidth int    // virtual page width in points for HTML layout (wider = smaller text)
	isReflowable  bool   // true for HTML (supports layout adjustment)
	darkMode       string // "": off, "smart": HSL invert, "invert": simple RGB invert, "dim": gray paper
	dualPageMode   string // "": off, "vertical": stacked, "horizontal": side-by-side, "half": half-page
	halfPageOffset int    // 0: top half, 1: bottom half (used when dualPageMode == "half")
	cropTop        float64 // fraction to cut from top edge (0.0–0.45)
	cropBottom     float64 // fraction to cut from bottom edge
	cropLeft       float64 // fraction to cut from left edge
	cropRight      float64 // fraction to cut from right edge
	chapters       []Chapter // table of contents / chapter list
	currentChapter int       // index into chapters for current position

	// lastKittyImageID is the kitty graphics image id currently on screen.
	// The next render draws the new page first, then deletes this id.
	lastKittyImageID uint32

	// Per-frame PNGs for the dual/half display paths (not covered by the render
	// cache). Each frame gets a fresh file; the previous frame's file is deleted
	// only after the next one is transmitted.
	ephemeralSeq      int
	lastEphemeralPath string

	// Rendered-page cache: maps a render signature to an on-disk PNG so
	// revisiting a page is instant, and a background goroutine can prefetch
	// neighbors.
	renderCache map[string]cachedRender
	cacheOrder  []string        // insertion order for simple LRU eviction
	inFlight    map[string]bool // signatures currently being prefetched
	cacheMu     sync.Mutex

	// visualContent caches pageHasVisualContent per page number. Cleared in
	// findContentPages on (re)open / relayout. Main-thread only.
	visualContent map[int]bool
}

// NewDocumentViewer creates a new viewer for the given file path.
func NewDocumentViewer(path string) *DocumentViewer {
	ext := strings.ToLower(filepath.Ext(path))
	fileType := strings.TrimPrefix(ext, ".")

	tempDir := filepath.Join(os.TempDir(), fmt.Sprintf("docviewer_%d", time.Now().UnixNano()))
	cacheDir := persistentCacheDir()
	if cacheDir == "" {
		cacheDir = tempDir // no persistence, but everything still works
	}

	absPath, _ := filepath.Abs(path)
	cfg := config.Load(absPath)

	dv := &DocumentViewer{
		path:          path,
		fileType:      fileType,
		tempDir:       tempDir,
		cacheDir:      cacheDir,
		fitMode:       cfg.FitMode,
		scaleFactor:   cfg.ScaleFactor,
		darkMode:      cfg.DarkMode,
		dualPageMode:  cfg.DualPageMode,
		forceMode:     cfg.ForceMode,
		htmlPageWidth: cfg.HTMLPageWidth,
		cropTop:       cfg.CropTop,
		cropBottom:    cfg.CropBottom,
		cropLeft:      cfg.CropLeft,
		cropRight:     cfg.CropRight,
		isReflowable:  fileType == "html" || fileType == "htm",
		renderCache:   make(map[string]cachedRender),
		inFlight:      make(map[string]bool),
	}

	return dv
}

// Open opens the document and prepares it for viewing.
func (d *DocumentViewer) Open() error {
	// Prune the persistent render cache in the background.
	go gcPersistentCache(d.cacheDir)

	doc, err := fitz.New(d.path)
	if err != nil {
		return fmt.Errorf("error opening %s: %v", d.fileType, err)
	}
	d.doc = doc

	if d.isReflowable {
		d.applyHTMLLayout()
	}

	if info, err := os.Stat(d.path); err == nil {
		d.lastModTime = info.ModTime()
	}

	d.findContentPages()
	if len(d.textPages) == 0 {
		return fmt.Errorf("no pages with extractable content found")
	}

	d.loadChapters()

	return nil
}

// Run runs the main viewer loop. Returns true if user wants to go back to file picker.
func (d *DocumentViewer) Run() bool {
	defer d.doc.Close()
	defer d.cleanup()
	defer d.saveConfig()
	defer d.clearKittyGraphics()

	// Probe kitty file transfer mode before the input goroutine starts.
	d.probeKittyTransferMode()

	d.cellWidth, d.cellHeight = terminal.DetectCellSize()

	oldState, err := terminal.SetRawMode()
	if err != nil {
		fmt.Printf("Error setting raw mode: %v\n", err)
		return false
	}
	defer terminal.RestoreTerminal(oldState)
	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h")

	d.currentPage = 0

	inputChan := make(chan byte, 1)
	stopChan := make(chan struct{})
	defer close(stopChan)

	pageChan := make(chan int, 1)

	d.setupFIFO()
	defer d.cleanupFIFO()

	go d.fifoListener(pageChan, stopChan)

	go func() {
		for {
			char := terminal.ReadSingleChar()
			select {
			case <-stopChan:
				return
			case inputChan <- char:
			}
		}
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	d.displayCurrentPage()

	for {
		select {
		case char := <-inputChan:
			action := d.handleInput(char)
			if action == 1 {
				fmt.Print("\033[2J\033[H")
				return d.wantBack
			}
			switch action {
			case -1:
				d.startSearch(inputChan)
			case -2:
				d.goToPage(inputChan)
			case -3:
				d.showHelp(inputChan)
			case -4:
				d.showDebugInfo(inputChan)
			case -5:
				d.showChapterList(inputChan)
			}
			d.displayCurrentPage()
		case page := <-pageChan:
			d.jumpToPage(page)
			d.displayCurrentPage()
		case <-ticker.C:
			if d.checkAndReload() {
				d.displayCurrentPage()
			}
		}
	}
}

func (d *DocumentViewer) cleanup() {
	if d.tempDir != "" {
		os.RemoveAll(d.tempDir)
	}
}

func (d *DocumentViewer) saveConfig() {
	absPath, err := filepath.Abs(d.path)
	if err != nil {
		return
	}

	cfg := config.DocConfig{
		FitMode:       d.fitMode,
		ScaleFactor:   d.scaleFactor,
		DarkMode:      d.darkMode,
		DualPageMode:  d.dualPageMode,
		ForceMode:     d.forceMode,
		HTMLPageWidth: d.htmlPageWidth,
		CropTop:       d.cropTop,
		CropBottom:    d.cropBottom,
		CropLeft:      d.cropLeft,
		CropRight:     d.cropRight,
	}

	config.Save(absPath, cfg)
}

func (d *DocumentViewer) setupFIFO() {
	absPath, _ := filepath.Abs(d.path)
	hash := md5.Sum([]byte(absPath))
	d.fifoPath = fmt.Sprintf("/tmp/docviewer_%x.ctrl", hash[:8])
	os.Remove(d.fifoPath)
}

func (d *DocumentViewer) cleanupFIFO() {
	if d.fifoPath != "" {
		os.Remove(d.fifoPath)
	}
}

func (d *DocumentViewer) fifoListener(pageChan chan<- int, stopChan <-chan struct{}) {
	var lastMod time.Time

	for {
		select {
		case <-stopChan:
			return
		default:
		}

		info, err := os.Stat(d.fifoPath)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if info.ModTime().After(lastMod) {
			lastMod = info.ModTime()

			data, err := os.ReadFile(d.fifoPath)
			if err == nil {
				line := strings.TrimSpace(string(data))
				if page, err := strconv.Atoi(line); err == nil && page >= 1 {
					select {
					case pageChan <- page:
					default:
					}
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func (d *DocumentViewer) jumpToPage(page int) {
	targetPdfPage := page - 1

	for i, pdfPage := range d.textPages {
		if pdfPage == targetPdfPage {
			d.currentPage = i
			return
		}
	}

	for i, pdfPage := range d.textPages {
		if pdfPage >= targetPdfPage {
			d.currentPage = i
			return
		}
	}

	if len(d.textPages) > 0 {
		d.currentPage = len(d.textPages) - 1
	}
}

func (d *DocumentViewer) checkAndReload() bool {
	info, err := os.Stat(d.path)
	if err != nil {
		return false
	}

	if info.ModTime().After(d.lastModTime) {
		d.lastModTime = info.ModTime()

		lastSize := info.Size()
		for i := 0; i < 5; i++ {
			time.Sleep(100 * time.Millisecond)
			newInfo, err := os.Stat(d.path)
			if err != nil {
				return false
			}
			if newInfo.Size() == lastSize && newInfo.Size() > 0 {
				break
			}
			lastSize = newInfo.Size()
		}

		savedPage := d.currentPage
		savedStderr, _ := syscall.Dup(2)
		devNull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if devNull != nil && savedStderr != -1 {
			syscall.Dup2(int(devNull.Fd()), 2)
		}

		doc, openErr := fitz.New(d.path)

		if savedStderr != -1 {
			syscall.Dup2(savedStderr, 2)
			syscall.Close(savedStderr)
		}
		if devNull != nil {
			devNull.Close()
		}

		if openErr != nil {
			return false
		}

		oldDoc := d.doc
		oldPages := d.textPages
		oldPage := d.currentPage

		d.doc = doc
		if d.isReflowable {
			d.applyHTMLLayout()
		} else {
			d.findContentPages()
		}

		if len(d.textPages) == 0 {
			d.doc = oldDoc
			d.textPages = oldPages
			d.currentPage = oldPage
			doc.Close()
			return false
		}

		oldDoc.Close()

		if savedPage >= len(d.textPages) {
			savedPage = len(d.textPages) - 1
		}
		if savedPage < 0 {
			savedPage = 0
		}
		d.currentPage = savedPage
		d.skipClear = true
		return true
	}
	return false
}

// applyHTMLLayout calls fz_layout_document to set page width for HTML files.
func (d *DocumentViewer) applyHTMLLayout() {
	h := float64(d.htmlPageWidth) * 1.414
	layout.LayoutDocument(d.doc, float64(d.htmlPageWidth), h, 12)
	d.findContentPages()
}

// adjustHTMLZoom changes the page width and preserves approximate scroll position.
func (d *DocumentViewer) adjustHTMLZoom(delta int) {
	frac := 0.0
	if len(d.textPages) > 1 {
		frac = float64(d.currentPage) / float64(len(d.textPages)-1)
	}

	d.htmlPageWidth += delta
	if d.htmlPageWidth < 200 {
		d.htmlPageWidth = 200
	}
	if d.htmlPageWidth > 3000 {
		d.htmlPageWidth = 3000
	}

	d.applyHTMLLayout()

	if len(d.textPages) > 1 {
		d.currentPage = int(frac*float64(len(d.textPages)-1) + 0.5)
	}
	if d.currentPage >= len(d.textPages) {
		d.currentPage = len(d.textPages) - 1
	}
	if d.currentPage < 0 {
		d.currentPage = 0
	}
}

func (d *DocumentViewer) findContentPages() {
	d.textPages = []int{}
	d.visualContent = nil // clear visual content cache on reload
	for i := 0; i < d.doc.NumPage(); i++ {
		hasContent := false

		text, err := d.doc.Text(i)
		if err == nil && len(strings.Fields(strings.TrimSpace(text))) >= 3 {
			hasContent = true
		}

		if !hasContent {
			if rect, err := d.doc.Bound(i); err == nil && rect.Dx() > 50 && rect.Dy() > 50 {
				if d.pageHasVisualContent(i) {
					hasContent = true
				}
			}
		}

		if hasContent {
			d.textPages = append(d.textPages, i)
		}
	}
}

func (d *DocumentViewer) pageHasVisualContent(pageNum int) bool {
	// Check cache first (main-thread only, no lock needed)
	if d.visualContent != nil {
		if v, ok := d.visualContent[pageNum]; ok {
			return v
		}
	}

	result := d.pageHasVisualContentUncached(pageNum)

	// Store in cache
	if d.visualContent == nil {
		d.visualContent = make(map[int]bool)
	}
	d.visualContent[pageNum] = result
	return result
}

func (d *DocumentViewer) pageHasVisualContentUncached(pageNum int) bool {
	img, err := d.doc.Image(pageNum)
	if err != nil {
		return false
	}

	bounds := img.Bounds()
	if bounds.Dx() < 50 || bounds.Dy() < 50 {
		return false
	}

	return d.hasNonBlankContent(img)
}

func (d *DocumentViewer) hasNonBlankContent(img image.Image) bool {
	bounds := img.Bounds()

	sampleRate := 10
	nonWhiteThreshold := 20
	whiteThreshold := uint8(240)

	nonWhitePixels := 0

	for y := bounds.Min.Y; y < bounds.Max.Y; y += sampleRate {
		for x := bounds.Min.X; x < bounds.Max.X; x += sampleRate {
			c := img.At(x, y)
			r, g, b, a := c.RGBA()

			r8 := uint8(r >> 8)
			g8 := uint8(g >> 8)
			b8 := uint8(b >> 8)
			a8 := uint8(a >> 8)

			if a8 < 10 {
				continue
			}

			if r8 < whiteThreshold || g8 < whiteThreshold || b8 < whiteThreshold {
				nonWhitePixels++

				if nonWhitePixels >= nonWhiteThreshold {
					return true
				}
			}
		}
	}

	colorVariance := d.checkColorVariance(img)
	if colorVariance > 100 {
		return true
	}

	return nonWhitePixels >= nonWhiteThreshold
}

func (d *DocumentViewer) checkColorVariance(img image.Image) float64 {
	bounds := img.Bounds()

	sampleRate := 20
	var rSum, gSum, bSum uint64
	var rSumSq, gSumSq, bSumSq uint64
	sampleCount := 0

	for y := bounds.Min.Y; y < bounds.Max.Y; y += sampleRate {
		for x := bounds.Min.X; x < bounds.Max.X; x += sampleRate {
			c := img.At(x, y)
			r, g, b, a := c.RGBA()

			if uint8(a>>8) < 10 {
				continue
			}

			r8 := uint8(r >> 8)
			g8 := uint8(g >> 8)
			b8 := uint8(b >> 8)

			rSum += uint64(r8)
			gSum += uint64(g8)
			bSum += uint64(b8)

			rSumSq += uint64(r8) * uint64(r8)
			gSumSq += uint64(g8) * uint64(g8)
			bSumSq += uint64(b8) * uint64(b8)

			sampleCount++
		}
	}

	if sampleCount < 10 {
		return 0
	}

	rMean := float64(rSum) / float64(sampleCount)
	gMean := float64(gSum) / float64(sampleCount)
	bMean := float64(bSum) / float64(sampleCount)

	rVar := float64(rSumSq)/float64(sampleCount) - rMean*rMean
	gVar := float64(gSumSq)/float64(sampleCount) - gMean*gMean
	bVar := float64(bSumSq)/float64(sampleCount) - bMean*bMean

	return rVar + gVar + bVar
}

// Terminal helper methods (thin wrappers with caching)

func (d *DocumentViewer) getTerminalSize() (int, int) {
	return terminal.GetSize()
}

func (d *DocumentViewer) detectTerminalType() string {
	return terminal.DetectType()
}

func (d *DocumentViewer) getTerminalCellSize() (float64, float64) {
	cols, rows := terminal.GetSize()
	if cols != d.lastTermCols || rows != d.lastTermRows {
		d.cellWidth = 0
		d.cellHeight = 0
		d.lastTermCols = cols
		d.lastTermRows = rows
	}

	if d.cellWidth > 0 && d.cellHeight > 0 {
		return d.cellWidth, d.cellHeight
	}

	d.cellWidth, d.cellHeight = terminal.DetectCellSize()
	return d.cellWidth, d.cellHeight
}

func (d *DocumentViewer) refreshCellSize() {
	d.cellWidth = 0
	d.cellHeight = 0
	d.lastTermCols = 0
	d.lastTermRows = 0
}

func (d *DocumentViewer) getTerminalPixelSize() (int, int) {
	return terminal.GetPixelSize()
}

func (d *DocumentViewer) getKittyCellSize() (float64, float64) {
	return terminal.GetKittyCellSize()
}
