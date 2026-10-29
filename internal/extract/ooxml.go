package extract

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// maxXMLPart limits how much of a single (decompressed) zip member we parse.
const maxXMLPart = 256 << 20

func openZipMember(zr *zip.ReadCloser, name string) (io.ReadCloser, error) {
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			return struct {
				io.Reader
				io.Closer
			}{io.LimitReader(rc, maxXMLPart), rc}, nil
		}
	}
	return nil, errors.New("missing zip member " + name)
}

// docxText extracts text from Word 2007+ documents: the body first, then
// footnotes, endnotes, headers and footers.
func docxText(p string) (string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	parts := []string{"word/document.xml"}
	var extra []string
	for _, f := range zr.File {
		n := f.Name
		if n == "word/footnotes.xml" || n == "word/endnotes.xml" ||
			(strings.HasPrefix(n, "word/header") || strings.HasPrefix(n, "word/footer")) && strings.HasSuffix(n, ".xml") {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	parts = append(parts, extra...)

	var b strings.Builder
	for i, name := range parts {
		rc, err := openZipMember(zr, name)
		if err != nil {
			if i == 0 {
				return "", err
			}
			continue
		}
		err = wordXMLText(rc, &b)
		rc.Close()
		if err != nil && i == 0 {
			return b.String(), err
		}
		b.WriteString("\n\n")
	}
	return b.String(), nil
}

func wordXMLText(r io.Reader, b *strings.Builder) error {
	d := xml.NewDecoder(r)
	d.Strict = false
	inText := 0
	skip := 0
	for b.Len() < MaxText {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "delText", "instrText", "Fallback":
				skip++
			case "t":
				inText++
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "delText", "instrText", "Fallback":
				skip--
			case "t":
				inText--
			case "p":
				b.WriteByte('\n')
			case "tc":
				b.WriteByte('\t')
			case "tr":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText > 0 && skip == 0 {
				b.Write(t)
			}
		}
	}
	return nil
}

// xlsxText extracts the cell contents of all sheets of an Excel 2007+ workbook.
func xlsxText(p string) (string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	var shared []string
	if rc, err := openZipMember(zr, "xl/sharedStrings.xml"); err == nil {
		shared = readSharedStrings(rc)
		rc.Close()
	}

	styles := readXLSXStyles(zr)

	var b strings.Builder
	for _, sh := range xlsxSheets(zr) {
		rc, err := openZipMember(zr, sh.file)
		if err != nil {
			continue
		}
		if sh.name != "" {
			b.WriteString("## " + sh.name + "\n")
		}
		readSheet(rc, shared, styles, &b)
		rc.Close()
		b.WriteString("\n\n")
		if b.Len() >= MaxText {
			break
		}
	}
	return b.String(), nil
}

type xlsxSheet struct{ name, file string }

// xlsxSheets lists the worksheets in workbook order, falling back to the
// worksheet files in the archive when the workbook metadata is unusable.
func xlsxSheets(zr *zip.ReadCloser) []xlsxSheet {
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	var rels struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	decode := func(name string, v any) error {
		rc, err := openZipMember(zr, name)
		if err != nil {
			return err
		}
		defer rc.Close()
		return xml.NewDecoder(rc).Decode(v)
	}

	var sheets []xlsxSheet
	if decode("xl/workbook.xml", &wb) == nil && decode("xl/_rels/workbook.xml.rels", &rels) == nil {
		targets := map[string]string{}
		for _, r := range rels.Rels {
			t := r.Target
			if strings.HasPrefix(t, "/") {
				t = strings.TrimPrefix(t, "/")
			} else {
				t = path.Join("xl", t)
			}
			targets[r.ID] = t
		}
		for _, s := range wb.Sheets {
			if t, ok := targets[s.ID]; ok {
				sheets = append(sheets, xlsxSheet{s.Name, t})
			}
		}
	}
	if len(sheets) > 0 {
		return sheets
	}
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/") && strings.HasSuffix(f.Name, ".xml") {
			sheets = append(sheets, xlsxSheet{file: f.Name})
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return naturalLess(sheets[i].file, sheets[j].file) })
	return sheets
}

