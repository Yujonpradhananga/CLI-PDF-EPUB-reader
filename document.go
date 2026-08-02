package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"html"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gen2brain/go-fitz"
	"golang.org/x/term"
)

type DocumentViewer struct {
	doc            *fitz.Document
	currentPage    int
	textPages      []int
	path           string
	oldState       *term.State
	fileType       string      // "pdf" or "epub"
	tempDir        string      // per-process dir for ephemeral files (dual/half frames, probe)
	cacheDir       string      // persistent cross-session render cache (falls back to tempDir)
	forceMode      string      // "", "text", or "image" - override auto-detection
	fitMode        string      // "auto", "height", "width"
	wantBack       bool        // signal to go back to file picker
	searchQuery    string      // current search query
	searchHits     []int       // pages with matches
	searchHitIdx   int         // current index in searchHits
	lastModTime    time.Time   // for auto-reload detection
	cellWidth      float64     // cached cell width in pixels
	cellHeight     float64     // cached cell height in pixels
	lastTermCols   int         // last known terminal columns (for change detection)
	lastTermRows   int         // last known terminal rows (for change detection)
	fifoPath       string      // path to FIFO for external page jump commands
	skipClear      bool        // skip screen clear on next display (for smooth reload)
	lastTitle      string      // last OSC-2 title written (see setTerminalTitle)
	agterm         *agtermReporter
	htmlPageWidth  int         // virtual page width in points for HTML layout (wider = smaller text)
	isReflowable   bool        // true for HTML (supports layout adjustment)
	darkMode       string      // "": off, "smart": HSL invert, "invert": simple RGB invert, "dim": gray paper
	dualPageMode   string      // "": off, "vertical": stacked, "horizontal": side-by-side, "half": half-page
	halfPageOffset int         // 0: top half, 1: bottom half (used when dualPageMode == "half")
	cropTop        float64     // fraction to cut from top edge (0.0–0.45)
	cropBottom     float64     // fraction to cut from bottom edge
	cropLeft       float64     // fraction to cut from left edge
	cropRight      float64     // fraction to cut from right edge
	isImage        bool        // true for standalone image files (PNG, JPG)
	sourceImage    image.Image // loaded image for standalone image viewing

	// zoomByView is the image scale of each view, keyed by viewKey: the views
	// fit pages to the terminal so differently that one shared scale left every
	// view but the one it was set in unusable. Missing (or non-positive) means
	// the view's own fit; see zoom.
	zoomByView map[string]float64

	// lastKittyImageID is the kitty graphics image id currently on screen. The next
	// render draws the new page first, then deletes this id — so a reload (LaTeX
	// rebuild) never blanks the screen with a delete-all-then-redraw gap.
	lastKittyImageID uint32

	// Per-frame PNGs for the dual/half display paths (not covered by the render
	// cache). Each frame gets a fresh file; the previous frame's file is deleted
	// only after the next one is transmitted, because t=f transmission means the
	// terminal reads the file asynchronously (see saveEphemeralPNG).
	ephemeralSeq      int
	lastEphemeralPath string

	// Opt+click inverse sync. clickMap records where the last-drawn page image
	// sits on screen and how its pixels map back to PDF points (rebuilt by the
	// image render paths on every display; see image.go). mouseCol/mouseRow
	// carry the clicked cell from the stdin-reader goroutine to handleAltClick;
	// the mutex matters because a second click can overwrite the fields before
	// the first keyMouseAltClick byte is consumed from the input channel.
	clickMap clickMap
	mouseMu  sync.Mutex
	mouseCol int
	mouseRow int

	// Forward-sync marker (\lv in vim): a control-file jump that carries a
	// synctex point (see parseSyncCommand) draws a margin marker at the
	// mapped row. It persists across redraws (reload, zoom, crop) while the
	// synced page stays on screen; paging away clears it, and the next jump
	// replaces it. Main-goroutine-only, like clickMap.
	//
	// The rule across the page is transient: flashUntil bounds its life, and
	// flashRuleID is the kitty image id of the placement currently on screen.
	flash       flashState
	flashUntil  time.Time
	flashRuleID uint32

	// visualContent caches pageHasVisualContent per page number — the check
	// renders the page, and getPageContentType runs it on every page display.
	// Cleared in findContentPages on (re)open / relayout. Main-thread only.
	visualContent map[int]bool

	// searchLineCache caches the positioned text lines of a page (searchLines),
	// which the search markers need on every redraw. Cleared alongside
	// visualContent. Main-thread only.
	searchLineCache map[int][]searchLine

	// toc is the document outline for the 'T' picker, flattened by documentTOC.
	// Extracted in findContentPages on every (re)open and relayout so its page
	// numbers track the current document. Main-thread only.
	toc []tocEntry

	// pageLinks caches documentLinks per 0-indexed page: warmed in the
	// background at display time (warmPageLinks), read on Ctrl+click.
	// Guarded by linksMu; linksGen bumps on every invalidation so a warm
	// goroutine that raced a reload discards its stale result. Nothing on
	// the render/redraw path blocks on it.
	pageLinks map[int][]pageLink
	linksGen  int
	linksMu   sync.Mutex

	// Browser-style jump history (Ctrl+I / Ctrl+O). Entries hold PDF
	// pages rather than textPages indices so they survive reloads. Any
	// navigation jump — link follow, forward sync, search hit, go-to-page —
	// pushes onto backStack and clears fwdStack; historyBack/historyForward
	// move between the stacks. Main-thread only.
	backStack []viewLoc
	fwdStack  []viewLoc

	// syncChan carries jump targets to Run's select loop. fifoListener feeds
	// it from the control file; handleCtrlClick feeds it internal link
	// destinations so both take the same jump+flash path.
	syncChan chan syncTarget

	// Rendered-page cache (single-page image path): maps a render signature to an
	// on-disk PNG so revisiting a page is instant, and a background goroutine can
	// prefetch neighbors. go-fitz is internally mutex-locked, so concurrent renders
	// are safe; cacheMu only guards the maps below.
	renderCache map[string]cachedRender
	cacheOrder  []string        // insertion order for simple LRU eviction
	inFlight    map[string]bool // signatures currently being prefetched
	cacheMu     sync.Mutex
}

