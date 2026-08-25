package viewer

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gen2brain/go-fitz"
)

func TestFindLinkAt(t *testing.T) {
	links := []pageLink{
		{uri: "#a", x0: 100, y0: 300, x1: 200, y1: 312, destPage0: 5},
		{uri: "#b", x0: 100, y0: 320, x1: 200, y1: 332, destPage0: 9},
		{uri: "https://x", x0: 400, y0: 300, x1: 500, y1: 312, external: true, destPage0: -1},
	}

	// Dead center of the first hot zone.
	if l, ok := findLinkAt(links, 150, 306, linkTolX, linkTolY); !ok || l.uri != "#a" {
		t.Errorf("center hit: got %+v ok=%v, want #a", l, ok)
	}

	// A cell center 6pt below the first rect's bottom edge and 6pt above the
	// second rect's top: nearest wins — that's #b (2pt outside vs 6pt... both
	// within tolY; distances: to #a bottom 318-312=6, to #b top 320-318=2).
	if l, ok := findLinkAt(links, 150, 318, linkTolX, linkTolY); !ok || l.uri != "#b" {
		t.Errorf("between rects: got %+v ok=%v, want #b (nearer)", l, ok)
	}

	// Just outside vertical tolerance (8pt) of everything.
	if _, ok := findLinkAt(links, 150, 341, linkTolX, linkTolY); ok {
		t.Error("9pt below #b must miss")
	}

	// Outside horizontal tolerance (4pt).
	if _, ok := findLinkAt(links, 205, 306, linkTolX, linkTolY); ok {
		t.Error("5pt right of #a must miss")
	}
	if l, ok := findLinkAt(links, 203, 306, linkTolX, linkTolY); !ok || l.uri != "#a" {
		t.Errorf("3pt right of #a should hit it, got %+v ok=%v", l, ok)
	}

	// External link hit.
	if l, ok := findLinkAt(links, 450, 310, linkTolX, linkTolY); !ok || !l.external {
		t.Errorf("external hit: got %+v ok=%v", l, ok)
	}

	// Dual-mode case: a cell covering ~40pt of page whose center is 15pt from
	// the link — dead with the 8pt floor, hittable with the widened tolerance.
	if _, ok := findLinkAt(links, 150, 347, linkTolX, linkTolY); ok {
		t.Error("15pt below #b must miss at floor tolerance")
	}
	if l, ok := findLinkAt(links, 150, 347, linkTolX, 20); !ok || l.uri != "#b" {
		t.Errorf("15pt below #b with 20pt tolerance: got %+v ok=%v, want #b", l, ok)
	}

	if _, ok := findLinkAt(nil, 150, 306, linkTolX, linkTolY); ok {
		t.Error("no links must not match")
	}
}

func TestCellSizePDF(t *testing.T) {
	// singleMap: 80x48 cells showing 1600x2071px of a full 612x792pt page.
	m := singleMap(0, 1, 0, 1)
	w, h, ok := m.cellSizePDF(4)
	if !ok || !approxEq(w, 612.0/80) || !approxEq(h, 792.0/48) {
		t.Errorf("full page: got %v x %v ok=%v, want %v x %v", w, h, ok, 612.0/80, 792.0/48)
	}

	// Half-page band [0.45,1.0]: a cell covers proportionally less page.
	m = singleMap(0, 1, 0.45, 1.0)
	_, h, ok = m.cellSizePDF(4)
	if !ok || !approxEq(h, 2071.0/48/2071*0.55*792) {
		t.Errorf("half band: got h=%v ok=%v", h, ok)
	}

	if _, _, ok := m.cellSizePDF(9); ok {
		t.Error("page not displayed must not report a cell size")
	}
	var empty clickMap
	if _, _, ok := empty.cellSizePDF(0); ok {
		t.Error("empty map must not report a cell size")
	}
}

func TestAllowedExternalScheme(t *testing.T) {
	for _, uri := range []string{"https://lpetrov.cc/x", "http://a.b/c?d=1", "mailto:x@y.z", "HTTPS://UPPER.CASE"} {
		if !allowedExternalScheme(uri) {
			t.Errorf("%q must be allowed", uri)
		}
	}
	for _, uri := range []string{"smb://attacker/share", "file:///etc/passwd", "x-custom-app://payload", "javascript:alert(1)", "://bad"} {
		if allowedExternalScheme(uri) {
			t.Errorf("%q must be blocked", uri)
		}
	}
}

// historyViewer builds a viewer with pages 0..9 all in textPages, currently
// showing page 0.
func historyViewer() *DocumentViewer {
	d := &DocumentViewer{}
	for i := 0; i < 10; i++ {
		d.textPages = append(d.textPages, i)
	}
	return d
}

// jumpVia simulates the syncChan case's navigation: record history, jump.
func jumpVia(d *DocumentViewer, page0 int) {
	d.pushHistory()
	d.jumpToPage(page0 + 1)
}

func TestHistoryBackForward(t *testing.T) {
	d := historyViewer()

	jumpVia(d, 5) // 0 -> 5
	jumpVia(d, 8) // 5 -> 8
	if d.currentPage != 8 {
		t.Fatalf("after jumps: page %d, want 8", d.currentPage)
	}

	d.historyBack()
	if d.currentPage != 5 {
		t.Fatalf("back: page %d, want 5", d.currentPage)
	}
	d.historyBack()
	if d.currentPage != 0 {
		t.Fatalf("back x2: page %d, want 0", d.currentPage)
	}
	d.historyBack() // empty stack: no-op
	if d.currentPage != 0 {
		t.Fatalf("back on empty: page %d, want 0", d.currentPage)
	}

	d.historyForward()
	if d.currentPage != 5 {
		t.Fatalf("forward: page %d, want 5", d.currentPage)
	}
	d.historyForward()
	if d.currentPage != 8 {
		t.Fatalf("forward x2: page %d, want 8", d.currentPage)
	}
	d.historyForward() // nothing ahead: no-op
	if d.currentPage != 8 {
		t.Fatalf("forward at tip: page %d, want 8", d.currentPage)
	}
}

