package viewer

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/mupdf/include

#include <mupdf/fitz.h>

// go-fitz's ToC() reports each entry's page as fz_location.page — the page
// within its chapter — which is wrong for chaptered formats (EPUB), and it
// never surfaces the destination point on the page. Extracting the outline
// directly gets both right: fz_page_number_from_location flattens the
// location to a global page, and x/y carry the anchor for the jump flash.
// Same vendored-header + static-libmupdf arrangement as links.go; MuPDF
// reports errors by longjmp, so anything that can throw sits in fz_try here.

static fz_outline *load_outline_ext(fz_context *ctx, fz_document *doc) {
	fz_outline *outline = NULL;
	fz_try(ctx) {
		outline = fz_load_outline(ctx, doc);
	}
	fz_catch(ctx) {
		return NULL;
	}
	return outline;
}

typedef struct { int page; float x; float y; } toc_dest;

// outline_dest resolves one outline entry to a 0-indexed global page and a
// point on it. Entries whose location fz_load_outline left unresolved fall
// back to resolving the URI. page is -1 on failure.
static toc_dest outline_dest(fz_context *ctx, fz_document *doc, fz_outline *o) {
	toc_dest td;
	td.page = -1; td.x = 0; td.y = 0;
	fz_try(ctx) {
		td.page = fz_page_number_from_location(ctx, doc, o->page);
		td.x = o->x; td.y = o->y;
	}
	fz_catch(ctx) {
		td.page = -1;
	}
	if (td.page < 0 && o->uri) {
		fz_try(ctx) {
			float x = 0, y = 0;
			fz_location loc = fz_resolve_link(ctx, doc, o->uri, &x, &y);
			td.page = fz_page_number_from_location(ctx, doc, loc);
			td.x = x; td.y = y;
		}
		fz_catch(ctx) {
			td.page = -1;
		}
	}
	return td;
}
*/
import "C"

import (
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
	"unsafe"

	"github.com/gen2brain/go-fitz"
	"github.com/sahilm/fuzzy"
)

// tocEntry is one outline (table of contents) entry, flattened from MuPDF's
// tree: level is the nesting depth (0 = top), page0 the 0-indexed page, and
// (x, y) the destination point on it in PDF points, top-left origin — (0,0)
// when the entry carries none, matching pageLink.
type tocEntry struct {
	level int
	title string
	page0 int
	x, y  float64
}

// documentTOC extracts the document outline. Like documentLinks, it reaches
// past go-fitz's public API via the private ctx/doc pointers and takes the
// document's own mutex. Entries that resolve to no page are dropped; their
// children keep their depth.
func documentTOC(doc *fitz.Document) []tocEntry {
	if doc == nil {
		return nil
	}
	v := reflect.ValueOf(doc).Elem()
	ctx := (*C.fz_context)(unsafe.Pointer(v.Field(0).Pointer()))
	fzdoc := (*C.fz_document)(unsafe.Pointer(v.Field(2).Pointer()))
	if ctx == nil || fzdoc == nil {
		return nil
	}
	mtx := (*sync.Mutex)(unsafe.Pointer(v.Field(3).UnsafeAddr()))
	mtx.Lock()
	defer mtx.Unlock()

	outline := C.load_outline_ext(ctx, fzdoc)
	if outline == nil {
		return nil
	}
	defer C.fz_drop_outline(ctx, outline)

	var out []tocEntry
	var walk func(o *C.fz_outline, level int)
	walk = func(o *C.fz_outline, level int) {
		for ; o != nil; o = o.next {
			td := C.outline_dest(ctx, fzdoc, o)
			if int(td.page) >= 0 {
				e := tocEntry{level: level, page0: int(td.page)}
				e.title = strings.Join(strings.Fields(C.GoString(o.title)), " ")
				if e.title == "" {
					e.title = "(untitled)"
				}
				// NaN coordinates mean "no point", as in documentLinks.
				if x := float64(td.x); !math.IsNaN(x) {
					e.x = x
				}
				if y := float64(td.y); !math.IsNaN(y) {
					e.y = y
				}
				out = append(out, e)
			}
			if o.down != nil {
				walk(o.down, level+1)
			}
		}
	}
	walk(outline, 0)
	return out
}

