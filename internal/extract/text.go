package extract

import (
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// readCapped reads at most limit bytes from path.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

func plainText(path string) (string, error) {
	data, err := readCapped(path, MaxText)
	if err != nil {
		return "", err
	}
	return decodeLegacy(data), nil
}

// decodeLegacy returns data as string, treating it as Windows-1252 when it is
// not valid UTF-8 (common for older text files).
func decodeLegacy(data []byte) string {
	if valid := utf8.Valid(data); valid || utf8.Valid(trimPartialRune(data)) {
		return string(data)
	}
	if s, err := charmap.Windows1252.NewDecoder().Bytes(data); err == nil {
		return string(s)
	}
	return string(data)
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of data,
// which happens when a file was cut off at the read limit.
func trimPartialRune(data []byte) []byte {
	for i := 1; i <= 3 && i <= len(data); i++ {
		if utf8.RuneStart(data[len(data)-i]) {
			return data[:len(data)-i]
		}
	}
	return data
}
