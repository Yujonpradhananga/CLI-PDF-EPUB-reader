package viewer

import "testing"

// One page of MuPDF's HTML rendering: a <p> per structured-text line, carrying
// the line box that doc.Text does not.
const samplePageHTML = `<div id="page3" style="width:411.0pt;height:609.4pt">
<p style="top:90.4pt;left:66.1pt;line-height:10.9pt"><span style="font-family:CharterITC,serif;font-size:10.9pt">The Gauss </span><span style="font-family:CharterITC,serif;font-size:10.9pt">&#x2014;</span><span style="font-family:CharterITC,serif;font-size:10.9pt"> Bonnet theorem</span></p>
<p style="top:104.0pt;left:51.1pt;line-height:10.9pt"><span style="font-family:CharterITC,serif;font-size:10.9pt">&#x413;&#x430;&#x443;&#x441;&#x441;</span></p>
<p style="top:117.5pt;left:51.1pt;line-height:10.9pt"><span style="font-family:CharterITC,serif;font-size:10.9pt">  </span></p>
</div>
`

func TestParseSearchLines(t *testing.T) {
	lines := parseSearchLines(samplePageHTML)

	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (the blank one dropped): %+v", len(lines), lines)
	}
	// The marker aims at the middle of the line box, not its top edge.
	if want := 90.4 + 10.9/2; !approxEq(lines[0].y, want) {
		t.Errorf("first line y = %v, want %v", lines[0].y, want)
	}
	// Spans concatenate, entities decode, and the text is lowercased to match
	// the (lowercased) search query.
	if want := "the gauss — bonnet theorem"; lines[0].text != want {
		t.Errorf("first line text = %q, want %q", lines[0].text, want)
	}
	if want := "гаусс"; lines[1].text != want {
		t.Errorf("second line text = %q, want %q", lines[1].text, want)
	}
}

func TestMarkerCellSinglePage(t *testing.T) {
	m := singleMap(0, 1, 0, 1)

	// Halfway down the page: halfway down the image's 48 rows, marked in the
	// column just right of the image.
	col, row, ok := m.markerCell(4, 396, 120)
	if !ok {
		t.Fatal("mid-page marker not placed")
	}
	if want := 11 + 80; col != want {
		t.Errorf("col = %d, want %d", col, want)
	}
	if want := 2 + 24; row != want {
		t.Errorf("row = %d, want %d", row, want)
	}

	if _, _, ok := m.markerCell(9, 396, 120); ok {
		t.Error("a page that is not on screen got a marker")
	}
}

// A match cropped away — or in the half a half-page view is not showing — has
// no row, so it draws nothing rather than landing on the wrong line.
func TestMarkerCellHiddenBand(t *testing.T) {
	m := singleMap(0, 1, 0.5, 1) // bottom half of the page on screen

	if _, _, ok := m.markerCell(4, 100, 120); ok {
		t.Error("a point above the displayed band got a marker")
	}
	col, row, ok := m.markerCell(4, 594, 120) // three quarters down the page
	if !ok {
		t.Fatal("a point inside the displayed band got no marker")
	}
	if want := 2 + 24; row != want {
		t.Errorf("row = %d, want %d (middle of the displayed half)", row, want)
	}
	if want := 11 + 80; col != want {
		t.Errorf("col = %d, want %d", col, want)
	}
}

// The image covers the cells it occupies, so the left page of a side-by-side
// spread is marked in the left margin — the gap between the pages is under the
// image.
func TestMarkerCellSideBySide(t *testing.T) {
	m := clickMap{
		originCol: 5, originRow: 1,
		cols: 110, rows: 40,
		pxW: 1600, pxH: 1000,
		targets: []clickTarget{
			{page0: 6, x1: 795, y1: 1000, pageW: 612, pageH: 792, fx1: 1, fy1: 1},
			{page0: 7, x0: 805, x1: 1600, y1: 1000, pageW: 612, pageH: 792, fx1: 1, fy1: 1},
		},
	}

	col, _, ok := m.markerCell(6, 396, 200)
	if !ok || col != 4 {
		t.Errorf("left page col = %d (ok=%v), want 4", col, ok)
	}
	col, _, ok = m.markerCell(7, 396, 200)
	if !ok || col != 115 {
		t.Errorf("right page col = %d (ok=%v), want 115", col, ok)
	}
}

// Stacked pages both span the composite, so both are marked on the right; only
// the rows differ.
func TestMarkerCellStacked(t *testing.T) {
	m := clickMap{
		originCol: 3, originRow: 1,
		cols: 90, rows: 40,
		pxW: 1600, pxH: 2000,
		targets: []clickTarget{
			{page0: 6, x1: 1600, y1: 1000, pageW: 612, pageH: 792, fx1: 1, fy1: 1},
			{page0: 7, x0: 0, y0: 1000, x1: 1600, y1: 2000, pageW: 612, pageH: 792, fx1: 1, fy1: 1},
		},
	}

	col, row, ok := m.markerCell(6, 396, 200)
	if !ok || col != 93 || row != 11 {
		t.Errorf("top page marker = (%d,%d) ok=%v, want (93,11)", col, row, ok)
	}
	col, row, ok = m.markerCell(7, 396, 200)
	if !ok || col != 93 || row != 31 {
		t.Errorf("bottom page marker = (%d,%d) ok=%v, want (93,31)", col, row, ok)
	}
}

// An image wide enough to leave no margin still places the marker inside the
// terminal.
func TestMarkerCellClampsToTerminal(t *testing.T) {
	m := singleMap(0, 1, 0, 1)

	col, _, ok := m.markerCell(4, 396, 80)
	if !ok || col != 80 {
		t.Errorf("col = %d (ok=%v), want 80", col, ok)
	}
}
