package main

// Scripting interface. Every running viewer listens on its own Unix socket
// under ctlSocketDir(), so an outside process (dvctl, a vim mapping, an agterm
// hotkey) can read the view state and change any setting without the reader
// pressing a key. The existing /tmp/docviewer_*.ctrl file stays as it was — it
// is vim's forward-sync channel and is keyed by document, whereas this is keyed
// by process and carries the whole settings vocabulary.
//
// Threading: nearly every field of DocumentViewer is main-goroutine-only, so
// the accept loop never touches the viewer. It hands each request to Run's
// select loop over ctlChan and waits for the reply the loop sends back.

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ctlRequest is one command, newline-delimited JSON on the socket. Args are
// strings so a caller can write either an absolute value ("0.06") or a
// relative one ("+0.02"); the server parses per key.
type ctlRequest struct {
	Cmd  string            `json:"cmd"`
	Args map[string]string `json:"args,omitempty"`
}

// ctlState is the whole externally visible state of a viewer.
type ctlState struct {
	PID         int     `json:"pid"`
	Path        string  `json:"path"`
	Name        string  `json:"name"`
	FileType    string  `json:"file_type"`
	Page        int     `json:"page"` // 1-indexed
	Pages       int     `json:"pages"`
	Fit         string  `json:"fit"`
	Tint        string  `json:"tint"` // off|dim|dark|invert
	View        string  `json:"view"` // auto|text|image
	Dual        string  `json:"dual"` // off|vertical|horizontal|half
	HalfOffset  string  `json:"half"` // top|bottom
	Zoom        float64 `json:"zoom"`
	HTMLWidth   int     `json:"html_width"`
	CropTop     float64 `json:"crop_top"`
	CropBottom  float64 `json:"crop_bottom"`
	CropLeft    float64 `json:"crop_left"`
	CropRight   float64 `json:"crop_right"`
	Search      string  `json:"search,omitempty"`
	SearchHits  int     `json:"search_hits"`
	SearchIndex int     `json:"search_index"`
	Reflowable  bool    `json:"reflowable"`
	IsImage     bool    `json:"is_image"`
	AgtermID    string  `json:"agterm_session,omitempty"`
	Socket      string  `json:"socket"`
}

type ctlResponse struct {
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	State *ctlState `json:"state,omitempty"`
	TOC   []ctlTOC  `json:"toc,omitempty"`
}

type ctlTOC struct {
	Level int    `json:"level"`
	Title string `json:"title"`
	Page  int    `json:"page"`
}

// ctlCommand carries a request from the accept loop to Run's select loop and
// the reply back. reply is buffered, so a caller that gave up on its deadline
// never blocks the viewer.
type ctlCommand struct {
	req   ctlRequest
	reply chan ctlResponse
}

// ctlAction tells Run what to do after applyCtl: whether to redraw and whether
// the viewer should exit.
type ctlAction struct {
	redraw bool
	quit   bool
	back   bool
}

func ctlSocketDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "docviewer-ctl")
	}
	return filepath.Join(home, ".cache", "docviewer", "ctl")
}

func ctlSocketPath(pid int) string {
	return filepath.Join(ctlSocketDir(), fmt.Sprintf("%d.sock", pid))
}

// startControlServer opens this process's socket and serves it until stopChan
// closes. Best-effort: a viewer whose socket cannot be created runs exactly as
// it did before, just unscriptable.
func (d *DocumentViewer) startControlServer(stopChan <-chan struct{}) {
	if err := os.MkdirAll(ctlSocketDir(), 0700); err != nil {
		return
	}
	path := ctlSocketPath(os.Getpid())
	os.Remove(path) // a socket left by a crashed process with our pid
	ln, err := net.Listen("unix", path)
	if err != nil {
		return
	}
	d.ctlSocket = path
	// Only close the listener here. Unlinking is cleanupControlServer's job,
	// which Run defers so it runs BEFORE stopChan closes: main's picker loop
	// reuses this process for the next document, and a late unlink from this
	// goroutine would delete the socket the NEXT viewer had just created —
	// leaving it listening on a path nothing can find.
	go func() {
		<-stopChan
		ln.Close()
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed on shutdown
			}
			go d.serveCtlConn(conn, stopChan)
		}
	}()
}

