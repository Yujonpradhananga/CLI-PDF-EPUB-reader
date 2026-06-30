package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/term"
)

func (d *DocumentViewer) getTerminalSize() (int, int) {
	// Try stdout first - it's connected to the correct PTY in Kitty splits
	if width, height, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 && height > 0 {
		return width, height
	}
	// Fallback to /dev/tty
	if tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0); err == nil {
		defer tty.Close()
		if width, height, err := term.GetSize(int(tty.Fd())); err == nil {
			return width, height
		}
	}
	return 80, 24 // Fallback default
}

// detecting Terminal
func (d *DocumentViewer) detectTerminalType() string {
	if termProgram := os.Getenv("TERM_PROGRAM"); termProgram != "" {
		switch termProgram {
		case "WezTerm":
			return "wezterm"
		case "ghostty":
			// Ghostty (and agterm, which embeds libghostty) implements the kitty
			// graphics protocol; treat as kitty so it gets the 300-DPI render path.
			return "kitty"
		case "iTerm.app":
			return "iterm2"
		case "Apple_Terminal":
			return "apple_terminal"
		}
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("KITTY_PID") != "" {
		return "kitty"
	}
	term := os.Getenv("TERM")
	switch {
	case strings.Contains(term, "kitty"):
		return "kitty"
	case strings.Contains(term, "ghostty"):
		return "kitty" // kitty graphics protocol; check before the "xterm" case
	case strings.Contains(term, "foot"):
		return "foot"
	case strings.Contains(term, "alacritty"):
		return "alacritty"
	case strings.Contains(term, "wezterm"):
		return "wezterm"
	case strings.Contains(term, "xterm"):
		return "xterm"
	case strings.Contains(term, "tmux"):
		return "tmux"
	case strings.Contains(term, "screen"):
		return "screen"
	}
	return "unknown"
}

func (d *DocumentViewer) getTerminalCellSize() (float64, float64) {
	// Check if terminal dimensions changed (resolution/monitor switch)
	cols, rows := d.getTerminalSize()
	if cols != d.lastTermCols || rows != d.lastTermRows {
		// Terminal changed - invalidate cache and re-detect
		d.cellWidth = 0
		d.cellHeight = 0
		d.lastTermCols = cols
		d.lastTermRows = rows
	}

	// Use cached values if available
	if d.cellWidth > 0 && d.cellHeight > 0 {
		return d.cellWidth, d.cellHeight
	}

	// Detect and cache
	d.cellWidth, d.cellHeight = d.detectCellSize()
	return d.cellWidth, d.cellHeight
}

// refreshCellSize forces re-detection of cell size (useful after resolution change)
func (d *DocumentViewer) refreshCellSize() {
	d.cellWidth = 0
	d.cellHeight = 0
	d.lastTermCols = 0
	d.lastTermRows = 0
}

// detectCellSize detects cell size - call before entering raw mode
func (d *DocumentViewer) detectCellSize() (float64, float64) {
	// Check for environment variable override first (most reliable for multi-resolution)
	// Format: DOCVIEWER_CELL_SIZE=WxH (e.g., "12x26")
	if cellSize := os.Getenv("DOCVIEWER_CELL_SIZE"); cellSize != "" {
		var w, h float64
		if _, err := fmt.Sscanf(cellSize, "%fx%f", &w, &h); err == nil && w > 0 && h > 0 {
			return w, h
		}
	}

	// Try Kitty-specific query first (most accurate)
	if kw, kh := d.getKittyCellSize(); kw > 0 && kh > 0 {
		return kw, kh
	}

	// Try TIOCGWINSZ pixel size
	pixelWidth, pixelHeight := d.getTerminalPixelSize()
	charWidth, charHeight := d.getTerminalSize()

	if pixelWidth > 0 && pixelHeight > 0 && charWidth > 0 && charHeight > 0 {
		cellWidth := float64(pixelWidth) / float64(charWidth)
		cellHeight := float64(pixelHeight) / float64(charHeight)
		if cellWidth > 4 && cellHeight > 8 {
			return cellWidth, cellHeight
		}
	}

	// Fallback to hardcoded values
	termType := d.detectTerminalType()
	switch termType {
	case "kitty":
		return 18.0, 36.0
	case "foot":
		return 15.0, 25.0
	case "alacritty":
		return 14.0, 28.0
	case "wezterm":
		return 18.0, 36.0
	case "iterm2":
		return 16.0, 32.0
	case "xterm":
		return 7.0, 14.0
	default:
		return 15.0, 30.0
	}
}

func (d *DocumentViewer) getTerminalPixelSize() (int, int) {
	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{}

	// Try stdout first - correct PTY in Kitty splits
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(syscall.Stdout),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&ws)))

	if errno == 0 && ws.Xpixel > 0 && ws.Ypixel > 0 {
		return int(ws.Xpixel), int(ws.Ypixel)
	}

	// Fallback to /dev/tty
	tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0)
	if err == nil {
		defer tty.Close()
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
			uintptr(tty.Fd()),
			uintptr(syscall.TIOCGWINSZ),
			uintptr(unsafe.Pointer(&ws)))

		if errno == 0 && ws.Xpixel > 0 && ws.Ypixel > 0 {
			return int(ws.Xpixel), int(ws.Ypixel)
		}
	}

	return 0, 0
}

