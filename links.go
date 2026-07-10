package main

/*
#cgo CFLAGS: -I${SRCDIR}/third_party/mupdf/include

#include <mupdf/fitz.h>

// go-fitz exposes Links() but discards the hot-zone rect and never resolves
// internal destinations (its Link struct is just a URI string), so link
// following needs direct MuPDF calls. The headers are vendored in
// third_party/mupdf; the symbols come from the static libmupdf that go-fitz
// already links. MuPDF reports errors by longjmp, so anything that can throw
// is wrapped in fz_try/fz_catch here rather than called from Go.

typedef struct { int page; float x; float y; } resolved_dest;

// resolve_link_uri resolves an internal link URI (e.g. "#nameddest=section.1.1"
// — hyperref URIs carry no page number, so string parsing cannot substitute)
// to a 0-indexed page and a point on it. page is -1 on failure.
static resolved_dest resolve_link_uri(fz_context *ctx, fz_document *doc, const char *uri) {
	resolved_dest rd;
	rd.page = -1; rd.x = 0; rd.y = 0;
	fz_try(ctx) {
		float x = 0, y = 0;
		fz_location loc = fz_resolve_link(ctx, doc, uri, &x, &y);
		rd.page = fz_page_number_from_location(ctx, doc, loc);
		rd.x = x; rd.y = y;
	}
	fz_catch(ctx) {
		rd.page = -1;
	}
	return rd;
}

static fz_page *load_page_ext(fz_context *ctx, fz_document *doc, int number) {
	fz_page *page = NULL;
	fz_try(ctx) {
		page = fz_load_page(ctx, doc, number);
	}
	fz_catch(ctx) {
		return NULL;
	}
	return page;
}

static fz_link *load_links_ext(fz_context *ctx, fz_page *page) {
	fz_link *links = NULL;
	fz_try(ctx) {
		links = fz_load_links(ctx, page);
	}
	fz_catch(ctx) {
		return NULL;
	}
	return links;
}
*/
import "C"

import (
	"math"
	"reflect"
	"sync"
	"unsafe"

	"github.com/gen2brain/go-fitz"
)

// pageLink is one hyperlink on a PDF page: its hot zone in PDF points with
// top-left origin — the same coordinate space clickMap.cellToPDF maps clicks
// into and Bound() reports page boxes in — plus either an external URI or a
// resolved internal destination.
type pageLink struct {
	uri            string
	x0, y0, x1, y1 float64 // hot zone
	external       bool
	destPage0      int     // 0-indexed destination page; -1 when external or unresolved
	destX, destY   float64 // destination point on destPage0; (0,0) when the dest has none
}

// documentLinks loads the links on a 0-indexed page, resolving internal
// destinations. Like layoutDocument, it reaches past go-fitz's public API via
// the private ctx/doc pointers. It also takes the document's own mutex (field
// 3): MuPDF contexts are not thread-safe, and background prefetch renders
// hold that mutex during ImageDPI.
func documentLinks(doc *fitz.Document, page0 int) []pageLink {
	v := reflect.ValueOf(doc).Elem()
	ctx := (*C.fz_context)(unsafe.Pointer(v.Field(0).Pointer()))
	fzdoc := (*C.fz_document)(unsafe.Pointer(v.Field(2).Pointer()))
	if ctx == nil || fzdoc == nil {
		return nil
	}
	mtx := (*sync.Mutex)(unsafe.Pointer(v.Field(3).UnsafeAddr()))
	mtx.Lock()
	defer mtx.Unlock()

	page := C.load_page_ext(ctx, fzdoc, C.int(page0))
	if page == nil {
		return nil
	}
	defer C.fz_drop_page(ctx, page)

	links := C.load_links_ext(ctx, page)
	if links == nil {
		return nil
	}
	defer C.fz_drop_link(ctx, links)

	var out []pageLink
	for l := links; l != nil; l = l.next {
		pl := pageLink{
			uri: C.GoString(l.uri),
			x0:  float64(l.rect.x0), y0: float64(l.rect.y0),
			x1: float64(l.rect.x1), y1: float64(l.rect.y1),
			destPage0: -1,
		}
		if C.fz_is_external_link(ctx, l.uri) != 0 {
			pl.external = true
		} else {
			rd := C.resolve_link_uri(ctx, fzdoc, l.uri)
			pl.destPage0 = int(rd.page)
			// /Fit-style and bare "#page=N" destinations carry no anchor
			// point; MuPDF reports those coordinates as NaN. Map them to the
			// documented "(0,0) = no point" so downstream float math (half
			// roll, flash placement) never sees NaN.
			if x := float64(rd.x); !math.IsNaN(x) {
				pl.destX = x
			}
			if y := float64(rd.y); !math.IsNaN(y) {
				pl.destY = y
			}
		}
		out = append(out, pl)
	}
	return out
}

// linkTolX/linkTolY are the floor click tolerances in PDF points; callers
// widen them to half the clicked cell's page footprint (cellSizePDF), since
// dual/half layouts can put 30-40pt of page in one cell.
const linkTolX, linkTolY = 4.0, 8.0

// findLinkAt picks the link whose hot zone contains (x, y), or the nearest
// one within tolerance. Hyperref hot zones are one text line tall (~10pt)
// while a terminal cell spans ~10pt+ of page and cellToPDF returns the
// clicked cell's center, so exact containment would make links unclickable
// from half the cells they cover. Tolerances are PDF points; vertical is
// looser because cells are about twice as tall as they are wide.
func findLinkAt(links []pageLink, x, y, tolX, tolY float64) (pageLink, bool) {
	best := -1
	bestDist := math.MaxFloat64
	for i, l := range links {
		dx := math.Max(math.Max(l.x0-x, x-l.x1), 0)
		dy := math.Max(math.Max(l.y0-y, y-l.y1), 0)
		if dx > tolX || dy > tolY {
			continue
		}
		if dist := dx*dx + dy*dy; dist < bestDist {
			bestDist = dist
			best = i
		}
	}
	if best < 0 {
		return pageLink{}, false
	}
	return links[best], true
}
