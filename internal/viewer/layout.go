package viewer

/*
// fz_layout_document is provided by the MuPDF library linked via go-fitz.
// It controls the page layout for reflowable documents (HTML, EPUB).
// w = page width in points, h = page height in points, em = base font size in points.
extern void fz_layout_document(void *ctx, void *doc, float w, float h, float em);
*/
import "C"

import (
	"reflect"
	"unsafe"

	"github.com/gen2brain/go-fitz"
)

// layoutDocument calls MuPDF's fz_layout_document to control page layout
// for reflowable documents (HTML, EPUB). The em parameter controls the
// base font size in points (default is ~12pt).
func layoutDocument(doc *fitz.Document, w, h, em float64) {
	v := reflect.ValueOf(doc).Elem()
	ctx := unsafe.Pointer(v.Field(0).Pointer())
	docPtr := unsafe.Pointer(v.Field(2).Pointer())
	C.fz_layout_document(ctx, docPtr, C.float(w), C.float(h), C.float(em))
}
