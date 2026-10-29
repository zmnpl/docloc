package index

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"
)

type Mode int

const (
	Exact Mode = iota // every term must occur as a substring
	Fuzzy             // terms may match words approximately
)

func (m Mode) String() string {
	if m == Fuzzy {
		return "fuzzy"
	}
	return "exact"
}

type Hit struct {
	Doc   int32
	Score int
}

// Result is the outcome of one search.
type Result struct {
	Query string
	Mode  Mode
	Hits  []Hit
	// Searched is the number of documents the search covered; documents
	// added later have ids >= Searched.
	Searched int
	// Highlight lists lowercase strings worth highlighting in previews.
	Highlight []string
}

type query struct {
	pos, neg []string
}

// parseQuery splits the input into lowercase terms; a leading '!' negates a
// term (like fzf's extended syntax).
func parseQuery(q string) query {
	var out query
	for _, f := range strings.Fields(strings.ToLower(q)) {
		if strings.HasPrefix(f, "!") {
			if len(f) > 1 {
				out.neg = append(out.neg, f[1:])
			}
			continue
		}
		out.pos = append(out.pos, f)
	}
	return out
}

const cancelCheckEvery = 256

// Search runs q against all documents. prev may be the result of an earlier
// search; when q only narrows it down, just its hits and newer documents are
// examined. A cancelled ctx returns ctx.Err().
func (s *Store) Search(ctx context.Context, q string, mode Mode, prev *Result) (*Result, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pq := parseQuery(q)
	res := &Result{Query: q, Mode: mode, Searched: len(s.docs)}

	if len(pq.pos) == 0 && len(pq.neg) == 0 {
		// Without a query, show the most recently modified documents first.
		res.Hits = make([]Hit, len(s.docs))
		for i := range s.docs {
			res.Hits[i] = Hit{Doc: int32(i)}
		}
		sort.Slice(res.Hits, func(i, j int) bool {
			a, b := s.docs[res.Hits[i].Doc], s.docs[res.Hits[j].Doc]
			if a.ModTime != b.ModTime {
				return a.ModTime > b.ModTime
			}
			return a.lowRel < b.lowRel
		})
		return res, nil
	}

	var hits []Hit
	var err error
	if mode == Fuzzy {
		hits, res.Highlight, err = s.fuzzySearch(ctx, pq)
	} else {
		res.Highlight = pq.pos
		hits, err = s.exactSearch(ctx, pq, s.candidates(q, mode, pq, prev))
	}
	if err != nil {
		return nil, err
	}
	s.sortHits(hits)
	res.Hits = hits
	return res, nil
}

// candidates returns the doc ids worth testing, or nil for all documents.
func (s *Store) candidates(q string, mode Mode, pq query, prev *Result) []int32 {
	if prev == nil || prev.Mode != mode || len(pq.neg) > 0 || strings.TrimSpace(prev.Query) == "" ||
		!strings.HasPrefix(q, prev.Query) || len(parseQuery(prev.Query).neg) > 0 {
		return nil
	}
	ids := make([]int32, 0, len(prev.Hits)+len(s.docs)-prev.Searched)
	for _, h := range prev.Hits {
		ids = append(ids, h.Doc)
	}
	for i := prev.Searched; i < len(s.docs); i++ {
		ids = append(ids, int32(i))
	}
	return ids
}

func (s *Store) exactSearch(ctx context.Context, pq query, cand []int32) ([]Hit, error) {
	n := len(s.docs)
	if cand != nil {
		n = len(cand)
	}
	var hits []Hit
	for i := 0; i < n; i++ {
		if i%cancelCheckEvery == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		id := int32(i)
		if cand != nil {
			id = cand[i]
		}
		if score, ok := exactScore(s.docs[id], pq); ok {
			hits = append(hits, Hit{Doc: id, Score: score})
		}
	}
	return hits, nil
}

