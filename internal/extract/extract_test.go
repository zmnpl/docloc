package extract

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDocuments(t *testing.T) {
	want := []string{
		"Grüße aus Köln",
		"Das ist ein Testdokument mit Umlauten: äöüß und Euro €.",
		"Zweite Zeile mit Rechnungsnummer 4711.",
	}
	for _, name := range []string{"sample.txt", "sample.doc", "sample.docx", "sample.odt", "sample.fodt", "sample.rtf", "sample.pdf"} {
		t.Run(name, func(t *testing.T) {
			text, err := File(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range want {
				if !strings.Contains(text, w) {
					t.Errorf("missing %q in:\n%s", w, text)
				}
			}
		})
	}
}

func TestSpreadsheets(t *testing.T) {
	want := "Name\tBetrag\tDatum\nMiete\t850.5\t2024-01-03\nStrom\t61\t2024-02-01"
	for _, name := range []string{"sheet.xls", "sheet.xlsx", "sheet.ods", "sheet.fods"} {
		t.Run(name, func(t *testing.T) {
			text, err := File(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, want) {
				t.Errorf("got:\n%s\nwant it to contain:\n%s", text, want)
			}
			if !strings.Contains(text, "## sheet") {
				t.Errorf("sheet name heading missing:\n%s", text)
			}
		})
	}
}

func TestSupported(t *testing.T) {
	for _, p := range []string{"a.PDF", "b.docx", "c.Doc", "d.xls", "e.xlsx", "f.odt", "g.ods", "h.md", "i.txt"} {
		if !Supported(p) {
			t.Errorf("%s should be supported", p)
		}
	}
	for _, p := range []string{"a.jpg", "b", "c.go", "d.zip"} {
		if Supported(p) {
			t.Errorf("%s should not be supported", p)
		}
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a\r\nb\rc", "a\nb\nc"},
		{"a\n\n\n\n\nb", "a\n\nb"},
		{"a\x00b\x1bc", "abc"},
		{"first\n  indented\ntrail   \n", "first\n  indented\ntrail"},
		{"x\u00a0y", "x y"},
		{"cell\t\tnext", "cell\tnext"},
		{"bad\xffutf8", "badutf8"},
	}
	for _, tt := range tests {
		if got := Sanitize(tt.in); got != tt.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRTF(t *testing.T) {
	in := `{\rtf1\ansi{\fonttbl{\f0 Arial;}}{\*\generator x;}\f0 Hallo {\b Welt}\par Gr\'fc\'dfe \u8364? Ende\par}`
	got := Sanitize(rtfText([]byte(in)))
	want := "Hallo Welt\nGrüße € Ende"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNumFmtKind(t *testing.T) {
	tests := []struct {
		id   int
		code string
		want dateKind
	}{
		{0, "General", dateNone},
		{14, "", dateOnly},
		{22, "", dateTime},
		{20, "", timeOnly},
		{164, "#,##0.00 [$€-407]", dateNone},
		{165, `0.00 "days"`, dateNone},
		{166, "DD.MM.YYYY", dateOnly},
		{167, "yyyy-mm-dd hh:mm", dateTime},
		{168, "[h]:mm:ss", timeOnly},
		{169, "0.00E+00", dateNone},
	}
	for _, tt := range tests {
		if got := numFmtKind(tt.id, tt.code); got != tt.want {
			t.Errorf("numFmtKind(%d, %q) = %v, want %v", tt.id, tt.code, got, tt.want)
		}
	}
}

func TestRKValue(t *testing.T) {
	tests := []struct {
		rk   uint32
		want float64
	}{
		{0x02F1853E | 0x02, float64(int32(0x02F1853E|0x02) >> 2)},
		{(1234 << 2) | 0x02, 1234},
		{(1234 << 2) | 0x03, 12.34},
		{0x3FF00000, 1}, // float 1.0, upper 30 bits
	}
	for _, tt := range tests {
		if got := rkValue(tt.rk); got != tt.want {
			t.Errorf("rkValue(%#x) = %v, want %v", tt.rk, got, tt.want)
		}
	}
}