func NewDocumentViewer(path string) *DocumentViewer {
	ext := strings.ToLower(filepath.Ext(path))
	fileType := strings.TrimPrefix(ext, ".")

	tempDir := filepath.Join(os.TempDir(), fmt.Sprintf("docviewer_%d", time.Now().UnixNano()))
	cacheDir := persistentCacheDir()
	if cacheDir == "" {
		cacheDir = tempDir // no persistence, but everything still works
	}

	absPath, _ := filepath.Abs(path)
	cfg := loadDocConfig(absPath)

	dv := &DocumentViewer{
		path:          path,
		fileType:      fileType,
		tempDir:       tempDir,
		cacheDir:      cacheDir,
		fitMode:       cfg.FitMode,
		zoomByView:    cfg.ScaleFactors,
		darkMode:      cfg.DarkMode,
		dualPageMode:  cfg.DualPageMode,
		forceMode:     cfg.ForceMode,
		htmlPageWidth: cfg.HTMLPageWidth,
		cropTop:       cfg.CropTop,
		cropBottom:    cfg.CropBottom,
		cropLeft:      cfg.CropLeft,
		cropRight:     cfg.CropRight,
		isReflowable:  fileType == "html" || fileType == "htm",
		isImage:       fileType == "png" || fileType == "jpg" || fileType == "jpeg",
		renderCache:   make(map[string]cachedRender),
		inFlight:      make(map[string]bool),
	}

	return dv
}

func (d *DocumentViewer) Open() error {
	// Prune the persistent render cache in the background.
	go gcPersistentCache(d.cacheDir)

	if d.isImage {
		f, err := os.Open(d.path)
		if err != nil {
			return fmt.Errorf("error opening image: %v", err)
		}
		defer f.Close()
		img, _, err := image.Decode(f)
		if err != nil {
			return fmt.Errorf("error decoding image: %v", err)
		}
		d.sourceImage = img
		d.textPages = []int{0}
		if info, err := os.Stat(d.path); err == nil {
			d.lastModTime = info.ModTime()
		}
		return nil
	}

	doc, err := fitz.New(d.path)
	if err != nil {
		return fmt.Errorf("error opening %s: %v", d.fileType, err)
	}
	d.doc = doc

	// For reflowable documents (HTML), set the layout with our font size
	if d.isReflowable {
		d.applyHTMLLayout()
	}

	// Store modification time for auto-reload
	if info, err := os.Stat(d.path); err == nil {
		d.lastModTime = info.ModTime()
	}

	d.findContentPages()
	if len(d.textPages) == 0 {
		return fmt.Errorf("no pages with extractable content found")
	}
	return nil
}

// applyHTMLLayout calls fz_layout_document to set page width for HTML files.
// Wider page = more text per line = text appears smaller when scaled to terminal.
func (d *DocumentViewer) applyHTMLLayout() {
	// Height proportional to width (A4 ratio ~1.414), em=12 (MuPDF default)
	h := float64(d.htmlPageWidth) * 1.414
	layoutDocument(d.doc, float64(d.htmlPageWidth), h, 12)
	d.findContentPages()
}

// zoom limits for the +/- keys.
const (
	minZoom = 0.1
	maxZoom = 2.0
)

// viewKey names the view the zoom keys act on. Each view fits pages to the
// terminal differently — a scale that reads well with one page filling the
// screen is far too large once two share it, and too small in the half-page
// view — so every view carries its own zoom. Spelled out rather than reusing
// dualPageMode because its "" would be an empty key in the saved config.
func (d *DocumentViewer) viewKey() string {
	if d.dualPageMode == "" {
		return viewSingle
	}
	return d.dualPageMode
}

// zoom is the image scale of the view on screen; 1.0 is that view's own fit.
func (d *DocumentViewer) zoom() float64 {
	return d.zoomOf(d.viewKey())
}

// zoomOf reports the stored scale for a named view, 1.0 when it has none.
func (d *DocumentViewer) zoomOf(view string) float64 {
	if z := d.zoomByView[view]; z > 0 {
		return z
	}
	return 1.0
}

// setZoom stores the scale for the view on screen, clamped to the range the
// +/- keys allow.
func (d *DocumentViewer) setZoom(z float64) {
	if d.zoomByView == nil {
		d.zoomByView = make(map[string]float64)
	}
	d.zoomByView[d.viewKey()] = min(max(z, minZoom), maxZoom)
}

// adjustHTMLZoom changes the page width and preserves approximate scroll position.
func (d *DocumentViewer) adjustHTMLZoom(delta int) {
	// Remember approximate position as fraction through document
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

	// Restore approximate position
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
	// The document (or its layout) changed: previous visual-content results are
	// stale (page numbering/content may differ), so reset the cache. The scan
	// below repopulates it for sparse-text pages as a side effect. Cached
	// links go stale the same way.
	d.visualContent = make(map[int]bool)
	d.searchLineCache = make(map[int][]searchLine)
	d.toc = documentTOC(d.doc)
	d.linksMu.Lock()
	d.pageLinks = make(map[int][]pageLink)
	d.linksGen++
	d.linksMu.Unlock()
	d.textPages = []int{}
	for i := 0; i < d.doc.NumPage(); i++ {
		hasContent := false

		text, err := d.doc.Text(i)
		if err == nil && len(strings.Fields(strings.TrimSpace(text))) >= 3 {
			hasContent = true
		}

		if !hasContent {
			if d.pageHasVisualContent(i) {
				hasContent = true
			}
		}

		if hasContent {
			d.textPages = append(d.textPages, i)
		}
	}
}

// pageHasVisualContent reports whether a page has non-blank visual content.
// The underlying check renders the page, which is expensive — and this is
// called from getPageContentType on EVERY page display — so results are cached
// per page. The cache is cleared in findContentPages whenever the document is
// (re)opened or relaid out. Main-thread only (plain map, no lock).
func (d *DocumentViewer) pageHasVisualContent(pageNum int) bool {
	if v, ok := d.visualContent[pageNum]; ok {
		return v
	}
	v := d.computePageHasVisualContent(pageNum)
	if d.visualContent == nil {
		d.visualContent = make(map[int]bool)
	}
	d.visualContent[pageNum] = v
	return v
}

