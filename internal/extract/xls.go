package extract

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/richardlehane/mscfb"
)

// BIFF8 record types ([MS-XLS] 2.3).
const (
	recFormula    = 0x0006
	recEOF        = 0x000A
	recDateMode   = 0x0022
	recContinue   = 0x003C
	recBoundSheet = 0x0085
	recMulRK      = 0x00BD
	recXF         = 0x00E0
	recSST        = 0x00FC
	recLabelSST   = 0x00FD
	recNumber     = 0x0203
	recLabel      = 0x0204
	recBoolErr    = 0x0205
	recString     = 0x0207
	recRK         = 0x027E
	recFormat     = 0x041E
	recBOF        = 0x0809
)

type biffRecord struct {
	typ  uint16
	data []byte
	// conts holds the payloads of directly following CONTINUE records.
	conts [][]byte
}

// readRecords splits a workbook stream into records, attaching CONTINUE
// records to their predecessor.
func readRecords(stream []byte) []biffRecord {
	var recs []biffRecord
	for len(stream) >= 4 {
		typ := binary.LittleEndian.Uint16(stream)
		n := int(binary.LittleEndian.Uint16(stream[2:]))
		if 4+n > len(stream) {
			break
		}
		data := stream[4 : 4+n]
		stream = stream[4+n:]
		if typ == recContinue && len(recs) > 0 {
			recs[len(recs)-1].conts = append(recs[len(recs)-1].conts, data)
			continue
		}
		recs = append(recs, biffRecord{typ: typ, data: data})
	}
	return recs
}

type xlsCell struct {
	row, col int
	text     string
}

// xlsText extracts the cells of a legacy Excel (BIFF8, Excel 97-2003) workbook.
func xlsText(p string) (string, error) {
	data, err := readCapped(p, maxLegacyFile)
	if err != nil {
		return "", err
	}
	cfb, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	var stream []byte
	for entry, err := cfb.Next(); err == nil; entry, err = cfb.Next() {
		if entry.Name == "Workbook" || entry.Name == "Book" {
			if stream, err = io.ReadAll(entry); err != nil {
				return "", err
			}
			break
		}
	}
	if len(stream) < 8 || binary.LittleEndian.Uint16(stream) != recBOF {
		return "", errors.New("no workbook stream")
	}
	if v := binary.LittleEndian.Uint16(stream[4:]); v != 0x0600 {
		return "", errors.New("unsupported Excel version (only BIFF8 / Excel 97+)")
	}

	wb := &xlsWorkbook{formats: map[int]string{}}
	type sheet struct {
		name   string
		offset int
	}
	var sheets []sheet

	// Globals substream: everything up to the first EOF.
	off := 0
	for off+4 <= len(stream) {
		typ := binary.LittleEndian.Uint16(stream[off:])
		n := int(binary.LittleEndian.Uint16(stream[off+2:]))
		if off+4+n > len(stream) {
			break
		}
		rec := stream[off+4 : off+4+n]
		next := off + 4 + n
		switch typ {
		case recBoundSheet:
			if len(rec) >= 8 && rec[5] == 0 { // worksheets only
				sheets = append(sheets, sheet{shortXLString(rec[6:]), int(binary.LittleEndian.Uint32(rec))})
			}
		case recSST:
			// Gather the SST together with its CONTINUE records.
			r := biffRecord{data: rec}
			for next+4 <= len(stream) && binary.LittleEndian.Uint16(stream[next:]) == recContinue {
				m := int(binary.LittleEndian.Uint16(stream[next+2:]))
				if next+4+m > len(stream) {
					break
				}
				r.conts = append(r.conts, stream[next+4:next+4+m])
				next += 4 + m
			}
			wb.sst = readSST(r)
		case recFormat:
			if len(rec) >= 5 {
				s, _ := xlUnicodeString(rec[2:])
				wb.formats[int(binary.LittleEndian.Uint16(rec))] = s
			}
		case recXF:
			if len(rec) >= 4 {
				wb.xfFormats = append(wb.xfFormats, int(binary.LittleEndian.Uint16(rec[2:])))
			}
		case recDateMode:
			wb.date1904 = len(rec) >= 2 && binary.LittleEndian.Uint16(rec) == 1
		}
		off = next
		if typ == recEOF {
			break
		}
	}

	var b strings.Builder
	for _, sh := range sheets {
		if sh.offset <= 0 || sh.offset >= len(stream) {
			continue
		}
		cells := wb.sheetCells(readRecords(stream[sh.offset:]))
		if len(cells) == 0 {
			continue
		}
		b.WriteString("## " + sh.name + "\n")
		writeCells(&b, cells)
		b.WriteString("\n")
		if b.Len() >= MaxText {
			break
		}
	}
	return b.String(), nil
}

type xlsWorkbook struct {
	sst       []string
	formats   map[int]string // custom number formats by id
	xfFormats []int          // number format id per XF index
	date1904  bool
}