// getKittyCellSize queries Kitty for actual cell size using escape sequence
// Uses /dev/tty directly for reliable TTY access
func (d *DocumentViewer) getKittyCellSize() (float64, float64) {
	if d.detectTerminalType() != "kitty" {
		return 0, 0
	}

	// Open /dev/tty directly for reliable TTY access
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, 0
	}
	defer tty.Close()

	// Save terminal state and set raw mode for reading response
	fd := int(tty.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return 0, 0
	}
	defer term.Restore(fd, oldState)

	// Query cell size: CSI 16 t -> CSI 6 ; height ; width t
	tty.WriteString("\x1b[16t")
	tty.Sync()

	// Read response with timeout
	resultChan := make(chan string, 1)
	go func() {
		buf := make([]byte, 32)
		n, _ := tty.Read(buf)
		if n > 0 {
			resultChan <- string(buf[:n])
		} else {
			resultChan <- ""
		}
	}()

	select {
	case response := <-resultChan:
		if response == "" {
			return 0, 0
		}
		// Parse response: ESC [ 6 ; height ; width t
		var cellHeight, cellWidth int
		if _, err := fmt.Sscanf(response, "\x1b[6;%d;%dt", &cellHeight, &cellWidth); err == nil {
			if cellWidth > 0 && cellHeight > 0 {
				return float64(cellWidth), float64(cellHeight)
			}
		}
	case <-time.After(100 * time.Millisecond):
		// Timeout - terminal didn't respond
	}

	return 0, 0
}

func (d *DocumentViewer) setRawMode() (*term.State, error) {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return nil, err
	}
	d.oldState = oldState
	return oldState, nil
}

func (d *DocumentViewer) restoreTerminal(old *term.State) {
	if old != nil {
		term.Restore(int(os.Stdin.Fd()), old)
	}
}

func (d *DocumentViewer) readSingleChar() byte {
	buf := make([]byte, 1)
	n, _ := os.Stdin.Read(buf)
	if n == 0 {
		return 0
	}

	// Handle escape sequences (arrow keys, shift+arrow)
	// Read bytes one at a time to avoid partial reads dropping sequence bytes
	if buf[0] == 27 {
		b := make([]byte, 1)
		n, _ = os.Stdin.Read(b)
		if n == 1 && b[0] == '[' {
			n, _ = os.Stdin.Read(b)
			if n == 1 {
				switch b[0] {
				case 'A': // Up arrow -> previous page (like k)
					return 'k'
				case 'B': // Down arrow -> next page (like j)
					return 'j'
				case 'C': // Right arrow -> next page
					return 'j'
				case 'D': // Left arrow -> previous page
					return 'k'
				case '1':
					// Could be shift+arrow: ESC [ 1 ; 2 A/B/C/D
					seq := make([]byte, 3)
					n2 := 0
					for i := 0; i < 3; i++ {
						nn, _ := os.Stdin.Read(seq[i : i+1])
						if nn == 0 {
							break
						}
						n2++
					}
					if n2 == 3 && seq[0] == ';' && seq[1] == '2' {
						switch seq[2] {
						case 'A': // Shift+Up
							return 'K' // uppercase = shift
						case 'B': // Shift+Down
							return 'J' // uppercase = shift
						case 'C': // Shift+Right
							return 'J' // uppercase = shift (forward)
						case 'D': // Shift+Left
							return 'K' // uppercase = shift (backward)
						}
					}
				}
			}
		}
		return 27 // Plain ESC
	}

	return buf[0]
}
