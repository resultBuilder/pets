package render

import (
	"image"
	"image/color"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Go Regular ships Latin, Greek, and Cyrillic glyphs, so bubbles with
// session titles or paths render correctly without system font lookups.
var (
	fontOnce   sync.Once
	parsedFont *sfnt.Font
)

func sharedFont() *sfnt.Font {
	fontOnce.Do(func() {
		parsed, err := opentype.Parse(goregular.TTF)
		if err == nil {
			parsedFont = parsed
		}
	})
	return parsedFont
}

func newFace(sizePx int) font.Face {
	parsed := sharedFont()
	if parsed == nil {
		return nil
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{
		Size:    float64(sizePx),
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil
	}
	return face
}

func textWidth(face font.Face, text string) int {
	return font.MeasureString(face, text).Ceil()
}

// wrapText breaks text into at most maxLines lines that each fit maxWidth
// when measured with face. Splitting happens on spaces, or inside a word by
// runes when a single word is too wide; the last line is ellipsized when
// text remains.
func wrapText(face font.Face, text string, maxWidth int, maxLines int) []string {
	words := strings.Fields(text)
	if len(words) == 0 || maxLines < 1 {
		return nil
	}
	var lines []string
	current := ""
	flush := func() bool {
		if current == "" {
			return len(lines) < maxLines
		}
		lines = append(lines, current)
		current = ""
		return len(lines) < maxLines
	}
	for index := 0; index < len(words); index++ {
		word := words[index]
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if textWidth(face, candidate) <= maxWidth {
			current = candidate
			continue
		}
		if current != "" && !flush() {
			return ellipsize(face, lines, words[index:], maxWidth)
		}
		for textWidth(face, word) > maxWidth {
			head, tail := splitToWidth(face, word, maxWidth)
			if head == "" {
				break
			}
			lines = append(lines, head)
			word = tail
			if len(lines) >= maxLines {
				return ellipsize(face, lines, append([]string{word}, words[index+1:]...), maxWidth)
			}
		}
		current = word
	}
	flush()
	return lines
}

// splitToWidth cuts the longest rune prefix of word that fits maxWidth.
func splitToWidth(face font.Face, word string, maxWidth int) (string, string) {
	runes := []rune(word)
	for count := len(runes); count > 0; count-- {
		head := string(runes[:count])
		if textWidth(face, head) <= maxWidth {
			return head, string(runes[count:])
		}
	}
	return string(runes[:1]), string(runes[1:])
}

func ellipsize(face font.Face, lines []string, remaining []string, maxWidth int) []string {
	if len(lines) == 0 {
		return lines
	}
	leftover := strings.Join(remaining, " ")
	if leftover == "" {
		return lines
	}
	last := lines[len(lines)-1]
	runes := []rune(last)
	for len(runes) > 0 && textWidth(face, string(runes)+"…") > maxWidth {
		runes = runes[:len(runes)-1]
	}
	lines[len(lines)-1] = string(runes) + "…"
	return lines
}

func drawText(dst *image.NRGBA, face font.Face, x int, baselineY int, text string, textColor color.NRGBA) {
	drawer := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(textColor),
		Face: face,
		Dot:  fixed.P(x, baselineY),
	}
	drawer.DrawString(text)
}

func lineHeight(face font.Face) int {
	metrics := face.Metrics()
	return (metrics.Ascent + metrics.Descent).Ceil()
}

func ascent(face font.Face) int {
	return face.Metrics().Ascent.Ceil()
}
