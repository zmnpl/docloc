// Package extract pulls plain text out of document files using pure Go code only.
package extract

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxText caps the amount of text kept per document (in bytes).
const MaxText = 1 << 20

var ErrUnsupported = errors.New("unsupported file type")

type extractor func(path string) (string, error)

var extractors = map[string]extractor{
	".pdf":      pdfText,
	".docx":     docxText,
	".docm":     docxText,
	".dotx":     docxText,
	".doc":      docText,
	".dot":      docText,
	".rtf":      rtfFile,
	".xlsx":     xlsxText,
	".xlsm":     xlsxText,
	".xls":      xlsText,
	".odt":      odfText,
	".ott":      odfText,
	".ods":      odfText,
	".odp":      odfText,
	".otp":      odfText,
	".pptx":     pptxText,
	".pptm":     pptxText,
	".html":     htmlFile,
	".htm":      htmlFile,
	".xhtml":    htmlFile,
	".epub":     epubText,
	".csv":      plainText,
	".tsv":      plainText,
	".ots":      odfText,
	".fodt":     flatOdfText,
	".fods":     flatOdfText,
	".txt":      plainText,
	".text":     plainText,
	".md":       plainText,
	".markdown": plainText,
}

// Supported reports whether the file extension is one we can index.
func Supported(path string) bool {
	_, ok := extractors[strings.ToLower(filepath.Ext(path))]
	return ok
}

// File extracts the text of the document at path. The result is sanitized
// for terminal display and capped at MaxText bytes.
func File(path string) (text string, err error) {
	ex, ok := extractors[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return "", ErrUnsupported
	}
	// Third party parsers may panic on malformed input.
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("extract %s: panic: %v", path, r)
		}
	}()
	raw, err := ex(path)
	return Sanitize(raw), err
}

// Sanitize makes extracted text safe and pleasant to display: valid UTF-8,
// no control characters besides newline and tab, normalized line endings,
// no runs of blank lines, trailing whitespace trimmed and capped at MaxText.
func Sanitize(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	var b strings.Builder
	b.Grow(min(len(s), MaxText))
	newlines := 0
	pendingSpace := 0
	for i, r := range s {
		if b.Len() >= MaxText {
			break
		}
		switch {
		case r == '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				continue
			}
			r = '\n'
		case r == ' ' || r == ' ' || r == ' ':
			r = ' '
		case r == '\f' || r == '\v':
			r = '\n'
		}
		switch {
		case r == '\n':
			pendingSpace = 0
			newlines++
			continue
		case r == ' ' || r == '\t':
			if r == '\t' {
				pendingSpace = -1 // tabs win, they separate table cells
			} else if pendingSpace >= 0 {
				pendingSpace++
			}
			continue
		case unicode.IsControl(r) || r == utf8.RuneError || r == '\ufeff':
			continue
		}
		if newlines > 0 {
			if b.Len() > 0 {
				b.WriteString(strings.Repeat("\n", min(newlines, 2)))
			}
			newlines = 0
		}
		if pendingSpace != 0 && b.Len() > 0 {
			if pendingSpace < 0 {
				b.WriteByte('\t')
			} else {
				b.WriteString(strings.Repeat(" ", min(pendingSpace, 8)))
			}
		}
		pendingSpace = 0
		b.WriteRune(r)
	}
	return b.String()
}
