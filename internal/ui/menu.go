package ui

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ANSI formatting (uses terminal's own color scheme)
const (
	reset       = "\033[0m"
	bold        = "\033[1m"
	dim         = "\033[2m"
	hideCursor  = "\033[?25l"
	showCursor  = "\033[?25h"
	clearScreen = "\033[2J\033[H"

	// Standard ANSI colors — these adapt to whatever the terminal theme is
	fgDefault = "\033[39m" // terminal's default foreground
	fgBold    = "\033[1m"  // bold (often brighter variant)
	fgDim     = "\033[2m"  // dim
	fgReverse = "\033[7m"  // reverse video (swap fg/bg)
	fgCyan    = "\033[36m" // standard cyan (adapts to theme)
	fgGreen   = "\033[32m" // standard green
	fgYellow  = "\033[33m" // standard yellow
	fgMagenta = "\033[35m" // standard magenta
	fgBrCyan  = "\033[96m" // bright cyan
	fgBrGreen = "\033[92m" // bright green
	fgBrWhite = "\033[97m" // bright white
)

// plainLength returns the visible length of text (without ANSI escape codes).
func plainLength(s string) int {
	length := 0
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		length++
	}
	return length
}

// centerText centers text within the given width.
func centerText(text string, width int) string {
	pl := plainLength(text)
	if pl >= width {
		return text
	}
	padding := (width - pl) / 2
	return strings.Repeat(" ", padding) + text
}

// getTermSize returns the terminal dimensions.
func getTermSize() (int, int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 80, 24
	}
	return w, h
}

// Logo is the ASCII art for PDF-CLI
const logo = `
 ██████╗  ██████╗  ███████╗       ██████╗ ██╗     ██╗
 ██╔══██╗ ██╔══██╗ ██╔════╝      ██╔════╝ ██║     ██║
 ██████╔╝ ██║  ██║ █████╗  █████╗██║      ██║     ██║
 ██╔═══╝  ██║  ██║ ██╔══╝  ╚════╝██║      ██║     ██║
 ██║      ██████╔╝ ██║           ╚██████╗ ███████╗██║
 ╚═╝      ╚═════╝  ╚═╝            ╚═════╝ ╚══════╝╚═╝`

// smallLogo is used when terminal is narrow
const smallLogo = `
 ╔═════════════════════╗
 ║     P D F - C L I   ║
 ╚═════════════════════╝`

// MenuItem represents a menu option
type MenuItem struct {
	Label       string
	Description string
}

// MainMenuItems are the options shown on the main screen
var MainMenuItems = []MenuItem{
	{Label: "📂  Browse Files", Description: "Search across common directories"},
	{Label: "📁  Enter Directory", Description: "Open a specific directory path"},
}

// applyLogoStyle styles the logo using bold + terminal default accent color.
func applyLogoStyle(text string) string {
	lines := strings.Split(text, "\n")
	result := make([]string, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			result[i] = line
		} else {
			result[i] = bold + fgCyan + line + reset
		}
	}
	return strings.Join(result, "\n")
}

// RenderMainMenu renders the complete main menu screen and returns the rendered string.
func RenderMainMenu(selected int) string {
	width, height := getTermSize()
	var sb strings.Builder

	// Choose logo based on width
	logoText := logo
	if width < 80 {
		logoText = smallLogo
	}

	// Style logo with terminal colors
	styledLogo := applyLogoStyle(strings.TrimPrefix(logoText, "\n"))

	// Subtitle
	subtitle := dim + "Terminal-based Document Viewer" + reset
	version := dim + "v.3.0" + reset

	// Build menu items
	var menuLines []string
	for i, item := range MainMenuItems {
		if i == selected {
			// Selected: bold with arrow indicator
			line := fmt.Sprintf("  %s%s▸ %s%s  %s%s%s",
				fgBrCyan, bold, item.Label, reset,
				dim, item.Description, reset)
			menuLines = append(menuLines, line)
		} else {
			// Unselected: normal
			line := fmt.Sprintf("    %s  %s%s%s",
				item.Label,
				dim, item.Description, reset)
			menuLines = append(menuLines, line)
		}
	}

	// Decorative separator
	sepWidth := 40
	if width < 50 {
		sepWidth = width - 10
	}
	separator := dim + centerText(strings.Repeat("─", sepWidth), width) + reset

	// Help text
	help := fmt.Sprintf("%s↑/↓ Navigate  Enter Select  q Quit%s", dim, reset)

	// Supported formats badge
	formats := fmt.Sprintf("%sPDF  •  EPUB  •  DOCX  •  HTML%s", dim, reset)

	// Build all content lines
	var contentLines []string

	// Logo lines
	for _, line := range strings.Split(styledLogo, "\n") {
		contentLines = append(contentLines, centerText(line, width))
	}
	contentLines = append(contentLines, "") // spacing
	contentLines = append(contentLines, centerText(subtitle, width))
	contentLines = append(contentLines, centerText(version, width))
	contentLines = append(contentLines, "") // spacing
	contentLines = append(contentLines, separator)
	contentLines = append(contentLines, "") // spacing

	// Menu items (centered as a block)
	for _, ml := range menuLines {
		contentLines = append(contentLines, centerText(ml, width))
	}
	contentLines = append(contentLines, "") // spacing
	contentLines = append(contentLines, separator)
	contentLines = append(contentLines, "") // spacing
	contentLines = append(contentLines, centerText(formats, width))
	contentLines = append(contentLines, "") // spacing
	contentLines = append(contentLines, centerText(help, width))

	// Vertically center the content
	totalContentHeight := len(contentLines)
	topPadding := (height - totalContentHeight) / 2
	if topPadding < 0 {
		topPadding = 0
	}

	// Write top padding
	for i := 0; i < topPadding; i++ {
		sb.WriteString("\r\n")
	}

	// Write content
	for _, line := range contentLines {
		sb.WriteString(line + "\r\n")
	}

	return sb.String()
}

