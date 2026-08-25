package terminal

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/term"
)

// GetSize returns the terminal columns and rows.
func GetSize() (int, int) {
	if width, height, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 && height > 0 {
		return width, height
	}
	if tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0); err == nil {
		defer tty.Close()
		if width, height, err := term.GetSize(int(tty.Fd())); err == nil {
			return width, height
		}
	}
	return 80, 24
}

// DetectType returns the terminal type string.
func DetectType() string {
	if termProgram := os.Getenv("TERM_PROGRAM"); termProgram != "" {
		switch termProgram {
		case "WezTerm":
			return "wezterm"
		case "iTerm.app":
			return "iterm2"
		case "Apple_Terminal":
			return "apple_terminal"
		}
	}
	if os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("KITTY_PID") != "" {
		return "kitty"
	}
	t := os.Getenv("TERM")
	switch {
	case strings.Contains(t, "kitty"):
		return "kitty"
	case strings.Contains(t, "foot"):
		return "foot"
	case strings.Contains(t, "alacritty"):
		return "alacritty"
	case strings.Contains(t, "wezterm"):
		return "wezterm"
	case strings.Contains(t, "xterm"):
		return "xterm"
	case strings.Contains(t, "tmux"):
		return "tmux"
	case strings.Contains(t, "screen"):
		return "screen"
	}
	return "unknown"
}

// GetPixelSize returns the terminal pixel dimensions via TIOCGWINSZ.
func GetPixelSize() (int, int) {
	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		uintptr(syscall.Stdout),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&ws)))

	if errno == 0 && ws.Xpixel > 0 && ws.Ypixel > 0 {
		return int(ws.Xpixel), int(ws.Ypixel)
	}
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

// GetKittyCellSize queries Kitty for actual cell size using escape sequence.
func GetKittyCellSize() (float64, float64) {
	if DetectType() != "kitty" {
		return 0, 0
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, 0
	}
	defer tty.Close()
	fd := int(tty.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return 0, 0
	}
	defer term.Restore(fd, oldState)
	tty.WriteString("\x1b[16t")
	tty.Sync()
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
		var cellHeight, cellWidth int
		if _, err := fmt.Sscanf(response, "\x1b[6;%d;%dt", &cellHeight, &cellWidth); err == nil {
			if cellWidth > 0 && cellHeight > 0 {
				return float64(cellWidth), float64(cellHeight)
			}
		}
	case <-time.After(100 * time.Millisecond):
	}
	return 0, 0
}

// DetectCellSize detects cell dimensions in pixels.
func DetectCellSize() (float64, float64) {
	if cellSize := os.Getenv("DOCVIEWER_CELL_SIZE"); cellSize != "" {
		var w, h float64
		if _, err := fmt.Sscanf(cellSize, "%fx%f", &w, &h); err == nil && w > 0 && h > 0 {
			return w, h
		}
	}
	if kw, kh := GetKittyCellSize(); kw > 0 && kh > 0 {
		return kw, kh
	}
	pixelWidth, pixelHeight := GetPixelSize()
	charWidth, charHeight := GetSize()
	if pixelWidth > 0 && pixelHeight > 0 && charWidth > 0 && charHeight > 0 {
		cellWidth := float64(pixelWidth) / float64(charWidth)
		cellHeight := float64(pixelHeight) / float64(charHeight)
		if cellWidth > 4 && cellHeight > 8 {
			return cellWidth, cellHeight
		}
	}
	termType := DetectType()
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

// SetRawMode puts the terminal into raw mode.
func SetRawMode() (*term.State, error) {
	return term.MakeRaw(int(os.Stdin.Fd()))
}

// RestoreTerminal restores terminal to the given state.
func RestoreTerminal(old *term.State) {
	if old != nil {
		term.Restore(int(os.Stdin.Fd()), old)
	}
}

// ReadSingleChar reads a single character from stdin, handling escape sequences.
func ReadSingleChar() byte {
	buf := make([]byte, 1)
	n, _ := os.Stdin.Read(buf)
	if n == 0 {
		return 0
	}
	if buf[0] == 27 {
		b := make([]byte, 1)
		n, _ = os.Stdin.Read(b)
		if n == 1 && b[0] == '[' {
			n, _ = os.Stdin.Read(b)
			if n == 1 {
				switch b[0] {
				case 'A':
					return 'k'
				case 'B':
					return 'j'
				case 'C':
					return 'j'
				case 'D':
					return 'k'
				case '1':
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
						case 'A':
							return 'K'
						case 'B':
							return 'J'
						case 'C':
							return 'J'
						case 'D':
							return 'K'
						}
					}
				}
			}
		}
		return 27
	}
	return buf[0]
}
