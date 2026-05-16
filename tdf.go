// Package tdf parses TheDraw Font (.tdf) files and renders text using them.
//
// Format summary:
//   - 20-byte file signature + 4 magic bytes
//   - One or more font records, each containing:
//       • 1-byte name length + N-byte name (null-padded)
//       • 6 bytes of metadata (type, letter spacing, block size, …)
//       • 95 × 2-byte LE offset table (chars 32–126; 0xFFFF = undefined)
//       • Character data section (offsets are relative to this section's start)
//   - Each character: 2-byte (width, height) preamble, then row data
//       terminated by 0x0D (row delimiter) and 0x00 (glyph terminator).
//       For Color fonts (type 2) row data is (cp437_char, cga_attr) pairs;
//       for Outline (0) and Block (1) fonts it is plain CP437 bytes with
//       no per-cell attribute.
//   - Upper and lowercase letters share the same glyphs.
//   - Space (char 32) offset points past end of char data — no glyph; use SpaceWidth.
package tdf

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const fileSig = "\x13TheDraw FONTS file\x1a"

// numChars is the number of characters in the offset table (ASCII 33–126 inclusive).
// Space (32) is not in the table; its width is derived from the other glyphs.
const numChars = 94

// firstChar is the first ASCII value present in the offset table.
const firstChar = 33

// Cell is one character cell: a CP437 byte and a CGA attribute byte.
// Attribute byte: bits 0–3 = foreground color index, bits 4–6 = background color index.
type Cell struct {
	Char byte
	Attr byte
}

// Glyph holds the cell grid for one character.
type Glyph struct {
	Width  int
	Height int
	Rows   [][]Cell
}

// Font is a parsed TheDraw font.
type Font struct {
	Name          string
	Type          byte // 0=Outline, 1=Block, 2=Color
	LetterSpacing int // columns to insert between adjacent glyphs
	SpaceWidth    int // columns to emit for ASCII space (derived from context)
	glyphs        [numChars]*Glyph
}

// Glyph returns the glyph for rune r, trying the opposite case as a fallback.
// Returns nil if the character is not defined.
func (f *Font) Glyph(r rune) *Glyph {
	if g := f.glyphDirect(r); g != nil {
		return g
	}
	// Case fallback: TDF fonts often define only one case.
	if r >= 'a' && r <= 'z' {
		return f.glyphDirect(r - 32)
	}
	if r >= 'A' && r <= 'Z' {
		return f.glyphDirect(r + 32)
	}
	return nil
}

func (f *Font) glyphDirect(r rune) *Glyph {
	idx := int(r) - firstChar
	if idx < 0 || idx >= numChars {
		return nil
	}
	return f.glyphs[idx]
}

// ParseFile parses a .tdf file and returns all fonts it contains.
func ParseFile(data []byte) ([]*Font, error) {
	if len(data) < len(fileSig)+4 {
		return nil, fmt.Errorf("tdf: file too short")
	}
	if string(data[:len(fileSig)]) != fileSig {
		return nil, fmt.Errorf("tdf: invalid file signature")
	}

	pos := len(fileSig) // skip 20-byte sig; each font record has its own 4-byte magic prefix

	var fonts []*Font
	for pos < len(data) {
		font, n, err := parseFont(data, pos)
		if err != nil {
			return nil, fmt.Errorf("tdf: font at offset %d: %w", pos, err)
		}
		fonts = append(fonts, font)
		pos += n
	}
	return fonts, nil
}