func (d *DocumentViewer) computePageHasVisualContent(pageNum int) bool {
	// 150 DPI instead of go-fitz's 300 DPI default: 4x fewer pixels to render,
	// still ~20k+ sample points on a letter page — plenty for blank detection.
	img, err := d.doc.ImageDPI(pageNum, 150)
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
	sampledPixels := 0

	for y := bounds.Min.Y; y < bounds.Max.Y; y += sampleRate {
		for x := bounds.Min.X; x < bounds.Max.X; x += sampleRate {
			sampledPixels++

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

// Terminal-restore hook for external kills (SIGTERM/SIGHUP): defers don't run
// then, and a shell left in mouse-reporting mode gets junk bytes on every
// click. zsh repairs raw mode and the cursor, but never DECRST 1000/1006.
var (
	sigCleanupOnce sync.Once
	sigRestoreMu   sync.Mutex
	sigRestore     func()
)

func installSignalCleanup() {
	sigCleanupOnce.Do(func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGHUP)
		go func() {
			<-ch
			sigRestoreMu.Lock()
			f := sigRestore
			sigRestoreMu.Unlock()
			if f != nil {
				f()
			}
			os.Exit(1)
		}()
	})
}

func (d *DocumentViewer) Run() bool {
	if d.doc != nil {
		defer d.doc.Close()
	}
	defer d.cleanup()
	defer d.saveConfig()

	// Cache cell size before entering raw mode (for Kitty query)
	d.cellWidth, d.cellHeight = d.detectCellSize()

	// Decide the kitty graphics transfer mode (t=f vs chunked direct) once, before
	// the stdin-reading goroutine starts — the probe reads a reply from the tty.
	d.probeKittyTransferMode()

	oldState, err := d.setRawMode()
	if err != nil {
		fmt.Printf("Error setting raw mode: %v\n", err)
		return false
	}
	defer d.restoreTerminal(oldState)

	// Silence MuPDF's stderr for the whole session. go-fitz wraps MuPDF, whose C
	// code writes PDF parser warnings ("syntax error", "N 0 R", "object in xref")
	// straight to fd 2 when it reads a damaged or partially written file — e.g. a
	// PDF being rewritten by pdflatex. Those bytes bypass our synchronized-update
	// frame and scatter across the terminal. Nothing here uses stderr for real
	// output, so we redirect it once and restore on exit (see silenceStderr).
	savedStderr, devNull := silenceStderr()
	defer restoreStderr(savedStderr, devNull)

	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h") // Show cursor on exit
	defer clearTerminalTitle()

	d.agterm = newAgtermReporter()
	defer d.agterm.close()
	// Force normal cursor-key mode (DECCKM reset) so arrows send ESC [ A-D rather
	// than the SS3 ESC O A-D form that some terminals (e.g. agterm) default to.
	fmt.Print("\033[?1l")

	// Enable the Kitty keyboard protocol's "disambiguate escape codes" flag so
	// Cmd (Super) modified keys — which have no legacy terminal representation
	// — are reported as CSI u sequences (see readSingleChar/parseKittyCSIU).
	// Only kitty-family terminals (kitty, ghostty, agterm) understand this;
	// others ignore the private-marker CSI as a no-op.
	if d.detectTerminalType() == "kitty" {
		fmt.Print("\x1b[>1u")
		defer fmt.Print("\x1b[<u")
		// SGR mouse reporting (button press/release only, no motion) for the
		// Opt+click synctex jump. The click→cell mapping relies on the kitty
		// graphics c/r cell placement, so this stays kitty-gated too.
		// readSingleChar consumes every other mouse event silently.
		fmt.Print("\x1b[?1000h\x1b[?1006h")
		defer fmt.Print("\x1b[?1006l\x1b[?1000l")
	}

	installSignalCleanup()
	sigRestoreMu.Lock()
	sigRestore = func() {
		fmt.Print("\x1b[?1006l\x1b[?1000l\x1b[<u\x1b[?25h\x1b]2;\x07")
		d.clearKittyGraphics()
		d.agterm.close()
		if d.oldState != nil {
			term.Restore(int(os.Stdin.Fd()), d.oldState)
		}
	}
	sigRestoreMu.Unlock()
	defer func() {
		sigRestoreMu.Lock()
		sigRestore = nil
		sigRestoreMu.Unlock()
	}()

	d.currentPage = 0

	// Channel for input from goroutine
	inputChan := make(chan byte, 1)
	stopChan := make(chan struct{})
	defer close(stopChan)

	// Channel for forward-sync commands: external via the control file,
	// internal via Ctrl+click link follows (handleCtrlClick).
	syncChan := make(chan syncTarget, 1)
	d.syncChan = syncChan

	// Signaled when a background post-reload render has warmed the cache
	// (see startReloadRender); the main loop then redraws as a cache hit.
	reloadRenderChan := make(chan struct{}, 1)

	// Signaled flashRuleWindow after a forward-sync jump, so the rule across
	// the page comes down without waiting for the reader to press a key.
	flashChan := make(chan struct{}, 1)
	var flashTimer *time.Timer
	defer func() {
		if flashTimer != nil {
			flashTimer.Stop()
		}
	}()

	// Set up FIFO for external control
	d.setupFIFO()
	defer d.cleanupFIFO()

	// FIFO listener goroutine
	go d.fifoListener(syncChan, stopChan)

	// Input reader goroutine
	go func() {
		for {
			char := d.readSingleChar()
			select {
			case <-stopChan:
				return
			case inputChan <- char:
			}
		}
	}()

	// Ticker for file change checking
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	d.displayCurrentPage()

	for {
		// Wait for input, page jump, or reload tick
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
				// A link follow queued its target on syncChan; that case
				// jumps and redraws — don't redraw the old page first.
				continue
			case -6:
				if d.showTOC(inputChan) {
					continue // jump queued on syncChan; its case redraws
				}
			}
			d.displayCurrentPage()
		case t := <-syncChan:
			// Any sync jump — vim forward sync or a followed link — is a
			// navigation: record where we were for Ctrl+I. A jump that
			// lands where it started leaves a stale top entry, which
			// historyBack skips.
			d.pushHistory()
			d.jumpToPage(t.page)
			d.flash = flashState{} // a jump without a point dismisses any current marker
			if t.hasPoint {
				d.setFlash(t.page-1, t.x, t.y)
				d.rollHalfToSyncPoint(t.page-1, t.y)
				if flashTimer != nil {
					flashTimer.Stop()
				}
				flashTimer = time.AfterFunc(flashRuleWindow, func() {
					select {
					case flashChan <- struct{}{}:
					default:
					}
				})
			}
			d.displayCurrentPage()
			// A click captured before this jump would map through the new
			// page's clickMap; discard any pending coordinates so it is
			// rejected instead of following a link the user never saw.
			d.mouseMu.Lock()
			d.mouseCol, d.mouseRow = 0, 0
			d.mouseMu.Unlock()
		case <-flashChan:
			d.clearFlashRule()
			os.Stdout.Sync()
		case <-ticker.C:
			if d.checkAndReload() {
				d.startReloadRender(reloadRenderChan)
			}
		case <-reloadRenderChan:
			d.displayCurrentPage()
		}
	}
}

