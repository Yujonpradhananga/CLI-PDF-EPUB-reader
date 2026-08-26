package picker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sahilm/fuzzy"
)

// FileResult represents a file found by the searcher.
type FileResult struct {
	Path         string
	RelativePath string
	Score        int
	Matches      []int
}

// FileSearcher scans for and searches supported document files.
type FileSearcher struct {
	files []string
}

// NewFileSearcher creates a new FileSearcher.
func NewFileSearcher() *FileSearcher {
	return &FileSearcher{
		files: []string{},
	}
}

// ScanDirectories scans common directories for supported documents and images.
func (fs *FileSearcher) ScanDirectories() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	searchDirs := []string{
		filepath.Join(homeDir, "Documents"),
		filepath.Join(homeDir, "Downloads"),
		filepath.Join(homeDir, "Desktop"),
		filepath.Join(homeDir, "Books"),
		filepath.Join(homeDir, "Projects"),
		".",
		"/usr/share/doc",
		filepath.Join(homeDir, ".local/share/books"),
	}

	const maxDepth = 5

	fmt.Println("Scanning for documents and images...")

	uniqueFiles := make(map[string]bool)

	for _, dir := range searchDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		absDir, _ := filepath.Abs(dir)

		filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}

			if strings.HasPrefix(filepath.Base(path), ".") {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			if info.IsDir() {
				// Enforce max depth
				rel, _ := filepath.Rel(absDir, path)
				depth := strings.Count(rel, string(filepath.Separator))
				if depth >= maxDepth {
					return filepath.SkipDir
				}
				if info.Name() == "node_modules" || info.Name() == "vendor" || info.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}

			ext := strings.ToLower(filepath.Ext(path))
			if isSupportedExtension(ext) {
				uniqueFiles[path] = true
			}

			return nil
		})
	}

	fs.files = make([]string, 0, len(uniqueFiles))
	for file := range uniqueFiles {
		fs.files = append(fs.files, file)
	}

	fmt.Printf("Found %d files\n\n", len(fs.files))
	return nil
}

// ScanDirectory scans a single directory for supported document files.
func (fs *FileSearcher) ScanDirectory(dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	var files []string

	err = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if strings.HasPrefix(filepath.Base(path), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if isSupportedExtension(ext) {
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

func isSupportedExtension(ext string) bool {
	switch ext {
	case ".pdf", ".epub", ".docx", ".html", ".htm", ".png", ".jpg", ".jpeg":
		return true
	default:
		return false
	}
}

// Search performs a fuzzy search on the file list.
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

// GetAllFiles returns all found files as FileResults.
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

// HighlightMatches returns the file path with matched characters highlighted.
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
