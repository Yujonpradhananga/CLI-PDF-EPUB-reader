package viewer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (d *DocumentViewer) displayCurrentPage() {
	termWidth, termHeight := d.getTerminalSize()
	actualPage := d.textPages[d.currentPage]

	// Begin synchronized update (Kitty) - buffers output for atomic display
	fmt.Print("\033[?2026h")

	if d.skipClear {
		fmt.Print("\033_Ga=d,d=A\033\\") // Delete all Kitty images
		fmt.Print("\033[H")              // Move cursor home
		d.skipClear = false
	} else {
		fmt.Print("\033[2J")
		fmt.Print("\033[3J")
		fmt.Print("\033[H")
	}
	fmt.Print("\033[1G")
	fmt.Print("\033[0m")

	if d.dualPageMode == "half" {
		d.displayHalfPage(termWidth, termHeight)
		fmt.Print("\033[9999;1H")
		fmt.Print("\033[?2026l")
		os.Stdout.Sync()
		return
	}
	if d.dualPageMode != "" {
		d.displayDualPage(termWidth, termHeight)
		fmt.Print("\033[9999;1H")
		fmt.Print("\033[?2026l")
		os.Stdout.Sync()
		return
	}

	contentType := d.getPageContentType(actualPage)
	switch contentType {
	case "text":
		d.displayTextPage(actualPage, termWidth, termHeight)
	case "image":
		d.displayImagePage(actualPage, termWidth, termHeight)
	case "mixed":
		d.displayMixedPage(actualPage, termWidth, termHeight)
	default:
		d.displayTextPage(actualPage, termWidth, termHeight)
	}
	fmt.Print("\033[9999;1H")

	// End synchronized update - display everything at once
	fmt.Print("\033[?2026l")
	os.Stdout.Sync()

	// Prefetch neighboring pages in background for instant navigation.
	d.prefetchNeighbors(termWidth, termHeight)
}

func (d *DocumentViewer) getPageContentType(pageNum int) string {
	if d.forceMode == "text" {
		return "text"
	}
	if d.forceMode == "image" {
		return "image"
	}

	if d.fileType == "pdf" || d.fileType == "html" || d.fileType == "htm" {
		if d.pageHasVisualContent(pageNum) {
			return "image"
		}
	}

	text, err := d.doc.Text(pageNum)
	hasText := err == nil && len(strings.Fields(strings.TrimSpace(text))) >= 3
	textWordCount := 0
	if err == nil {
		words := strings.Fields(strings.TrimSpace(text))
		for _, word := range words {
			if len(word) > 1 {
				textWordCount++
			}
		}
	}
	hasVisual := d.pageHasVisualContent(pageNum)
	if textWordCount >= 50 {
		return "text"
	} else if textWordCount >= 3 && textWordCount < 20 && hasVisual {
		return "mixed"
	} else if textWordCount < 3 && hasVisual {
		return "image"
	} else if hasText {
		return "text"
	} else {
		return "text"
	}
}

func (d *DocumentViewer) highlightSearchMatches(line string) string {
	if d.searchQuery == "" {
		return line
	}
	lowerLine := strings.ToLower(line)
	query := d.searchQuery
	if !strings.Contains(lowerLine, query) {
		return line
	}

	var result strings.Builder
	pos := 0
	for {
		idx := strings.Index(lowerLine[pos:], query)
		if idx < 0 {
			result.WriteString(line[pos:])
			break
		}
		result.WriteString(line[pos : pos+idx])
		result.WriteString("\033[43;30m") // yellow bg, black text
		result.WriteString(line[pos+idx : pos+idx+len(query)])
		result.WriteString("\033[0m") // reset
		pos += idx + len(query)
	}
	return result.String()
}

