package main

import (
	"os"
	"testing"
)

// feedKey writes raw input bytes to a pipe standing in for the terminal and
// returns the key readSingleChar decodes from them.
func feedKey(t *testing.T, in string) byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	if _, err := w.WriteString(in); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.Close()

	saved := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = saved }()

	d := &DocumentViewer{}
	return d.readSingleChar()
}

func TestPagingStepsBySpread(t *testing.T) {
	cases := []struct {
		name string
		mode string
		keys []byte
		want int // currentPage after the keys, starting from page 0
	}{
		{"single mode pages one at a time", "", []byte{'j', 'j'}, 2},
		{"spread pages two at a time", "horizontal", []byte{'j', 'j'}, 4},
		{"spread also stacked vertically", "vertical", []byte{'j'}, 2},
		{"shift offsets a spread by one", "horizontal", []byte{'j', 'J'}, 3},
		{"shift back one from a spread", "horizontal", []byte{'j', 'j', 'K'}, 3},
		{"spread back steps two", "horizontal", []byte{'j', 'j', 'k'}, 2},
		{"shift is a single page in single mode", "", []byte{'J', 'J'}, 2},
		{"space follows the spread step", "horizontal", []byte{' '}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &DocumentViewer{dualPageMode: tc.mode, textPages: []int{0, 1, 2, 3, 4, 5, 6, 7}}
			for _, k := range tc.keys {
				d.handleInput(k)
			}
			if d.currentPage != tc.want {
				t.Errorf("currentPage = %d, want %d", d.currentPage, tc.want)
			}
		})
	}
}

func TestPagingClampsAtEnds(t *testing.T) {
	// An odd page count leaves a lone last page: a two-page step from page 3
	// must still land on it rather than stall on the last full spread.
	d := &DocumentViewer{dualPageMode: "horizontal", textPages: []int{0, 1, 2, 3, 4}}
	d.currentPage = 3
	d.handleInput('j')
	if d.currentPage != 4 {
		t.Errorf("forward from 3 of 0..4: currentPage = %d, want 4", d.currentPage)
	}
	d.handleInput('j')
	if d.currentPage != 4 {
		t.Errorf("forward past the end: currentPage = %d, want 4", d.currentPage)
	}
	d.currentPage = 1
	d.handleInput('k')
	if d.currentPage != 0 {
		t.Errorf("back from 1: currentPage = %d, want 0", d.currentPage)
	}
	d.handleInput('k')
	if d.currentPage != 0 {
		t.Errorf("back past the start: currentPage = %d, want 0", d.currentPage)
	}
}

func TestHalfPageModeStillHalfSteps(t *testing.T) {
	d := &DocumentViewer{dualPageMode: "half", textPages: []int{0, 1, 2}}
	d.handleInput('j')
	if d.currentPage != 0 || d.halfPageOffset != 1 {
		t.Fatalf("first j: page %d offset %d, want page 0 offset 1", d.currentPage, d.halfPageOffset)
	}
	d.handleInput('j')
	if d.currentPage != 1 || d.halfPageOffset != 0 {
		t.Fatalf("second j: page %d offset %d, want page 1 offset 0", d.currentPage, d.halfPageOffset)
	}
	d.handleInput('k')
	if d.currentPage != 0 || d.halfPageOffset != 1 {
		t.Fatalf("k: page %d offset %d, want page 0 offset 1", d.currentPage, d.halfPageOffset)
	}
}

func TestSearchJumpsPushHistory(t *testing.T) {
	d := &DocumentViewer{textPages: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}}
	d.searchHits = []int{2, 7}
	d.jumpToHit() // fresh search from page 0 lands on the first hit
	if d.currentPage != 2 {
		t.Fatalf("first hit: currentPage = %d, want 2", d.currentPage)
	}
	d.nextSearchHit() // page 7
	d.nextSearchHit() // wraps to page 2
	if d.currentPage != 2 {
		t.Fatalf("after wrap: currentPage = %d, want 2", d.currentPage)
	}
	for _, want := range []int{7, 2, 0} {
		d.historyBack()
		if d.currentPage != want {
			t.Fatalf("historyBack: currentPage = %d, want %d", d.currentPage, want)
		}
	}
	d.historyForward()
	if d.currentPage != 2 {
		t.Fatalf("historyForward: currentPage = %d, want 2", d.currentPage)
	}
}

func TestSearchHitOnSamePagePushesNothing(t *testing.T) {
	d := &DocumentViewer{textPages: []int{0, 1, 2}}
	d.searchHits = []int{1}
	d.currentPage = 1
	d.nextSearchHit()
	if len(d.backStack) != 0 {
		t.Fatalf("backStack = %v, want empty", d.backStack)
	}
}

func TestGotoPushesHistory(t *testing.T) {
	d := &DocumentViewer{textPages: []int{0, 1, 2, 3, 4}}
	in := make(chan byte, 2)
	in <- '4'
	in <- 13 // Enter
	d.goToPage(in)
	if d.currentPage != 3 {
		t.Fatalf("goToPage: currentPage = %d, want 3", d.currentPage)
	}
	d.historyBack()
	if d.currentPage != 0 {
		t.Fatalf("historyBack after goto: currentPage = %d, want 0", d.currentPage)
	}
}

func TestHistoryKeys(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want byte
	}{
		{"ctrl+i as tab byte", "\x09", keyHistoryBack},
		{"ctrl+o control byte", "\x0f", keyHistoryForward},
		{"ctrl+i disambiguated CSI u", "\x1b[105;5u", keyHistoryBack},
		{"ctrl+o disambiguated CSI u", "\x1b[111;5u", keyHistoryForward},
		{"cmd+left", "\x1b[1;9D", keyHistoryBack},
		{"cmd+right", "\x1b[1;9C", keyHistoryForward},
		// Unmodified keys must keep their meanings: 'i' toggles dark mode and a
		// bare left arrow pages back.
		{"plain i", "i", 'i'},
		{"plain o", "o", 'o'},
		{"left arrow", "\x1b[D", 'k'},
		{"shift+left arrow", "\x1b[1;2D", 'K'},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := feedKey(t, tc.in); got != tc.want {
				t.Errorf("readSingleChar(%q) = %#x, want %#x", tc.in, got, tc.want)
			}
		})
	}
}