// tocMatch is one row of the picker's filtered view: an index into the toc
// slice plus the byte positions fuzzy matching highlighted.
type tocMatch struct {
	idx     int
	matched []int
}

// tocFilter returns the rows to display for a query: every entry in document
// order when the query is empty, otherwise fuzzy matches over the titles,
// best first.
func tocFilter(entries []tocEntry, query string) []tocMatch {
	if query == "" {
		out := make([]tocMatch, len(entries))
		for i := range entries {
			out[i] = tocMatch{idx: i}
		}
		return out
	}
	titles := make([]string, len(entries))
	for i, e := range entries {
		titles[i] = e.title
	}
	var out []tocMatch
	for _, m := range fuzzy.Find(query, titles) {
		out = append(out, tocMatch{idx: m.Index, matched: m.MatchedIndexes})
	}
	return out
}

// tocCurrentIndex is the entry covering a page: the last one starting at or
// before it, in document order. The picker opens with it selected.
func tocCurrentIndex(entries []tocEntry, page0 int) int {
	sel := 0
	for i, e := range entries {
		if e.page0 <= page0 {
			sel = i
		}
	}
	return sel
}

// showTOC runs the table-of-contents overlay: type to fuzzy-filter, arrows
// (or Ctrl+P/N) to move, Enter to jump, Esc to close. Returns true when a
// jump was queued on syncChan — the caller then skips its redraw, and the
// jump takes the exact history+flash path of a Ctrl+click link follow.
func (d *DocumentViewer) showTOC(inputChan <-chan byte) bool {
	if len(d.toc) == 0 {
		_, rows := d.getTerminalSize()
		fmt.Printf("\033[%d;1H\033[K\033[7m No table of contents in this document — press any key \033[0m", rows)
		<-inputChan
		return false
	}

	curPage := 0
	if d.currentPage >= 0 && d.currentPage < len(d.textPages) {
		curPage = d.textPages[d.currentPage]
	}
	query := ""
	results := tocFilter(d.toc, query)
	sel := tocCurrentIndex(d.toc, curPage)
	offset := 0

	refilter := func() {
		results = tocFilter(d.toc, query)
		if query == "" {
			sel = tocCurrentIndex(d.toc, curPage)
		} else {
			sel = 0
		}
		offset = 0
	}

	for {
		sel = min(sel, len(results)-1)
		sel = max(sel, 0)
		width, height := d.getTerminalSize()
		visible := max(height-5, 1)
		if sel < offset {
			offset = sel
		} else if sel >= offset+visible {
			offset = sel - visible + 1
		}
		d.renderTOC(results, query, sel, offset, visible, width, height)

		switch ch := <-inputChan; ch {
		case 3, 27: // Ctrl+C / Esc — close without jumping
			return false
		case 13, 10: // Enter — jump to the selected entry
			if len(results) == 0 {
				continue
			}
			e := d.toc[results[sel].idx]
			if d.syncChan == nil {
				return false
			}
			t := syncTarget{page: e.page0 + 1, x: e.x, y: e.y,
				hasPoint: e.x != 0 || e.y != 0}
			select {
			case d.syncChan <- t:
				return true
			default:
				return false // a control-file jump got there first; let it win
			}
		case keyArrowPrev, 16: // Up / Ctrl+P
			sel--
		case keyArrowNext, 14: // Down / Ctrl+N
			sel++
		case 127, 8: // Backspace
			if len(query) > 0 {
				query = query[:len(query)-1]
				refilter()
			}
		case 21: // Ctrl+U — clear the query
			if query != "" {
				query = ""
				refilter()
			}
		default:
			if ch >= 32 && ch < 127 {
				query += string(ch)
				refilter()
			}
		}
	}
}

