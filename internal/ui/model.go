// Package ui implements the interactive search interface.
package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/simonpaul/docloc/internal/index"
)

const tickInterval = 150 * time.Millisecond

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type Options struct {
	Store   *index.Store
	Indexer *index.Indexer
	Mode    index.Mode
	// PrintOnly makes enter print the selected path instead of opening it.
	PrintOnly bool
	// Warning is shown in the status line, e.g. when the cache is unavailable.
	Warning string
}

type Model struct {
	opts  Options
	store *index.Store
	ix    *index.Indexer
	input textinput.Model
	mode  index.Mode

	res         *index.Result
	gen         int
	cancel      context.CancelFunc
	searching   bool
	stale       bool // the store changed while a search was running
	resetCursor bool // the next result belongs to a new query
	searchedLen int  // store size the latest search was started with

	cursor, offset int
	selected       int32 // doc id under the cursor, -1 if none

	pv preview

	width, height int
	frame         int
	flash         string // transient message, e.g. errors when opening
	chosen        string // path to print on exit in PrintOnly mode
}

type preview struct {
	doc       int32
	lines     []string
	scroll    int
	highlight []string
}

type tickMsg struct{}

type searchMsg struct {
	gen int
	res *index.Result
	err error
}

type openMsg struct {
	err  error
	quit bool
}

func New(opts Options) *Model {
	in := textinput.New()
	in.Prompt = "❯ "
	in.Placeholder = "type to search document names and contents"
	in.PromptStyle = promptStyle
	in.Focus()
	return &Model{
		opts:     opts,
		store:    opts.Store,
		ix:       opts.Indexer,
		input:    in,
		mode:     opts.Mode,
		selected: -1,
		pv:       preview{doc: -1},
	}
}

// Chosen returns the path selected in PrintOnly mode, if any.
func (m *Model) Chosen() string { return m.chosen }

func (m *Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.tick(), m.startSearch())
}

func (m *Model) tick() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// startSearch cancels a running search and starts one for the current input.
func (m *Model) startSearch() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.gen++
	m.searching = true
	m.stale = false
	m.searchedLen = m.store.Len()
	gen, q, mode, prev, store := m.gen, m.input.Value(), m.mode, m.res, m.store
	return func() tea.Msg {
		res, err := store.Search(ctx, q, mode, prev)
		return searchMsg{gen, res, err}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = max(10, m.width-len(m.input.Prompt)-12)
		m.clampOffset()
		return m, nil

	case tickMsg:
		m.frame++
		var cmds []tea.Cmd
		if m.store.Len() != m.searchedLen {
			if m.searching {
				m.stale = true
			} else {
				cmds = append(cmds, m.startSearch())
			}
		}
		if !m.ix.Progress().Finished || m.store.Len() != m.searchedLen {
			cmds = append(cmds, m.tick())
		}
		return m, tea.Batch(cmds...)

	case searchMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.searching = false
		if msg.err == nil {
			m.applyResult(msg.res)
		}
		if m.stale {
			return m, m.startSearch()
		}
		return m, nil

	case openMsg:
		if msg.err != nil {
			m.flash = "could not open: " + msg.err.Error()
			return m, nil
		}
		if msg.quit {
			return m, tea.Quit
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.flash = ""
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.input.Value() != "" {
			m.input.SetValue("")
			m.resetCursor = true
			return m, m.startSearch()
		}
		return m, tea.Quit
	case "up", "ctrl+p":
		m.moveCursor(-1)
		return m, nil
	case "down", "ctrl+n":
		m.moveCursor(1)
		return m, nil
	case "pgup":
		m.moveCursor(-max(1, m.listHeight()-1))
		return m, nil
	case "pgdown":
		m.moveCursor(max(1, m.listHeight()-1))
		return m, nil
	case "ctrl+u":
		m.scrollPreview(-max(1, m.previewHeight()/2))
		return m, nil
	case "ctrl+d":
		m.scrollPreview(max(1, m.previewHeight()/2))
		return m, nil
	case "shift+up", "alt+up":
		m.scrollPreview(-1)
		return m, nil
	case "shift+down", "alt+down":
		m.scrollPreview(1)
		return m, nil
	case "tab":
		if m.mode == index.Exact {
			m.mode = index.Fuzzy
		} else {
			m.mode = index.Exact
		}
		m.resetCursor = true
		return m, m.startSearch()
	case "enter":
		return m, m.open(true)
	case "ctrl+o":
		return m, m.open(false)
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.resetCursor = true
		return m, tea.Batch(cmd, m.startSearch())
	}
	return m, cmd
}

