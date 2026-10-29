package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"path"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

func htmlFile(p string) (string, error) {
	data, err := readCapped(p, maxXMLPart)
	if err != nil {
		return "", err
	}
	return htmlText(data), nil
}

// htmlSkip lists elements whose content is never visible text.
var htmlSkip = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "math": true, "iframe": true, "object": true,
}

// htmlBlock lists elements that start a new line.
var htmlBlock = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "table": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"title": true, "section": true, "article": true, "header": true, "footer": true,
	"blockquote": true, "pre": true, "ul": true, "ol": true, "dl": true, "dt": true,
	"dd": true, "hr": true, "form": true, "address": true, "figcaption": true,
	"nav": true, "aside": true, "main": true, "caption": true,
}

// htmlText converts an HTML document to plain text. The character set is
// taken from the BOM, a <meta> declaration or a guess, as browsers do.
func htmlText(data []byte) string {
	var r io.Reader = bytes.NewReader(data)
	if cr, err := charset.NewReader(r, ""); err == nil {
		r = cr
	}
	z := html.NewTokenizer(r)
	var b strings.Builder
	skip := 0
	pre := 0
	for b.Len() < MaxText {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return b.String()
		case html.StartTagToken, html.SelfClosingTagToken, html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if htmlSkip[tag] && tt != html.SelfClosingTagToken {
				if tt == html.StartTagToken {
					skip++
				} else if skip > 0 {
					skip--
				}
				continue
			}
			if tag == "pre" {
				if tt == html.StartTagToken {
					pre++
				} else if tt == html.EndTagToken && pre > 0 {
					pre--
				}
			}
			switch {
			case htmlBlock[tag]:
				b.WriteByte('\n')
			case tag == "td" || tag == "th":
				if tt == html.EndTagToken {
					b.WriteByte('\t')
				}
			}
		case html.TextToken:
			if skip > 0 {
				continue
			}
			text := string(z.Text())
			if pre == 0 {
				// Outside <pre>, source line breaks are just whitespace.
				text = strings.Join(strings.Fields(text), " ")
				out := b.String()
				atLineStart := out == "" || out[len(out)-1] == '\n'
				raw := z.Raw()
				if text != "" && !atLineStart && isHTMLSpace(raw[0]) {
					text = " " + text
				}
				if isHTMLSpace(raw[len(raw)-1]) && (text != "" || !atLineStart) {
					text += " " // Sanitize collapses doubled spaces
				}
			}
			b.WriteString(text)
		}
	}
	return b.String()
}

func isHTMLSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\f'
}

// epubText extracts the chapters of an EPUB book in reading order.
func epubText(p string) (string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	var b strings.Builder
	for _, name := range epubChapters(zr) {
		rc, err := openZipMember(zr, name)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		b.WriteString(htmlText(data))
		b.WriteString("\n\n")
		if b.Len() >= MaxText {
			break
		}
	}
	return b.String(), nil
}

// epubChapters returns the content documents following the package's spine,
// or all (X)HTML files in the archive if the metadata is unusable.
func epubChapters(zr *zip.ReadCloser) []string {
	decode := func(name string, v any) error {
		rc, err := openZipMember(zr, name)
		if err != nil {
			return err
		}
		defer rc.Close()
		d := xml.NewDecoder(rc)
		d.Strict = false
		return d.Decode(v)
	}
	var container struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	var opf struct {
		Items []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	var chapters []string
	if decode("META-INF/container.xml", &container) == nil && len(container.Rootfiles) > 0 {
		opfPath := container.Rootfiles[0].Path
		if decode(opfPath, &opf) == nil {
			hrefs := map[string]string{}
			for _, it := range opf.Items {
				hrefs[it.ID] = it.Href
			}
			for _, ref := range opf.Spine {
				if h, ok := hrefs[ref.IDRef]; ok {
					chapters = append(chapters, path.Join(path.Dir(opfPath), unescapeURLPath(h)))
				}
			}
		}
	}
	if len(chapters) > 0 {
		return chapters
	}
	for _, f := range zr.File {
		switch strings.ToLower(path.Ext(f.Name)) {
		case ".xhtml", ".html", ".htm":
			chapters = append(chapters, f.Name)
		}
	}
	sort.Slice(chapters, func(i, j int) bool { return naturalLess(chapters[i], chapters[j]) })
	return chapters
}

// unescapeURLPath decodes %XX escapes in manifest hrefs.
func unescapeURLPath(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && unhexByte(s[i+1]) >= 0 && unhexByte(s[i+2]) >= 0 {
			b.WriteByte(byte(unhexByte(s[i+1])<<4 | unhexByte(s[i+2])))
			i += 2
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func unhexByte(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