// renderTOC draws the picker: header, query, and the visible window of
// results with the selection in reverse video and fuzzy-matched characters
// highlighted. One buffered write per frame.
func (d *DocumentViewer) renderTOC(results []tocMatch, query string, sel, offset, visible, width, height int) {
	maxPage := 0
	for _, e := range d.toc {
		maxPage = max(maxPage, e.page0+1)
	}
	pageW := len(strconv.Itoa(maxPage))

	var b strings.Builder
	b.WriteString("\033[2J\033[H")
	b.WriteString(fmt.Sprintf("\033[1;36mTable of Contents — %s\033[0m\r\n", filepath.Base(d.path)))
	b.WriteString(fmt.Sprintf("\033[1;32m>\033[0m %s\r\n", query))
	b.WriteString("\033[2m" + strings.Repeat("─", width) + "\033[0m\r\n")

	if len(results) == 0 {
		b.WriteString("\033[2m  no matching entries\033[0m\r\n")
	}
	end := min(offset+visible, len(results))
	for i := offset; i < end; i++ {
		b.WriteString(d.tocLine(results[i], i == sel, width, pageW))
		b.WriteString("\r\n")
	}

	b.WriteString(fmt.Sprintf("\033[%d;1H", height))
	pos := ""
	if len(results) > 0 {
		pos = fmt.Sprintf("   [%d/%d]", sel+1, len(results))
	}
	b.WriteString("\033[2m  ↑/↓ move   Enter jump   Esc close   type to filter" + pos + "\033[0m")
	fmt.Print(b.String())
}

// tocLine formats one picker row: selection marker, indentation by outline
// depth, the title (truncated to fit), and the right-aligned 1-indexed page.
func (d *DocumentViewer) tocLine(m tocMatch, selected bool, width, pageW int) string {
	e := d.toc[m.idx]
	indent := strings.Repeat("  ", min(e.level, 6))
	pageStr := fmt.Sprintf("%*d", pageW, e.page0+1)

	// marker(2) + indent + title field + space + page, kept one short of the
	// terminal width so the last cell never autowraps.
	avail := width - 1 - 2 - len(indent) - 1 - pageW
	if avail < 4 {
		indent = ""
		avail = max(width-1-2-1-pageW, 1)
	}

	restore := "\033[0m"
	if selected {
		restore = "\033[0m\033[7m"
	}
	title, shown := highlightTitle(e.title, m.matched, avail, restore)
	pad := strings.Repeat(" ", max(avail-shown, 0))

	if selected {
		return "\033[7m► " + indent + title + pad + " " + pageStr + "\033[0m"
	}
	return "  " + indent + title + pad + " \033[2m" + pageStr + "\033[0m"
}

// highlightTitle renders a title truncated to avail display runes, wrapping
// each fuzzy-matched byte position in highlight color; restore is the SGR
// state to return to afterwards (reverse video on the selected row). Returns
// the string and how many runes it occupies.
func highlightTitle(title string, matched []int, avail int, restore string) (string, int) {
	if avail < 1 {
		return "", 0
	}
	ms := make(map[int]bool, len(matched))
	for _, i := range matched {
		ms[i] = true
	}
	total := utf8.RuneCountInString(title)
	limit := avail
	if total > avail {
		limit = avail - 1 // leave a cell for the ellipsis
	}
	var b strings.Builder
	n := 0
	for bi, r := range title {
		if n >= limit {
			break
		}
		if ms[bi] {
			b.WriteString("\033[1;33m")
			b.WriteRune(r)
			b.WriteString(restore)
		} else {
			b.WriteRune(r)
		}
		n++
	}
	if total > avail {
		b.WriteString("…")
		n++
	}
	return b.String(), n
}