func (d *DocumentViewer) cleanupControlServer() {
	if d.ctlSocket != "" {
		os.Remove(d.ctlSocket)
	}
}

func (d *DocumentViewer) serveCtlConn(conn net.Conn, stopChan <-chan struct{}) {
	defer conn.Close()
	// Comfortably longer than the waits below, so a timeout can still be
	// written back instead of the client seeing an unexplained EOF.
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	dec := json.NewDecoder(conn)
	var req ctlRequest
	if err := dec.Decode(&req); err != nil {
		writeCtlResponse(conn, ctlResponse{Error: "bad request: " + err.Error()})
		return
	}

	// status is answered from the published snapshot, off the main loop
	// entirely: a viewer sitting in its search prompt, TOC picker or help
	// screen must still be listed and targetable by name.
	if req.Cmd == "status" || req.Cmd == "" {
		if snap := d.ctlSnapshot.Load(); snap != nil {
			state := *snap
			writeCtlResponse(conn, ctlResponse{OK: true, State: &state})
			return
		}
	}

	// The handoff is unbuffered on purpose: it succeeds only when the main
	// loop is actually receiving. A command that could not be handed over was
	// therefore never queued, so "busy" cannot be followed by the command
	// running anyway once the reader closes a modal screen.
	cmd := ctlCommand{req: req, reply: make(chan ctlResponse, 1)}
	select {
	case d.ctlChan <- cmd:
	case <-stopChan:
		writeCtlResponse(conn, ctlResponse{Error: "viewer shutting down"})
		return
	case <-time.After(5 * time.Second):
		writeCtlResponse(conn, ctlResponse{Error: "viewer busy (its own prompt or picker is open)"})
		return
	}

	select {
	case resp := <-cmd.reply:
		writeCtlResponse(conn, resp)
	case <-time.After(10 * time.Second):
		writeCtlResponse(conn, ctlResponse{Error: "viewer did not answer"})
	}
}

// publishCtlState republishes the snapshot that answers status. Main goroutine
// only; Run calls it on every pass of its loop.
func (d *DocumentViewer) publishCtlState() {
	state := d.ctlStateOf()
	d.ctlSnapshot.Store(&state)
}

func writeCtlResponse(conn net.Conn, resp ctlResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	conn.Write(append(data, '\n'))
}

// tintName maps the internal darkMode values onto the names the CLI uses.
func tintName(darkMode string) string {
	switch darkMode {
	case "":
		return "off"
	case "smart":
		return "dark"
	default:
		return darkMode
	}
}

// parseTint is the inverse, accepting the internal spellings and a few aliases
// so "pale"/"gray" reach the dim tint and "white"/"none" turn tinting off.
func parseTint(v string) (string, bool) {
	switch strings.ToLower(v) {
	case "off", "none", "white", "normal":
		return "", true
	case "dim", "pale", "gray", "grey":
		return "dim", true
	case "dark", "smart":
		return "smart", true
	case "invert", "inverse", "inv":
		return "invert", true
	}
	return "", false
}

func dualName(mode string) string {
	if mode == "" {
		return "off"
	}
	return mode
}

func parseDual(v string) (string, bool) {
	switch strings.ToLower(v) {
	case "off", "none", "single", "1":
		return "", true
	case "vertical", "stacked", "v":
		return "vertical", true
	case "horizontal", "side", "h", "2":
		return "horizontal", true
	case "half", "halfpage", "half-page":
		return "half", true
	}
	return "", false
}

func viewName(forceMode string) string {
	if forceMode == "" {
		return "auto"
	}
	return forceMode
}