// startReloadRender warms the render cache for the current page in the
// background after an auto-reload, then signals ch so the main loop redraws
// (as a cache hit). The old page thus stays on screen during the MuPDF render
// + PNG encode of the rebuilt file instead of the UI blocking in the ticker
// handler. Display modes that don't use the single-page render cache signal
// immediately and render synchronously as before. Must be called on the main
// thread (it snapshots viewer state).
func (d *DocumentViewer) startReloadRender(ch chan<- struct{}) {
	signal := func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	if d.dualPageMode != "" || d.isImage || len(d.textPages) == 0 {
		signal()
		return
	}
	termWidth, termHeight := d.getTerminalSize()
	// Match displayImagePage's available height (reserved 2 + top padding 1).
	maxHeight := termHeight - 3
	if maxHeight <= 0 {
		signal()
		return
	}
	pageNum := d.textPages[d.currentPage]
	rp := d.snapshotParams()
	termType := d.detectTerminalType()
	go func() {
		defer func() {
			// A corrupt mid-recompile page must not crash the reader; the
			// display path will fall back to a synchronous render attempt.
			recover()
			signal()
		}()
		d.savePageAsImage(pageNum, termWidth, maxHeight, termType, rp)
	}()
}

func (d *DocumentViewer) cleanup() {
	d.clearKittyGraphics()
	if d.tempDir != "" {
		os.RemoveAll(d.tempDir)
	}
}

func (d *DocumentViewer) setupFIFO() {
	// Create control file path based on absolute PDF path hash
	absPath, _ := filepath.Abs(d.path)
	hash := md5.Sum([]byte(absPath))
	d.fifoPath = fmt.Sprintf("/tmp/docviewer_%x.ctrl", hash[:8])

	// Remove existing file if present
	os.Remove(d.fifoPath)
}

func (d *DocumentViewer) cleanupFIFO() {
	if d.fifoPath != "" {
		os.Remove(d.fifoPath)
	}
}

// syncTarget is a forward-sync command read from the control file: jump to
// the 1-indexed page, optionally flashing a marker at (x, y) — PDF points,
// top-left origin, the same coordinate space cellToPDF maps clicks into.
type syncTarget struct {
	page     int
	x, y     float64
	hasPoint bool
}

// flashState records the current forward-sync marker. page0/x/y locate the
// synctex point (PDF points, top-left origin). The marker persists until its
// page leaves the screen (see drawFlash) or the next jump replaces it.
type flashState struct {
	active bool
	page0  int
	x, y   float64
}

// flashRuleWindow is how long the rule across the page stays up after a jump;
// the margin markers outlive it.
const flashRuleWindow = 3 * time.Second

// setFlash records the forward-sync marker for the jump being processed.
// Main goroutine only.
func (d *DocumentViewer) setFlash(page0 int, x, y float64) {
	d.flash = flashState{active: true, page0: page0, x: x, y: y}
	d.flashUntil = time.Now().Add(flashRuleWindow)
}

// chooseHalf picks the half-page offset (0 top, 1 bottom) that best shows the
// point at page-height fraction fy, given the bands the two halves display and
// the half currently on screen. The halves overlap — by a lot when a clamped
// DPI makes each band well over 55% of the page — so "the point is somewhere in
// this band" is too weak a test: it leaves the point pressed against the top or
// bottom edge of the screen. A band counts only when fy sits at least
// halfEdgeMargin of the band's height inside it. Preferring the current half
// when it qualifies gives repeated syncs to nearby lines hysteresis instead of
// flipping the view back and forth.
func chooseHalf(fy, topY0, topY1, botY0, botY1 float64, current int) int {
	const halfEdgeMargin = 0.15
	comfortable := func(y0, y1 float64) bool {
		m := (y1 - y0) * halfEdgeMargin
		return fy >= y0+m && fy <= y1-m
	}
	inTop, inBottom := comfortable(topY0, topY1), comfortable(botY0, botY1)
	switch {
	case current == 0 && inTop, current == 1 && inBottom:
		return current
	case inTop:
		return 0
	case inBottom:
		return 1
	}
	// Neither half shows the point clear of an edge (or a crop hides it
	// entirely): put it on the half whose band it is nearest the middle of.
	if math.Abs(fy-(topY0+topY1)/2) <= math.Abs(fy-(botY0+botY1)/2) {
		return 0
	}
	return 1
}

// rollHalfToSyncPoint switches the half-page view to the half that best shows
// the forward-synced point, using the bands each half actually displays
// (halfPageBands), which depend on the render DPI and on the user's outer-edge
// crop. No-op outside half-page mode.
func (d *DocumentViewer) rollHalfToSyncPoint(page0 int, y float64) {
	if d.dualPageMode != "half" || d.doc == nil {
		return
	}
	r, err := d.doc.Bound(page0)
	if err != nil || r.Dy() <= 0 {
		return
	}
	_, termHeight := d.getTerminalSize()
	topY0, topY1, botY0, botY1, ok := d.halfPageBands(page0, halfPageViewHeight(termHeight))
	if !ok {
		return
	}
	d.halfPageOffset = chooseHalf(y/float64(r.Dy()), topY0, topY1, botY0, botY1, d.halfPageOffset)
}

// parseSyncCommand parses one control-file command written by vim's forward
// sync (DocViewerForwardSync in vimrc.d/latex.vim): either "page" — the
// original jump-only format — or "page x y" with the synctex position of the
// source line in PDF points, top-left origin. Anything else is rejected.
func parseSyncCommand(line string) (syncTarget, bool) {
	fields := strings.Fields(line)
	if len(fields) != 1 && len(fields) != 3 {
		return syncTarget{}, false
	}
	page, err := strconv.Atoi(fields[0])
	if err != nil || page < 1 {
		return syncTarget{}, false
	}
	if len(fields) == 1 {
		return syncTarget{page: page}, true
	}
	x, errX := strconv.ParseFloat(fields[1], 64)
	y, errY := strconv.ParseFloat(fields[2], 64)
	if errX != nil || errY != nil {
		return syncTarget{}, false
	}
	return syncTarget{page: page, x: x, y: y, hasPoint: true}, true
}