func (d *DocumentViewer) displayTextPage(pageNum, termWidth, termHeight int) {
	text, err := d.doc.Text(pageNum)
	if err != nil {
		fmt.Printf("Error extracting text: %v\n", err)
		return
	}
	effectiveWidth := termWidth - 3
	reflowedLines := d.reflowText(text, effectiveWidth)
	reserved := 2
	available := termHeight - reserved

	if d.darkMode != "" {
		fmt.Print("\033[38;2;255;255;255m\033[48;2;30;30;30m")
	}

	row := 1
	for i, line := range reflowedLines {
		if row > available {
			break
		}
		fmt.Printf("\033[%d;1H", row)
		if d.darkMode != "" {
			fmt.Printf("\033[K  %s", d.highlightSearchMatches(line))
		} else {
			fmt.Printf("  %s", d.highlightSearchMatches(line))
		}
		row++
		if i == len(reflowedLines)-1 {
			break
		}
	}
	for row <= available {
		fmt.Printf("\033[%d;1H", row)
		if d.darkMode != "" {
			fmt.Print("\033[K")
		} else {
			fmt.Print(strings.Repeat(" ", termWidth))
		}
		row++
	}

	if d.darkMode != "" {
		fmt.Print("\033[0m")
	}
	fmt.Printf("\033[%d;1H", termHeight-1)
	fmt.Print(strings.Repeat(" ", termWidth))
	fmt.Printf("\033[%d;1H", termHeight)
	d.displayPageInfo(pageNum, termWidth, "Text")
}

func (d *DocumentViewer) displayImagePage(pageNum, termWidth, termHeight int) {
	reserved := 2
	verticalPadding := 1
	availableHeight := termHeight - reserved - verticalPadding
	fmt.Print("\033[1;1H")
	fmt.Print("\r\n")
	fmt.Print("\033[2;1H")
	imageHeight := d.renderPageImage(pageNum, termWidth, availableHeight)
	if imageHeight <= 0 {
		fmt.Print("\033[2;1H")
		fmt.Printf("  [Image content - page %d]", pageNum+1)
		fmt.Print("\033[3;1H")
		fmt.Print("  (Image rendering failed)")
		imageHeight = 2
	}
	if d.searchQuery != "" && imageHeight > 0 {
		d.drawSearchMarkers(pageNum, termWidth, verticalPadding, imageHeight)
	}
	for row := imageHeight + 1 + verticalPadding; row <= termHeight-reserved; row++ {
		fmt.Printf("\033[%d;1H", row)
		fmt.Print(strings.Repeat(" ", termWidth))
	}
	fmt.Printf("\033[%d;1H", termHeight)
	d.displayPageInfo(pageNum, termWidth, "Image")
}

func (d *DocumentViewer) displayMixedPage(pageNum, termWidth, termHeight int) {
	reserved := 3
	verticalPadding := 1
	available := termHeight - reserved - verticalPadding
	maxImageHeight := available / 2
	if maxImageHeight > 12 {
		maxImageHeight = 12
	}
	fmt.Print("\033[1;1H")
	fmt.Print("\r\n")
	fmt.Print("\033[2;1H")
	imageHeight := d.renderPageImage(pageNum, termWidth, maxImageHeight)
	if imageHeight <= 0 {
		imageHeight = 0
	}
	currentRow := imageHeight + 1 + verticalPadding
	separatorUsed := 0
	if imageHeight > 0 && available-imageHeight > 2 {
		fmt.Printf("\033[%d;1H", currentRow)
		fmt.Print(strings.Repeat("─", termWidth))
		currentRow++
		separatorUsed = 1
	}
	textAvailable := available - imageHeight - separatorUsed
	if textAvailable > 0 {
		text, err := d.doc.Text(pageNum)
		if err == nil && strings.TrimSpace(text) != "" {
			effectiveWidth := termWidth - 4
			reflowedLines := d.reflowText(text, effectiveWidth)
			textLinesDisplayed := 0
			for i, line := range reflowedLines {
				if textLinesDisplayed >= textAvailable {
					break
				}
				fmt.Printf("\033[%d;1H", currentRow)
				fmt.Printf("  %s", d.highlightSearchMatches(line))
				currentRow++
				textLinesDisplayed++
				if i == len(reflowedLines)-1 {
					break
				}
			}
			for textLinesDisplayed < textAvailable {
				fmt.Printf("\033[%d;1H", currentRow)
				fmt.Print(strings.Repeat(" ", termWidth))
				currentRow++
				textLinesDisplayed++
			}
		} else {
			for i := 0; i < textAvailable; i++ {
				fmt.Printf("\033[%d;1H", currentRow)
				fmt.Print(strings.Repeat(" ", termWidth))
				currentRow++
			}
		}
	}
	fmt.Printf("\033[%d;1H", termHeight-1)
	fmt.Print(strings.Repeat(" ", termWidth))
	fmt.Printf("\033[%d;1H", termHeight)
	d.displayPageInfo(pageNum, termWidth, "Image+Text")
}