// ctlStateOf snapshots the viewer. Main goroutine only.
func (d *DocumentViewer) ctlStateOf() ctlState {
	absPath, _ := filepath.Abs(d.path)
	half := "top"
	if d.halfPageOffset == 1 {
		half = "bottom"
	}
	return ctlState{
		PID:         os.Getpid(),
		Path:        absPath,
		Name:        filepath.Base(absPath),
		FileType:    d.fileType,
		Page:        d.currentPage + 1,
		Pages:       len(d.textPages),
		Fit:         d.fitMode,
		Tint:        tintName(d.darkMode),
		View:        viewName(d.forceMode),
		Dual:        dualName(d.dualPageMode),
		HalfOffset:  half,
		Zoom:        d.zoom(),
		HTMLWidth:   d.htmlPageWidth,
		CropTop:     d.cropTop,
		CropBottom:  d.cropBottom,
		CropLeft:    d.cropLeft,
		CropRight:   d.cropRight,
		Search:      d.searchQuery,
		SearchHits:  len(d.searchHits),
		SearchIndex: d.searchHitIdx + 1,
		Reflowable:  d.isReflowable,
		IsImage:     d.isImage,
		AgtermID:    os.Getenv("AGTERM_SESSION_ID"),
		Socket:      d.ctlSocket,
	}
}

// applyNumber resolves an absolute or relative ("+0.02", "-3") argument
// against cur and clamps the result.
func applyNumber(cur float64, arg string, lo, hi float64) (float64, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return cur, fmt.Errorf("empty value")
	}
	relative := arg[0] == '+' || arg[0] == '-'
	v, err := strconv.ParseFloat(arg, 64)
	if err != nil {
		return cur, fmt.Errorf("not a number: %q", arg)
	}
	if relative {
		v += cur
	}
	return min(max(v, lo), hi), nil
}

// ctlSetOrder fixes the order settings are applied in, so one call carrying
// several keys behaves the same every time. dual comes before zoom because
// zoom is stored per view.
var ctlSetOrder = []string{
	"view", "fit", "tint", "dual", "half",
	"crop", "crop_top", "crop_bottom", "crop_left", "crop_right",
	"html_width", "zoom", "page",
}

