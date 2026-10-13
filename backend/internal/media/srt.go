package media

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

// decodeSubtitleText returns the subtitle file as UTF-8 without a BOM.
// UTF-16 is recognised by its BOM; anything that is not valid UTF-8 is read
// as Windows-1252 (a superset of the printable ISO-8859-1 range), the usual
// encoding of older Western European SRT files.
func decodeSubtitleText(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		b = b[3:]
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		dec := unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder()
		if out, err := dec.Bytes(b); err == nil {
			return strings.TrimPrefix(string(out), "\uFEFF")
		}
	}
	if utf8.Valid(b) {
		return string(b)
	}
	out, err := charmap.Windows1252.NewDecoder().Bytes(b)
	if err != nil {
		return strings.ToValidUTF8(string(b), "\uFFFD")
	}
	return string(out)
}

var (
	srtTimestamp = regexp.MustCompile(`(\d{1,2}):(\d{1,2}):(\d{1,2})(?:[,.](\d{1,3}))?`)
	// ASS override blocks ({\an8}, {\i1}) and <font> tags: not WebVTT.
	srtASSTag  = regexp.MustCompile(`\{\\[^}]*\}`)
	srtFontTag = regexp.MustCompile(`(?i)</?font[^>]*>`)
)

// vttTimestamp normalises one SRT timestamp ("1:02:03,4") to "01:02:03.400".
func vttTimestamp(m []string) string {
	ms := m[4]
	for len(ms) < 3 {
		ms += "0"
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	s, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%02d:%02d:%02d.%s", h, mi, s, ms)
}

// timingLine converts "00:00:01,000 --> 00:00:02,500 X1:..." to the WebVTT
// form; ok is false when the line is not a valid cue timing.
func timingLine(line string) (string, bool) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return "", false
	}
	start := srtTimestamp.FindStringSubmatch(strings.TrimSpace(parts[0]))
	end := srtTimestamp.FindStringSubmatch(strings.TrimSpace(parts[1]))
	if start == nil || end == nil {
		return "", false
	}
	// SRT position hints (X1:.. Y1:..) have no WebVTT equivalent: dropped.
	return vttTimestamp(start) + " --> " + vttTimestamp(end), true
}

// SRTToVTT converts an SRT file (any of the encodings decodeSubtitleText
// handles) to WebVTT. Cue numbers are dropped, timestamps use a dot, and
// text that WebVTT would misparse ("-->", ASS/font tags) is cleaned. Blocks
// without a valid timing line are skipped; an error means no cue at all.
func SRTToVTT(src []byte) ([]byte, error) {
	text := decodeSubtitleText(src)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var out strings.Builder
	out.WriteString("WEBVTT\n")
	cues := 0
	for _, block := range strings.Split(text, "\n\n") {
		lines := strings.Split(strings.Trim(block, "\n"), "\n")
		// The timing line is the first or (after the cue number) the second.
		ti := -1
		for i := 0; i < len(lines) && i < 2; i++ {
			if strings.Contains(lines[i], "-->") {
				ti = i
				break
			}
		}
		if ti < 0 {
			continue
		}
		timing, ok := timingLine(lines[ti])
		if !ok {
			continue
		}
		var body []string
		for _, l := range lines[ti+1:] {
			l = srtFontTag.ReplaceAllString(srtASSTag.ReplaceAllString(l, ""), "")
			l = strings.ReplaceAll(l, "-->", "->")
			l = strings.TrimRight(l, " \t")
			if strings.TrimSpace(l) == "" {
				continue
			}
			body = append(body, l)
		}
		if len(body) == 0 {
			continue
		}
		out.WriteString("\n")
		out.WriteString(timing)
		out.WriteString("\n")
		out.WriteString(strings.Join(body, "\n"))
		out.WriteString("\n")
		cues++
	}
	if cues == 0 {
		return nil, fmt.Errorf("no subtitle cues found")
	}
	return []byte(out.String()), nil
}
