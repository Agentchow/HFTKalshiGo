package ticker

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalize lowercases, strips diacritics, collapses whitespace,
// then resolves through the sport-specific alias map.
func Normalize(s string, aliases map[string]string) string {
	if s == "" {
		return ""
	}
	s = stripDiacritics(s)
	s = strings.ToLower(strings.TrimSpace(s))
	s = collapseWhitespace(s)
	if canonical, ok := aliases[s]; ok {
		return canonical
	}
	return s
}

func stripDiacritics(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) { // Mn = Mark, Nonspacing (combining accents)
			b.WriteRune(r)
		}
	}
	return b.String()
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
