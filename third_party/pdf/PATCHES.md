# Vendored copy of github.com/ledongthuc/pdf

Version v0.0.0-20261020125853-5e46805f718e, BSD license (see LICENSE).
Wired in through a `replace` directive in docloc's go.mod.

Local changes (marked with `docloc:` comments) make it more forgiving with
malformed files, the way poppler is:

- lex.go: stray delimiters, unknown string escapes, octal overflow and
  malformed `#xx` name escapes no longer abort parsing.
- read.go: V=4 encryption with RC4 crypt filters (CFM /V2) is supported in
  addition to AESV2.
- page.go: a Type0 font's ToUnicode CMap takes precedence over /Encoding.
