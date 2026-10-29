// docloc finds documents by name and content in a directory tree.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/simonpaul/docloc/internal/extract"
	"github.com/simonpaul/docloc/internal/index"
	"github.com/simonpaul/docloc/internal/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "docloc:", err)
		os.Exit(1)
	}
}

func run() error {
	fuzzy := flag.Bool("fuzzy", false, "start in fuzzy instead of exact matching mode")
	hidden := flag.Bool("hidden", false, "also index hidden directories")
	printOnly := flag.Bool("print", false, "print the selected path on enter instead of opening it")
	cachePath := flag.String("cache", "", "cache database `path` (default: user cache dir)")
	clearCache := flag.Bool("clear-cache", false, "delete the cache database and exit")
	extractFile := flag.String("extract", "", "print the text extracted from `file` and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: docloc [flags] [directory]\n\n"+
			"Search pdf, doc(x), xls(x), pptx, odt/ods/odp, rtf, html, epub, csv, txt and md\n"+
			"documents by name and content.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *extractFile != "" {
		text, err := extract.File(*extractFile)
		fmt.Println(text)
		return err
	}

	if *cachePath == "" {
		p, err := index.DefaultCachePath()
		if err != nil {
			return err
		}
		*cachePath = p
	}
	if *clearCache {
		if err := os.Remove(*cachePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Println("removed", *cachePath)
		return nil
	}

	root := "."
	if flag.NArg() > 1 {
		flag.Usage()
		os.Exit(2)
	}
	if flag.NArg() == 1 {
		root = flag.Arg(0)
	}
	if fi, err := os.Stat(root); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	root = filepath.Clean(root)

	warning := ""
	cache, err := index.OpenCache(*cachePath)
	if err != nil {
		// Searching still works, just without persistence.
		warning = "cache unavailable (" + err.Error() + "), nothing will be remembered"
		cache = nil
	}
	defer cache.Close()

	store := index.NewStore()
	ix := &index.Indexer{Root: root, Hidden: *hidden, Cache: cache, Store: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ix.Run(ctx)

	mode := index.Exact
	if *fuzzy {
		mode = index.Fuzzy
	}
	model := ui.New(ui.Options{Store: store, Indexer: ix, Mode: mode, PrintOnly: *printOnly, Warning: warning})
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if *printOnly {
		// Keep stdout clean for the printed path, e.g. in $(docloc -print).
		opts = append(opts, tea.WithOutput(os.Stderr))
	}
	if _, err := tea.NewProgram(model, opts...).Run(); err != nil {
		return err
	}
	if p := model.Chosen(); p != "" {
		fmt.Println(p)
	}
	return nil
}
