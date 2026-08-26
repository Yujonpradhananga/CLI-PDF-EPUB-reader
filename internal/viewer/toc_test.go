package viewer

import "testing"

func tocFixture() []tocEntry {
	return []tocEntry{
		{level: 0, title: "Introduction", page0: 0},
		{level: 1, title: "Motivation", page0: 1},
		{level: 0, title: "Main results", page0: 4},
		{level: 1, title: "The main theorem", page0: 5, x: 72, y: 300},
		{level: 0, title: "Conclusion", page0: 9},
	}
}

func TestTOCFilterEmptyQueryKeepsOrder(t *testing.T) {
	entries := tocFixture()
	results := tocFilter(entries, "")
	if len(results) != len(entries) {
		t.Fatalf("len = %d, want %d", len(results), len(entries))
	}
	for i, r := range results {
		if r.idx != i || r.matched != nil {
			t.Errorf("results[%d] = %+v, want idx %d with no matches", i, r, i)
		}
	}
}

func TestTOCFilterFuzzy(t *testing.T) {
	results := tocFilter(tocFixture(), "concl")
	if len(results) != 1 || results[0].idx != 4 {
		t.Fatalf("results = %+v, want just Conclusion (idx 4)", results)
	}
	if len(results[0].matched) == 0 {
		t.Errorf("no matched positions for highlighting")
	}
	if got := tocFilter(tocFixture(), "zzz"); len(got) != 0 {
		t.Errorf("bogus query matched %+v", got)
	}
}

func TestTOCCurrentIndex(t *testing.T) {
	entries := tocFixture()
	cases := []struct{ page0, want int }{
		{0, 0}, {3, 1}, {5, 3}, {7, 3}, {100, 4},
	}
	for _, tc := range cases {
		if got := tocCurrentIndex(entries, tc.page0); got != tc.want {
			t.Errorf("tocCurrentIndex(%d) = %d, want %d", tc.page0, got, tc.want)
		}
	}
}

func TestTOCHotkeySignals(t *testing.T) {
	d := &DocumentViewer{}
	if got := d.handleInput('T'); got != -6 {
		t.Errorf("handleInput('T') = %d, want -6", got)
	}
}

// The picker opens on the current section, arrows move the selection, and
// Enter queues the entry on syncChan so the jump shares the link-follow path
// (history push, flash rule, half-page roll).
func TestTOCEnterQueuesJump(t *testing.T) {
	d := &DocumentViewer{
		path:      "x.pdf",
		textPages: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		toc:       tocFixture(),
		syncChan:  make(chan syncTarget, 1),
	}
	d.currentPage = 4 // inside "Main results"
	in := make(chan byte, 4)
	in <- keyArrowNext // to "The main theorem"
	in <- 13           // Enter
	if !d.showTOC(in) {
		t.Fatal("showTOC = false, want a queued jump")
	}
	got := <-d.syncChan
	want := syncTarget{page: 6, x: 72, y: 300, hasPoint: true}
	if got != want {
		t.Fatalf("syncTarget = %+v, want %+v", got, want)
	}
}

func TestTOCEscapeCloses(t *testing.T) {
	d := &DocumentViewer{
		path:     "x.pdf",
		toc:      tocFixture(),
		syncChan: make(chan syncTarget, 1),
	}
	in := make(chan byte, 4)
	in <- 'q' // goes to the query, must not quit or jump
	in <- 27  // Esc
	if d.showTOC(in) {
		t.Fatal("showTOC = true after Esc, want false")
	}
	select {
	case tgt := <-d.syncChan:
		t.Fatalf("unexpected jump queued: %+v", tgt)
	default:
	}
}

func TestHighlightTitleTruncates(t *testing.T) {
	s, n := highlightTitle("Introduction", nil, 6, "\033[0m")
	if s != "Intro…" || n != 6 {
		t.Errorf("truncated = %q (%d runes), want \"Intro…\" (6)", s, n)
	}
	s, n = highlightTitle("Intro", nil, 10, "\033[0m")
	if s != "Intro" || n != 5 {
		t.Errorf("untouched = %q (%d runes), want \"Intro\" (5)", s, n)
	}
}
