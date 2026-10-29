package extract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// maxPDFFile is the largest PDF we try to parse. The reader seeks instead of
// loading the whole file, so this mostly bounds the time spent.
const maxPDFFile = 1 << 30

// pdfText reads the text layer of a PDF. Scanned documents without a text
// layer yield an empty string.
func pdfText(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if fi.Size() > maxPDFFile {
		return "", errors.New("file too large")
	}

	// Some producers put junk before the header, which readers tolerate
	// within the first KiB. Offsets in the file are relative to the header.
	head := make([]byte, 1024)
	n, _ := io.ReadFull(f, head)
	off := int64(bytes.Index(head[:n], []byte("%PDF-")))
	if off < 0 {
		return "", errors.New("not a PDF file")
	}
	size := fi.Size() - off
	r, err := pdf.NewReader(io.NewSectionReader(f, off, size), size)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	var firstErr error
	for i := 1; i <= r.NumPage() && b.Len() < MaxText; i++ {
		if err := pageText(r, i, &b); err != nil && firstErr == nil {
			firstErr = err
		}
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(b.String()) == "" && firstErr != nil {
		return "", firstErr
	}
	return b.String(), nil
}

func pageText(r *pdf.Reader, num int, b *strings.Builder) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("page %d: %v", num, rec)
		}
	}()
	page := r.Page(num)
	if page.V.IsNull() {
		return nil
	}
	w := &pdfWalker{}
	w.walk(page.V.Key("Contents"), page.Resources(), pdfState{Th: 1, CTM: identity}, 0)
	writeTexts(b, w.texts)
	return nil
}

// writeTexts lays out text fragments in content stream order, which is
// usually the reading order. Line breaks are inferred from vertical jumps and
// word breaks from horizontal gaps. Some fonts lack width information (W is
// 0 and all glyphs of one show operation share an X); then the width of a run
// is estimated from its character count.
func writeTexts(b *strings.Builder, texts []pdf.Text) {
	var prev *pdf.Text
	runStart, runLen := 0.0, 0
	for i := range texts {
		t := &texts[i]
		if t.S == "" {
			continue
		}
		if prev != nil {
			fs := math.Max(math.Max(prev.FontSize, t.FontSize), 1)
			sep := ""
			switch {
			case math.Abs(t.Y-prev.Y) > fs*0.6:
				sep = "\n"
			case t.X < prev.X-fs*0.5:
				sep = " "
			case prev.W > 0:
				sep = gapSeparator(t.X-(prev.X+prev.W), fs)
			case t.X != runStart && runLen > 1:
				sep = gapSeparator(t.X-runStart-float64(runLen)*fs*0.4, fs)
			}
			if sep == " " && (strings.HasSuffix(prev.S, " ") || strings.HasPrefix(t.S, " ")) {
				sep = ""
			}
			b.WriteString(sep)
		}
		if prev == nil || t.X != prev.X || prev.W > 0 {
			runStart, runLen = t.X, 0
		}
		b.WriteString(t.S)
		runLen += utf8.RuneCountInString(t.S)
		prev = t
	}
	b.WriteByte('\n')
}

func gapSeparator(gap, fs float64) string {
	switch {
	case gap > fs*3:
		return "\t"
	case gap > fs*0.12:
		return " "
	}
	return ""
}

type matrix [3][3]float64

var identity = matrix{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (x matrix) mul(y matrix) matrix {
	var z matrix
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				z[i][j] += x[i][k] * y[k][j]
			}
		}
	}
	return z
}

func translate(tx, ty float64) matrix {
	return matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}}
}

// pdfState is the part of the graphics and text state relevant for text
// positions (PDF 32000-1, section 9.3).
type pdfState struct {
	Tc, Tw, Th, Tl, Tfs, Trise float64
	Tm, Tlm, CTM               matrix
	font                       *pdfFont
}

// pdfFont wraps a font resource with what is needed to measure glyphs.
type pdfFont struct {
	enc     pdf.TextEncoding
	simple  pdf.Font
	cid     bool
	widths  map[int]float64 // CID fonts: per CID widths from /W
	dfltW   float64
	hasWdth bool
}

func newPDFFont(v pdf.Value) *pdfFont {
	f := &pdfFont{simple: pdf.Font{V: v}, dfltW: 500}
	if enc := f.simple.Encoder(); enc != nil {
		f.enc = enc
	}
	if v.Key("Subtype").Name() == "Type0" {
		f.cid = true
		f.widths = map[int]float64{}
		f.dfltW = 1000
		desc := v.Key("DescendantFonts").Index(0)
		if dw := desc.Key("DW"); dw.Kind() == pdf.Integer || dw.Kind() == pdf.Real {
			f.dfltW = dw.Float64()
		}
		w := desc.Key("W")
		for i := 0; i < w.Len() && len(f.widths) < 1<<16; {
			first := int(w.Index(i).Int64())
			next := w.Index(i + 1)
			if next.Kind() == pdf.Array {
				for j := 0; j < next.Len(); j++ {
					f.widths[first+j] = next.Index(j).Float64()
				}
				i += 2
			} else {
				last := int(next.Int64())
				width := w.Index(i + 2).Float64()
				for c := first; c <= last && c-first < 1<<16; c++ {
					f.widths[c] = width
				}
				i += 3
			}
		}
	} else {
		f.hasWdth = v.Key("Widths").Len() > 0
	}
	return f
}