func exactScore(d *Doc, pq query) (int, bool) {
	for _, t := range pq.neg {
		if strings.Contains(d.lowRel, t) || strings.Contains(d.lowText, t) {
			return 0, false
		}
	}
	score := 0
	for _, t := range pq.pos {
		s := 0
		switch {
		case strings.Contains(d.lowName, t):
			s = 300
		case strings.Contains(d.lowRel, t):
			s = 100
		}
		if c := countUpTo(d.lowText, t, 20); c > 0 {
			s += 10 + c*5
		} else if s == 0 {
			return 0, false
		}
		score += s
	}
	return score, true
}

func countUpTo(s, sub string, max int) int {
	n := 0
	for n < max {
		i := strings.Index(s, sub)
		if i < 0 {
			break
		}
		n++
		s = s[i+len(sub):]
	}
	return n
}

func (s *Store) fuzzySearch(ctx context.Context, pq query) ([]Hit, []string, error) {
	type termMatch struct {
		docs  map[int32]int // best word score per document
		words []scoredWord
	}
	matches := make([]termMatch, len(pq.pos))
	var highlight []string
	for i, t := range pq.pos {
		m := termMatch{docs: map[int32]int{}}
		j := 0
		for w, ids := range s.vocab {
			if j++; j%4096 == 0 && ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			sc := wordScore(t, w)
			if sc == 0 {
				continue
			}
			m.words = append(m.words, scoredWord{w, sc})
			for _, id := range ids {
				if sc > m.docs[id] {
					m.docs[id] = sc
				}
			}
		}
		sort.Slice(m.words, func(a, b int) bool { return m.words[a].score > m.words[b].score })
		highlight = append(highlight, t)
		for _, sw := range m.words[:min(len(m.words), 50)] {
			if sw.word != t {
				highlight = append(highlight, sw.word)
			}
		}
		matches[i] = m
	}

	var hits []Hit
	for id, d := range s.docs {
		if id%cancelCheckEvery == 0 && ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		excluded := false
		for _, t := range pq.neg {
			if strings.Contains(d.lowRel, t) || strings.Contains(d.lowText, t) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		score := 0
		for i, t := range pq.pos {
			sc := pathScore(t, d) + matches[i].docs[int32(id)]
			if sc == 0 {
				score = -1
				break
			}
			score += sc
		}
		if score >= 0 {
			hits = append(hits, Hit{Doc: int32(id), Score: score})
		}
	}
	return hits, highlight, nil
}

type scoredWord struct {
	word  string
	score int
}

// wordScore rates how well a query term matches a word of a document, or 0.
func wordScore(term, word string) int {
	switch {
	case word == term:
		return 100
	case strings.HasPrefix(word, term):
		return 70
	case strings.Contains(word, term):
		return 50
	}
	tl, wl := utf8.RuneCountInString(term), utf8.RuneCountInString(word)
	if allowed := typoAllowance(tl); allowed > 0 && abs(tl-wl) <= allowed {
		if d := editDistance(term, word, allowed); d <= allowed {
			return 45 - 10*d
		}
	}
	if tl >= 3 && wl <= 2*tl+2 && firstRune(term) == firstRune(word) && isSubsequence(term, word) {
		return 20
	}
	return 0
}

func typoAllowance(n int) int {
	switch {
	case n >= 8:
		return 2
	case n >= 4:
		return 1
	}
	return 0
}

func pathScore(term string, d *Doc) int {
	switch {
	case strings.Contains(d.lowName, term):
		return 300
	case strings.Contains(d.lowRel, term):
		return 100
	case isSubsequence(term, d.lowName):
		return 60
	case isSubsequence(term, d.lowRel):
		return 30
	}
	return 0
}

func isSubsequence(needle, hay string) bool {
	for _, r := range needle {
		i := strings.IndexRune(hay, r)
		if i < 0 {
			return false
		}
		hay = hay[i+utf8.RuneLen(r):]
	}
	return true
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

// editDistance computes the Damerau-Levenshtein (optimal string alignment)
// distance, giving up early once it exceeds limit.
func editDistance(a, b string, limit int) int {
	ra, rb := []rune(a), []rune(b)
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
			rowMin = min(rowMin, cur[j])
		}
		if rowMin > limit {
			return limit + 1
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (s *Store) sortHits(hits []Hit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return s.docs[hits[i].Doc].lowRel < s.docs[hits[j].Doc].lowRel
	})
}