func (d *DocumentViewer) fifoListener(syncChan chan<- syncTarget, stopChan <-chan struct{}) {
	var lastMod time.Time

	for {
		select {
		case <-stopChan:
			return
		default:
		}

		// Check if control file exists and was modified
		info, err := os.Stat(d.fifoPath)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		if info.ModTime().After(lastMod) {
			lastMod = info.ModTime()

			data, err := os.ReadFile(d.fifoPath)
			if err == nil {
				if t, ok := parseSyncCommand(string(data)); ok {
					select {
					case syncChan <- t:
					default:
					}
				}
			}
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// viewLoc is one jump-history entry: a 0-indexed PDF page plus which half was
// showing. PDF pages (not textPages indices) survive reloads — jumpToPage
// re-resolves them with a closest-match.
type viewLoc struct {
	page0      int
	halfOffset int
}

// currentLoc captures the on-screen location for the history stacks.
func (d *DocumentViewer) currentLoc() (viewLoc, bool) {
	if len(d.textPages) == 0 || d.currentPage < 0 || d.currentPage >= len(d.textPages) {
		return viewLoc{}, false
	}
	return viewLoc{page0: d.textPages[d.currentPage], halfOffset: d.halfPageOffset}, true
}

// pushHistory records the current location before a navigation jump and, like
// a browser, clears the forward stack. Consecutive duplicates are dropped so
// repeated syncs to the same page don't pile up.
func (d *DocumentViewer) pushHistory() {
	loc, ok := d.currentLoc()
	if !ok {
		return
	}
	if n := len(d.backStack); n > 0 && d.backStack[n-1] == loc {
		d.fwdStack = nil
		return
	}
	d.backStack = append(d.backStack, loc)
	d.fwdStack = nil
}

// historyBack (Ctrl+I) returns to where the last jump came from, moving the
// current location to the forward stack. Entries equal to the current
// location are skipped: a jump that landed where it started left one behind.
func (d *DocumentViewer) historyBack() {
	cur, curOK := d.currentLoc()
	for len(d.backStack) > 0 {
		loc := d.backStack[len(d.backStack)-1]
		d.backStack = d.backStack[:len(d.backStack)-1]
		if curOK && loc == cur {
			continue
		}
		if curOK {
			d.fwdStack = append(d.fwdStack, cur)
		}
		d.applyLoc(loc)
		return
	}
}

// historyForward (Ctrl+O) re-applies a jump undone by historyBack.
func (d *DocumentViewer) historyForward() {
	cur, curOK := d.currentLoc()
	for len(d.fwdStack) > 0 {
		loc := d.fwdStack[len(d.fwdStack)-1]
		d.fwdStack = d.fwdStack[:len(d.fwdStack)-1]
		if curOK && loc == cur {
			continue
		}
		if curOK {
			d.backStack = append(d.backStack, cur)
		}
		d.applyLoc(loc)
		return
	}
}

// applyLoc jumps to a history location. A history move is itself a jump, so
// it dismisses any forward-sync marker, matching the point-less sync case.
func (d *DocumentViewer) applyLoc(loc viewLoc) {
	d.jumpToPage(loc.page0 + 1)
	d.halfPageOffset = loc.halfOffset
	d.flash = flashState{}
}

// pageStep is how far a plain arrow / j / k moves: a spread shows two pages, so
// stepping by one would re-show a page that was already on screen. Half-page
// mode does its own stepping and never asks.
func (d *DocumentViewer) pageStep() int {
	if d.dualPageMode == "vertical" || d.dualPageMode == "horizontal" {
		return 2
	}
	return 1
}

// stepPages moves delta pages, clamped to the document. Landing short of a full
// step at the end still moves to the last page, so the tail of the document is
// always reachable.
func (d *DocumentViewer) stepPages(delta int) {
	page := d.currentPage + delta
	if page < 0 {
		page = 0
	}
	if page > len(d.textPages)-1 {
		page = len(d.textPages) - 1
	}
	if page >= 0 {
		d.currentPage = page
	}
}

func (d *DocumentViewer) jumpToPage(page int) {
	// page is 1-indexed from external command
	// Find the index in textPages that corresponds to this PDF page
	targetPdfPage := page - 1 // Convert to 0-indexed PDF page

	// First try exact match
	for i, pdfPage := range d.textPages {
		if pdfPage == targetPdfPage {
			d.currentPage = i
			return
		}
	}

	// If exact page not in textPages, find closest page
	for i, pdfPage := range d.textPages {
		if pdfPage >= targetPdfPage {
			d.currentPage = i
			return
		}
	}

	// If target is beyond all pages, go to last
	if len(d.textPages) > 0 {
		d.currentPage = len(d.textPages) - 1
	}
}

func (d *DocumentViewer) checkAndReload() bool {
	info, err := os.Stat(d.path)
	if err != nil {
		return false
	}

	if !info.ModTime().After(d.lastModTime) {
		return false
	}

	// File changed. Wait briefly for the size to stop changing, which catches the
	// common case of statting the file mid-write.
	lastSize := info.Size()
	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		newInfo, err := os.Stat(d.path)
		if err != nil {
			return false
		}
		info = newInfo
		if newInfo.Size() == lastSize && newInfo.Size() > 0 {
			break
		}
		lastSize = newInfo.Size()
	}

	// For PDFs, only reload once the file is completely written, detected by a
	// trailing %%EOF marker. pdflatex truncates and rewrites the PDF on every
	// build; opening it mid-write makes MuPDF emit parser warnings and render a
	// garbled or failed page. While the file is incomplete we keep the current
	// page on screen and, crucially, do NOT advance lastModTime — so the pending
	// change is re-detected and retried on a later tick.
	if !d.isImage && d.fileType == "pdf" && !pdfLooksComplete(d.path) {
		return false
	}

	d.lastModTime = info.ModTime()

	if d.isImage {
		f, err := os.Open(d.path)
		if err != nil {
			return false
		}
		defer f.Close()
		img, _, err := image.Decode(f)
		if err != nil {
			return false
		}
		d.sourceImage = img
		d.skipClear = true
		return true
	}

	savedPage := d.currentPage

	// stderr is silenced for the whole interactive session (see silenceStderr in
	// Run), so even an imperfect file can't leak MuPDF warnings onto the screen.
	doc, openErr := fitz.New(d.path)
	if openErr != nil {
		return false
	}

	// Check that the new doc has valid pages before switching to it.
	oldDoc := d.doc
	oldPages := d.textPages
	oldPage := d.currentPage
	oldTOC := d.toc

	d.doc = doc
	if d.isReflowable {
		d.applyHTMLLayout()
	} else {
		d.findContentPages()
	}

	if len(d.textPages) == 0 {
		// New doc is invalid/corrupted - keep the old one and the page on screen.
		d.doc = oldDoc
		d.textPages = oldPages
		d.currentPage = oldPage
		d.toc = oldTOC
		doc.Close()
		return false
	}

	// New doc is good, close the old one.
	oldDoc.Close()

	// Restore page position (clamp to valid range).
	if savedPage >= len(d.textPages) {
		savedPage = len(d.textPages) - 1
	}
	if savedPage < 0 {
		savedPage = 0
	}
	d.currentPage = savedPage
	d.skipClear = true // Skip screen clear to avoid blink on reload
	return true
}

// pdfLooksComplete reports whether the file appears to be a fully written PDF,
// i.e. its tail contains the %%EOF end-of-file marker. PDF writers (pdflatex
// included) emit %%EOF only after the whole file is flushed, so its absence
// means the file is still being written.
func pdfLooksComplete(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return false
	}

	const tailLen = 2048
	size := info.Size()
	start := size - tailLen
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return false
	}
	return bytes.Contains(buf, []byte("%%EOF"))
}

