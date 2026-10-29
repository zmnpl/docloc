package ui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const tabWidth = 4

// expandTabs replaces tabs with spaces up to the next tab stop.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += runewidth.RuneWidth(r)
	}
	return b.String()
}

// wrap breaks s into segments of at most width cells, preferring to break
// after spaces.
func wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	if runewidth.StringWidth(s) <= width {
		return []string{s}
	}
	var out []string
	runes := []rune(s)
	for len(runes) > 0 {
		w, cut, lastSpace := 0, 0, -1
		for cut < len(runes) {
			rw := runewidth.RuneWidth(runes[cut])
			if w+rw > width {
				break
			}
			w += rw
			if runes[cut] == ' ' {
				lastSpace = cut
			}
			cut++
		}
		if cut == 0 { // a rune wider than the whole line
			cut = 1
		} else if cut < len(runes) && lastSpace > cut/2 {
			cut = lastSpace + 1
		}
		out = append(out, string(runes[:cut]))
		runes = runes[cut:]
	}
	return out
}

// highlight renders s with every case-insensitive occurrence of a needle in
// the match style and the rest in the base style.
func highlight(s string, needles []string, base, match lipgloss.Style) string {
	if len(needles) == 0 || s == "" {
		return base.Render(s)
	}
	lower := strings.ToLower(s)
	if len(lower) != len(s) { // byte offsets would not line up
		return base.Render(s)
	}
	var spans [][2]int
	for _, n := range needles {
		if n == "" {
			continue
		}
		for off := 0; ; {
			i := strings.Index(lower[off:], n)
			if i < 0 {
				break
			}
			spans = append(spans, [2]int{off + i, off + i + len(n)})
			off += i + len(n)
		}
	}
	if len(spans) == 0 {
		return base.Render(s)
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var b strings.Builder
	pos := 0
	for _, sp := range spans {
		if sp[1] <= pos {
			continue
		}
		start := max(sp[0], pos)
		if start > pos {
			b.WriteString(base.Render(s[pos:start]))
		}
		b.WriteString(match.Render(s[start:sp[1]]))
		pos = sp[1]
	}
	if pos < len(s) {
		b.WriteString(base.Render(s[pos:]))
	}
	return b.String()
}

// containsAny reports whether the lowercase form of s contains a needle.
func containsAny(s string, needles []string) bool {
	if len(needles) == 0 {
		return false
	}
	lower := strings.ToLower(s)
	for _, n := range needles {
		if n != "" && strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

// truncateLeft shortens s to width cells by cutting from the left.
func truncateLeft(s string, width int) string {
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width <= 1 {
		return runewidth.Truncate(s, width, "")
	}
	runes := []rune(s)
	w := 0
	i := len(runes)
	for i > 0 {
		rw := runewidth.RuneWidth(runes[i-1])
		if w+rw > width-1 {
			break
		}
		w += rw
		i--
	}
	return "…" + string(runes[i:])
}