// applyCtl runs one request on the main goroutine and reports what Run must do
// next.
func (d *DocumentViewer) applyCtl(req ctlRequest) (ctlResponse, ctlAction) {
	state := func() *ctlState { s := d.ctlStateOf(); return &s }
	fail := func(format string, a ...any) (ctlResponse, ctlAction) {
		return ctlResponse{Error: fmt.Sprintf(format, a...), State: state()}, ctlAction{}
	}

	switch req.Cmd {
	case "status", "":
		return ctlResponse{OK: true, State: state()}, ctlAction{}

	case "toc":
		entries := make([]ctlTOC, 0, len(d.toc))
		for _, e := range d.toc {
			entries = append(entries, ctlTOC{Level: e.level, Title: e.title, Page: e.page0 + 1})
		}
		return ctlResponse{OK: true, State: state(), TOC: entries}, ctlAction{}

	case "set":
		for key := range req.Args {
			if !ctlKnownSetting(key) {
				return fail("unknown setting %q", key)
			}
		}
		// Resolve every value before writing any of them, so one bad value
		// cannot leave the viewer half-changed.
		var writes []func()
		for _, key := range ctlSetOrder {
			v, ok := req.Args[key]
			if !ok {
				continue
			}
			write, err := d.resolveCtlSetting(key, v)
			if err != nil {
				return fail("%s: %v", key, err)
			}
			writes = append(writes, write)
		}
		for _, write := range writes {
			write()
		}
		if len(writes) > 0 {
			d.saveConfig()
		}
		return ctlResponse{OK: true, State: state()}, ctlAction{redraw: len(writes) > 0}

	case "cycle":
		what := strings.ToLower(req.Args["what"])
		switch what {
		case "tint":
			d.handleInput('D')
		case "fit":
			d.handleInput('f')
		case "view":
			d.toggleViewMode()
		case "dual":
			d.handleInput('2')
		default:
			return fail("cycle what? (tint|fit|view|dual)")
		}
		d.saveConfig()
		return ctlResponse{OK: true, State: state()}, ctlAction{redraw: true}

	case "page":
		write, err := d.resolveCtlSetting("page", req.Args["to"])
		if err != nil {
			return fail("page: %v", err)
		}
		write()
		return ctlResponse{OK: true, State: state()}, ctlAction{redraw: true}

	case "search":
		switch strings.ToLower(req.Args["action"]) {
		case "next":
			d.nextSearchHit()
		case "prev":
			d.prevSearchHit()
		case "clear":
			d.searchQuery, d.searchHits, d.searchHitIdx = "", nil, 0
		default:
			d.runSearch(req.Args["query"])
		}
		return ctlResponse{OK: true, State: state()}, ctlAction{redraw: true}

	case "sync":
		page, err := strconv.Atoi(req.Args["page"])
		if err != nil || page < 1 {
			return fail("sync: page must be a positive integer")
		}
		t := syncTarget{page: page}
		if xs, ok := req.Args["x"]; ok {
			x, errX := strconv.ParseFloat(xs, 64)
			y, errY := strconv.ParseFloat(req.Args["y"], 64)
			if errX != nil || errY != nil {
				return fail("sync: x and y must be numbers")
			}
			t.x, t.y, t.hasPoint = x, y, true
		}
		select {
		case d.syncChan <- t:
		default:
			return fail("sync: a jump is already queued")
		}
		return ctlResponse{OK: true, State: state()}, ctlAction{}

	case "key":
		keys := req.Args["keys"]
		if keys == "" {
			return fail("key: nothing to send")
		}
		// The keys that open one of the viewer's own prompts read from the
		// keyboard afterwards, which a socket request cannot answer. Refuse
		// them up front rather than accept the key and do nothing.
		for i := 0; i < len(keys); i++ {
			if c := keys[i]; strings.IndexByte(ctlInteractiveKeys, c) >= 0 {
				return fail("key %q opens a prompt only the keyboard can answer; "+
					"use the search, page or toc command instead", string(c))
			}
		}
		for i := 0; i < len(keys); i++ {
			if d.handleInput(keys[i]) == 1 {
				return ctlResponse{OK: true, State: state()}, ctlAction{quit: true, back: d.wantBack}
			}
		}
		d.saveConfig()
		return ctlResponse{OK: true, State: state()}, ctlAction{redraw: true}

	case "reload":
		d.lastModTime = time.Time{}
		d.checkAndReload()
		return ctlResponse{OK: true, State: state()}, ctlAction{redraw: true}

	case "refresh":
		d.refreshCellSize()
		return ctlResponse{OK: true, State: state()}, ctlAction{redraw: true}

	case "quit":
		return ctlResponse{OK: true, State: state()}, ctlAction{quit: true}

	case "back":
		d.wantBack = true
		return ctlResponse{OK: true, State: state()}, ctlAction{quit: true, back: true}
	}
	return fail("unknown command %q", req.Cmd)
}

// ctlInteractiveKeys are the viewer keys whose handler opens a prompt or picker
// that then reads the keyboard: search, go-to-page, help, debug and the table of
// contents (see handleInput's negative return codes).
const ctlInteractiveKeys = "/gh?dT"

func ctlKnownSetting(key string) bool {
	for _, k := range ctlSetOrder {
		if k == key {
			return true
		}
	}
	return false
}