// handleInput returns: 0 = continue, 1 = quit, -1 = search, -2 = goto page,
// -6 = table of contents
func (d *DocumentViewer) handleInput(c byte) int {
	switch c {
	case 'q':
		return 1
	case 'b':
		d.wantBack = true
		return 1
	case 'j', ' ', keyArrowNext:
		if d.dualPageMode == "half" {
			if d.halfPageOffset == 0 {
				d.halfPageOffset = 1
			} else {
				d.halfPageOffset = 0
				d.stepPages(1)
			}
		} else {
			d.stepPages(d.pageStep())
		}
	case 'k', keyArrowPrev:
		if d.dualPageMode == "half" {
			if d.halfPageOffset == 1 {
				d.halfPageOffset = 0
			} else {
				d.halfPageOffset = 1
				d.stepPages(-1)
			}
		} else {
			d.stepPages(-d.pageStep())
		}
	case 'g':
		return -2 // signal: go to page
	case 'T':
		return -6 // signal: show table of contents
	case 'h', '?':
		return -3 // signal: show help
	case 't':
		d.toggleViewMode()
	case 'f':
		switch d.fitMode {
		case "height":
			d.fitMode = "width"
		case "width":
			d.fitMode = "auto"
		default:
			d.fitMode = "height"
		}
	case '/':
		return -1 // signal: start search
	case 'n':
		d.nextSearchHit()
	case 'N':
		d.prevSearchHit()
	case '+', '=':
		if d.isReflowable {
			// Narrower page = larger text
			d.adjustHTMLZoom(-100)
		} else {
			d.setZoom(d.zoom() + 0.1)
		}
	case '-', '_':
		if d.isReflowable {
			// Wider page = smaller text
			d.adjustHTMLZoom(100)
		} else {
			d.setZoom(d.zoom() - 0.1)
		}
	case 'r':
		// Refresh cell size (useful after resolution/monitor change)
		d.refreshCellSize()
	case 'S':
		d.openInExternalApp("Skim")
	case 'P':
		d.openInExternalApp("Preview")
	case 'v':
		d.syncToVim()
	case keyMouseAltClick: // Opt+left click — synctex jump at the clicked point
		d.handleAltClick()
	case keyMouseCtrlClick: // Ctrl+left click — follow the hyperlink under it
		if d.handleCtrlClick() {
			return -5 // internal jump queued on syncChan; its case redraws
		}
	case keyHistoryBack: // Ctrl+I (or Cmd+Left) — back through the jump history
		d.historyBack()
	case keyHistoryForward: // Ctrl+O (or Cmd+Right) — forward again
		d.historyForward()
	case 'O':
		absPath, _ := filepath.Abs(d.path)
		exec.Command("open", "-R", absPath).Start()
	case 'D': // cycle: off -> dark -> invert -> dim -> off
		switch d.darkMode {
		case "":
			d.darkMode = "smart"
		case "smart":
			d.darkMode = "invert"
		case "invert":
			d.darkMode = "dim"
		default:
			d.darkMode = ""
		}
	case 'd':
		// Debug: show detected dimensions
		return -4 // signal: show debug info
	case '2':
		switch d.dualPageMode {
		case "":
			d.dualPageMode = "vertical"
		case "vertical":
			d.dualPageMode = "horizontal"
		case "horizontal":
			d.dualPageMode = "half"
			d.halfPageOffset = 0
		default:
			d.dualPageMode = ""
		}
	case 'J': // Shift+Down/Right: one page, whatever the plain step is
		d.stepPages(1)
	case 'K': // Shift+Up/Left: back one page
		d.stepPages(-1)
	case '{': // shift+[ — cut more from top
		d.cropTop = min(d.cropTop+0.02, 0.45)
	case '}': // shift+] — cut more from bottom
		d.cropBottom = min(d.cropBottom+0.02, 0.45)
	case '[': // cut more from left
		d.cropLeft = min(d.cropLeft+0.02, 0.45)
	case ']': // cut more from right
		d.cropRight = min(d.cropRight+0.02, 0.45)
	case keyUncropTop: // cmd+opt+shift+[ — restore top edge (inverse of '{')
		d.cropTop = max(d.cropTop-0.02, 0)
	case keyUncropBottom: // cmd+opt+shift+] — restore bottom edge (inverse of '}')
		d.cropBottom = max(d.cropBottom-0.02, 0)
	case keyUncropLeft: // cmd+opt+[ — restore left edge (inverse of '[')
		d.cropLeft = max(d.cropLeft-0.02, 0)
	case keyUncropRight: // cmd+opt+] — restore right edge (inverse of ']')
		d.cropRight = max(d.cropRight-0.02, 0)
	case '\\': // backslash — reset all crops
		d.cropTop, d.cropBottom, d.cropLeft, d.cropRight = 0, 0, 0, 0
	case 27: // ESC key (arrow keys handled in readSingleChar)
		// Do nothing for plain ESC
	}
	return 0
}

func (d *DocumentViewer) openInExternalApp(appName string) {
	absPath, _ := filepath.Abs(d.path)
	page := d.currentPage + 1 // convert 0-indexed to 1-indexed
	switch appName {
	case "Skim":
		// Revert (reload) if already open, then open and navigate to page
		script := fmt.Sprintf(`
set theFile to POSIX file "%s"
tell application "Skim"
  set theDocs to get documents whose path is (get POSIX path of theFile)
  if (count of theDocs) > 0 then
    try
      revert theDocs
    end try
  end if
  open theFile
  set index of current page of document 1 to %d
end tell
`, absPath, page)
		go exec.Command("osascript", "-e", script).Run()
	case "Preview":
		exec.Command("open", "-a", appName, absPath).Start()
	}
}

// handleAltClick maps the last Opt+click (cell coordinates stored by
// readSingleChar) through the current clickMap to a point on a PDF page and
// asks synctex which source line produced it. Clicks outside the displayed
// image — or on a text page, which has no map — are ignored.
func (d *DocumentViewer) handleAltClick() {
	d.mouseMu.Lock()
	col, row := d.mouseCol, d.mouseRow
	// Consume the coordinates: a stray 0xE8 byte (e.g. a pasted UTF-8 lead
	// byte) then maps through (0,0) and is rejected instead of replaying the
	// previous click.
	d.mouseCol, d.mouseRow = 0, 0
	d.mouseMu.Unlock()
	page0, x, y, ok := d.clickMap.cellToPDF(col, row)
	if !ok {
		return
	}
	d.syncToVimAt(page0, x, y)
}

