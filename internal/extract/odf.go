package extract

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// odfText extracts text from OpenDocument files (LibreOffice Writer / Calc).
func odfText(p string) (string, error) {
	zr, err := zip.OpenReader(p)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	rc, err := openZipMember(zr, "content.xml")
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return odfXMLText(rc, isSpreadsheet(p))
}

// flatOdfText handles the single-file XML variants (.fodt, .fods).
func flatOdfText(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return odfXMLText(io.LimitReader(f, maxXMLPart), isSpreadsheet(p))
}

func isSpreadsheet(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".ods", ".ots", ".fods":
		return true
	}
	return false
}

// odfXMLText walks an OpenDocument content tree. Spreadsheet sheet names are
// emitted as headings when sheetNames is set.
func odfXMLText(r io.Reader, sheetNames bool) (string, error) {
	d := xml.NewDecoder(r)
	d.Strict = false
	var b strings.Builder
	inPara := 0 // depth of text:p / text:h elements
	skip := 0   // depth of subtrees whose content is not document text
	cellDepth := 0
	cellHasText := false
	rowHasCells := false
	for b.Len() < MaxText {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return b.String(), err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tracked-changes", "binary-data", "annotation", "sequence-decls", "master-styles":
				skip++
			case "p", "h":
				if inPara == 0 && cellDepth > 0 && cellHasText {
					b.WriteByte(' ')
				}
				inPara++
			case "s":
				if inPara > 0 && skip == 0 {
					n := 1
					for _, a := range t.Attr {
						if a.Name.Local == "c" {
							if c, err := strconv.Atoi(a.Value); err == nil && c > 0 {
								n = min(c, 8)
							}
						}
					}
					b.WriteString(strings.Repeat(" ", n))
				}
			case "tab":
				if inPara > 0 && skip == 0 {
					b.WriteByte('\t')
				}
			case "line-break":
				if inPara > 0 && skip == 0 {
					b.WriteByte('\n')
				}
			case "table-cell", "covered-table-cell":
				cellDepth++
				cellHasText = false
			case "table":
				for _, a := range t.Attr {
					if sheetNames && a.Name.Local == "name" && a.Value != "" {
						b.WriteString("\n## " + a.Value + "\n")
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "tracked-changes", "binary-data", "annotation", "sequence-decls", "master-styles":
				skip--
			case "p", "h":
				inPara--
				if inPara == 0 && skip == 0 && cellDepth == 0 {
					b.WriteByte('\n')
				}
			case "table-cell", "covered-table-cell":
				cellDepth--
				if cellHasText {
					b.WriteByte('\t')
					rowHasCells = true
				}
				cellHasText = false
			case "table-row":
				if rowHasCells {
					b.WriteByte('\n')
				}
				rowHasCells = false
			}
		case xml.CharData:
			if inPara > 0 && skip == 0 {
				b.Write(t)
				if cellDepth > 0 {
					cellHasText = true
				}
			}
		}
	}
	return b.String(), nil
}