func (d *DocumentViewer) drawSearchMarkers(pageNum, termWidth, topPadding, imageHeight int) {
	text, err := d.doc.Text(pageNum)
	if err != nil || strings.TrimSpace(text) == "" {
		return
	}
	lines := strings.Split(text, "\n")
	totalLines := len(lines)
	if totalLines == 0 {
		return
	}

	markerRows := make(map[int]bool)
	query := d.searchQuery
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), query) {
			row := topPadding + 1 + int(float64(i)/float64(totalLines)*float64(imageHeight))
			if row < topPadding+1 {
				row = topPadding + 1
			}
			if row > topPadding+imageHeight {
				row = topPadding + imageHeight
			}
			markerRows[row] = true
		}
	}

	for row := range markerRows {
		fmt.Printf("\033[%d;%dH", row, termWidth)
		fmt.Print("\033[43m \033[0m")
	}
}

// statusIndicators returns the trailing indicator group shared by every status
// bar: fit, zoom, dark mode, crop, search, and the file type. (Ported from
// upstream's refactor; chapter info stays out of this shared helper since only
// the single-page text view has chapters — see displayPageInfo.)
func (d *DocumentViewer) statusIndicators() string {
	fitIndicator := fmt.Sprintf(" [fit:%s]", d.fitMode)
	scaleIndicator := ""
	if d.isReflowable {
		zoomPct := 595 * 100 / d.htmlPageWidth
		scaleIndicator = fmt.Sprintf(" [zoom:%d%%]", zoomPct)
	} else if d.scaleFactor != 1.0 {
		scaleIndicator = fmt.Sprintf(" [%.0f%%]", d.scaleFactor*100)
	}
	darkIndicator := ""
	switch d.darkMode {
	case "smart":
		darkIndicator = " [dark]"
	case "invert":
		darkIndicator = " [dark:inv]"
	}
	cropIndicator := ""
	if d.cropTop > 0 || d.cropBottom > 0 || d.cropLeft > 0 || d.cropRight > 0 {
		cropIndicator = " [crop]"
	}
	searchIndicator := ""
	if d.searchQuery != "" {
		if len(d.searchHits) > 0 {
			searchIndicator = fmt.Sprintf(" [/%s: %d/%d]", d.searchQuery, d.searchHitIdx+1, len(d.searchHits))
		} else {
			searchIndicator = fmt.Sprintf(" [/%s: no matches]", d.searchQuery)
		}
	}
	return fmt.Sprintf("%s%s%s%s%s - %s",
		fitIndicator, scaleIndicator, darkIndicator, cropIndicator, searchIndicator,
		strings.ToUpper(d.fileType))
}

// drawStatusBar writes the centered status line at the cursor: the file name,
// then the caller's position and mode text. When the line does not fit, the
// name is elided first — where you are in the file matters more than seeing the
// whole name. Widths count runes, since file names need not be ASCII.
func (d *DocumentViewer) drawStatusBar(termWidth int, body string) {
	if termWidth <= 0 {
		return
	}
	line := body
	if name := filepath.Base(d.path); name != "." && name != string(filepath.Separator) {
		if room := termWidth - utf8.RuneCountInString(body) - 3; room >= 4 {
			if r := []rune(name); len(r) > room {
				name = string(r[:room-1]) + "…"
			}
			line = name + " · " + body
		}
	}
	n := utf8.RuneCountInString(line)
	if n > termWidth {
		line = string([]rune(line)[:termWidth])
	} else {
		line = strings.Repeat(" ", (termWidth-n)/2) + line
	}
	fmt.Print(line)
}