func parseFont(data []byte, base int) (*Font, int, error) {
	pos := base

	// Each font record begins with a 4-byte magic prefix (55 aa 00 ff).
	if pos+4 > len(data) {
		return nil, 0, fmt.Errorf("unexpected EOF before font magic")
	}
	pos += 4

	// Name: 1-byte length prefix (for display) + 12-byte null-padded field.
	// The field is always 12 bytes regardless of the length prefix value.
	if pos+1+12 > len(data) {
		return nil, 0, fmt.Errorf("name truncated")
	}
	pos++ // skip length prefix
	name := strings.TrimRight(string(data[pos:pos+12]), "\x00")
	pos += 12

	// Metadata: 8 bytes (file offsets 37–44).
	//   Bytes 0–3: padding, unused.
	//   Byte  4:   font type (00=Outline, 01=Block, 02=Color).
	//   Byte  5:   letter spacing — see note below.
	//   Bytes 6–7: block size, LE uint16 = total byte length of char data section.
	if pos+8 > len(data) {
		return nil, 0, fmt.Errorf("metadata truncated")
	}
	fontType := data[pos+4]
	// The stored letter-spacing byte appears to be a 1-indexed advance
	// distance — i.e. one *more* than the desired inter-glyph column gap.
	// Translating with abs(raw - 1) matches every reference rendering in
	// a 1198-font corpus: the dominant stored=2 becomes a 1-col gap,
	// stored=1 (e.g. HOARD) abuts into a continuous wall, and stored=0
	// (the JSF* family, FRISTI) gets the small gap their previews show.
	// We expose the translated value so callers see the literal gap to
	// emit between glyphs. -- claude, 2026-05-16
	raw := int(data[pos+5])
	letterSpacing := raw - 1
	if letterSpacing < 0 {
		letterSpacing = -letterSpacing
	}
	charDataSize := int(binary.LittleEndian.Uint16(data[pos+6:]))
	pos += 8

	// Offset table: 94 × 2-byte LE values, ASCII 33–126.
	// 0xFFFF = character not defined.
	if pos+numChars*2 > len(data) {
		return nil, 0, fmt.Errorf("offset table truncated")
	}
	var offsets [numChars]uint16
	for i := range offsets {
		offsets[i] = binary.LittleEndian.Uint16(data[pos+i*2:])
	}
	charDataBase := pos + numChars*2 // = 233

	font := &Font{
		Name:          name,
		Type:          fontType,
		LetterSpacing: letterSpacing,
	}

	// Parse glyphs. charDataSize (from the block size header field) bounds the
	// section so we don't walk into the next font's data.
	maxEnd := charDataBase + charDataSize
	for i, off := range offsets {
		if off == 0xFFFF || int(off) >= charDataSize {
			continue
		}
		abs := charDataBase + int(off)
		if abs+2 > len(data) {
			continue
		}
		g, _, err := parseGlyph(data, abs, fontType)
		if err != nil {
			continue
		}
		font.glyphs[i] = g
	}

	// Derive a reasonable space width from the average glyph width.
	font.SpaceWidth = spaceWidth(font)

	return font, maxEnd - base, nil
}

func spaceWidth(f *Font) int {
	total, count := 0, 0
	for _, g := range f.glyphs {
		if g != nil {
			total += g.Width
			count++
		}
	}
	if count == 0 {
		return 2
	}
	return total / count
}

// Default CGA attribute used for cells in Outline and Block fonts, which do
// not store a per-cell attribute. 0x0F = bright white on black, the most
// common rendering of these fonts in TheDraw.
const defaultAttr byte = 0x0F

// outlineChars maps the Outline-font "drawing alphabet" (cell byte values
// 0x40..0x4F) to the CP437 characters TheDraw substitutes at render time.
// '@' is "outside the letter" and 'O' is the interior fill — both render as
// blank space. The rest are corners and edges using a mix of single- and
// double-line box-drawing characters, which together produce the stylised
// "outline" look TheDraw is named for. -- claude, 2026-05-16
var outlineChars = [16]byte{
	/* @ */ 0x20,
	/* A */ 0xCD, /* B */ 0xC4, /* C */ 0xB3, /* D */ 0xBA,
	/* E */ 0xD5, /* F */ 0xBB, /* G */ 0xD6, /* H */ 0xBF,
	/* I */ 0xC8, /* J */ 0xBE, /* K */ 0xC0, /* L */ 0xBD,
	/* M */ 0xB5, /* N */ 0xC7, /* O */ 0x20,
}

// outlineCellChar translates a raw Outline-font cell byte into the CP437
// byte that should appear on screen. Bytes outside the drawing alphabet
// are passed through unchanged, so the occasional non-conforming font
// still produces *something* readable rather than vanishing.
func outlineCellChar(b byte) byte {
	if b >= 0x40 && b <= 0x4F {
		return outlineChars[b-0x40]
	}
	return b
}

