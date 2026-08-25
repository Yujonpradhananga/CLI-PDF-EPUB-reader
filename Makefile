BINARY = docviewer
PREFIX = $(HOME)/local/bin

.PHONY: build install clean

build:
	go build -o $(BINARY) .

# Copy-then-rename, never cp over the installed path: overwriting a binary in
# place keeps the inode, and macOS then execs it against the old cached code
# signature and SIGKILLs it (Taskgated Invalid Signature).
# dvctl is the same binary under another name: main() dispatches to the control
# client when argv[0] says dvctl, so the viewer and the CLI can never drift.
install: build
	mkdir -p $(PREFIX)
	cp $(BINARY) $(PREFIX)/$(BINARY).new
	mv -f $(PREFIX)/$(BINARY).new $(PREFIX)/$(BINARY)
	ln -sf $(PREFIX)/$(BINARY) $(PREFIX)/dvctl

clean:
	rm -f $(BINARY)
