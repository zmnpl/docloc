# docloc

Find any document in a directory tree by name **and content**, from the terminal.
docloc is a single, self-contained Go binary. It needs no pdftotext, fzf, ag or
other system tools.

```
❯ rechnung telekom                                                     exact
  50/1448
▌ 2_Areas/Steuer/2021/Telekom/2021_Telekom_02rechnung_5621292086.pdf
  2_Areas/Steuer/2021/Telekom/2021_Telekom_01rechnung_5621292086.pdf
  …
── 2_Areas/Steuer/2021/Telekom/2021_Telekom_02rechnung_5621292086.pdf ── 1/76
 Telekom Deutschland GmbH, 53171 Bonn
 …
```

## Build

```sh
go build -o docloc .
```

## Usage

```sh
cd ~/Documents
docloc              # search the current directory
docloc ~/Archive    # or any other directory
```

Indexing runs in the background while you type. Documents found in the cache
appear almost at once; new or changed ones are extracted in parallel and
join the results as they come in.

| Key                      | Action                                         |
|--------------------------|------------------------------------------------|
| typing                   | search (results update as you type)            |
| `↑`/`↓`, `ctrl+p/n`      | move through results                           |
| `pgup`/`pgdown`          | page through results                           |
| `ctrl+u`/`ctrl+d`        | scroll preview half a page                     |
| `shift+↑`/`shift+↓`      | scroll preview by a line                       |
| `alt+↓`/`alt+↑` (`alt+n/p`) | jump to the next/previous match in the preview |
| `tab`                    | switch between exact and fuzzy matching        |
| `enter`                  | open with the default app (`xdg-open`) and quit |
| `ctrl+o`                 | open and keep docloc running                   |
| `ctrl+f`                 | open the containing folder                     |
| `esc`                    | clear the query, or quit if it is empty        |
| `ctrl+c`                 | quit                                           |

### Query syntax

- Space-separated terms must **all** match, either in the relative path or in the content (case-insensitive).
- `!term` excludes documents containing `term`.
- **exact** mode (default): each term must appear as a substring.
- **fuzzy** mode: terms also match words with typos (1 edit from 4 letters on, 2 edits from 8 letters on),
  prefixes, and letters in order within a word or the file name (`tlkrchng` finds `telekom_rechnung.pdf`).

Results are ranked by matches in the file name, then the path, then how often terms occur in the content.
With an empty query, the most recently modified documents come first.

### Ignoring directories

A `.doclocignore` file in the searched directory lists paths to skip, one per line:

```
# a file or directory name anywhere (globs work)
bios
*.log.txt
# a path relative to the searched directory
4_Archive/RG35XX Plus
# a trailing slash matches directories only
tmp/
```

Hidden directories are skipped unless you pass `-hidden`.

### Flags

```
-fuzzy          start in fuzzy mode
-hidden         also index hidden directories (skipped by default)
-print          print the selected path on enter (or its folder on ctrl+f)
                instead of opening it, e.g. okular "$(docloc -print)"
-cache PATH     cache database location (default ~/.cache/docloc/cache.db)
-clear-cache    delete the cache and exit
-extract FILE   print the text docloc extracts from FILE (for debugging)
```

## Supported formats

| Format                         | Extensions                         |
|--------------------------------|------------------------------------|
| PDF (text layer)               | `.pdf`                             |
| Word                           | `.docx` `.docm` `.dotx` `.doc` `.dot` |
| Excel                          | `.xlsx` `.xlsm` `.xls`             |
| PowerPoint (slides + notes)    | `.pptx` `.pptm`                    |
| LibreOffice / OpenDocument     | `.odt` `.ott` `.ods` `.ots` `.odp` `.otp` `.fodt` `.fods` |
| Rich text                      | `.rtf` (also `.doc` files that are really RTF) |
| Web pages                      | `.html` `.htm` `.xhtml`            |
| E-books                        | `.epub` (chapters in reading order) |
| Plain text / tables            | `.txt` `.text` `.md` `.markdown` `.csv` `.tsv` |

Up to 1 MiB of text is indexed per document. Spreadsheet dates come out as
`YYYY-MM-DD` so you can search for them.

Limitations:

- Scanned PDFs without a text layer can only be found by file name (there is no OCR).
  PDFs OCR'd with e.g. OCRmyPDF work fine.
- Legacy `.doc` (Word 97–2003) support is best effort and handles only the main text.
  Encrypted `.doc` files and files from Word 6/95 are skipped.
- `.xls` must be BIFF8 (Excel 97 or newer).
- Files that fail to extract can still be found by name. The preview says why there is no text.

## How it works

- **Cache**: a bbolt database maps each file's content hash (xxhash) to its
  extracted text, deflate-compressed (about 3× smaller), so moved or copied
  documents are not processed again. A second bucket remembers size and mtime
  per path, which lets an unchanged file skip hashing. Like fast-p, known
  documents are loaded first, then the new ones are extracted. After a
  complete run, entries for deleted files below the searched directory are
  pruned, along with texts no file refers to. Entries from other directories
  you searched are kept. When most of the file is unused space, it gets
  compacted on the next start.
- **Search** runs in a background goroutine and is cancelled when you keep typing.
  Typing more of the same query narrows the previous results instead of
  searching everything again. Fuzzy mode uses an in-memory inverted word index.
- **PDF** text is read with a vendored, patched copy of
  [ledongthuc/pdf](https://github.com/ledongthuc/pdf) (see
  `third_party/pdf/PATCHES.md`). docloc runs its own content-stream walker on
  top of it. The walker follows form XObjects and uses real glyph widths, CID
  fonts included, to find word and line breaks.
- **HTML/EPUB** text comes from `golang.org/x/net/html`, which also detects the character set.
- **Office** formats are parsed directly: OOXML and ODF via `encoding/xml`,
  legacy `.doc`/`.xls` via the compound file reader
  [mscfb](https://github.com/richardlehane/mscfb) plus small decoders for the
  Word piece table and the BIFF8 records.

## License

MIT, see [LICENSE](LICENSE). The vendored PDF library in `third_party/pdf`
keeps its original BSD license ([third_party/pdf/LICENSE](third_party/pdf/LICENSE)).