func (d *DocumentViewer) displayPageInfo(pageNum, termWidth int, contentType string) {
	modeIndicator := ""
	if d.forceMode != "" {
		modeIndicator = fmt.Sprintf(" [%s]", d.forceMode)
	}
	// Chapter indicator is fork-specific (epub support) and has no place in
	// the shared statusIndicators() helper, so it's built here and spliced in
	// right after the mode indicator.
	chapterIndicator := ""
	if len(d.chapters) > 0 {
		d.updateCurrentChapter()
		ch := d.chapters[d.currentChapter]
		title := ch.Title
		if len(title) > 30 {
			title = title[:27] + "..."
		}
		chapterIndicator = fmt.Sprintf(" [Ch %d/%d: %s]", d.currentChapter+1, len(d.chapters), title)
	}
	d.drawStatusBar(termWidth, fmt.Sprintf("Page %d/%d (%s)%s%s%s",
		d.currentPage+1, len(d.textPages), contentType, modeIndicator, chapterIndicator, d.statusIndicators()))
}

// halfPageViewHeight is the number of rows the half-page image may use: the
// terminal minus the status bar. rollHalfToSyncPoint must agree with
// displayHalfPage on this, because the render DPI — and hence which band of the
// page each half shows — is derived from it.
func halfPageViewHeight(termHeight int) int {
	if termHeight <= 1 {
		return termHeight
	}
	return termHeight - 1
}

func (d *DocumentViewer) displayHalfPage(termWidth, termHeight int) {
	pageNum := d.textPages[d.currentPage]
	availableHeight := halfPageViewHeight(termHeight)
	isBottom := d.halfPageOffset == 1

	fmt.Print("\033[1;1H")
	imgHeight := d.renderHalfPage(pageNum, termWidth, availableHeight, isBottom)
	if imgHeight <= 0 {
		fmt.Print("\033[1;1H")
		fmt.Printf("  [Render failed]")
	}

	// Status bar
	fmt.Printf("\033[%d;1H", termHeight)
	half := "top"
	if isBottom {
		half = "bottom"
	}
	d.drawStatusBar(termWidth, fmt.Sprintf("Page %d/%d (%s half) [half]%s",
		d.currentPage+1, len(d.textPages), half, d.statusIndicators()))
}

func (d *DocumentViewer) reflowText(text string, termWidth int) []string {
	if termWidth <= 0 {
		termWidth = 80
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if d.fileType == "epub" {
		text = d.cleanEpubText(text)
	}
	lines := strings.Split(text, "\n")
	hasShortLines := false
	shortLineCount := 0
	for _, line := range lines {
		if len(strings.TrimSpace(line)) > 0 && len(strings.TrimSpace(line)) < termWidth/2 {
			shortLineCount++
		}
	}
	if float64(shortLineCount)/float64(len(lines)) > 0.3 {
		hasShortLines = true
	}
	var reflowedLines []string
	if hasShortLines {
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				reflowedLines = append(reflowedLines, "")
				continue
			}
			if len(trimmed) > termWidth {
				wrapped := d.wrapText(trimmed, termWidth)
				reflowedLines = append(reflowedLines, wrapped...)
			} else {
				reflowedLines = append(reflowedLines, trimmed)
			}
		}
	} else {
		paragraphs := strings.Split(text, "\n\n")
		for _, paragraph := range paragraphs {
			if strings.TrimSpace(paragraph) == "" {
				reflowedLines = append(reflowedLines, "")
				continue
			}
			cleanParagraph := strings.ReplaceAll(paragraph, "\n", " ")
			cleanParagraph = d.normalizeWhitespace(cleanParagraph)
			if strings.TrimSpace(cleanParagraph) == "" {
				continue
			}
			wrappedLines := d.wrapText(cleanParagraph, termWidth)
			reflowedLines = append(reflowedLines, wrappedLines...)
			reflowedLines = append(reflowedLines, "")
		}
	}
	for len(reflowedLines) > 0 && reflowedLines[len(reflowedLines)-1] == "" {
		reflowedLines = reflowedLines[:len(reflowedLines)-1]
	}
	return reflowedLines
}

func (d *DocumentViewer) cleanEpubText(text string) string {
	replacements := map[string]string{
		"&nbsp;":  " ",
		"&amp;":   "&",
		"&lt;":    "<",
		"&gt;":    ">",
		"&quot;":  "\"",
		"&apos;":  "'",
		"&#8217;": "'",
		"&#8220;": "\"",
		"&#8221;": "\"",
		"&#8230;": "...",
		"&#8212;": "—",
		"&#8211;": "–",
	}
	for entity, replacement := range replacements {
		text = strings.ReplaceAll(text, entity, replacement)
	}
	return text
}