// MenuResult holds the result of the main menu interaction.
type MenuResult struct {
	Selection int    // 0 = Browse, 1 = Enter Directory, -1 = Quit
	DirPath   string // populated when Selection == 1
}

// RunMainMenu displays the main menu and returns the user's selection.
func RunMainMenu() MenuResult {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return MenuResult{Selection: 0} // fallback to browse
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	fmt.Print(hideCursor)
	defer fmt.Print(showCursor)

	selected := 0

	for {
		fmt.Print(clearScreen)
		fmt.Print(RenderMainMenu(selected))

		// Read input
		buf := make([]byte, 3)
		n, _ := os.Stdin.Read(buf)
		if n == 0 {
			continue
		}

		switch {
		case buf[0] == 'q' || buf[0] == 3: // q or Ctrl+C
			return MenuResult{Selection: -1}
		case buf[0] == 13: // Enter
			if selected == 1 {
				// "Enter Directory" — prompt for path
				term.Restore(int(os.Stdin.Fd()), oldState)
				fmt.Print(showCursor)
				dir := promptForDirectory()
				fmt.Print(hideCursor)
				if dir == "" {
					// User cancelled, re-enter raw mode and continue
					oldState, _ = term.MakeRaw(int(os.Stdin.Fd()))
					continue
				}
				return MenuResult{Selection: 1, DirPath: dir}
			}
			return MenuResult{Selection: selected}
		case buf[0] == 'j' || (n >= 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'B'): // j or Down
			selected = (selected + 1) % len(MainMenuItems)
		case buf[0] == 'k' || (n >= 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'A'): // k or Up
			selected--
			if selected < 0 {
				selected = len(MainMenuItems) - 1
			}
		case buf[0] == 27 && n == 1: // plain ESC
			return MenuResult{Selection: -1}
		}
	}
}