func (m *Model) open(quit bool) tea.Cmd {
	d := m.store.Doc(m.selected)
	if d == nil {
		return nil
	}
	if m.opts.PrintOnly {
		m.chosen = d.Abs
		return tea.Quit
	}
	path := d.Abs
	return func() tea.Msg {
		return openMsg{err: openFile(path), quit: quit}
	}
}

func (m *Model) applyResult(res *index.Result) {
	m.res = res
	cursor := 0
	if !m.resetCursor && m.selected >= 0 {
		for i, h := range res.Hits {
			if h.Doc == m.selected {
				cursor = i
				break
			}
		}
	}
	m.resetCursor = false
	m.cursor = cursor
	m.syncSelection()
}

func (m *Model) moveCursor(delta int) {
	if m.res == nil || len(m.res.Hits) == 0 {
		return
	}
	m.cursor = min(max(m.cursor+delta, 0), len(m.res.Hits)-1)
	m.syncSelection()
}

// syncSelection updates the selected document, list scroll offset and
// preview after the cursor or the results changed.
func (m *Model) syncSelection() {
	if m.res == nil || len(m.res.Hits) == 0 {
		m.cursor, m.selected = 0, -1
	} else {
		m.cursor = min(m.cursor, len(m.res.Hits)-1)
		m.selected = m.res.Hits[m.cursor].Doc
	}
	m.clampOffset()

	var hl []string
	if m.res != nil {
		hl = m.res.Highlight
	}
	if m.pv.doc == m.selected && equalStrings(m.pv.highlight, hl) {
		return
	}
	if m.pv.doc != m.selected {
		m.pv = preview{doc: m.selected}
		if d := m.store.Doc(m.selected); d != nil {
			m.pv.lines = strings.Split(d.Text, "\n")
		}
	}
	m.pv.highlight = hl
	m.pv.scroll = 0
	for i, l := range m.pv.lines {
		if containsAny(l, hl) {
			m.pv.scroll = max(0, i-2)
			break
		}
	}
}

func (m *Model) clampOffset() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(0, m.offset)
}

func (m *Model) scrollPreview(delta int) {
	m.pv.scroll = min(max(m.pv.scroll+delta, 0), max(0, len(m.pv.lines)-1))
}

// Layout: input, status, list, preview header, preview body, help.
func (m *Model) listHeight() int {
	avail := m.height - 4
	return max(1, avail*2/5)
}

func (m *Model) previewHeight() int {
	return max(0, m.height-4-m.listHeight())
}

func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(m.viewInput())
	b.WriteByte('\n')
	b.WriteString(m.viewStatus())
	b.WriteByte('\n')
	b.WriteString(m.viewList())
	b.WriteString(m.viewPreview())
	b.WriteString(m.viewHelp())
	return b.String()
}

func (m *Model) viewInput() string {
	badge := modeStyle.Render(" " + m.mode.String() + " ")
	in := m.input.View()
	gap := m.width - lipgloss.Width(in) - lipgloss.Width(badge)
	if gap < 1 {
		return in
	}
	return in + strings.Repeat(" ", gap) + badge
}

// viewStatus renders the counts and indexing progress as a thin rule,
// matching the preview header.
func (m *Model) viewStatus() string {
	total := m.store.Len()
	hits := 0
	if m.res != nil {
		hits = len(m.res.Hits)
	}
	s := borderStyle.Render("──") + statusStyle.Render(fmt.Sprintf(" %d/%d ", hits, total))
	p := m.ix.Progress()
	switch {
	case p.Walking:
		s += statusStyle.Render(fmt.Sprintf(" %s scanning… %d files found ", spinner[m.frame%len(spinner)], p.Found))
	case !p.Finished:
		s += statusStyle.Render(fmt.Sprintf(" %s indexing %d/%d ", spinner[m.frame%len(spinner)], p.Done, p.Found))
	}
	if m.searching {
		s += statusStyle.Render(" searching… ")
	}
	if m.opts.Warning != "" {
		s += " " + warnStyle.Render(m.opts.Warning) + " "
	}
	if m.flash != "" {
		s += " " + warnStyle.Render(m.flash) + " "
	}
	if fill := m.width - lipgloss.Width(s); fill > 0 {
		s += borderStyle.Render(strings.Repeat("─", fill))
	}
	return truncateRight(s, m.width)
}