func parseGlyph(data []byte, pos int, fontType byte) (*Glyph, int, error) {
	if pos+2 > len(data) {
		return nil, 0, fmt.Errorf("glyph preamble truncated")
	}
	width := int(data[pos])
	height := int(data[pos+1])
	pos += 2

	// Only Color fonts (type 2) store a CGA attribute byte after each
	// character byte; Outline (0) and Block (1) fonts store a single byte
	// per cell. The earlier always-pair logic was eating every other byte
	// as a phantom attribute in non-Color fonts, which also let stray 0x0D
	// or 0x00 bytes in those positions short-circuit rows or whole glyphs —
	// hence the "total gibberish" symptom. -- claude, 2026-05-16
	colorFont := fontType == 2

	g := &Glyph{Width: width, Height: height}
	var row []Cell

	for pos < len(data) {
		b := data[pos]
		pos++
		switch b {
		case 0x00:
			if len(row) > 0 {
				g.Rows = append(g.Rows, row)
			}
			return g, pos, nil
		case 0x0D:
			g.Rows = append(g.Rows, row)
			row = nil
		default:
			attr := defaultAttr
			if colorFont {
				if pos >= len(data) {
					return nil, 0, fmt.Errorf("glyph attr byte missing")
				}
				attr = data[pos]
				pos++
			}
			ch := b
			if fontType == 0 {
				ch = outlineCellChar(b)
			}
			row = append(row, Cell{Char: ch, Attr: attr})
		}
	}
	return nil, 0, fmt.Errorf("glyph not terminated")
}

