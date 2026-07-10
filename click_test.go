package main

import (
	"math"
	"testing"
)

const clickEps = 1e-9

func approxEq(a, b float64) bool { return math.Abs(a-b) < clickEps }

// singleMap mirrors what setPageClickMap builds: one target covering the whole
// image, showing the band [fx0,fx1]x[fy0,fy1] of a 612x792pt page.
func singleMap(fx0, fx1, fy0, fy1 float64) clickMap {
	return clickMap{
		originCol: 11, originRow: 2,
		cols: 80, rows: 48,
		pxW: 1600, pxH: 2071,
		targets: []clickTarget{{
			page0: 4,
			x1:    1600, y1: 2071,
			pageW: 612, pageH: 792,
			fx0: fx0, fx1: fx1, fy0: fy0, fy1: fy1,
		}},
	}
}

func TestCellToPDFSinglePageNoCrop(t *testing.T) {
	m := singleMap(0, 1, 0, 1)

	// Top-left cell of the image: cell center is half a cell into the box, so
	// x = 0.5/80 of the page width, y = 0.5/48 of the page height. The image
	// pixel size cancels out for a whole-image target.
	page0, x, y, ok := m.cellToPDF(11, 2)
	if !ok || page0 != 4 {
		t.Fatalf("top-left click: got page=%d ok=%v", page0, ok)
	}
	if wantX := 0.5 / 80 * 612; !approxEq(x, wantX) {
		t.Errorf("top-left x = %v, want %v", x, wantX)
	}
	if wantY := 0.5 / 48 * 792; !approxEq(y, wantY) {
		t.Errorf("top-left y = %v, want %v", y, wantY)
	}

	// A cell 40 columns and 24 rows into the box.
	_, x, y, ok = m.cellToPDF(51, 26)
	if !ok {
		t.Fatal("mid click not mapped")
	}
	if wantX := 40.5 / 80 * 612; !approxEq(x, wantX) {
		t.Errorf("mid x = %v, want %v", x, wantX)
	}
	if wantY := 24.5 / 48 * 792; !approxEq(y, wantY) {
		t.Errorf("mid y = %v, want %v", y, wantY)
	}
}

func TestCellToPDFCropped(t *testing.T) {
	// cropLeft=0.1, cropRight=0.05, cropTop=0.2, cropBottom=0 — the image
	// shows the page band [0.1,0.95]x[0.2,1.0].
	m := singleMap(0.1, 0.95, 0.2, 1.0)

	_, x, y, ok := m.cellToPDF(51, 26)
	if !ok {
		t.Fatal("cropped click not mapped")
	}
	// fx = 40.5/80 across the image maps to 0.1 + fx*(0.95-0.1) of the page.
	if wantX := (0.1 + 40.5/80*0.85) * 612; !approxEq(x, wantX) {
		t.Errorf("cropped x = %v, want %v", x, wantX)
	}
	if wantY := (0.2 + 24.5/48*0.8) * 792; !approxEq(y, wantY) {
		t.Errorf("cropped y = %v, want %v", y, wantY)
	}
}

func TestCellToPDFHalfPageBand(t *testing.T) {
	// Bottom-half mode: the image shows the vertical band [0.45, 1.0].
	m := singleMap(0, 1, 0.45, 1.0)

	_, _, y, ok := m.cellToPDF(51, 26)
	if !ok {
		t.Fatal("half-page click not mapped")
	}
	if wantY := (0.45 + 24.5/48*0.55) * 792; !approxEq(y, wantY) {
		t.Errorf("half-page y = %v, want %v", y, wantY)
	}
}

func TestCellToPDFOutsideImage(t *testing.T) {
	m := singleMap(0, 1, 0, 1)
	cases := [][2]int{
		{10, 2},  // left of the image box
		{91, 2},  // right of the image box (origin 11 + 80 cols)
		{11, 1},  // above
		{11, 50}, // below (origin 2 + 48 rows)
	}
	for _, c := range cases {
		if _, _, _, ok := m.cellToPDF(c[0], c[1]); ok {
			t.Errorf("click at (%d,%d) should be outside the image", c[0], c[1])
		}
	}

	var empty clickMap // text page: no map at all
	if _, _, _, ok := empty.cellToPDF(5, 5); ok {
		t.Error("empty click map must not map any cell")
	}
}

func TestCellToPDFDualHorizontal(t *testing.T) {
	// Two 612x792pt pages in a 2000x1000px composite: page 3 at [0,990),
	// page 7 at [1010,2000), 20px gap between them. 200x50 cell box at (1,1).
	m := clickMap{
		originCol: 1, originRow: 1,
		cols: 200, rows: 50,
		pxW: 2000, pxH: 1000,
		targets: []clickTarget{
			{page0: 3, x0: 0, y0: 0, x1: 990, y1: 1000, pageW: 612, pageH: 792, fx0: 0, fx1: 1, fy0: 0, fy1: 1},
			{page0: 7, x0: 1010, y0: 0, x1: 2000, y1: 1000, pageW: 612, pageH: 792, fx0: 0, fx1: 1, fy0: 0, fy1: 1},
		},
	}

	// Cell (151,26): px = 150.5/200*2000 = 1505, py = 25.5/50*1000 = 510 —
	// the exact middle of page 7's rect, 51% down the page.
	page0, x, y, ok := m.cellToPDF(151, 26)
	if !ok || page0 != 7 {
		t.Fatalf("right-page click: got page=%d ok=%v", page0, ok)
	}
	if wantX := 0.5 * 612; !approxEq(x, wantX) {
		t.Errorf("right-page x = %v, want %v", x, wantX)
	}
	if wantY := 0.51 * 792; !approxEq(y, wantY) {
		t.Errorf("right-page y = %v, want %v", y, wantY)
	}

	// Cell (26,26): px = 255 — inside page 3.
	page0, _, _, ok = m.cellToPDF(26, 26)
	if !ok || page0 != 3 {
		t.Fatalf("left-page click: got page=%d ok=%v", page0, ok)
	}

	// Cell (101,26): px = 100.5/200*2000 = 1005 — in the gap between pages.
	if _, _, _, ok := m.cellToPDF(101, 26); ok {
		t.Error("click in the inter-page gap must not map")
	}
}