// promptForDirectory shows a text input for the user to type a directory path.
// Supports shell-like Tab completion: completes common prefix, shows candidates, cycles through them.
// Returns empty string if cancelled.
func promptForDirectory() string {
	width, height := getTermSize()

	// Layout
	promptRow := height/2 + 6
	inputRow := promptRow + 1
	candidateStartRow := promptRow + 3
	helpRow := promptRow + 2

	padLeft := (width - 50) / 2
	if padLeft < 2 {
		padLeft = 2
	}
	promptPrefix := "  > "

	// Draw static parts
	drawPromptChrome := func() {
		fmt.Printf("\033[%d;1H\033[K", promptRow)
		fmt.Printf("%s", centerText(fmt.Sprintf("%s%sEnter directory path:%s ", fgBrCyan, bold, reset), width))
		fmt.Printf("\033[%d;1H\033[K", helpRow)
		fmt.Printf("%s", centerText(fmt.Sprintf("%sESC cancel • Enter confirm • Tab complete • ~ = home%s", dim, reset), width))
	}

	redrawInput := func(input string) {
		fmt.Printf("\033[%d;1H\033[K", inputRow)
		fmt.Printf("%s%s%s", strings.Repeat(" ", padLeft), promptPrefix, input)
	}

	clearCandidates := func() {
		for row := candidateStartRow; row < height-1; row++ {
			fmt.Printf("\033[%d;1H\033[K", row)
		}
	}

	drawPromptChrome()

	var input []byte
	var lastTabInput string    // input when Tab was last pressed
	var tabCycleIndex int      // which candidate we're cycling through (-1 = common prefix)
	var tabCandidates []string // cached candidate list for cycling
	var tabBaseDir string      // base dir for current tab session

	redrawInput("")

	for {
		buf := make([]byte, 1)
		n, _ := os.Stdin.Read(buf)
		if n == 0 {
			continue
		}

		switch buf[0] {
		case 13, 10: // Enter
			result := strings.TrimSpace(string(input))
			if result == "" {
				return ""
			}
			return expandTilde(result)
		case 27: // ESC
			return ""
		case 127, 8: // Backspace
			if len(input) > 0 {
				input = input[:len(input)-1]
				clearCandidates()
				lastTabInput = ""
				tabCandidates = nil
				redrawInput(string(input))
			}
		case 9: // Tab
			partial := string(input)
			if partial == "" {
				partial = "./"
				input = []byte(partial)
			}

			currentInput := string(input)

			// Check if this is a repeated Tab press on the same input
			if currentInput == lastTabInput && len(tabCandidates) > 0 {
				// Cycle to next candidate
				tabCycleIndex++
				if tabCycleIndex >= len(tabCandidates) {
					tabCycleIndex = 0
				}
				candidate := tabCandidates[tabCycleIndex]
				completed := tabBaseDir + candidate + "/"
				completed = collapseTilde(completed)
				input = []byte(completed)
				redrawInput(string(input))
				// Highlight current candidate in the list
				drawCandidateList(tabCandidates, tabCycleIndex, candidateStartRow, padLeft, height)
				lastTabInput = string(input)
				continue
			}

			// Fresh Tab press — find matches
			expanded := expandTilde(partial)
			dir, prefix := splitDirPrefix(expanded)

			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}

			var matches []string
			for _, e := range entries {
				name := e.Name()
				if !e.IsDir() {
					continue
				}
				if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
					continue // skip hidden unless user typed a dot
				}
				if strings.HasPrefix(name, prefix) {
					matches = append(matches, name)
				}
			}

			clearCandidates()

			if len(matches) == 0 {
				// No matches — beep or do nothing
				continue
			}

			if len(matches) == 1 {
				// Single match — complete fully
				completed := dir + matches[0] + "/"
				completed = collapseTilde(completed)
				input = []byte(completed)
				redrawInput(string(input))
				lastTabInput = ""
				tabCandidates = nil
				continue
			}

			// Multiple matches — complete common prefix first
			common := longestCommonPrefix(matches)
			if len(common) > len(prefix) {
				// We can extend the input with the common prefix
				completed := dir + common
				completed = collapseTilde(completed)
				input = []byte(completed)
				redrawInput(string(input))
			}

			// Show candidates
			tabCandidates = matches
			tabBaseDir = dir
			tabCycleIndex = -1 // not cycling yet, next Tab will start cycling
			lastTabInput = string(input)

			drawCandidateList(matches, -1, candidateStartRow, padLeft, height)

		default:
			if buf[0] >= 32 && buf[0] < 127 {
				input = append(input, buf[0])
				fmt.Printf("%c", buf[0])
				// Clear candidate list when user types
				if len(tabCandidates) > 0 {
					clearCandidates()
					lastTabInput = ""
					tabCandidates = nil
				}
			}
		}
	}
}

// drawCandidateList renders the tab-completion candidates below the prompt.
func drawCandidateList(candidates []string, highlighted int, startRow, padLeft, maxHeight int) {
	maxShow := maxHeight - startRow - 1
	if maxShow < 1 {
		maxShow = 1
	}
	if maxShow > 12 {
		maxShow = 12
	}

	// Determine columns — try to fit candidates side by side
	maxNameLen := 0
	for _, c := range candidates {
		if len(c) > maxNameLen {
			maxNameLen = len(c)
		}
	}
	colWidth := maxNameLen + 3
	if colWidth < 10 {
		colWidth = 10
	}

	termWidth, _ := getTermSize()
	availWidth := termWidth - padLeft - 4
	numCols := availWidth / colWidth
	if numCols < 1 {
		numCols = 1
	}

	row := startRow
	col := 0
	for i, name := range candidates {
		if row-startRow >= maxShow {
			fmt.Printf("\033[%d;%dH%s... and %d more%s",
				row, padLeft+4, dim, len(candidates)-i, reset)
			break
		}

		if col == 0 {
			fmt.Printf("\033[%d;1H\033[K", row)
			fmt.Printf("\033[%d;%dH", row, padLeft+4)
		}

		display := name + "/"
		if i == highlighted {
			fmt.Printf("%s%s%-*s%s", fgBrCyan, bold, colWidth, display, reset)
		} else {
			fmt.Printf("%s%-*s%s", dim, colWidth, display, reset)
		}

		col++
		if col >= numCols {
			col = 0
			row++
		}
	}
}

// expandTilde expands ~ to the home directory.
func expandTilde(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}

// collapseTilde replaces the home directory prefix with ~.
func collapseTilde(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(path, home+"/") {
			return "~" + path[len(home):]
		}
		if path == home {
			return "~"
		}
	}
	return path
}

// splitDirPrefix splits a path into the directory part and the incomplete name being typed.
// e.g. "/home/user/Doc" → ("/home/user/", "Doc")
// e.g. "/home/user/Documents/" → ("/home/user/Documents/", "")
func splitDirPrefix(expanded string) (string, string) {
	if strings.HasSuffix(expanded, "/") {
		return expanded, ""
	}
	idx := strings.LastIndex(expanded, "/")
	if idx < 0 {
		return "./", expanded
	}
	return expanded[:idx+1], expanded[idx+1:]
}

// longestCommonPrefix returns the longest common prefix of a list of strings.
func longestCommonPrefix(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	prefix := strs[0]
	for _, s := range strs[1:] {
		for len(prefix) > 0 && !strings.HasPrefix(s, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
}
