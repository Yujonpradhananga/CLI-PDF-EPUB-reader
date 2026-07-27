package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
		scaleFactor:    d.zoom(),
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

// maxCachedPages caps the in-memory render index. Evicted entries only drop the
// map entry; the PNG stays in the persistent cache dir, owned by the GC below.
const maxCachedPages = 48

// maxPersistentCached caps the cross-session render cache (newest-by-mtime
// survive; cache hits touch the file, giving approximate LRU).
const maxPersistentCached = 200

// persistentCacheDir returns the cross-session render cache directory
// (~/Library/Caches/docviewer on macOS), or "" if it can't be created.
func persistentCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(base, "docviewer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

// renderSig is a cache key covering everything that changes a rendered page.
// If any of these differ, the cached image is stale and must be re-rendered.
// rp.lastMod busts the cache on auto-reload. Cell pixel size matters for the
// persistent cache: across sessions the same cols x rows can be a different
// monitor/font, i.e. different pixel dimensions.
func renderSig(pageNum, termWidth, termHeight int, termType string, rp renderParams) string {
	return fmt.Sprintf("p%d|w%d|h%d|t%s|f%s|s%.3f|d%s|dp%s|ho%d|c%.4f,%.4f,%.4f,%.4f|hw%d|m%d|px%.2fx%.2f|ss%.2f",
		pageNum, termWidth, termHeight, termType, rp.fitMode, rp.scaleFactor, rp.darkMode,
		rp.dualPageMode, rp.halfPageOffset, rp.cropTop, rp.cropBottom, rp.cropLeft, rp.cropRight,
		rp.htmlPageWidth, rp.lastMod, rp.pixelsPerChar, rp.pixelsPerLine, superSampleCeiling())
}

// cachePath returns a stable filename in the persistent cache dir, keyed by
// the document's absolute path plus the render signature (the signature alone
// isn't unique across documents once the cache dir is shared).
func (d *DocumentViewer) cachePath(sig string) string {
	absPath, _ := filepath.Abs(d.path)
	h := fnv.New64a()
	_, _ = h.Write([]byte(absPath))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(sig))
	return filepath.Join(d.cacheDir, fmt.Sprintf("cache_%016x.png", h.Sum64()))
}

// writeMeta persists the placement geometry next to a cached PNG so a later
// session can reuse the render without re-deriving lines/cols/pixels.
func writeMeta(pngPath string, c cachedRender) {
	data := fmt.Sprintf("%d %d %d %d\n", c.lines, c.widthChars, c.pxW, c.pxH)
	_ = os.WriteFile(pngPath+".meta", []byte(data), 0o644)
}

// readMeta loads the geometry sidecar for a cached PNG.
func readMeta(pngPath string) (cachedRender, bool) {
	data, err := os.ReadFile(pngPath + ".meta")
	if err != nil {
		return cachedRender{}, false
	}
	var c cachedRender
	if _, err := fmt.Sscanf(string(data), "%d %d %d %d", &c.lines, &c.widthChars, &c.pxW, &c.pxH); err != nil {
		return cachedRender{}, false
	}
	if c.lines <= 0 || c.widthChars <= 0 {
		return cachedRender{}, false
	}
	c.path = pngPath
	return c, true
}

// gcPersistentCache prunes the shared cache dir to the newest maxPersistentCached
// PNGs and sweeps orphaned .meta and stale .tmp files. Run in a goroutine at
// viewer startup; concurrent viewers at worst re-render a pruned page.
func gcPersistentCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type pngFile struct {
		path string
		mod  time.Time
	}
	var pngs []pngFile
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "cache_") {
			continue
		}
		full := filepath.Join(dir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		switch {
		case strings.Contains(name, ".tmp"):
			// Leftover from a crashed writer; the rename never happened.
			if time.Since(info.ModTime()) > time.Hour {
				os.Remove(full)
			}
		case strings.HasSuffix(name, ".png"):
			pngs = append(pngs, pngFile{full, info.ModTime()})
		case strings.HasSuffix(name, ".meta"):
			if _, err := os.Stat(strings.TrimSuffix(full, ".meta")); err != nil {
				os.Remove(full)
			}
		}
	}
	if len(pngs) <= maxPersistentCached {
		return
	}
	sort.Slice(pngs, func(i, j int) bool { return pngs[i].mod.After(pngs[j].mod) })
	for _, p := range pngs[maxPersistentCached:] {
		os.Remove(p.path)
		os.Remove(p.path + ".meta")
	}
}

// cacheGet returns a cached render if present and its file still exists. On an
// in-memory miss it falls back to the persistent cache dir, so reopening a
// document (or re-toggling dark mode/zoom) reuses renders from past sessions.
func (d *DocumentViewer) cacheGet(sig string) (cachedRender, bool) {
	d.cacheMu.Lock()
	c, ok := d.renderCache[sig]
	d.cacheMu.Unlock()
	if ok {
		if _, err := os.Stat(c.path); err == nil {
			return c, true
		}
		// File vanished (e.g. GC pruned it) - drop the stale entry.
		d.cacheMu.Lock()
		delete(d.renderCache, sig)
		d.cacheMu.Unlock()
		return cachedRender{}, false
	}

	// Disk fallback: a previous session may have rendered this exact signature.
	pngPath := d.cachePath(sig)
	if _, err := os.Stat(pngPath); err != nil {
		return cachedRender{}, false
	}
	c, ok = readMeta(pngPath)
	if !ok {
		return cachedRender{}, false
	}
	now := time.Now()
	_ = os.Chtimes(pngPath, now, now) // LRU touch for gcPersistentCache
	d.cacheStore(sig, c)
	return c, true
}

// cacheStore records a render and evicts the oldest map entries past the cap.
// Eviction drops only the in-memory index; the PNG stays on disk for the
// persistent cache (gcPersistentCache owns file deletion).
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
		delete(d.renderCache, oldest)
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
	// Nearest pages first: goroutines contend on go-fitz's internal lock, so
	// spawn order roughly determines render order.
	for _, delta := range []int{1, -1, 2, -2, 3, -3} {
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
