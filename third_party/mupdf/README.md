MuPDF C headers, copied verbatim from the pinned go-fitz module
(github.com/gen2brain/go-fitz@v1.24.15, include/). links.go compiles against
them; the symbols resolve from the static libmupdf that go-fitz links. If the
go-fitz dependency is ever upgraded, re-copy the headers from the new module
so they match its bundled library.
