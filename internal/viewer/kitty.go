package viewer

// Kitty graphics transmission, bypassing go-termimg for the kitty path.
//
// go-termimg's kitty renderer transmits f=32 raw RGBA: a fullscreen retina page
// at the 2x supersample is ~12 MP ≈ 48 MB of pixels ≈ 64 MB of base64 escape
// data through the PTY on EVERY page display, even render-cache hits (plus a
// full PNG decode and an RGBA copy per display). That, not MuPDF rendering, is
// what made kitty/ghostty feel slow.
//
// Instead we hand the terminal the PNG that savePageAsImage already wrote:
//   - t=f (file transmission): send just the base64-encoded *path* (~200 bytes);
//     the terminal opens and decodes the PNG itself. Probed once at startup.
//   - fallback t=d (direct): stream the on-disk PNG bytes as chunked base64.
//     Still 20-40x less data than raw RGBA, and no decode/re-encode round trip.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// kittyImageID is a process-wide counter for kitty graphics image ids, seeded
// with pid+time so ids from a previous run still held by the terminal are not
// reused (and then wrongly deleted by the swap logic).
var kittyImageID = uint32(os.Getpid()<<16) + uint32(time.Now().UnixMicro()&0xFFFF)

func nextKittyImageID() uint32 { return atomic.AddUint32(&kittyImageID, 1) }

const (
	kittyXferDirect = iota // chunked base64 of the PNG bytes (mandatory protocol surface)
	kittyXferFile          // t=f: terminal reads the PNG from disk itself
)

var (
	kittyXferMode = kittyXferDirect
	kittyXferOnce sync.Once
)

// probeKittyTransferMode decides, once per process, how to ship pages to a
// kitty-protocol terminal. Must run before the stdin-reading goroutine starts,
// because the probe reads a query response from the tty. DOCVIEWER_KITTY_XFER
// (file|direct) overrides the probe.
func (d *DocumentViewer) probeKittyTransferMode() {
	kittyXferOnce.Do(func() {
		switch os.Getenv("DOCVIEWER_KITTY_XFER") {
		case "file":
			kittyXferMode = kittyXferFile
			return
		case "direct":
			return
		}
		if d.detectTerminalType() != "kitty" {
			return
		}
		if probeKittyFileXfer(d.tempDir) {
			kittyXferMode = kittyXferFile
		}
	})
}

// probeKittyFileXfer asks the terminal whether it accepts t=f file transmission
// by sending an a=q query for a tiny probe PNG and reading the ;OK response.
func probeKittyFileXfer(tempDir string) bool {
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return false
	}
	probePath := filepath.Join(tempDir, "probe.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		return false
	}
	if err := os.WriteFile(probePath, buf.Bytes(), 0o644); err != nil {
		return false
	}
	absPath, err := filepath.Abs(probePath)
	if err != nil {
		return false
	}

	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer tty.Close()

	fd := int(tty.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer term.Restore(fd, oldState)

	id := nextKittyImageID()
	fmt.Fprintf(tty, "\x1b_Ga=q,f=100,t=f,i=%d;%s\x1b\\",
		id, base64.StdEncoding.EncodeToString([]byte(absPath)))

	resultChan := make(chan string, 1)
	go func() {
		var acc []byte
		readBuf := make([]byte, 256)
		for {
			n, rerr := tty.Read(readBuf)
			if n > 0 {
				acc = append(acc, readBuf[:n]...)
				if bytes.Contains(acc, []byte("\x1b\\")) {
					resultChan <- string(acc)
					return
				}
			}
			if rerr != nil {
				resultChan <- string(acc)
				return
			}
		}
	}()

	select {
	case resp := <-resultChan:
		return strings.Contains(resp, ";OK")
	case <-time.After(300 * time.Millisecond):
		return false
	}
}

// kittyDeleteImage removes an image and its placements by id.
func kittyDeleteImage(id uint32) {
	fmt.Printf("\x1b_Ga=d,d=I,i=%d\x1b\\", id)
}

// clearKittyGraphics removes every placement owned by this viewer.
func (d *DocumentViewer) clearKittyGraphics() {
	if d.lastKittyImageID == 0 {
		return
	}
	kittyDeleteImage(d.lastKittyImageID)
	d.lastKittyImageID = 0
}

// kittySendPNG transmits an on-disk PNG at the current cursor position, scaled
// to cols x rows cells, using the transfer mode chosen by the startup probe.
func kittySendPNG(imagePath string, id uint32, cols, rows int) error {
	if kittyXferMode == kittyXferFile {
		if abs, err := filepath.Abs(imagePath); err == nil {
			_, err = fmt.Printf("\x1b_Ga=T,f=100,t=f,i=%d,c=%d,r=%d,q=2;%s\x1b\\",
				id, cols, rows, base64.StdEncoding.EncodeToString([]byte(abs)))
			return err
		}
		// Path resolution failed; fall through to direct transmission.
	}
	return kittySendPNGDirect(imagePath, id, cols, rows)
}

// kittySendPNGDirect streams the PNG file bytes as chunked base64 (t=d). The
// kitty protocol caps each escape's payload at 4096 bytes of encoded data.
func kittySendPNGDirect(imagePath string, id uint32, cols, rows int) error {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return err
	}
	enc := base64.StdEncoding.EncodeToString(data)

	const chunkSize = 4096
	var out strings.Builder
	out.Grow(len(enc) + (len(enc)/chunkSize+1)*32 + 64)

	first := true
	for len(enc) > 0 {
		n := chunkSize
		if n > len(enc) {
			n = len(enc)
		}
		piece := enc[:n]
		enc = enc[n:]
		more := 0
		if len(enc) > 0 {
			more = 1
		}
		if first {
			first = false
			fmt.Fprintf(&out, "\x1b_Ga=T,f=100,i=%d,c=%d,r=%d,q=2,m=%d;%s\x1b\\",
				id, cols, rows, more, piece)
		} else {
			fmt.Fprintf(&out, "\x1b_Gm=%d,q=2;%s\x1b\\", more, piece)
		}
	}

	_, err = os.Stdout.WriteString(out.String())
	return err
}

// Ensure color is used (for the probe PNG).
var _ = color.RGBA{}