func (m *Model) viewList() string {
	h := m.listHeight()
	var b strings.Builder
	n := 0
	if m.res != nil {
		for i := m.offset; i < len(m.res.Hits) && n < h; i, n = i+1, n+1 {
			d := m.store.Doc(m.res.Hits[i].Doc)
			b.WriteString(m.listRow(d, i == m.cursor))
			b.WriteByte('\n')
		}
	}
	if n == 0 && h > 0 {
		msg := "no matching documents"
		if !m.ix.Progress().Finished {
			msg = "nothing found yet, still indexing…"
		}
		b.WriteString(dimStyle.Render("  " + msg))
		b.WriteByte('\n')
		n++
	}
	for ; n < h; n++ {
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *Model) listRow(d *index.Doc, selected bool) string {
	rel := filepath.ToSlash(d.Rel)
	w := m.width - 2
	if selected {
		line := truncateLeft(rel, w)
		pad := max(0, w-lipgloss.Width(line))
		return cursorStyle.Render("▌ ") + selectedStyle.Render(line+strings.Repeat(" ", pad))
	}
	dir, name := "", rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		dir, name = rel[:i+1], rel[i+1:]
	}
	if lipgloss.Width(name) >= w {
		return "  " + nameStyle.Render(truncateLeft(name, w))
	}
	dir = truncateLeft(dir, w-lipgloss.Width(name))
	return "  " + dirStyle.Render(dir) + nameStyle.Render(name)
}

func (m *Model) viewPreview() string {
	h := m.previewHeight()
	if h <= 0 {
		return ""
	}
	d := m.store.Doc(m.selected)

	title := ""
	pos := ""
	if d != nil {
		title = " " + filepath.ToSlash(d.Rel) + " "
		if len(m.pv.lines) > 0 {
			pos = fmt.Sprintf(" %d/%d ", m.pv.scroll+1, len(m.pv.lines))
		}
	}
	title = truncateLeft(title, max(0, m.width-lipgloss.Width(pos)-4))
	fill := max(0, m.width-2-lipgloss.Width(title)-lipgloss.Width(pos))
	header := borderStyle.Render("──") + previewTitleStyle.Render(title) +
		borderStyle.Render(strings.Repeat("─", fill)) + dimStyle.Render(pos)

	var rows []string
	switch {
	case d == nil:
	case strings.TrimSpace(d.Text) == "":
		note := "no text could be extracted (scanned document without a text layer?)"
		if d.Note != "" {
			note = "no text could be extracted: " + d.Note
		}
		for _, l := range wrap(note, m.width-2) {
			rows = append(rows, " "+dimStyle.Render(l))
		}
	default:
		width := max(1, m.width-1)
		for i := m.pv.scroll; i < len(m.pv.lines) && len(rows) < h; i++ {
			for _, seg := range wrap(expandTabs(m.pv.lines[i]), width) {
				if len(rows) == h {
					break
				}
				rows = append(rows, " "+highlight(seg, m.pv.highlight, textStyle, matchStyle))
			}
		}
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	for i := 0; i < h; i++ {
		if i < len(rows) {
			b.WriteString(rows[i])
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (m *Model) viewHelp() string {
	verb := "open"
	if m.opts.PrintOnly {
		verb = "print"
	}
	keys := []string{
		"↑/↓ select", "enter " + verb, "tab exact/fuzzy", "ctrl+u/d scroll preview", "esc clear/quit",
	}
	if !m.opts.PrintOnly {
		keys = append(keys[:2], append([]string{"ctrl+o open & stay"}, keys[2:]...)...)
	}
	return helpStyle.Render(truncateRight(" "+strings.Join(keys, " · "), m.width))
}

// truncateRight shortens a possibly styled string to width cells.
func truncateRight(s string, width int) string {
	return ansi.Truncate(s, width, "…")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
