package documents

import (
	"strings"
	"unicode"
)

// CleanOCRText tidies OCR'd text before it's embedded/stored: a structured
// "head" (e.g. "Page 12 (OCR)" or a breadcrumb like "Heating and Air
// Conditioning > ... > Locations: ...") followed by a blank line and the
// raw OCR "body" is a convention used throughout this project's document
// ingestion (see internal/webui/pdf.go). The head is already clean,
// human-written text — left untouched. Only the body, which is tesseract's
// raw guess at a scanned diagram's text, gets cleaned: OCR misreads produce
// tokens that are pure noise (a lone symbol, a single stray letter) or
// actively wrong (English glyphs misread as look-alike Cyrillic ones,
// especially when OCR ran with more than one language loaded — see
// docs/memo-search.md) — both kinds hurt embedding quality by diluting the
// few real, searchable words (component names, "FUSE", "SWITCH", ...)
// buried in the noise. Text with no blank-line-separated body (i.e. no
// "\n\n") is returned unchanged — this heuristic only applies to that
// specific head/body convention.
func CleanOCRText(text string) string {
	head, body, ok := strings.Cut(text, "\n\n")
	if !ok {
		return text
	}
	cleaned := cleanOCRBody(body)
	if cleaned == "" {
		return head
	}
	return head + "\n\n" + cleaned
}

// cleanOCRBody keeps only tokens that look like a real word or number:
// ASCII-only (any non-ASCII letter — the Cyrillic-look-alike misread
// signal — drops the whole token), at least 2 characters after trimming
// leading/trailing punctuation, and with no punctuation left embedded in
// the middle (drops OCR noise like "GW>TCH" that a pure prefix/suffix trim
// wouldn't catch).
func cleanOCRBody(body string) string {
	fields := strings.Fields(body)
	kept := make([]string, 0, len(fields))
	for _, token := range fields {
		if hasNonASCIILetter(token) {
			continue
		}
		core := strings.TrimFunc(token, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len([]rune(core)) < 2 {
			continue
		}
		if !isAlnumOnly(core) {
			continue
		}
		kept = append(kept, core)
	}
	return strings.Join(kept, " ")
}

func hasNonASCIILetter(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

func isAlnumOnly(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
