package media

import (
	"html"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var (
	// Block-level tags and <br> become line breaks before tags are dropped.
	htmlBreakRe = regexp.MustCompile(`(?i)<\s*(br\s*/?|/p|/div|/li|/h[1-6]|/tr)\s*>`)
	htmlTagRe   = regexp.MustCompile(`<[^>]*>`)
	blankRunRe  = regexp.MustCompile(`\n{3,}`)
)

// PlainText turns a provider description (archive.org stores HTML) into plain
// text: paragraphs and <br> become newlines, tags are removed and entities
// decoded. The result is rendered as text by the frontend, never as HTML.
func PlainText(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return strings.TrimSpace(s)
	}
	s = htmlBreakRe.ReplaceAllString(s, "\n")
	s = htmlTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	s = blankRunRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(s)
}

var (
	clockRe   = regexp.MustCompile(`^(?:(\d+):)?(\d{1,2}):(\d{2})(?:\.\d+)?$`)
	minutesRe = regexp.MustCompile(`(?i)^(\d+)\s*(?:'|min|mins|minutes|m)?$`)
	hmRe      = regexp.MustCompile(`(?i)^(\d+)\s*h(?:ours?)?\s*(\d+)?\s*(?:'|m|min|mins|minutes)?$`)
)

// parseArchiveRuntime reads archive.org's free-form runtime ("12'", "85 min",
// "1:23:45", "1h 25m", "4500" seconds in a file length) as seconds; 0 when
// it cannot be parsed.
func parseArchiveRuntime(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if m := clockRe.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		se, _ := strconv.Atoi(m[3])
		return float64(h*3600 + mi*60 + se)
	}
	if m := hmRe.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		return float64(h*3600 + mi*60)
	}
	if m := minutesRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return float64(n * 60)
	}
	return 0
}

// archiveDuration is the item's runtime, else the longest video file length.
func archiveDuration(runtime string, files []archiveMetadataFile) float64 {
	if d := parseArchiveRuntime(runtime); d > 0 {
		return d
	}
	var best float64
	for _, f := range files {
		if !nativeVideoExt[strings.ToLower(path.Ext(f.Name))] && !NeedsConversion(f.Name) {
			continue
		}
		if d, err := strconv.ParseFloat(strings.TrimSpace(f.Length), 64); err == nil && d > best {
			best = d
		} else if d := parseArchiveRuntime(f.Length); d > best {
			best = d
		}
	}
	return best
}