// roundTrip maps a cell through cellToPDF and back through pdfToCell,
// requiring the result within ±1 cell of the original.
func roundTrip(t *testing.T, m clickMap, col, row int) {
	t.Helper()
	page0, x, y, ok := m.cellToPDF(col, row)
	if !ok {
		t.Fatalf("cellToPDF(%d,%d) not mapped", col, row)
	}
	gotCol, gotRow, ok := m.pdfToCell(page0, x, y)
	if !ok {
		t.Fatalf("pdfToCell(page %d, %.2f, %.2f) not mapped", page0, x, y)
	}
	if abs(gotCol-col) > 1 || abs(gotRow-row) > 1 {
		t.Errorf("round trip (%d,%d) -> (%.2f,%.2f) -> (%d,%d), want within ±1",
			col, row, x, y, gotCol, gotRow)
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func TestPDFToCellRoundTripNoCrop(t *testing.T) {
	m := singleMap(0, 1, 0, 1)
	// Corners and interior of the 80x48 cell box at (11,2).
	for _, c := range [][2]int{{11, 2}, {90, 49}, {51, 26}, {11, 49}, {90, 2}} {
		roundTrip(t, m, c[0], c[1])
	}
}

func TestPDFToCellRoundTripCropped(t *testing.T) {
	// Same crop as TestCellToPDFCropped: the image shows [0.1,0.95]x[0.2,1.0].
	m := singleMap(0.1, 0.95, 0.2, 1.0)
	for _, c := range [][2]int{{11, 2}, {90, 49}, {51, 26}} {
		roundTrip(t, m, c[0], c[1])
	}

	// A point cropped off screen (left of the visible band) must not map.
	if _, _, ok := m.pdfToCell(4, 0.05*612, 0.5*792); ok {
		t.Error("point in the cropped-away band must not map to a cell")
	}
	// A point on another page must not map either.
	if _, _, ok := m.pdfToCell(5, 306, 396); ok {
		t.Error("point on a page not displayed must not map to a cell")
	}
}

func TestPDFToCellDualHorizontal(t *testing.T) {
	m := clickMap{
		originCol: 1, originRow: 1,
		cols: 200, rows: 50,
		pxW: 2000, pxH: 1000,
		targets: []clickTarget{
			{page0: 3, x0: 0, y0: 0, x1: 990, y1: 1000, pageW: 612, pageH: 792, fx0: 0, fx1: 1, fy0: 0, fy1: 1},
			{page0: 7, x0: 1010, y0: 0, x1: 2000, y1: 1000, pageW: 612, pageH: 792, fx0: 0, fx1: 1, fy0: 0, fy1: 1},
		},
	}
	// Cells inside each page's rect round-trip to the right page.
	roundTrip(t, m, 26, 26)  // page 3
	roundTrip(t, m, 151, 26) // page 7

	// Page center of page 7 lands in the right half of the cell box.
	col, _, ok := m.pdfToCell(7, 306, 396)
	if !ok || col <= 101 {
		t.Errorf("page 7 center mapped to col=%d ok=%v, want col > 101", col, ok)
	}

	var empty clickMap // text page: no map at all
	if _, _, ok := empty.pdfToCell(3, 306, 396); ok {
		t.Error("empty click map must not map any point")
	}
}

func TestParseSyncCommand(t *testing.T) {
	if s, ok := parseSyncCommand("7\n"); !ok || s.page != 7 || s.hasPoint {
		t.Errorf("page-only: got %+v ok=%v", s, ok)
	}
	s, ok := parseSyncCommand("12 148.71 396.35\n")
	if !ok || s.page != 12 || !s.hasPoint || !approxEq(s.x, 148.71) || !approxEq(s.y, 396.35) {
		t.Errorf("page+point: got %+v ok=%v", s, ok)
	}
	for _, bad := range []string{"", "abc", "0", "-3", "5 1", "5 1 2 3", "5 x y", "5 1.0 y"} {
		if _, ok := parseSyncCommand(bad); ok {
			t.Errorf("parseSyncCommand(%q) must not parse", bad)
		}
	}
}

func TestParseSGRMouse(t *testing.T) {
	if btn, col, row, ok := parseSGRMouse([]byte("8;42;17")); !ok || btn != 8 || col != 42 || row != 17 {
		t.Errorf("alt+left press: got btn=%d col=%d row=%d ok=%v", btn, col, row, ok)
	}
	if btn, _, _, ok := parseSGRMouse([]byte("0;1;1")); !ok || btn != 0 {
		t.Errorf("plain left press: got btn=%d ok=%v", btn, ok)
	}
	if _, _, _, ok := parseSGRMouse([]byte("8;42")); ok {
		t.Error("two fields must not parse")
	}
	if _, _, _, ok := parseSGRMouse([]byte("8;0;5")); ok {
		t.Error("zero column must not parse")
	}
}
