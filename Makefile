BINARY = docviewer
PREFIX = $(HOME)/local/bin

.PHONY: build install clean

build:
	go build -o $(BINARY) .

# Copy-then-rename, never cp over the installed path: overwriting a binary in
# place keeps the inode, and macOS then execs it against the old cached code
# signature and SIGKILLs it (Taskgated Invalid Signature).
install: build
	mkdir -p $(PREFIX)
	cp $(BINARY) $(PREFIX)/$(BINARY).new
	mv -f $(PREFIX)/$(BINARY).new $(PREFIX)/$(BINARY)

clean:
	rm -f $(BINARY)
