package main

import (
	"fmt"
	"hash/fnv"
	"os"
)

// cachedRender is a fully-rendered single page on disk plus the geometry the
// caller needs to place it (return values of savePageAsImage).
type cachedRender struct {
	path       string
	lines      int
	widthChars int
	pxW        int
	pxH        int
}

// renderParams is a snapshot of every viewer field that affects a rendered page.
// It is captured on the main thread so the background prefetch goroutine never
// reads mutable viewer state directly (avoids a data race with key handling).
type renderParams struct {
	pixelsPerChar float64
	pixelsPerLine float64
	scaleFactor   float64
	fitMode       string
	darkMode      string
	cropTop       float64
	cropBottom    float64
	cropLeft      float64
	cropRight     float64
	// included in the cache signature only:
	dualPageMode   string
	halfPageOffset int
	htmlPageWidth  int
	lastMod        int64
}

// snapshotParams captures the current render-affecting state. Call on the main
// thread (it reads cached cell size and viewer fields).
func (d *DocumentViewer) snapshotParams() renderParams {
	pc, pl := d.getTerminalCellSize()
	return renderParams{
		pixelsPerChar:  pc,
		pixelsPerLine:  pl,
		scaleFactor:    d.scaleFactor,
		fitMode:        d.fitMode,
		darkMode:       d.darkMode,
		cropTop:        d.cropTop,
		cropBottom:     d.cropBottom,
		cropLeft:       d.cropLeft,
		cropRight:      d.cropRight,
		dualPageMode:   d.dualPageMode,
		halfPageOffset: d.halfPageOffset,
		htmlPageWidth:  d.htmlPageWidth,
		lastMod:        d.lastModTime.UnixNano(),
	}
}

// maxCachedPages caps the on-disk render cache. Each entry is one PNG in tempDir;
// they are removed on eviction and the whole tempDir is cleaned up on quit.
const maxCachedPages = 12

// renderSig is a cache key covering everything that changes a rendered page.
// If any of these differ, the cached image is stale and must be re-rendered.
// rp.lastMod busts the cache on auto-reload.
func renderSig(pageNum, termWidth, termHeight int, termType string, rp renderParams) string {
	return fmt.Sprintf("p%d|w%d|h%d|t%s|f%s|s%.3f|d%s|dp%s|ho%d|c%.4f,%.4f,%.4f,%.4f|hw%d|m%d",
		pageNum, termWidth, termHeight, termType, rp.fitMode, rp.scaleFactor, rp.darkMode,
		rp.dualPageMode, rp.halfPageOffset, rp.cropTop, rp.cropBottom, rp.cropLeft, rp.cropRight,
		rp.htmlPageWidth, rp.lastMod)
}

// cachePath returns a stable per-signature filename in tempDir.
func (d *DocumentViewer) cachePath(sig string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(sig))
	return fmt.Sprintf("%s/cache_%016x.png", d.tempDir, h.Sum64())
}

// cacheGet returns a cached render if present and its file still exists.
func (d *DocumentViewer) cacheGet(sig string) (cachedRender, bool) {
	d.cacheMu.Lock()
	c, ok := d.renderCache[sig]
	d.cacheMu.Unlock()
	if !ok {
		return cachedRender{}, false
	}
	if _, err := os.Stat(c.path); err != nil {
		// File vanished (e.g. tempDir cleaned) - drop the stale entry.
		d.cacheMu.Lock()
		delete(d.renderCache, sig)
		d.cacheMu.Unlock()
		return cachedRender{}, false
	}
	return c, true
}

// cacheStore records a render and evicts the oldest entries past the cap,
// deleting their files.
func (d *DocumentViewer) cacheStore(sig string, c cachedRender) {
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	if _, exists := d.renderCache[sig]; !exists {
		d.cacheOrder = append(d.cacheOrder, sig)
	}
	d.renderCache[sig] = c
	for len(d.cacheOrder) > maxCachedPages {
		oldest := d.cacheOrder[0]
		d.cacheOrder = d.cacheOrder[1:]
		if old, ok := d.renderCache[oldest]; ok {
			os.Remove(old.path)
			delete(d.renderCache, oldest)
		}
	}
}

// prefetchNeighbors renders the next/previous logical pages into the cache in the
// background so sequential reading feels instant. Called on the main thread for
// the single-page image path; a no-op when dual/half/reflow mode is active.
func (d *DocumentViewer) prefetchNeighbors(termWidth, termHeight int) {
	if d.dualPageMode != "" || d.isReflowable || d.isImage {
		return
	}
	// Match displayImagePage's available height (reserved 2 + top padding 1).
	maxHeight := termHeight - 3
	if maxHeight <= 0 {
		return
	}
	rp := d.snapshotParams() // snapshot on the main thread
	termType := d.detectTerminalType()
	for _, delta := range []int{1, -1} {
		idx := d.currentPage + delta
		if idx < 0 || idx >= len(d.textPages) {
			continue
		}
		pageNum := d.textPages[idx]
		sig := renderSig(pageNum, termWidth, maxHeight, termType, rp)
		if _, ok := d.cacheGet(sig); ok {
			continue
		}
		d.cacheMu.Lock()
		if d.inFlight[sig] {
			d.cacheMu.Unlock()
			continue
		}
		d.inFlight[sig] = true
		d.cacheMu.Unlock()
		go d.warmPage(pageNum, termWidth, maxHeight, termType, rp, sig)
	}
}

// warmPage renders a page into the cache off the display path (no terminal
// output), using a state snapshot captured on the main thread.
func (d *DocumentViewer) warmPage(pageNum, maxWidth, maxHeight int, termType string, rp renderParams, sig string) {
	defer func() {
		// A corrupt mid-recompile page must not crash the reader; prefetch is
		// best-effort, so swallow any panic from the render.
		recover()
		d.cacheMu.Lock()
		delete(d.inFlight, sig)
		d.cacheMu.Unlock()
	}()
	// savePageAsImage populates the cache as a side effect.
	d.savePageAsImage(pageNum, maxWidth, maxHeight, termType, rp)
}
