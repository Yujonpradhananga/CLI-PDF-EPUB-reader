package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sahilm/fuzzy"
)

type FileResult struct {
	Path         string
	RelativePath string
	Score        int
	Matches      []int
}

type FileSearcher struct {
	files []string
}

func NewFileSearcher() *FileSearcher {
	return &FileSearcher{
		files: []string{},
	}
}

func (fs *FileSearcher) ScanDirectories() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// Common directories to search
	searchDirs := []string{
		homeDir,
		filepath.Join(homeDir, "Documents"),
		filepath.Join(homeDir, "Downloads"),
		filepath.Join(homeDir, "Desktop"),
		".", // Current directory
		"/usr/share/doc",
		filepath.Join(homeDir, ".local/share/books"),
	}

	fmt.Println("Scanning for documents and images...")
	fmt.Println("This may take a moment on first run...")

	// Use a map to track unique files and avoid duplicates
	uniqueFiles := make(map[string]bool)

	for _, dir := range searchDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		// Use filepath.Walk for simple, straightforward directory traversal
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // Skip errors
			}

			// Skip hidden files and directories
			if strings.HasPrefix(filepath.Base(path), ".") {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			// Skip common large directories
			if info.IsDir() && (info.Name() == "node_modules" || info.Name() == "vendor") {
				return filepath.SkipDir
			}

			// Only collect supported files
			if !info.IsDir() {
				ext := strings.ToLower(filepath.Ext(path))
				if ext == ".pdf" || ext == ".epub" || ext == ".docx" ||
					ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
					uniqueFiles[path] = true
				}
			}

			return nil
		})
	}

	// Convert map to slice
	fs.files = make([]string, 0, len(uniqueFiles))
	for file := range uniqueFiles {
		fs.files = append(fs.files, file)
	}

	fmt.Printf("Found %d files\n\n", len(fs.files))
	return nil
}

// ScanDirectory scans a single directory for PDF/EPUB/DOCX files
func (fs *FileSearcher) ScanDirectory(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	var files []string

	// Use filepath.Walk for simple directory traversal
	err = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		// Skip hidden files and directories
		if strings.HasPrefix(filepath.Base(path), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}


		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".pdf" || ext == ".epub" || ext == ".docx" || ext == ".html" || ext == ".htm" ||
			ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
			files = append(files, path)
		}

		return nil
	})
	if err != nil {
		return err
	}

	fs.files = files
	return nil
}

func (fs *FileSearcher) Search(query string) []FileResult {
	if query == "" {
		results := make([]FileResult, 0, len(fs.files))
		for _, file := range fs.files {
			results = append(results, FileResult{
				Path:         file,
				RelativePath: fs.getDisplayPath(file),
				Score:        0,
			})
		}
		return results
	}

	displayPaths := make([]string, len(fs.files))
	for i, file := range fs.files {
		displayPaths[i] = fs.getDisplayPath(file)
	}

	matches := fuzzy.Find(query, displayPaths)

	results := make([]FileResult, 0, len(matches))
	for _, match := range matches {
		if match.Index < len(fs.files) {
			results = append(results, FileResult{
				Path:         fs.files[match.Index],
				RelativePath: displayPaths[match.Index],
				Score:        match.Score,
				Matches:      match.MatchedIndexes,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score < results[j].Score
	})

	return results
}

func (fs *FileSearcher) getDisplayPath(path string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return path
	}

	if strings.HasPrefix(path, homeDir) {
		return "~" + strings.TrimPrefix(path, homeDir)
	}

	return path
}

func (fs *FileSearcher) GetAllFiles() []FileResult {
	results := make([]FileResult, 0, len(fs.files))
	for _, file := range fs.files {
		results = append(results, FileResult{
			Path:         file,
			RelativePath: fs.getDisplayPath(file),
		})
	}
	return results
}

func (fr *FileResult) HighlightMatches() string {
	if len(fr.Matches) == 0 {
		return fr.RelativePath
	}

	var result strings.Builder
	matchSet := make(map[int]bool)
	for _, idx := range fr.Matches {
		matchSet[idx] = true
	}

	for i, char := range fr.RelativePath {
		if matchSet[i] {
			result.WriteString("\033[1;33m")
			result.WriteRune(char)
			result.WriteString("\033[0m")
		} else {
			result.WriteRune(char)
		}
	}

	return result.String()
}