func (wb *xlsWorkbook) number(xf int, v float64) string {
	kind := dateNone
	if xf >= 0 && xf < len(wb.xfFormats) {
		id := wb.xfFormats[xf]
		kind = numFmtKind(id, wb.formats[id])
	}
	return formatNumber(v, kind, wb.date1904)
}

func (wb *xlsWorkbook) sheetCells(recs []biffRecord) []xlsCell {
	var cells []xlsCell
	add := func(row, col int, text string) {
		if text = strings.TrimSpace(text); text != "" {
			cells = append(cells, xlsCell{row, col, text})
		}
	}
	u16 := func(b []byte, i int) int { return int(binary.LittleEndian.Uint16(b[i:])) }
	pendingFormula := [2]int{-1, -1}
	for i, r := range recs {
		d := r.data
		if i > 0 && r.typ == recEOF {
			break
		}
		switch r.typ {
		case recLabelSST:
			if len(d) >= 10 {
				if idx := int(binary.LittleEndian.Uint32(d[6:])); idx < len(wb.sst) {
					add(u16(d, 0), u16(d, 2), wb.sst[idx])
				}
			}
		case recLabel:
			if len(d) >= 9 {
				s, _ := xlUnicodeString(d[6:])
				add(u16(d, 0), u16(d, 2), s)
			}
		case recNumber:
			if len(d) >= 14 {
				v := math.Float64frombits(binary.LittleEndian.Uint64(d[6:]))
				add(u16(d, 0), u16(d, 2), wb.number(u16(d, 4), v))
			}
		case recRK:
			if len(d) >= 10 {
				add(u16(d, 0), u16(d, 2), wb.number(u16(d, 4), rkValue(binary.LittleEndian.Uint32(d[6:]))))
			}
		case recMulRK:
			if len(d) >= 6 {
				row, col := u16(d, 0), u16(d, 2)
				for j := 4; j+6 <= len(d)-2; j += 6 {
					add(row, col, wb.number(u16(d, j), rkValue(binary.LittleEndian.Uint32(d[j+2:]))))
					col++
				}
			}
		case recBoolErr:
			if len(d) >= 8 && d[7] == 0 {
				add(u16(d, 0), u16(d, 2), map[byte]string{0: "FALSE", 1: "TRUE"}[d[6]])
			}
		case recFormula:
			if len(d) < 14 {
				continue
			}
			row, col := u16(d, 0), u16(d, 2)
			res := d[6:14]
			if res[6] == 0xFF && res[7] == 0xFF {
				switch res[0] {
				case 0: // string result in the following STRING record
					pendingFormula = [2]int{row, col}
				case 1:
					add(row, col, map[byte]string{0: "FALSE", 1: "TRUE"}[res[2]])
				}
				continue
			}
			add(row, col, wb.number(u16(d, 4), math.Float64frombits(binary.LittleEndian.Uint64(res))))
		case recString:
			if pendingFormula[0] >= 0 {
				s, _ := xlUnicodeString(d)
				add(pendingFormula[0], pendingFormula[1], s)
				pendingFormula = [2]int{-1, -1}
			}
		}
	}
	return cells
}

func writeCells(b *strings.Builder, cells []xlsCell) {
	sort.SliceStable(cells, func(i, j int) bool {
		if cells[i].row != cells[j].row {
			return cells[i].row < cells[j].row
		}
		return cells[i].col < cells[j].col
	})
	for i, c := range cells {
		if i > 0 {
			if c.row != cells[i-1].row {
				b.WriteByte('\n')
			} else {
				b.WriteByte('\t')
			}
		}
		b.WriteString(c.text)
	}
	b.WriteByte('\n')
}

// rkValue decodes the compressed RK number format.
func rkValue(rk uint32) float64 {
	var v float64
	if rk&0x02 != 0 {
		v = float64(int32(rk) >> 2)
	} else {
		v = math.Float64frombits(uint64(rk&0xFFFFFFFC) << 32)
	}
	if rk&0x01 != 0 {
		v /= 100
	}
	return v
}

// shortXLString decodes a ShortXLUnicodeString (8-bit length).
func shortXLString(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	n, flags := int(b[0]), b[1]
	return decodeXLChars(b[2:], n, flags&1 != 0)
}

// xlUnicodeString decodes an XLUnicodeString (16-bit length) and returns the
// number of bytes consumed.
func xlUnicodeString(b []byte) (string, int) {
	if len(b) < 3 {
		return "", len(b)
	}
	n, flags := int(binary.LittleEndian.Uint16(b)), b[2]
	high := flags&1 != 0
	size := n
	if high {
		size *= 2
	}
	return decodeXLChars(b[3:], n, high), 3 + min(size, len(b)-3)
}

func decodeXLChars(b []byte, n int, high bool) string {
	if high {
		n = min(n, len(b)/2)
		u := make([]uint16, n)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(b[2*i:])
		}
		return string(utf16.Decode(u))
	}
	n = min(n, len(b))
	r := make([]rune, n) // compressed: Latin-1 code points
	for i := range r {
		r[i] = rune(b[i])
	}
	return string(r)
}

