# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build / run / test

```sh
go build ./...
go test ./...
go vet ./...

# Run the CLI:
go run ./cmd/tdfprint -list path/to/file.tdf
go run ./cmd/tdfprint -n 2 -b 1 path/to/file.tdf "HELLO"
```

There is no test suite yet; `go test ./...` is a no-op.

## Architecture

Single Go package `tdf` (in `tdf.go`) plus one CLI in `cmd/tdfprint/`.

The package parses TheDraw `.tdf` font files — a DOS-era format where each glyph is a grid of `(CP437 byte, CGA attribute byte)` cells — and renders strings using `lipgloss` to emit ANSI-colored multi-line output.

Key things to know before editing:

- **File layout** (documented in the package doc comment at the top of `tdf.go`):
  20-byte file signature → one or more font records. Each font record is `4-byte magic` + `1-byte name length` + `12-byte name field` (always 12 bytes regardless of the length prefix) + `8 bytes metadata` + `94 × uint16 LE offset table` (ASCII 33–126; `0xFFFF` = undefined) + char data section. Offsets in the table are **relative to the start of the char data section**, not the file.
- **Glyph encoding**: 2-byte `(width, height)` preamble, then rows of `(char, attr)` pairs terminated by `0x0D` (row delimiter) or `0x00` (glyph terminator).
- **Char data size** (bytes 6–7 of metadata) bounds the section so `parseFont` knows where one font ends and the next begins — the return value `maxEnd - base` is what advances `pos` in `ParseFile`.
- **Space (ASCII 32) has no offset entry.** `spaceWidth()` derives a reasonable width from the average glyph width.
- **Case fallback**: TDF fonts typically define only one case, so `Font.Glyph` falls back to the opposite case before giving up.
- **CGA attribute byte**: low nibble = fg color index (0–15), bits 4–6 = bg color index (0–7). `cgaColor()` maps these to a 16-color palette.
- **CP437 → Unicode** mapping lives in `cp437Rune()`; only graphical/block chars common in TDF fonts are mapped explicitly, plain ASCII passes through, and unknowns become `?`.

When changing the parser, remember that malformed glyphs are tolerated silently (`continue` in `parseFont`) rather than failing the whole font — preserve that behavior unless you have a reason not to.
