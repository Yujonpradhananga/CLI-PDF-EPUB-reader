package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	// Handle --help and -h flags
	if len(os.Args) > 1 {
		arg := os.Args[1]
		if arg == "--help" || arg == "-h" {
			printHelp()
			return
		}
		if arg == "--version" || arg == "-v" {
			fmt.Println("docviewer 1.0.0")
			return
		}
	}

	// Determine if user provided an argument
	hasArg := len(os.Args) > 1
	arg := "."
	if hasArg {
		arg = os.Args[1]
	}

	// Expand ~ to home directory
	if strings.HasPrefix(arg, "~/") {
		homeDir, _ := os.UserHomeDir()
		arg = filepath.Join(homeDir, arg[2:])
	}

	// When no argument given, do a broad search across common directories
	if !hasArg {
		// Main loop for broad search mode
		for {
			filePath, err := selectFileWithPickerBroadSearch()
			if err != nil {
				fmt.Printf("File selection cancelled: %v\n", err)
				return
			}
			if filePath == "" {
				return
			}

			viewer := NewDocumentViewer(filePath)
			if err := viewer.Open(); err != nil {
				fmt.Printf("Error opening file: %v\n", err)
				return
			}

			wantBack := viewer.Run()
			if !wantBack {
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
			// Search within directory
			filePath, err = selectFileWithPickerInDir(searchDir)
			if err != nil {
				fmt.Printf("File selection cancelled: %v\n", err)
				return
			}
		} else {
			// First time with a file - use directly, then switch to directory mode
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
		if ext != ".pdf" && ext != ".epub" && ext != ".docx" && ext != ".html" && ext != ".htm" &&
			ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
			fmt.Printf("Unsupported file format: %s\nSupported formats: .pdf, .epub, .docx, .html, .png, .jpg\n", ext)
			return
		}

		viewer := NewDocumentViewer(filePath)
		if err := viewer.Open(); err != nil {
			fmt.Printf("Error opening file: %v\n", err)
			return
		}

		wantBack := viewer.Run()
		if !wantBack {
			return
		}
		// Loop continues - go back to file picker
	}
}

func printHelp() {
	help := `docviewer - Terminal-based document viewer

USAGE:
    docviewer [OPTIONS] [PATH]

ARGUMENTS:
    [PATH]    File or directory to open (default: current directory)
              - If a directory, opens file picker with fuzzy search
              - If a file, opens it directly

OPTIONS:
    -h, --help       Show this help message
    -v, --version    Show version

SUPPORTED FORMATS:
    PDF, EPUB, DOCX, HTML, PNG, JPG/JPEG

KEYBOARD SHORTCUTS:
    Navigation:
        j, Space, Down, Right    Next page
        k, Up, Left              Previous page
        g                        Go to specific page
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
    docviewer                    Search current directory
    docviewer ~/Documents        Search specific directory
    docviewer paper.pdf          Open file directly

For LaTeX workflows, the viewer auto-reloads when the file changes.
`
	fmt.Print(help)
}

func selectFileWithPickerInDir(dir string) (string, error) {
	searcher := NewFileSearcher()
	if err := searcher.ScanDirectory(dir); err != nil {
		return "", fmt.Errorf("error scanning directory: %v", err)
	}
	allFiles := searcher.GetAllFiles()
	if len(allFiles) == 0 {
		return "", fmt.Errorf("no supported files found in %s", dir)
	}
	picker := NewFilePicker(searcher)
	return picker.Run()
}

func selectFileWithPickerBroadSearch() (string, error) {
	searcher := NewFileSearcher()
	if err := searcher.ScanDirectories(); err != nil {
		return "", fmt.Errorf("error scanning directories: %v", err)
	}
	allFiles := searcher.GetAllFiles()
	if len(allFiles) == 0 {
		return "", fmt.Errorf("no supported files found in common directories")
	}
	picker := NewFilePicker(searcher)
	return picker.Run()
}
