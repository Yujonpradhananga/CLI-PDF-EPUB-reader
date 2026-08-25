package layout

/*
#include <stdlib.h>
// fz_layout_document is provided by the MuPDF library linked via go-fitz.
// It controls the page layout for reflowable documents (HTML, EPUB).
// w = page width in points, h = page height in points, em = base font size in points.
extern void fz_layout_document(void *ctx, void *doc, float w, float h, float em);

// fz_resolve_link + fz_page_number_from_location resolve a URI (e.g. "#chapter1.xhtml")
// to a flat page number. This is needed for EPUB ToC entries which use URIs instead of
// page numbers.
typedef struct { int chapter; int page; } fz_location;
extern fz_location fz_resolve_link(void *ctx, void *doc, const char *uri, float *xp, float *yp);
extern int fz_page_number_from_location(void *ctx, void *doc, fz_location loc);
*/
import "C"

import (
	"reflect"
	"unsafe"

	"github.com/gen2brain/go-fitz"
)

// LayoutDocument calls MuPDF's fz_layout_document to control page layout
// for reflowable documents (HTML, EPUB). The em parameter controls the
// base font size in points (default is ~12pt).
func LayoutDocument(doc *fitz.Document, w, h, em float64) {
	v := reflect.ValueOf(doc).Elem()
	ctx := unsafe.Pointer(v.Field(0).Pointer())
	docPtr := unsafe.Pointer(v.Field(2).Pointer())
	C.fz_layout_document(ctx, docPtr, C.float(w), C.float(h), C.float(em))
}

// ResolveLink converts a document URI (e.g. from an EPUB ToC entry) to a
// flat 0-indexed page number. Returns -1 if the URI cannot be resolved.
func ResolveLink(doc *fitz.Document, uri string) int {
	v := reflect.ValueOf(doc).Elem()
	ctx := unsafe.Pointer(v.Field(0).Pointer())
	docPtr := unsafe.Pointer(v.Field(2).Pointer())

	curi := C.CString(uri)
	defer C.free(unsafe.Pointer(curi))

	loc := C.fz_resolve_link(ctx, docPtr, curi, nil, nil)
	if loc.chapter < 0 && loc.page < 0 {
		return -1
	}
	pageNum := int(C.fz_page_number_from_location(ctx, docPtr, loc))
	return pageNum
}