// sstReader walks the shared string table across CONTINUE boundaries. When
// a string's characters continue in the next record, that record starts
// with a fresh flags byte telling whether the rest is compressed.
type sstReader struct {
	parts [][]byte
	part  int
	pos   int
}

func (r *sstReader) avail() int {
	for r.part < len(r.parts) && r.pos >= len(r.parts[r.part]) {
		r.part++
		r.pos = 0
	}
	if r.part >= len(r.parts) {
		return 0
	}
	return len(r.parts[r.part]) - r.pos
}

func (r *sstReader) bytes(n int) []byte {
	var out []byte
	for n > 0 && r.avail() > 0 {
		k := min(n, r.avail())
		out = append(out, r.parts[r.part][r.pos:r.pos+k]...)
		r.pos += k
		n -= k
	}
	return out
}

func (r *sstReader) skip(n int) { r.bytes(n) }

func readSST(rec biffRecord) []string {
	if len(rec.data) < 8 {
		return nil
	}
	unique := int(binary.LittleEndian.Uint32(rec.data[4:]))
	r := &sstReader{parts: append([][]byte{rec.data[8:]}, rec.conts...)}
	out := make([]string, 0, min(unique, 1<<20))
	for len(out) < unique && r.avail() >= 3 {
		hdr := r.bytes(3)
		n, flags := int(binary.LittleEndian.Uint16(hdr)), hdr[2]
		runs, ext := 0, 0
		if flags&0x08 != 0 {
			runs = int(binary.LittleEndian.Uint16(append(r.bytes(2), 0, 0)))
		}
		if flags&0x04 != 0 {
			ext = int(binary.LittleEndian.Uint32(append(r.bytes(4), 0, 0, 0, 0)))
		}
		high := flags&1 != 0
		var sb strings.Builder
		for n > 0 && r.part < len(r.parts) {
			cur := r.parts[r.part]
			if r.pos >= len(cur) {
				// The characters continue in the next record, which
				// starts with a new flags byte.
				r.part, r.pos = r.part+1, 0
				if r.part >= len(r.parts) || len(r.parts[r.part]) == 0 {
					break
				}
				high = r.parts[r.part][0]&1 != 0
				r.pos = 1
				continue
			}
			width := 1
			if high {
				width = 2
			}
			k := min(n, (len(cur)-r.pos)/width)
			if k == 0 { // a two-byte char cannot straddle records; resync
				r.pos = len(cur)
				continue
			}
			sb.WriteString(decodeXLChars(cur[r.pos:r.pos+k*width], k, high))
			r.pos += k * width
			n -= k
		}
		r.skip(4*runs + ext)
		out = append(out, sb.String())
	}
	return out
}

type dateKind int

const (
	dateNone dateKind = iota
	dateOnly
	dateTime
	timeOnly
)

// numFmtKind classifies a spreadsheet number format by id (built-in
// formats) or by its format code.
func numFmtKind(id int, code string) dateKind {
	switch {
	case id >= 14 && id <= 17:
		return dateOnly
	case id == 22:
		return dateTime
	case id >= 18 && id <= 21, id >= 45 && id <= 47:
		return timeOnly
	case id >= 27 && id <= 36, id >= 50 && id <= 58: // East Asian dates
		return dateOnly
	}
	if code == "" {
		return dateNone
	}
	// Ignore quoted literals, escaped characters and [color]/[$-locale]
	// sections; only consider the first (positive) section.
	var clean strings.Builder
	inQuote, inBracket := false, false
	for i := 0; i < len(code); i++ {
		c := code[i]
		switch {
		case inQuote:
			inQuote = c != '"'
		case inBracket:
			inBracket = c != ']'
		case c == '"':
			inQuote = true
		case c == '[':
			inBracket = true
		case c == '\\' || c == '_' || c == '*':
			i++
		case c == ';':
			i = len(code)
		default:
			clean.WriteByte(c | 0x20) // lowercase ASCII letters
		}
	}
	s := clean.String()
	hasDate := strings.ContainsAny(s, "yd") || strings.Contains(s, "mmm")
	hasTime := strings.ContainsAny(s, "hs")
	switch {
	case hasDate && hasTime:
		return dateTime
	case hasDate:
		return dateOnly
	case hasTime:
		return timeOnly
	case strings.Contains(s, "m") && !strings.ContainsAny(s, "0#?"):
		return dateOnly // e.g. "mm/yy" without y would be odd; plain "m" is a month
	}
	return dateNone
}

func formatNumber(v float64, kind dateKind, date1904 bool) string {
	if kind == dateNone || v < 0 || v > 2958465 { // beyond 9999-12-31
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	epoch := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if date1904 {
		epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	t := epoch.Add(time.Duration(math.Round(v*86400)) * time.Second)
	switch kind {
	case timeOnly:
		return t.Format("15:04:05")
	case dateTime:
		return t.Format("2006-01-02 15:04")
	}
	return t.Format("2006-01-02")
}