func readSharedStrings(r io.Reader) []string {
	d := xml.NewDecoder(r)
	d.Strict = false
	var out []string
	var cur strings.Builder
	inT, inPhonetic := 0, 0
	for {
		tok, err := d.Token()
		if err != nil {
			return out
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				cur.Reset()
			case "t":
				inT++
			case "rPh":
				inPhonetic++
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
			case "t":
				inT--
			case "rPh":
				inPhonetic--
			}
		case xml.CharData:
			if inT > 0 && inPhonetic == 0 {
				cur.Write(t)
			}
		}
	}
}

// xlsxStyles knows which cell styles format numbers as dates.
type xlsxStyles struct {
	kinds    []dateKind // per cellXfs index
	date1904 bool
}

func readXLSXStyles(zr *zip.ReadCloser) xlsxStyles {
	var st xlsxStyles
	var styles struct {
		NumFmts []struct {
			ID   int    `xml:"numFmtId,attr"`
			Code string `xml:"formatCode,attr"`
		} `xml:"numFmts>numFmt"`
		Xfs []struct {
			NumFmtID int `xml:"numFmtId,attr"`
		} `xml:"cellXfs>xf"`
	}
	if rc, err := openZipMember(zr, "xl/styles.xml"); err == nil {
		if xml.NewDecoder(rc).Decode(&styles) == nil {
			codes := map[int]string{}
			for _, f := range styles.NumFmts {
				codes[f.ID] = f.Code
			}
			for _, xf := range styles.Xfs {
				st.kinds = append(st.kinds, numFmtKind(xf.NumFmtID, codes[xf.NumFmtID]))
			}
		}
		rc.Close()
	}
	var wb struct {
		Pr struct {
			Date1904 string `xml:"date1904,attr"`
		} `xml:"workbookPr"`
	}
	if rc, err := openZipMember(zr, "xl/workbook.xml"); err == nil {
		if xml.NewDecoder(rc).Decode(&wb) == nil {
			st.date1904 = wb.Pr.Date1904 == "1" || wb.Pr.Date1904 == "true"
		}
		rc.Close()
	}
	return st
}

func readSheet(r io.Reader, shared []string, styles xlsxStyles, b *strings.Builder) {
	d := xml.NewDecoder(r)
	d.Strict = false
	var cellType string
	cellStyle := 0
	var val strings.Builder
	inV, inT := false, false
	rowHasCells := false
	for b.Len() < MaxText {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				cellType, cellStyle = "", 0
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "t":
						cellType = a.Value
					case "s":
						cellStyle, _ = strconv.Atoi(a.Value)
					}
				}
				val.Reset()
			case "v":
				inV = true
			case "t":
				inT = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				v := strings.TrimSpace(val.String())
				if cellType == "s" {
					if i, err := strconv.Atoi(v); err == nil && i >= 0 && i < len(shared) {
						v = shared[i]
					}
				} else if cellType == "b" {
					v = map[string]string{"0": "FALSE", "1": "TRUE"}[v]
				} else if (cellType == "" || cellType == "n") && cellStyle < len(styles.kinds) && styles.kinds[cellStyle] != dateNone {
					if f, err := strconv.ParseFloat(v, 64); err == nil {
						v = formatNumber(f, styles.kinds[cellStyle], styles.date1904)
					}
				}
				if v != "" {
					if rowHasCells {
						b.WriteByte('\t')
					}
					b.WriteString(v)
					rowHasCells = true
				}
			case "row":
				if rowHasCells {
					b.WriteByte('\n')
				}
				rowHasCells = false
			}
		case xml.CharData:
			if inV || inT {
				val.Write(t)
			}
		}
	}
}

// naturalLess compares strings so that "sheet2" sorts before "sheet10".
func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := leadingDigits(a), leadingDigits(b)
		if da != "" && db != "" {
			na, _ := strconv.Atoi(da)
			nb, _ := strconv.Atoi(db)
			if na != nb {
				return na < nb
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
