package documents

import "testing"

func TestCleanOCRTextLeavesTextWithoutABodyUnchanged(t *testing.T) {
	text := "Page 12 (diagram or scanned image, no text recognized)"
	if got := CleanOCRText(text); got != text {
		t.Errorf("CleanOCRText(%q) = %q, want unchanged", text, got)
	}
}

func TestCleanOCRTextLeavesHeadUntouched(t *testing.T) {
	text := "Heating and Air Conditioning > Air Conditioning Switch > Locations: Air Conditioning Switch: Locations\n\nreal words here"
	got := CleanOCRText(text)
	if !hasPrefixLine(got, "Heating and Air Conditioning > Air Conditioning Switch > Locations: Air Conditioning Switch: Locations") {
		t.Errorf("CleanOCRText(%q) = %q, head was altered", text, got)
	}
}

// TestCleanOCRTextDropsCyrillicLookalikeGarbage is a regression test for a
// real production case: tesseract run with -l eng+rus misread the English
// diagram label "ILLUMINATION SWITCH" as Cyrillic-look-alike garbage
// ("ИЕЦАЮНАТЮН"), which then got embedded as if it were meaningful text.
func TestCleanOCRTextDropsCyrillicLookalikeGarbage(t *testing.T) {
	text := "Heating and Air Conditioning > Air Conditioning Switch > Locations: Air Conditioning Switch: Locations\n\n" +
		"ee\n\nсмт\n\n|\n\ni\n\nАК\nИЕЦАЮНАТЮН SWITCH GW>TCH © SWITCH"
	got := CleanOCRText(text)
	for _, garbage := range []string{"смт", "АК", "ИЕЦАЮНАТЮН", "GW>TCH", "©", "|"} {
		if containsToken(got, garbage) {
			t.Errorf("CleanOCRText result %q still contains garbage token %q", got, garbage)
		}
	}
	if !containsToken(got, "SWITCH") {
		t.Errorf("CleanOCRText result %q lost the real word SWITCH", got)
	}
}

func TestCleanOCRTextKeepsRealWordsAndNumbers(t *testing.T) {
	text := "Power Distribution > Fuse 9: Fuse 9\n\n" +
		"НОТ AT ALL TIMES} О 20| FUSE ga| LINK M a м у Е 361 бя 37"
	got := CleanOCRText(text)
	for _, want := range []string{"AT", "ALL", "TIMES", "FUSE", "LINK", "20", "361", "37"} {
		if !containsToken(got, want) {
			t.Errorf("CleanOCRText result %q lost expected token %q", got, want)
		}
	}
	// НОТ, О, м, у, Е, бя are Cyrillic (either genuine or look-alike) and
	// must not survive.
	for _, garbage := range []string{"НОТ", "О", "бя"} {
		if containsToken(got, garbage) {
			t.Errorf("CleanOCRText result %q still contains garbage token %q", got, garbage)
		}
	}
}

func TestCleanOCRTextEmptiesBodyFallsBackToHeadOnly(t *testing.T) {
	text := "Page 3 (OCR)\n\n© | > мат"
	got := CleanOCRText(text)
	if got != "Page 3 (OCR)" {
		t.Errorf("CleanOCRText(%q) = %q, want just the head when the whole body is noise", text, got)
	}
}

func hasPrefixLine(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return s[:len(prefix)] == prefix
}

func containsToken(s, token string) bool {
	for _, field := range splitFields(s) {
		if field == token {
			return true
		}
	}
	return false
}

func splitFields(s string) []string {
	var fields []string
	var current []rune
	for _, r := range s {
		if r == ' ' || r == '\n' {
			if len(current) > 0 {
				fields = append(fields, string(current))
				current = nil
			}
			continue
		}
		current = append(current, r)
	}
	if len(current) > 0 {
		fields = append(fields, string(current))
	}
	return fields
}