// linksForPage returns the cached links of a 0-indexed page, extracting them
// on first use. Extraction is a few MuPDF calls (no rendering), but it waits
// on go-fitz's document mutex, which queued prefetch renders can hold for
// seconds — hence warmPageLinks below. Main goroutine only.
func (d *DocumentViewer) linksForPage(page0 int) []pageLink {
	if d.doc == nil {
		return nil
	}
	d.linksMu.Lock()
	links, ok := d.pageLinks[page0]
	d.linksMu.Unlock()
	if ok {
		return links
	}
	links = documentLinks(d.doc, page0)
	d.linksMu.Lock()
	d.pageLinks[page0] = links
	d.linksMu.Unlock()
	return links
}

// warmPageLinks extracts a page's links in the background so the first
// Ctrl+click finds them cached. Spawned at display time just before the
// neighbor prefetch renders, so it takes the go-fitz mutex ahead of them;
// a click a moment later is then a map hit instead of a multi-second wait
// behind queued 600-DPI renders. doc is captured by the caller on the main
// goroutine; the generation check discards results that raced a reload.
func (d *DocumentViewer) warmPageLinks(doc *fitz.Document, page0 int) {
	if doc == nil {
		return
	}
	d.linksMu.Lock()
	_, ok := d.pageLinks[page0]
	gen := d.linksGen
	d.linksMu.Unlock()
	if ok {
		return
	}
	links := documentLinks(doc, page0)
	d.linksMu.Lock()
	if d.linksGen == gen {
		d.pageLinks[page0] = links
	}
	d.linksMu.Unlock()
}

// allowedExternalScheme reports whether a link URI is safe to hand to macOS
// `open`. fz_is_external_link means merely "the URI has a scheme", which
// includes file://, smb://, and arbitrary app schemes that LaunchServices
// would act on without any confirmation — a hostile PDF must not fire those.
func allowedExternalScheme(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto":
		return true
	}
	return false
}

func openExternalURI(uri string) {
	if allowedExternalScheme(uri) {
		exec.Command("open", uri).Start()
	}
}

// handleCtrlClick follows the hyperlink under the last Ctrl+click: an
// external URI opens via macOS `open`; an internal destination goes through
// syncChan, taking the exact jump+flash+half-roll path of a vim forward sync
// so the target line gets the same transient rule. Returns true when an
// internal jump was queued (the caller then skips its redraw). Clicks that
// hit no link, a text page, or an unresolvable destination do nothing.
func (d *DocumentViewer) handleCtrlClick() bool {
	d.mouseMu.Lock()
	col, row := d.mouseCol, d.mouseRow
	// Consume the coordinates, like handleAltClick: a stray 0xE9 byte then
	// maps through (0,0) and is rejected instead of replaying the last click.
	d.mouseCol, d.mouseRow = 0, 0
	d.mouseMu.Unlock()
	page0, x, y, ok := d.clickMap.cellToPDF(col, row)
	if !ok {
		return false
	}
	tolX, tolY := linkTolX, linkTolY
	if w, h, ok := d.clickMap.cellSizePDF(page0); ok {
		tolX = math.Max(tolX, w/2)
		tolY = math.Max(tolY, h/2)
	}
	link, ok := findLinkAt(d.linksForPage(page0), x, y, tolX, tolY)
	if !ok {
		return false
	}
	if link.external {
		openExternalURI(link.uri)
		return false
	}
	if link.destPage0 < 0 || d.syncChan == nil {
		return false
	}
	// A destination of exactly (0,0) is a bare "go to page" with no anchor
	// point; jump without flashing a rule at the page's top-left corner.
	t := syncTarget{page: link.destPage0 + 1, x: link.destX, y: link.destY,
		hasPoint: link.destX != 0 || link.destY != 0}
	select {
	case d.syncChan <- t:
		return true
	default:
		return false // a control-file jump got there first; let it win
	}
}

// syncToVim ('v' key): inverse sync at the center of the current page.
func (d *DocumentViewer) syncToVim() {
	if d.doc == nil || len(d.textPages) == 0 {
		return
	}
	pdfPage := d.textPages[d.currentPage] // 0-indexed PDF page on screen

	// Center of the page, in PDF points (top-left origin, what synctex wants).
	x, y := 306.0, 396.0 // US-Letter center fallback
	if r, err := d.doc.Bound(pdfPage); err == nil {
		x = float64(r.Dx()) / 2
		y = float64(r.Dy()) / 2
	}
	d.syncToVimAt(pdfPage, x, y)
}

// syncToVimAt performs inverse sync (docviewer -> editor): asks synctex which
// source file/line produced the point (x, y) — PDF points, top-left origin —
// on the 0-indexed page page0, then hands (file, line) to
// ~/.vim/docviewer-to-vim.sh, which drives the vim session in agterm. Mirrors
// DocViewerForwardSync in vimrc.d/latex.vim.
func (d *DocumentViewer) syncToVimAt(page0 int, x, y float64) {
	if d.doc == nil || len(d.textPages) == 0 {
		return
	}
	absPath, err := filepath.Abs(d.path)
	if err != nil {
		return
	}
	page := page0 + 1 // synctex is 1-indexed

	// Off the main loop: a book-sized .synctex.gz can take a while to query
	// and must not stall input or redraws. Only locals are captured.
	go func() {
		synctexBin := "/Library/TeX/texbin/synctex"
		if _, err := os.Stat(synctexBin); err != nil {
			synctexBin = "synctex" // fall back to PATH
		}
		query := fmt.Sprintf("%d:%.2f:%.2f:%s", page, x, y, absPath)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, synctexBin, "edit", "-o", query).Output()
		if err != nil {
			return
		}
		var srcFile string
		var line int
		for _, ln := range strings.Split(string(out), "\n") {
			switch {
			case strings.HasPrefix(ln, "Input:"):
				if srcFile == "" {
					srcFile = strings.TrimSpace(strings.TrimPrefix(ln, "Input:"))
				}
			case strings.HasPrefix(ln, "Line:"):
				if line == 0 {
					line, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(ln, "Line:")))
				}
			}
		}
		if srcFile == "" || line <= 0 {
			return
		}
		// synctex may report the source relative to the PDF's directory
		if !filepath.IsAbs(srcFile) {
			srcFile = filepath.Join(filepath.Dir(absPath), srcFile)
		}
		// Run (not Start) so the child is reaped — we're already off the
		// main loop, and the script drives vim/agterm on its own.
		home, _ := os.UserHomeDir()
		exec.Command(filepath.Join(home, ".vim", "docviewer-to-vim.sh"),
			srcFile, strconv.Itoa(line)).Run()
	}()
}