func (d *DocumentViewer) normalizeWhitespace(text string) string {
	var result strings.Builder
	var lastWasSpace bool
	for _, r := range text {
		if unicode.IsSpace(r) {
			if !lastWasSpace {
				result.WriteRune(' ')
				lastWasSpace = true
			}
		} else {
			result.WriteRune(r)
			lastWasSpace = false
		}
	}
	return strings.TrimSpace(result.String())
}

func (d *DocumentViewer) wrapText(text string, width int) []string {
	if width <= 0 {
		width = 80
	}
	if width < 20 {
		width = 20
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	var currentLine strings.Builder
	for _, word := range words {
		if len(word) > width {
			if currentLine.Len() > 0 {
				lines = append(lines, currentLine.String())
				currentLine.Reset()
			}
			for len(word) > width {
				lines = append(lines, word[:width])
				word = word[width:]
			}
			if len(word) > 0 {
				currentLine.WriteString(word)
			}
			continue
		}
		proposedLength := currentLine.Len()
		if proposedLength > 0 {
			proposedLength += 1
		}
		proposedLength += len(word)
		if proposedLength <= width {
			if currentLine.Len() > 0 {
				currentLine.WriteString(" ")
			}
			currentLine.WriteString(word)
		} else {
			if currentLine.Len() > 0 {
				lines = append(lines, currentLine.String())
				currentLine.Reset()
			}
			currentLine.WriteString(word)
		}
	}
	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}
	return lines
}

func (d *DocumentViewer) displayDualPage(termWidth, termHeight int) {
	page1 := d.textPages[d.currentPage]
	hasPage2 := d.currentPage+1 < len(d.textPages)

	reserved := 2

	if d.dualPageMode == "vertical" {
		d.displayDualVertical(page1, hasPage2, termWidth, termHeight, reserved)
	} else {
		d.displayDualHorizontal(page1, hasPage2, termWidth, termHeight, reserved)
	}
}

func (d *DocumentViewer) displayDualVertical(page1 int, hasPage2 bool, termWidth, termHeight, reserved int) {
	availableHeight := termHeight - reserved

	fmt.Print("\033[1;1H")
	var page2 int
	if hasPage2 {
		page2 = d.textPages[d.currentPage+1]
	}
	imgHeight := d.renderDualComposite(page1, page2, hasPage2, termWidth, availableHeight, "vertical", 1)
	if imgHeight <= 0 {
		fmt.Print("\033[1;1H")
		fmt.Printf("  [Render failed]")
	}

	fmt.Printf("\033[%d;1H", termHeight)
	d.displayDualPageInfo(hasPage2, termWidth, "2pg-v")
}

func (d *DocumentViewer) displayDualHorizontal(page1 int, hasPage2 bool, termWidth, termHeight, reserved int) {
	availableHeight := termHeight - reserved

	fmt.Print("\033[1;1H")
	var page2 int
	if hasPage2 {
		page2 = d.textPages[d.currentPage+1]
	}
	imgHeight := d.renderDualComposite(page1, page2, hasPage2, termWidth, availableHeight, "horizontal", 1)
	if imgHeight <= 0 {
		fmt.Print("\033[1;1H")
		fmt.Printf("  [Render failed]")
	}

	fmt.Printf("\033[%d;1H", termHeight)
	d.displayDualPageInfo(hasPage2, termWidth, "2pg-h")
}

func (d *DocumentViewer) displayDualPageInfo(hasPage2 bool, termWidth int, modeLabel string) {
	page1Num := d.currentPage + 1
	page2Num := page1Num + 1
	totalPages := len(d.textPages)

	var pageRange string
	if hasPage2 {
		pageRange = fmt.Sprintf("Pages %d-%d/%d", page1Num, page2Num, totalPages)
	} else {
		pageRange = fmt.Sprintf("Page %d/%d", page1Num, totalPages)
	}

	d.drawStatusBar(termWidth, fmt.Sprintf("%s (Image) [%s]%s", pageRange, modeLabel, d.statusIndicators()))
}