func TestHistoryNewJumpClearsForward(t *testing.T) {
	d := historyViewer()
	jumpVia(d, 5)
	jumpVia(d, 8)
	d.historyBack() // at 5, forward holds 8
	jumpVia(d, 2)   // browser semantics: forward stack dies
	if d.currentPage != 2 {
		t.Fatalf("page %d, want 2", d.currentPage)
	}
	d.historyForward()
	if d.currentPage != 2 {
		t.Fatalf("forward after new jump must no-op, page %d", d.currentPage)
	}
	d.historyBack()
	if d.currentPage != 5 {
		t.Fatalf("back: page %d, want 5", d.currentPage)
	}
}

func TestHistorySkipsStaleAndDedups(t *testing.T) {
	d := historyViewer()
	jumpVia(d, 0) // jump to the page already shown: stale entry
	jumpVia(d, 0) // consecutive duplicate: dropped
	if len(d.backStack) != 1 {
		t.Fatalf("backStack len %d, want 1 (dedup)", len(d.backStack))
	}
	d.historyBack() // only entry equals current: skipped, no move
	if d.currentPage != 0 || len(d.fwdStack) != 0 {
		t.Fatalf("stale back: page %d fwd %d, want 0 and 0", d.currentPage, len(d.fwdStack))
	}

	jumpVia(d, 7)
	d.halfPageOffset = 1
	jumpVia(d, 3)
	if d.halfPageOffset != 1 {
		t.Fatal("jump must not touch halfOffset by itself")
	}
	d.historyBack()
	if d.currentPage != 7 || d.halfPageOffset != 1 {
		t.Fatalf("back: page %d half %d, want 7 and 1", d.currentPage, d.halfPageOffset)
	}
	d.historyBack()
	if d.currentPage != 0 || d.halfPageOffset != 0 {
		t.Fatalf("back x2: page %d half %d, want 0 and 0 (restored)", d.currentPage, d.halfPageOffset)
	}
}

// writeFitLinkPDF crafts a minimal two-page PDF whose only link is a GoTo
// with a /Fit destination — the kind MuPDF resolves to NaN coordinates.
func writeFitLinkPDF(t *testing.T) string {
	t.Helper()
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Annots [4 0 R] >>",
		"<< /Type /Annot /Subtype /Link /Rect [100 700 200 712] /Border [0 0 0] /Dest [5 0 R /Fit] >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	path := filepath.Join(t.TempDir(), "fitlink.pdf")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDocumentLinksFitDest locks the NaN guard: a /Fit destination has no
// anchor point, and must come back as the documented (0,0) — never NaN,
// which would poison chooseHalf and pdfToCell float math downstream.
func TestDocumentLinksFitDest(t *testing.T) {
	doc, err := fitz.New(writeFitLinkPDF(t))
	if err != nil {
		t.Fatalf("open crafted PDF: %v", err)
	}
	defer doc.Close()

	links := documentLinks(doc, 0)
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	l := links[0]
	if l.external || l.destPage0 != 1 {
		t.Fatalf("bad resolution: %+v", l)
	}
	if math.IsNaN(l.destX) || math.IsNaN(l.destY) {
		t.Fatalf("NaN dest leaked: %+v", l)
	}
	if l.destX != 0 || l.destY != 0 {
		t.Fatalf("point-less dest must be (0,0), got (%v,%v)", l.destX, l.destY)
	}
}

// TestDocumentLinksRealPDF exercises the cgo extraction end-to-end against a
// real hyperref PDF when one is available locally; internal links must come
// back with sane hot zones and resolved destinations.
func TestDocumentLinksRealPDF(t *testing.T) {
	const path = "/Users/leo/homepage/rmt25/S25-rmt-lecture-notes.pdf"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no local test PDF: %v", err)
	}
	doc, err := fitz.New(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer doc.Close()

	r, err := doc.Bound(1)
	if err != nil {
		t.Fatalf("bound: %v", err)
	}
	pageW, pageH := float64(r.Dx()), float64(r.Dy())

	var internal, resolved int
	for page0 := 0; page0 < 8 && page0 < doc.NumPage(); page0++ {
		for _, l := range documentLinks(doc, page0) {
			if l.external {
				continue
			}
			internal++
			if l.x1 <= l.x0 || l.y1 <= l.y0 || l.x0 < 0 || l.x1 > pageW+1 || l.y0 < 0 || l.y1 > pageH+1 {
				t.Errorf("page %d: bad hot zone %+v (page %gx%g)", page0, l, pageW, pageH)
			}
			if l.destPage0 >= 0 {
				resolved++
				if l.destPage0 >= doc.NumPage() {
					t.Errorf("page %d: dest page %d out of range", page0, l.destPage0)
				}
			}
		}
	}
	if internal == 0 {
		t.Fatal("expected internal links in the first pages (ToC)")
	}
	if resolved == 0 {
		t.Fatal("no internal link resolved to a page")
	}
	t.Logf("internal=%d resolved=%d", internal, resolved)
}