// resolveCtlSetting validates one setting and returns the write that applies
// it. Nothing is mutated until the returned function runs, which is what lets
// a multi-setting request be rejected whole (see applyCtl's "set"). Values are
// absolute, except the numeric ones, which also take a signed delta.
func (d *DocumentViewer) resolveCtlSetting(key, v string) (func(), error) {
	switch key {
	case "view":
		var mode string
		switch strings.ToLower(v) {
		case "auto", "":
			mode = ""
		case "text", "image":
			mode = strings.ToLower(v)
		default:
			return nil, fmt.Errorf("want auto|text|image, got %q", v)
		}
		return func() { d.forceMode = mode }, nil

	case "fit":
		switch strings.ToLower(v) {
		case "height", "width", "auto":
			mode := strings.ToLower(v)
			return func() { d.fitMode = mode }, nil
		}
		return nil, fmt.Errorf("want height|width|auto, got %q", v)

	case "tint":
		mode, ok := parseTint(v)
		if !ok {
			return nil, fmt.Errorf("want off|dim|dark|invert, got %q", v)
		}
		return func() { d.darkMode = mode }, nil

	case "dual":
		mode, ok := parseDual(v)
		if !ok {
			return nil, fmt.Errorf("want off|vertical|horizontal|half, got %q", v)
		}
		return func() {
			if mode == "half" && d.dualPageMode != "half" {
				d.halfPageOffset = 0
			}
			d.dualPageMode = mode
		}, nil

	case "half":
		switch strings.ToLower(v) {
		case "top", "0":
			return func() { d.halfPageOffset = 0 }, nil
		case "bottom", "1":
			return func() { d.halfPageOffset = 1 }, nil
		}
		return nil, fmt.Errorf("want top|bottom, got %q", v)

	case "crop":
		var writes []func()
		for _, edge := range []string{"crop_top", "crop_bottom", "crop_left", "crop_right"} {
			write, err := d.resolveCtlSetting(edge, v)
			if err != nil {
				return nil, err
			}
			writes = append(writes, write)
		}
		return func() {
			for _, write := range writes {
				write()
			}
		}, nil

	case "crop_top", "crop_bottom", "crop_left", "crop_right":
		edges := map[string]*float64{
			"crop_top": &d.cropTop, "crop_bottom": &d.cropBottom,
			"crop_left": &d.cropLeft, "crop_right": &d.cropRight,
		}
		edge := edges[key]
		n, err := applyNumber(*edge, v, 0, 0.45)
		if err != nil {
			return nil, err
		}
		return func() { *edge = n }, nil

	case "html_width":
		if !d.isReflowable {
			return nil, fmt.Errorf("only reflowable documents have a page width")
		}
		n, err := applyNumber(float64(d.htmlPageWidth), v, 200, 3000)
		if err != nil {
			return nil, err
		}
		return func() { d.adjustHTMLZoom(int(n) - d.htmlPageWidth) }, nil

	case "zoom":
		n, err := applyNumber(d.zoom(), v, minZoom, maxZoom)
		if err != nil {
			return nil, err
		}
		return func() { d.setZoom(n) }, nil

	case "page":
		return d.resolveCtlPage(v)
	}
	return nil, fmt.Errorf("unknown setting")
}

// resolveCtlPage reads a position: an absolute page, a signed delta, or an end
// of the document. Absolute pages count the pages the viewer DISPLAYS, matching
// what status reports and what the viewer's own "g" prompt accepts — not the
// document's own numbering, which the sync command uses because synctex speaks
// it.
func (d *DocumentViewer) resolveCtlPage(v string) (func(), error) {
	v = strings.TrimSpace(strings.ToLower(v))
	switch v {
	case "first", "start":
		return func() { d.pushHistory(); d.currentPage = 0 }, nil
	case "last", "end":
		return func() {
			d.pushHistory()
			if len(d.textPages) > 0 {
				d.currentPage = len(d.textPages) - 1
			}
		}, nil
	case "next":
		return func() { d.stepPages(d.pageStep()) }, nil
	case "prev", "previous":
		return func() { d.stepPages(-d.pageStep()) }, nil
	case "":
		return nil, fmt.Errorf("no page given")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, fmt.Errorf("not a page: %q", v)
	}
	if v[0] == '+' || v[0] == '-' {
		return func() { d.stepPages(n) }, nil
	}
	if n < 1 || n > len(d.textPages) {
		return nil, fmt.Errorf("page must be between 1 and %d", len(d.textPages))
	}
	return func() {
		d.pushHistory()
		d.currentPage = n - 1
	}, nil
}

// runSearch is startSearch's non-interactive half: it scans the document for
// query and jumps to the first hit. An empty query clears the search.
func (d *DocumentViewer) runSearch(query string) {
	query = strings.TrimSpace(query)
	if query == "" || d.isImage || d.doc == nil {
		d.searchQuery, d.searchHits, d.searchHitIdx = "", nil, 0
		return
	}
	d.searchQuery = strings.ToLower(query)
	d.searchHits = nil
	d.searchHitIdx = 0
	for _, pageNum := range d.textPages {
		text, err := d.doc.Text(pageNum)
		if err == nil && strings.Contains(strings.ToLower(text), d.searchQuery) {
			d.searchHits = append(d.searchHits, pageNum)
		}
	}
	if len(d.searchHits) > 0 {
		d.jumpToHit()
	}
}