// searchLine is one line of a page's text with the vertical center of its box
// in PDF points (top-left origin). text is lowercased for matching against the
// (already lowercased) search query.
type searchLine struct {
	y    float64
	text string
}

// htmlLineRe matches one line of MuPDF's HTML rendering of a page's structured
// text: <p style="top:90.4pt;left:66.1pt;line-height:10.9pt"><span …>…</span>…</p>.
// The line box is what makes this worth parsing — doc.Text gives the same text
// with no positions at all.
var htmlLineRe = regexp.MustCompile(`(?s)<p[^>]*top:([0-9.]+)pt[^>]*>(.*?)</p>`)

// htmlLineHeightRe pulls the line box height out of the same style attribute.
var htmlLineHeightRe = regexp.MustCompile(`line-height:([0-9.]+)pt`)

// htmlTagRe strips the per-font <span> markup inside a line.
var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// parseSearchLines extracts the positioned text lines from MuPDF's HTML output
// for one page.
func parseSearchLines(pageHTML string) []searchLine {
	var lines []searchLine
	for _, m := range htmlLineRe.FindAllStringSubmatch(pageHTML, -1) {
		top, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(html.UnescapeString(htmlTagRe.ReplaceAllString(m[2], "")))
		if text == "" {
			continue
		}
		// Aim at the middle of the line box: top alone sits a third of a
		// terminal row high in the half-page view, enough to shift a marker.
		center := top
		if h := htmlLineHeightRe.FindStringSubmatch(m[0]); h != nil {
			if lh, err := strconv.ParseFloat(h[1], 64); err == nil {
				center += lh / 2
			}
		}
		lines = append(lines, searchLine{y: center, text: strings.ToLower(text)})
	}
	return lines
}

// searchLines returns the page's text lines with their positions, cached per
// page: a redraw happens on every keystroke, and the extraction runs the page
// through MuPDF's structured-text device. Cleared on reload by findContentPages.
func (d *DocumentViewer) searchLines(page0 int) []searchLine {
	if lines, ok := d.searchLineCache[page0]; ok {
		return lines
	}
	if d.doc == nil {
		return nil
	}
	pageHTML, err := d.doc.HTML(page0, false)
	if err != nil {
		return nil
	}
	lines := parseSearchLines(pageHTML)
	if d.searchLineCache == nil {
		d.searchLineCache = make(map[int][]searchLine)
	}
	d.searchLineCache[page0] = lines
	return lines
}

func (d *DocumentViewer) startSearch(inputChan <-chan byte) {
	if d.isImage {
		return // no text to search in standalone images
	}
	_, rows := d.getTerminalSize()
	fmt.Printf("\033[%d;1H\033[K", rows) // bottom line
	fmt.Print("\033[?25h")               // show cursor
	fmt.Print("Search: ")

	var query []byte
	for {
		ch := <-inputChan
		switch ch {
		case 13, 10: // Enter
			goto done
		case 27: // Escape - cancel
			fmt.Print("\033[?25l")
			return
		case 127, 8: // Backspace
			if len(query) > 0 {
				query = query[:len(query)-1]
				fmt.Printf("\033[%d;1H\033[K", rows)
				fmt.Printf("Search: %s", string(query))
			}
		default:
			if ch >= 32 && ch < 127 {
				query = append(query, ch)
				fmt.Printf("%c", ch)
			}
		}
	}
done:
	fmt.Print("\033[?25l") // hide cursor
	queryStr := strings.TrimSpace(string(query))

	if queryStr == "" {
		d.searchQuery = ""
		d.searchHits = nil
		return
	}

	d.searchQuery = strings.ToLower(queryStr)
	d.searchHits = nil
	d.searchHitIdx = 0

	// Search all pages
	for _, pageNum := range d.textPages {
		text, err := d.doc.Text(pageNum)
		if err == nil && strings.Contains(strings.ToLower(text), d.searchQuery) {
			d.searchHits = append(d.searchHits, pageNum)
		}
	}

	if len(d.searchHits) > 0 {
		// Jump to first hit
		d.jumpToHit()
	}
}

// jumpToHit moves to the current search hit. A search jump is a navigation like
// a link follow, so it records where it came from for Ctrl+I; cycling hits on
// the same page moves nothing and records nothing.
func (d *DocumentViewer) jumpToHit() {
	targetPage := d.searchHits[d.searchHitIdx]
	for i, p := range d.textPages {
		if p == targetPage {
			if i != d.currentPage {
				d.pushHistory()
				d.currentPage = i
			}
			break
		}
	}
}

func (d *DocumentViewer) nextSearchHit() {
	if len(d.searchHits) == 0 {
		return
	}
	d.searchHitIdx = (d.searchHitIdx + 1) % len(d.searchHits)
	d.jumpToHit()
}

func (d *DocumentViewer) prevSearchHit() {
	if len(d.searchHits) == 0 {
		return
	}
	d.searchHitIdx--
	if d.searchHitIdx < 0 {
		d.searchHitIdx = len(d.searchHits) - 1
	}
	d.jumpToHit()
}

func (d *DocumentViewer) toggleViewMode() {
	switch d.forceMode {
	case "":
		d.forceMode = "text"
	case "text":
		d.forceMode = "image"
	case "image":
		d.forceMode = ""
	}
}

func (d *DocumentViewer) goToPage(inputChan <-chan byte) {
	_, rows := d.getTerminalSize()
	fmt.Printf("\033[%d;1H\033[K", rows)
	fmt.Print("\033[?25h")
	fmt.Printf("Go to page (1-%d): ", len(d.textPages))

	var input []byte
	for {
		ch := <-inputChan
		switch ch {
		case 13, 10: // Enter
			goto done
		case 27: // Escape
			fmt.Print("\033[?25l")
			return
		case 127, 8: // Backspace
			if len(input) > 0 {
				input = input[:len(input)-1]
				fmt.Printf("\033[%d;1H\033[K", rows)
				fmt.Printf("Go to page (1-%d): %s", len(d.textPages), string(input))
			}
		default:
			if ch >= '0' && ch <= '9' {
				input = append(input, ch)
				fmt.Printf("%c", ch)
			}
		}
	}
done:
	fmt.Print("\033[?25l")
	var num int
	if _, err := fmt.Sscanf(string(input), "%d", &num); err == nil {
		if num >= 1 && num <= len(d.textPages) && num-1 != d.currentPage {
			d.pushHistory()
			d.currentPage = num - 1
		}
	}
}
