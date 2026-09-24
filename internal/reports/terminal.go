package reports

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

type terminalRow struct {
	cells []string
	color int
}

// Widths include the space on each side of a cell, matching ccusage-terminal.
// User text is sanitized before reaching this renderer; colors are added only
// after wrapping, so ANSI sequences cannot affect measurement or leak styles.
type terminalTable struct {
	headers      []string
	textColumns  int
	rows         []terminalRow
	width        int
	color        bool
	compactDates bool
}

func (t terminalTable) render() string {
	widths := t.columnWidths()
	var out strings.Builder
	border := func(left, middle, right string) {
		out.WriteString(left)
		for i, width := range widths {
			out.WriteString(strings.Repeat("─", width))
			if i+1 == len(widths) {
				out.WriteString(right)
			} else {
				out.WriteString(middle)
			}
		}
		out.WriteByte('\n')
	}
	writeRow := func(row terminalRow, dates bool) {
		cells := make([][]string, len(widths))
		height := 1
		for i, value := range row.cells {
			if dates && i == 0 && widths[0]-2 < 10 {
				if _, err := time.Parse("2006-01-02", value); err == nil {
					value = value[:4] + "\n" + value[5:]
				}
			}
			cells[i] = wrapCell(value, widths[i]-2)
			height = max(height, len(cells[i]))
		}
		for line := 0; line < height; line++ {
			out.WriteString("│")
			for i, width := range widths {
				value := ""
				if line < len(cells[i]) {
					value = cells[i][line]
				}
				padding := strings.Repeat(" ", max(0, width-2-ansi.StringWidth(value)))
				out.WriteByte(' ')
				if i >= t.textColumns {
					out.WriteString(padding)
				}
				out.WriteString(terminalColor(value, row.color, t.color))
				if i < t.textColumns {
					out.WriteString(padding)
				}
				out.WriteString(" │")
			}
			out.WriteByte('\n')
		}
	}
	border("┌", "┬", "┐")
	writeRow(terminalRow{cells: t.headers, color: 34}, false)
	for _, row := range t.rows {
		border("├", "┼", "┤")
		writeRow(row, t.compactDates)
	}
	border("└", "┴", "┘")
	return out.String()
}

func (t terminalTable) columnWidths() []int {
	limit := t.width
	if limit <= 0 {
		limit = 120
	}
	modelColumn := -1
	widths, contents := make([]int, len(t.headers)), make([]int, len(t.headers))
	for i, header := range t.headers {
		if header == "Models" {
			modelColumn = i
		}
		for _, row := range t.rows {
			contents[i] = max(contents[i], maxLineWidth(row.cells[i]))
		}
		width := max(maxLineWidth(header), contents[i])
		switch {
		case i == modelColumn:
			widths[i] = min(27, max(15, width+2))
		case i >= t.textColumns:
			widths[i] = max(11, width+3)
		case i == 1:
			widths[i] = max(15, width+2)
		default:
			widths[i] = max(10, width+2)
		}
	}
	if tableWidth(widths) <= limit {
		return widths
	}
	fallback, minimums := make([]int, len(widths)), make([]int, len(widths))
	for i := range widths {
		switch {
		case i >= t.textColumns || i == 0:
			fallback[i] = 10
		case i == modelColumn || modelColumn < 0 && i == 1:
			fallback[i] = 12
		default:
			fallback[i] = min(12, max(8, contents[i]+2))
		}
		minimums[i] = fallback[i]
		if i >= t.textColumns {
			minimums[i] = max(minimums[i], contents[i]+2)
		}
	}
	// Preserve numeric values first; give up text width before truncating numbers.
	for tableWidth(minimums) > limit {
		candidate := -1
		for i := 0; i < t.textColumns; i++ {
			if minimums[i] <= 8 {
				continue
			}
			priority := i == modelColumn || modelColumn < 0 && i == 1
			bestPriority := candidate == modelColumn || modelColumn < 0 && candidate == 1
			if candidate < 0 || priority && !bestPriority || priority == bestPriority && minimums[i] >= minimums[candidate] {
				candidate = i
			}
		}
		if candidate < 0 {
			break
		}
		minimums[candidate]--
	}
	natural := append([]int(nil), widths...)
	if t.compactDates {
		natural[0] = 10
	}
	if tableWidth(minimums) <= limit {
		return expandWidths(minimums, natural, limit)
	}
	available := max(0, limit-len(widths)-1)
	total := tableWidth(widths) - len(widths) - 1
	for i, width := range widths {
		widths[i] = max(fallback[i], int(float64(width)*float64(available)/float64(total)))
	}
	for tableWidth(widths) > limit {
		candidate := -1
		for i, width := range widths {
			if width <= fallback[i] {
				continue
			}
			if candidate < 0 || i < t.textColumns && candidate >= t.textColumns || (i < t.textColumns) == (candidate < t.textColumns) && width >= widths[candidate] {
				candidate = i
			}
		}
		if candidate < 0 {
			break // Like ccusage, retain readable minimums on very narrow screens.
		}
		widths[candidate]--
	}
	return expandWidths(widths, natural, limit)
}

func tableWidth(widths []int) int {
	total := len(widths) + 1
	for _, width := range widths {
		total += width
	}
	return total
}

func expandWidths(widths, natural []int, limit int) []int {
	for spare := limit - tableWidth(widths); spare > 0; {
		changed := false
		for i := range widths {
			if spare > 0 && widths[i] < natural[i] {
				widths[i]++
				spare--
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return widths
}

func maxLineWidth(value string) int {
	width := 0
	for _, line := range strings.Split(value, "\n") {
		width = max(width, ansi.StringWidth(line))
	}
	return width
}

func wrapCell(value string, width int) []string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		if ansi.StringWidth(line) <= width {
			lines = append(lines, line)
			continue
		}
		words := strings.Fields(line)
		// Keep the bullet attached to its model, even when the name is shortened.
		for i := 0; i+1 < len(words); i++ {
			if words[i] == "-" || words[i] == "└─" {
				words[i] += " " + words[i+1]
				words = append(words[:i+1], words[i+2:]...)
			}
		}
		current := ""
		for _, word := range words {
			if current != "" && ansi.StringWidth(current)+1+ansi.StringWidth(word) > width {
				lines = append(lines, current)
				current = ""
			}
			if current != "" {
				current += " "
			}
			current += ansi.Truncate(word, width, "…")
		}
		lines = append(lines, current)
	}
	return lines
}

func terminalColor(value string, code int, enabled bool) string {
	if !enabled || code == 0 || value == "" {
		return value
	}
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", code, value)
}

func boxTitle(title string, color bool, limit int) string {
	if limit <= 0 {
		limit = 120
	}
	width := min(max(40, maxLineWidth(title)), max(1, limit-6))
	lines := wrapCell(title, width)
	var out strings.Builder
	out.WriteString("\n╭" + strings.Repeat("─", width+4) + "╮\n")
	out.WriteString("│" + strings.Repeat(" ", width+4) + "│\n")
	for _, line := range lines {
		padding := width + 2 - ansi.StringWidth(line)
		out.WriteString("│ " + strings.Repeat(" ", padding/2) + terminalColor(line, 34, color) + strings.Repeat(" ", padding-padding/2) + " │\n")
	}
	out.WriteString("│" + strings.Repeat(" ", width+4) + "│\n")
	out.WriteString("╰" + strings.Repeat("─", width+4) + "╯\n\n")
	return out.String()
}