// RenderString renders s using f and returns a multi-line ANSI-colored string,
// one terminal line per glyph row. Characters not in the font are skipped;
// spaces are rendered as blank columns. letterSpacing blank columns are
// inserted between each pair of adjacent glyphs.
func RenderString(f *Font, s string, letterSpacing int) string {
	runes := []rune(s)
	glyphs := make([]*Glyph, len(runes))
	height := 0
	for i, r := range runes {
		if r == ' ' {
			continue
		}
		g := f.Glyph(r)
		glyphs[i] = g
		if g != nil && g.Height > height {
			height = g.Height
		}
	}
	if height == 0 {
		return ""
	}

	lines := make([]string, height)
	for row := 0; row < height; row++ {
		var b strings.Builder
		first := true
		for i, r := range runes {
			g := glyphs[i]
			if r == ' ' || g == nil {
				if !first {
					b.WriteString(strings.Repeat(" ", letterSpacing))
				}
				b.WriteString(strings.Repeat(" ", f.SpaceWidth))
				first = false
				continue
			}
			if !first && letterSpacing > 0 {
				b.WriteString(strings.Repeat(" ", letterSpacing))
			}
			first = false
			if row >= len(g.Rows) {
				b.WriteString(strings.Repeat(" ", g.Width))
				continue
			}
			// TDF rows store only up to the last non-blank cell, so a row
			// can be shorter than the glyph's declared width. We must pad
			// it out to g.Width here — otherwise the next glyph's row would
			// start at a column that varies row-by-row, producing a
			// diagonal "slant" across the rendered word. -- claude, 2026-05-16
			cells := g.Rows[row]
			for _, cell := range cells {
				fg := cgaColor(cell.Attr & 0x0F)
				bg := cgaColor((cell.Attr >> 4) & 0x07)
				ch := cp437Rune(cell.Char)
				b.WriteString(lipgloss.NewStyle().Foreground(fg).Background(bg).Render(string(ch)))
			}
			if pad := g.Width - len(cells); pad > 0 {
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
		lines[row] = b.String()
	}
	return strings.Join(lines, "\n")
}

// cgaColor maps a 4-bit CGA color index to a lipgloss Color.
//
// These are the canonical IBM CGA RGB values rather than ANSI palette
// indices (0..15). Using fixed hex means a real DOS box's colors come
// out the same on every terminal, regardless of the user's theme.
//
// The tdfiglet reference renderer instead emits ANSI SGR codes (30-37,
// 90-97), which lets the terminal's own theme decide the actual RGB —
// usually producing noticeably *lighter* output, since most modern
// themes brighten the "light" CGA slots well past #AAAAAA/#FFFFFF.
// Both are defensible; we picked authenticity over theme-conformance.
// -- claude, 2026-05-16
func cgaColor(idx byte) lipgloss.Color {
	palette := [16]lipgloss.Color{
		"#000000", "#0000AA", "#00AA00", "#00AAAA",
		"#AA0000", "#AA00AA", "#AA5500", "#AAAAAA",
		"#555555", "#5555FF", "#55FF55", "#55FFFF",
		"#FF5555", "#FF55FF", "#FFFF55", "#FFFFFF",
	}
	if idx > 15 {
		return palette[7]
	}
	return palette[idx]
}

// cp437Rune maps a CP437 byte to its Unicode equivalent.
// Only the graphical/block characters common in TDF fonts are mapped;
// everything else falls back to the Unicode CP437 range (U+F000+).
func cp437Rune(b byte) rune {
	// Common block-drawing and shading characters used in TDF fonts.
	cp437 := map[byte]rune{
		0x00: ' ',
		0x01: '☺', 0x02: '☻', 0x03: '♥', 0x04: '♦', 0x05: '♣', 0x06: '♠',
		0x07: '•', 0x08: '◘', 0x09: '○', 0x0A: '◙', 0x0B: '♂', 0x0C: '♀',
		0x0D: '♪', 0x0E: '♫', 0x0F: '☼',
		0x10: '►', 0x11: '◄', 0x12: '↕', 0x13: '‼', 0x14: '¶', 0x15: '§',
		0x16: '▬', 0x17: '↨', 0x18: '↑', 0x19: '↓', 0x1A: '→', 0x1B: '←',
		0x1C: '∟', 0x1D: '↔', 0x1E: '▲', 0x1F: '▼',
		0x20: ' ',
		// 0x21–0x7E are standard ASCII, handled below.
		0x7F: '⌂',
		0x80: 'Ç', 0x81: 'ü', 0x82: 'é', 0x83: 'â', 0x84: 'ä', 0x85: 'à',
		0x86: 'å', 0x87: 'ç', 0x88: 'ê', 0x89: 'ë', 0x8A: 'è', 0x8B: 'ï',
		0x8C: 'î', 0x8D: 'ì', 0x8E: 'Ä', 0x8F: 'Å',
		0x90: 'É', 0x91: 'æ', 0x92: 'Æ', 0x93: 'ô', 0x94: 'ö', 0x95: 'ò',
		0x96: 'û', 0x97: 'ù', 0x98: 'ÿ', 0x99: 'Ö', 0x9A: 'Ü', 0x9B: '¢',
		0x9C: '£', 0x9D: '¥', 0x9E: '₧', 0x9F: 'ƒ',
		0xA0: 'á', 0xA1: 'í', 0xA2: 'ó', 0xA3: 'ú', 0xA4: 'ñ', 0xA5: 'Ñ',
		0xA6: 'ª', 0xA7: 'º', 0xA8: '¿', 0xA9: '⌐', 0xAA: '¬', 0xAB: '½',
		0xAC: '¼', 0xAD: '¡', 0xAE: '«', 0xAF: '»',
		0xB0: '░', 0xB1: '▒', 0xB2: '▓',
		0xB3: '│', 0xB4: '┤', 0xB5: '╡', 0xB6: '╢', 0xB7: '╖', 0xB8: '╕',
		0xB9: '╣', 0xBA: '║', 0xBB: '╗', 0xBC: '╝', 0xBD: '╜', 0xBE: '╛',
		0xBF: '┐',
		0xC0: '└', 0xC1: '┴', 0xC2: '┬', 0xC3: '├', 0xC4: '─', 0xC5: '┼',
		0xC6: '╞', 0xC7: '╟', 0xC8: '╚', 0xC9: '╔', 0xCA: '╩', 0xCB: '╦',
		0xCC: '╠', 0xCD: '═', 0xCE: '╬', 0xCF: '╧',
		0xD0: '╨', 0xD1: '╤', 0xD2: '╥', 0xD3: '╙', 0xD4: '╘', 0xD5: '╒',
		0xD6: '╓', 0xD7: '╫', 0xD8: '╪', 0xD9: '┘', 0xDA: '┌',
		0xDB: '█', 0xDC: '▄', 0xDD: '▌', 0xDE: '▐', 0xDF: '▀',
		0xE0: 'α', 0xE1: 'ß', 0xE2: 'Γ', 0xE3: 'π', 0xE4: 'Σ', 0xE5: 'σ',
		0xE6: 'µ', 0xE7: 'τ', 0xE8: 'Φ', 0xE9: 'Θ', 0xEA: 'Ω', 0xEB: 'δ',
		0xEC: '∞', 0xED: 'φ', 0xEE: 'ε', 0xEF: '∩',
		0xF0: '≡', 0xF1: '±', 0xF2: '≥', 0xF3: '≤', 0xF4: '⌠', 0xF5: '⌡',
		0xF6: '÷', 0xF7: '≈', 0xF8: '°', 0xF9: '∙', 0xFA: '·', 0xFB: '√',
		0xFC: 'ⁿ', 0xFD: '²', 0xFE: '■', 0xFF: '\u00A0',
	}
	if r, ok := cp437[b]; ok {
		return r
	}
	if b >= 0x21 && b <= 0x7E {
		return rune(b) // standard ASCII
	}
	return '?'
}
