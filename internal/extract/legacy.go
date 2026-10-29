package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"unicode/utf16"

	"github.com/richardlehane/mscfb"
	"golang.org/x/text/encoding/charmap"
)

// maxLegacyFile is the largest legacy binary office file we try to parse;
// these parsers keep the whole file in memory.
const maxLegacyFile = 64 << 20

// docText extracts text from a legacy Word 97-2003 document. It follows the
// piece table described in [MS-DOC]; files that are really RTF (a common
// disguise) are handled by the RTF extractor.
func docText(p string) (string, error) {
	data, err := readCapped(p, maxLegacyFile)
	if err != nil {
		return "", err
	}
	if bytes.HasPrefix(data, []byte(`{\rtf`)) {
		return rtfText(data), nil
	}

	cfb, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	streams := map[string][]byte{}
	for entry, err := cfb.Next(); err == nil; entry, err = cfb.Next() {
		switch entry.Name {
		case "WordDocument", "0Table", "1Table":
			if buf, err := io.ReadAll(entry); err == nil {
				streams[entry.Name] = buf
			}
		}
	}
	word := streams["WordDocument"]
	if len(word) < 0x1AA {
		return "", errors.New("no WordDocument stream")
	}
	if binary.LittleEndian.Uint16(word) != 0xA5EC {
		return "", errors.New("unsupported Word version")
	}
	flags := binary.LittleEndian.Uint16(word[0x0A:])
	if flags&0x0100 != 0 {
		return "", errors.New("document is encrypted")
	}
	tableName := "0Table"
	if flags&0x0200 != 0 {
		tableName = "1Table"
	}
	table := streams[tableName]

	fcClx := binary.LittleEndian.Uint32(word[0x01A2:])
	lcbClx := binary.LittleEndian.Uint32(word[0x01A6:])
	if table != nil && lcbClx > 0 && uint64(fcClx)+uint64(lcbClx) <= uint64(len(table)) {
		if text, err := pieceTableText(word, table[fcClx:fcClx+lcbClx]); err == nil {
			return cleanWordText(text), nil
		}
	}

	// Fallback for documents without a usable piece table: the main text
	// lives between fcMin and fcMac as 8-bit characters.
	fcMin := binary.LittleEndian.Uint32(word[0x18:])
	fcMac := binary.LittleEndian.Uint32(word[0x1C:])
	if fcMin < fcMac && int(fcMac) <= len(word) {
		s, _ := charmap.Windows1252.NewDecoder().Bytes(word[fcMin:fcMac])
		return cleanWordText(string(s)), nil
	}
	return "", errors.New("no text found")
}

// pieceTableText resolves the Clx structure into the document text.
func pieceTableText(word, clx []byte) (string, error) {
	// Skip Prc entries (formatting) until the Pcdt (0x02) appears.
	for len(clx) > 0 && clx[0] == 0x01 {
		if len(clx) < 3 {
			return "", errors.New("bad clx")
		}
		n := int(binary.LittleEndian.Uint16(clx[1:]))
		if 3+n > len(clx) {
			return "", errors.New("bad clx")
		}
		clx = clx[3+n:]
	}
	if len(clx) < 5 || clx[0] != 0x02 {
		return "", errors.New("no piece table")
	}
	lcb := int(binary.LittleEndian.Uint32(clx[1:]))
	plc := clx[5:]
	if lcb > len(plc) || lcb < 4 || (lcb-4)%12 != 0 {
		return "", errors.New("bad piece table")
	}
	n := (lcb - 4) / 12
	cps := plc[:4*(n+1)]
	pcds := plc[4*(n+1):]

	var b strings.Builder
	dec := charmap.Windows1252.NewDecoder()
	for i := 0; i < n && b.Len() < MaxText; i++ {
		cpStart := binary.LittleEndian.Uint32(cps[4*i:])
		cpEnd := binary.LittleEndian.Uint32(cps[4*i+4:])
		if cpEnd <= cpStart {
			continue
		}
		count := int(cpEnd - cpStart)
		fc := binary.LittleEndian.Uint32(pcds[8*i+2:])
		if fc&0x40000000 != 0 {
			off := int((fc &^ 0x40000000) / 2)
			if off < 0 || off+count > len(word) {
				continue
			}
			s, _ := dec.Bytes(word[off : off+count])
			b.Write(s)
		} else {
			off := int(fc)
			if off < 0 || off+2*count > len(word) {
				continue
			}
			u := make([]uint16, count)
			for j := range u {
				u[j] = binary.LittleEndian.Uint16(word[off+2*j:])
			}
			b.WriteString(string(utf16.Decode(u)))
		}
	}
	return b.String(), nil
}

