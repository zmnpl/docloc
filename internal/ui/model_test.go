package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/simonpaul/docloc/internal/index"
)

func TestJumpMatch(t *testing.T) {
	text := strings.Join([]string{
		"intro", "first Rechnung", "filler", "filler", "second rechnung", "filler", "third RECHNUNG",
	}, "\n")
	store := index.NewStore()
	store.Add(index.NewDoc("a.txt", "/a.txt", 0, text, ""))
	m := New(Options{Store: store, Indexer: &index.Indexer{Store: store}})
	res, err := store.Search(context.Background(), "rechnung", index.Exact, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.applyResult(res)

	if m.pv.match != 1 {
		t.Fatalf("initial match on line %d, want 1", m.pv.match)
	}
	var lines []int
	for range 4 {
		m.jumpMatch(1)
		lines = append(lines, m.pv.match)
	}
	if want := []int{4, 6, 1, 4}; !equalInts(lines, want) {
		t.Errorf("next jumps visited %v, want %v", lines, want)
	}
	m.jumpMatch(-1)
	m.jumpMatch(-1)
	if m.pv.match != 6 { // 4 -> 1 -> wraps to 6
		t.Errorf("previous jumps ended on line %d, want 6", m.pv.match)
	}
	if got := m.matchIndex(); got != 3 {
		t.Errorf("matchIndex = %d, want 3", got)
	}
}

func equalInts(a, b []int) bool {
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