// advance returns the horizontal displacement of a raw string in text space
// units (before scaling by the font size).
func (f *pdfFont) advance(raw string, st *pdfState) float64 {
	tx := 0.0
	if f.cid {
		for i := 0; i+1 < len(raw); i += 2 {
			code := int(raw[i])<<8 | int(raw[i+1])
			w, ok := f.widths[code]
			if !ok {
				w = f.dfltW
			}
			tx += w/1000*st.Tfs + st.Tc
		}
		return tx * st.Th
	}
	for i := 0; i < len(raw); i++ {
		w := f.dfltW
		if f.hasWdth {
			if fw := f.simple.Width(int(raw[i])); fw > 0 {
				w = fw
			}
		}
		tx += w/1000*st.Tfs + st.Tc
		if raw[i] == ' ' {
			tx += st.Tw
		}
	}
	return tx * st.Th
}

// pdfWalker interprets content streams, following form XObjects, and
// collects positioned text fragments.
type pdfWalker struct {
	texts []pdf.Text
}

const maxFormDepth = 8

func (w *pdfWalker) walk(strm, res pdf.Value, g pdfState, depth int) {
	if strm.IsNull() || depth > maxFormDepth {
		return
	}
	fonts := map[string]*pdfFont{}
	var stack []pdfState

	show := func(raw string) {
		if g.font == nil {
			return
		}
		text := raw
		if g.font.enc != nil {
			text = g.font.enc.Decode(raw)
		}
		trm := matrix{{g.Tfs * g.Th, 0, 0}, {0, g.Tfs, 0}, {0, g.Trise, 1}}.mul(g.Tm).mul(g.CTM)
		tx := g.font.advance(raw, &g)
		next := translate(tx, 0).mul(g.Tm)
		end := matrix{{g.Tfs * g.Th, 0, 0}, {0, g.Tfs, 0}, {0, g.Trise, 1}}.mul(next).mul(g.CTM)
		g.Tm = next
		if text == "" {
			return
		}
		w.texts = append(w.texts, pdf.Text{
			FontSize: math.Hypot(trm[1][0], trm[1][1]),
			X:        trm[2][0],
			Y:        trm[2][1],
			W:        end[2][0] - trm[2][0],
			S:        text,
		})
	}

	pdf.Interpret(strm, func(stk *pdf.Stack, op string) {
		args := make([]pdf.Value, stk.Len())
		for i := len(args) - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		num := func(i int) float64 {
			if i < len(args) {
				return args[i].Float64()
			}
			return 0
		}
		switch op {
		case "cm":
			if len(args) == 6 {
				g.CTM = matrixOf(args).mul(g.CTM)
			}
		case "q":
			stack = append(stack, g)
		case "Q":
			if n := len(stack); n > 0 {
				g, stack = stack[n-1], stack[:n-1]
			}
		case "BT":
			g.Tm, g.Tlm = identity, identity
		case "Tc":
			g.Tc = num(0)
		case "Tw":
			g.Tw = num(0)
		case "Tz":
			g.Th = num(0) / 100
		case "TL":
			g.Tl = num(0)
		case "Ts":
			g.Trise = num(0)
		case "Tf":
			if len(args) == 2 {
				name := args[0].Name()
				f, ok := fonts[name]
				if !ok {
					f = newPDFFont(res.Key("Font").Key(name))
					fonts[name] = f
				}
				g.font = f
				g.Tfs = num(1)
			}
		case "TD":
			g.Tl = -num(1)
			fallthrough
		case "Td":
			g.Tlm = translate(num(0), num(1)).mul(g.Tlm)
			g.Tm = g.Tlm
		case "Tm":
			if len(args) == 6 {
				g.Tm = matrixOf(args)
				g.Tlm = g.Tm
			}
		case "T*":
			g.Tlm = translate(0, -g.Tl).mul(g.Tlm)
			g.Tm = g.Tlm
		case "\"":
			if len(args) == 3 {
				g.Tw, g.Tc = num(0), num(1)
				args = args[2:]
			}
			fallthrough
		case "'":
			g.Tlm = translate(0, -g.Tl).mul(g.Tlm)
			g.Tm = g.Tlm
			fallthrough
		case "Tj":
			if len(args) > 0 {
				show(args[len(args)-1].RawString())
			}
		case "TJ":
			if len(args) == 0 {
				return
			}
			v := args[0]
			for i := 0; i < v.Len(); i++ {
				x := v.Index(i)
				if x.Kind() == pdf.String {
					show(x.RawString())
				} else {
					g.Tm = translate(-x.Float64()/1000*g.Tfs*g.Th, 0).mul(g.Tm)
				}
			}
		case "Do":
			if len(args) != 1 {
				return
			}
			xobj := res.Key("XObject").Key(args[0].Name())
			if xobj.Key("Subtype").Name() != "Form" {
				return
			}
			sub := g
			if m := xobj.Key("Matrix"); m.Len() == 6 {
				vals := make([]pdf.Value, 6)
				for i := range vals {
					vals[i] = m.Index(i)
				}
				sub.CTM = matrixOf(vals).mul(g.CTM)
			}
			subRes := xobj.Key("Resources")
			if subRes.IsNull() {
				subRes = res
			}
			w.walk(xobj, subRes, sub, depth+1)
		}
	})
}

func matrixOf(args []pdf.Value) matrix {
	var m matrix
	for i := range 6 {
		m[i/2][i%2] = args[i].Float64()
	}
	m[2][2] = 1
	return m
}
