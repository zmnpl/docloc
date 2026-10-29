package extract

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeZip creates a zip archive with the given members in a temp dir.
func writeZip(t *testing.T, name string, members [][2]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, m := range members {
		w, err := zw.Create(m[0])
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(m[1]))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func extractOK(t *testing.T, p string) string {
	t.Helper()
	text, err := File(p)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestPPTX(t *testing.T) {
	slide := func(text string) string {
		return `<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree><p:sp><p:txBody>` +
			`<a:p><a:r><a:t>` + text + `</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`
	}
	p := writeZip(t, "deck.pptx", [][2]string{
		{"ppt/slides/slide10.xml", slide("Zehn")},
		{"ppt/slides/slide2.xml", slide("Zwei")},
		{"ppt/slides/slide1.xml", slide("Eins")},
		{"ppt/notesSlides/notesSlide1.xml", slide("Sprechernotiz")},
	})
	got := extractOK(t, p)
	want := "## Slide 1\nEins\n\n## Slide 2\nZwei\n\n## Slide 3\nZehn\n\n## Notes\nSprechernotiz"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestODP(t *testing.T) {
	content := `<office:document-content xmlns:office="o" xmlns:draw="d" xmlns:text="t" xmlns:presentation="p">
<office:body><office:presentation>
<draw:page draw:name="page1"><draw:frame><draw:text-box><text:p>Folie eins</text:p><text:p>Punkt <text:span>zwei</text:span></text:p></draw:text-box></draw:frame>
<presentation:notes><draw:frame><draw:text-box><text:p>Notiz</text:p></draw:text-box></draw:frame></presentation:notes></draw:page>
</office:presentation></office:body></office:document-content>`
	got := extractOK(t, writeZip(t, "deck.odp", [][2]string{{"content.xml", content}}))
	for _, w := range []string{"Folie eins\nPunkt zwei", "Notiz"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
}

func TestHTML(t *testing.T) {
	in := `<!doctype html><html><head><meta charset="iso-8859-1"><title>Beleg</title>
<style>p { color: red }</style><script>var x = "hidden";</script></head>
<body><h1>Rechnung</h1><p>Betrag:
   <b>12,50&nbsp;&euro;</b> f` + "\xfc" + `r <a href="#">Strom</a></p>
<table><tr><td>A</td><td>B</td></tr></table><pre>  einger` + "\xfc" + `ckt
  bleibt</pre></body></html>`
	got := Sanitize(htmlText([]byte(in)))
	for _, w := range []string{"Beleg\n", "Rechnung\n", "Betrag: 12,50 € für Strom", "A\tB", "  eingerückt\n  bleibt"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	for _, bad := range []string{"color", "hidden", "<"} {
		if strings.Contains(got, bad) {
			t.Errorf("unexpected %q in %q", bad, got)
		}
	}
}

func TestEPUB(t *testing.T) {
	chapter := func(s string) string { return "<html><body><p>" + s + "</p></body></html>" }
	p := writeZip(t, "book.epub", [][2]string{
		{"mimetype", "application/epub+zip"},
		{"META-INF/container.xml", `<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`},
		{"OEBPS/content.opf", `<package><manifest>
<item id="c2" href="text/kapitel%202.xhtml"/><item id="c1" href="text/kapitel1.xhtml"/>
</manifest><spine><itemref idref="c1"/><itemref idref="c2"/></spine></package>`},
		{"OEBPS/text/kapitel1.xhtml", chapter("Erstes Kapitel")},
		{"OEBPS/text/kapitel 2.xhtml", chapter("Zweites Kapitel")},
	})
	got := extractOK(t, p)
	i, j := strings.Index(got, "Erstes Kapitel"), strings.Index(got, "Zweites Kapitel")
	if i < 0 || j < 0 || i > j {
		t.Errorf("chapters missing or out of spine order: %q", got)
	}
}

func TestCSV(t *testing.T) {
	p := filepath.Join(t.TempDir(), "umsatz.csv")
	os.WriteFile(p, []byte("Datum;Betrag\n07.06.13;\"1584,81\"\n"), 0o644)
	if got := extractOK(t, p); !strings.Contains(got, "07.06.13;\"1584,81\"") {
		t.Errorf("got %q", got)
	}
}
