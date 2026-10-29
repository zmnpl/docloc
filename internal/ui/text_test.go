package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

func TestWrap(t *testing.T) {
	got := wrap("the quick brown fox jumps", 10)
	want := []string{"the quick ", "brown fox ", "jumps"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrap = %q, want %q", got, want)
	}
	for _, seg := range wrap("Grüße aus 東京 und überall", 7) {
		if w := runewidth.StringWidth(seg); w > 7 {
			t.Errorf("segment %q is %d cells wide", seg, w)
		}
	}
	if got := wrap("", 10); len(got) != 1 || got[0] != "" {
		t.Errorf("wrap of empty line = %q", got)
	}
}

func TestHighlight(t *testing.T) {
	mark := lipgloss.NewStyle().Transform(func(s string) string { return "[" + s + "]" })
	plain := lipgloss.NewStyle()
	got := highlight("Rechnung und rechnungen", []string{"rechnung"}, plain, mark)
	if want := "[Rechnung] und [rechnung]en"; !strings.Contains(stripANSI(got), want) {
		t.Errorf("highlight = %q, want %q", got, want)
	}
}

func TestTruncateLeft(t *testing.T) {
	if got := truncateLeft("a/b/c/document.pdf", 12); got != "…ocument.pdf" {
		t.Errorf("got %q", got)
	}
	if got := truncateLeft("short.pdf", 20); got != "short.pdf" {
		t.Errorf("got %q", got)
	}
}

func TestExpandTabs(t *testing.T) {
	if got := expandTabs("a\tbc\td"); got != "a   bc  d" {
		t.Errorf("got %q", got)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}
