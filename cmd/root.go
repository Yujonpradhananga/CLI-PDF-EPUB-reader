package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pdf-cli/internal/picker"
	"pdf-cli/internal/ui"
	"pdf-cli/internal/viewer"
)

// the main entry point for the application.
func Execute() {
	// Handle --help and -h flags
	if len(os.Args) > 1 {
		arg := os.Args[1]
		if arg == "--help" || arg == "-h" {
			printHelp()
			return
		}
		if arg == "--version" || arg == "-v" {
			fmt.Println("pdf-cli 2.0.0")
			return
		}
	}

	hasArg := len(os.Args) > 1
	arg := "."
	if hasArg {
		arg = os.Args[1]
	}

	if strings.HasPrefix(arg, "~/") {
		homeDir, _ := os.UserHomeDir()
		arg = filepath.Join(homeDir, arg[2:])
	}

	// When no argument given, show the main menu
	if !hasArg {
		for {
			result := ui.RunMainMenu()
			fmt.Print("\033[2J\033[H") // clear screen after menu

			switch result.Selection {
			case -1: // Quit
				return
			case 0: // Browse Files (broad search)
				if !runWithBroadSearch() {
					return
				}
			case 1: // Enter Directory
				dir := result.DirPath
				if dir == "" {
					continue // no path entered, back to menu
				}
				// Validate the directory exists
				info, err := os.Stat(dir)
				if err != nil {
					fmt.Printf("\n  Directory not found: %s\n  Press any key to go back...\n", dir)
					buf := make([]byte, 1)
					os.Stdin.Read(buf)
					continue
				}
				if !info.IsDir() {
					// User entered a file path directly — try to open it
					v := viewer.NewDocumentViewer(dir)
					if err := v.Open(); err != nil {
						fmt.Printf("\n  Error opening file: %v\n  Press any key to go back...\n", err)
						buf := make([]byte, 1)
						os.Stdin.Read(buf)
						continue
					}
					wantBack := v.Run()
					if !wantBack {
						return
					}
					continue
				}
				if !runWithDirectoryPicker(dir) {
					return
				}
			default:
				return
			}
		}
	}

	// Check if argument is a directory or file
	info, statErr := os.Stat(arg)
	if statErr != nil {
		fmt.Printf("Path not found: %s\n", arg)
		return
	}

	// Determine the search directory for "back" functionality
	searchDir := arg
	isDir := info.IsDir()
	if !isDir {
		searchDir = filepath.Dir(arg)
	}

	// Main loop - allows going back to file picker
	firstFile := true
	for {
		var filePath string
		var err error

		if isDir || !firstFile {
			filePath, err = selectFileWithPickerInDir(searchDir)
			if err != nil {
				fmt.Printf("File selection cancelled: %v\n", err)
				return
			}
		} else {
			filePath = arg
			firstFile = false
		}

		if filePath == "" {
			return
		}

		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			fmt.Printf("File not found at: %s\n", filePath)
			return
		}

		ext := strings.ToLower(filepath.Ext(filePath))
		if ext != ".pdf" && ext != ".epub" && ext != ".docx" && ext != ".html" && ext != ".htm" {
			fmt.Printf("Unsupported file format: %s\nSupported formats: .pdf, .epub, .docx, .html\n", ext)
			return
		}

		v := viewer.NewDocumentViewer(filePath)
		if err := v.Open(); err != nil {
			fmt.Printf("Error opening file: %v\n", err)
			return
		}

		wantBack := v.Run()
		if !wantBack {
			return
		}
	}
}

func printHelp() {
	help := `pdf-cli - Terminal-based document viewer

USAGE:
    pdf-cli [OPTIONS] [PATH]

ARGUMENTS:
    [PATH]    File or directory to open (default: current directory)
              - If a directory, opens file picker with fuzzy search
              - If a file, opens it directly

OPTIONS:
    -h, --help       Show this help message
    -v, --version    Show version

SUPPORTED FORMATS:
    PDF, EPUB, DOCX, HTML

KEYBOARD SHORTCUTS:
    Navigation:
        j, Space, Down, Right    Next page
        k, Up, Left              Previous page
        g                        Go to specific page
        c                        Show chapter list (Table of Contents)
        >                        Next chapter
        <                        Previous chapter
        b                        Back to file picker

    Search:
        /                        Search in document
        n                        Next search result
        N                        Previous search result

    Display:
        t                        Toggle view mode (auto/text/image)
        f                        Cycle fit modes (height/width/auto)
        i                        Toggle dark mode (smart invert, preserves hue)
        D                        Toggle dark mode (simple invert)
        +, =                     Zoom in
        -                        Zoom out
        r                        Refresh display (re-detect cell size)
        d                        Show debug info

    Other:
        h                        Show help
        q                        Quit

EXAMPLES:
    pdf-cli                    Search current directory
    pdf-cli ~/Documents        Search specific directory
    pdf-cli paper.pdf          Open file directly

For LaTeX workflows, the viewer auto-reloads when the file changes.
`
	fmt.Print(help)
}

// Returns true if the user wants to go back to the main menu.
func runWithBroadSearch() bool {
	for {
		filePath, err := selectFileWithPickerBroadSearch()
		if err != nil {
			return true // go back to menu
		}
		if filePath == "" {
			return true
		}

		v := viewer.NewDocumentViewer(filePath)
		if err := v.Open(); err != nil {
			fmt.Printf("Error opening file: %v\n", err)
			return false
		}

		wantBack := v.Run()
		if !wantBack {
			return false
		}
	}
}

// Returns true if the user wants to go back to the main menu.
func runWithDirectoryPicker(dir string) bool {
	for {
		filePath, err := selectFileWithPickerInDir(dir)
		if err != nil {
			fmt.Printf("\n  %v\n  Press any key to go back...\n", err)
			buf := make([]byte, 1)
			os.Stdin.Read(buf)
			return true // go back to menu
		}
		if filePath == "" {
			return true
		}

		v := viewer.NewDocumentViewer(filePath)
		if err := v.Open(); err != nil {
			fmt.Printf("Error opening file: %v\n", err)
			return false
		}

		wantBack := v.Run()
		if !wantBack {
			return false
		}
	}
}

func selectFileWithPickerInDir(dir string) (string, error) {
	searcher := picker.NewFileSearcher()
	if err := searcher.ScanDirectory(dir); err != nil {
		return "", fmt.Errorf("error scanning directory: %v", err)
	}
	allFiles := searcher.GetAllFiles()
	if len(allFiles) == 0 {
		return "", fmt.Errorf("no supported files found in %s", dir)
	}
	p := picker.NewFilePicker(searcher)
	return p.Run()
}

func selectFileWithPickerBroadSearch() (string, error) {
	searcher := picker.NewFileSearcher()
	if err := searcher.ScanDirectories(); err != nil {
		return "", fmt.Errorf("error scanning directories: %v", err)
	}
	allFiles := searcher.GetAllFiles()
	if len(allFiles) == 0 {
		return "", fmt.Errorf("no PDF or EPUB files found in common directories")
	}
	p := picker.NewFilePicker(searcher)
	return p.Run()
}