// cleanWordText maps Word's special characters to plain text and drops
// field instructions (the part between 0x13 and 0x14 of a field).
func cleanWordText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	fieldDepth := 0
	inInstr := []bool{}
	for _, r := range s {
		switch r {
		case 0x13: // field begin
			fieldDepth++
			inInstr = append(inInstr, true)
			continue
		case 0x14: // field separator: result follows
			if fieldDepth > 0 {
				inInstr[fieldDepth-1] = false
			}
			continue
		case 0x15: // field end
			if fieldDepth > 0 {
				fieldDepth--
				inInstr = inInstr[:fieldDepth]
			}
			continue
		}
		if fieldDepth > 0 && inInstr[fieldDepth-1] {
			continue
		}
		switch r {
		case '\r', 0x0B, 0x0C:
			b.WriteByte('\n')
		case 0x07: // table cell / row mark
			b.WriteByte('\t')
		case 0x1E: // non-breaking hyphen
			b.WriteByte('-')
		case 0x1F, 0x01, 0x08: // optional hyphen, pictures, drawn objects
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func rtfFile(p string) (string, error) {
	data, err := readCapped(p, maxLegacyFile)
	if err != nil {
		return "", err
	}
	return rtfText(data), nil
}

// rtfDestinations whose content is not document text.
var rtfSkip = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true,
	"pict": true, "object": true, "header": true, "footer": true,
	"headerl": true, "headerr": true, "footerl": true, "footerr": true,
	"themedata": true, "colorschememapping": true, "datastore": true,
	"latentstyles": true, "listtable": true, "listoverridetable": true,
	"rsidtbl": true, "xmlnstbl": true, "generator": true, "filetbl": true,
	"revtbl": true, "fldinst": true, "datafield": true, "bkmkstart": true,
	"bkmkend": true, "mmathPr": true, "wgrffmtfilter": true, "pgdsctbl": true,
}

// rtfText is a small RTF to text converter: it understands groups, control
// words, hex escapes and unicode escapes, which covers typical documents.
func rtfText(data []byte) string {
	type state struct {
		skip bool
		uc   int // number of fallback chars after \u
	}
	stack := []state{{uc: 1}}
	cur := &stack[0]
	var b strings.Builder
	var hexBuf []byte
	dec := charmap.Windows1252.NewDecoder()
	flushHex := func() {
		if len(hexBuf) > 0 {
			s, _ := dec.Bytes(hexBuf)
			b.Write(s)
			hexBuf = hexBuf[:0]
		}
	}
	skipFallback := 0
	groupStart := false
	for i := 0; i < len(data) && b.Len() < MaxText; i++ {
		c := data[i]
		switch c {
		case '{':
			flushHex()
			stack = append(stack, *cur)
			cur = &stack[len(stack)-1]
			groupStart = true
			continue
		case '}':
			flushHex()
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
				cur = &stack[len(stack)-1]
			}
			groupStart = false
			continue
		case '\\':
			if i+1 >= len(data) {
				continue
			}
			n := data[i+1]
			switch {
			case n == '\'' && i+3 < len(data):
				if skipFallback > 0 {
					skipFallback--
				} else if !cur.skip {
					var v byte
					for _, h := range data[i+2 : i+4] {
						v <<= 4
						switch {
						case h >= '0' && h <= '9':
							v |= h - '0'
						case h >= 'a' && h <= 'f':
							v |= h - 'a' + 10
						case h >= 'A' && h <= 'F':
							v |= h - 'A' + 10
						}
					}
					hexBuf = append(hexBuf, v)
				}
				i += 3
			case n == '*':
				if groupStart {
					cur.skip = true
				}
				i++
			case n == '\\' || n == '{' || n == '}':
				flushHex()
				if !cur.skip {
					b.WriteByte(n)
				}
				i++
			case n == '~':
				if !cur.skip {
					b.WriteByte(' ')
				}
				i++
			case n == '\n' || n == '\r':
				if !cur.skip {
					b.WriteByte('\n')
				}
				i++
			case isAlpha(n):
				flushHex()
				j := i + 1
				for j < len(data) && isAlpha(data[j]) {
					j++
				}
				word := string(data[i+1 : j])
				k := j
				if k < len(data) && (data[k] == '-' || isDigit(data[k])) {
					k++
					for k < len(data) && isDigit(data[k]) {
						k++
					}
				}
				param := string(data[j:k])
				if k < len(data) && data[k] == ' ' {
					k++
				}
				i = k - 1
				if groupStart && rtfSkip[word] {
					cur.skip = true
				}
				groupStart = false
				if cur.skip {
					continue
				}
				switch word {
				case "par", "line", "sect", "page", "row":
					b.WriteByte('\n')
				case "tab", "cell":
					b.WriteByte('\t')
				case "uc":
					cur.uc = atoiSafe(param)
				case "u":
					v := atoiSafe(param)
					if v < 0 {
						v += 65536
					}
					b.WriteRune(rune(v))
					skipFallback = cur.uc
				case "emdash":
					b.WriteString("—")
				case "endash":
					b.WriteString("–")
				case "bullet":
					b.WriteString("•")
				case "lquote", "rquote":
					b.WriteByte('\'')
				case "ldblquote", "rdblquote":
					b.WriteByte('"')
				}
				continue
			default:
				i++
			}
			groupStart = false
			continue
		case '\r', '\n':
			continue
		}
		groupStart = false
		if skipFallback > 0 {
			skipFallback--
			continue
		}
		flushHex()
		if !cur.skip {
			b.WriteByte(c)
		}
	}
	flushHex()
	return b.String()
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func atoiSafe(s string) int {
	n, neg := 0, false
	for i, c := range []byte(s) {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			break
		}
	}
	if neg {
		return -n
	}
	return n
}
