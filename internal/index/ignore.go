package index

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// IgnoreFile is the name of the optional file in the search root listing
// paths to leave out, one pattern per line.
const IgnoreFile = ".doclocignore"

// ignoreRules is a small subset of the gitignore syntax:
//
//	# comment
//	bios          a file or directory with this name, anywhere
//	*.log         glob on names, anywhere
//	4_Archive/RG35XX Plus   a path relative to the search root (contains /)
//	tmp/          a trailing slash matches directories only
type ignoreRules []ignoreRule

type ignoreRule struct {
	pattern string
	anchor  bool // match the whole relative path instead of a name
	dirOnly bool
}

func loadIgnore(root string) ignoreRules {
	f, err := os.Open(filepath.Join(root, IgnoreFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	var rules ignoreRules
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if strings.Contains(line, "/") {
			r.anchor = true
			line = strings.TrimPrefix(line, "/")
		}
		if line == "" {
			continue
		}
		r.pattern = line
		rules = append(rules, r)
	}
	return rules
}

// match reports whether the entry at rel (slash separated, relative to the
// root) is ignored.
func (rules ignoreRules) match(rel string, isDir bool) bool {
	name := path.Base(rel)
	for _, r := range rules {
		if r.dirOnly && !isDir {
			continue
		}
		subject := name
		if r.anchor {
			subject = rel
		}
		if ok, _ := path.Match(r.pattern, subject); ok {
			return true
		}
	}
	return false
}
