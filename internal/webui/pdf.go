package webui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif" // registers the GIF decoder with image.Decode, for a rotated standalone GIF upload
	_ "image/jpeg" // same, for JPEG
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/roman220/bosun-smarthelper/internal/documents"
)

// minPDFPageTextChars below this, a page's real content is treated as a
// diagram/scanned image rather than text — it's rendered to PNG and OCR'd
// (see ocrImage) so it's still searchable by that recognized text, not
// just a generic "page N" label. OCR quality varies a lot with scan
// quality and isn't guaranteed to find anything.
const minPDFPageTextChars = 40

// extractPDFPages shells out to poppler-utils (pdfinfo, pdftotext,
// pdftoppm — must be present in the runtime image) to turn a PDF into
// per-page PageInputs: real text when a page has an extractable text
// layer, otherwise a rendered page image. ocrLanguage is a tesseract
// language spec (e.g. "eng", "rus", "eng+rus") for pages that need OCR —
// see ValidOCRLanguage and ocrImage for why this isn't just always both.
func extractPDFPages(ctx context.Context, pdfBytes []byte, imagesDir, imageURLPrefix, ocrLanguage string) ([]documents.PageInput, error) {
	tempDir, err := os.MkdirTemp("", "bosun-pdf-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	pdfPath := filepath.Join(tempDir, "input.pdf")
	if err := os.WriteFile(pdfPath, pdfBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write temp pdf: %w", err)
	}

	pageCount, err := pdfPageCount(ctx, pdfPath)
	if err != nil {
		return nil, err
	}
	if pageCount == 0 {
		return nil, fmt.Errorf("PDF has no pages")
	}

	// Extracted in a first pass, not decided page-by-page as before,
	// because classifying a page needs to know what's boilerplate across
	// the *whole* document first — see detectBoilerplateLines.
	rawTexts := make([]string, pageCount)
	for page := 1; page <= pageCount; page++ {
		text, err := pdfPageText(ctx, pdfPath, page)
		if err != nil {
			return nil, err
		}
		rawTexts[page-1] = text
	}
	boilerplate := detectBoilerplateLines(rawTexts)

	pages := make([]documents.PageInput, 0, pageCount)
	for i, rawText := range rawTexts {
		page := i + 1
		cleanText := strings.TrimSpace(stripBoilerplateLines(rawText, boilerplate))
		if len(cleanText) >= minPDFPageTextChars {
			pages = append(pages, documents.PageInput{Text: fmt.Sprintf("Page %d\n\n%s", page, cleanText)})
			continue
		}
		imagePath, imageURL, err := renderPDFPageImage(ctx, pdfPath, page, imagesDir, imageURLPrefix)
		if err != nil {
			return nil, err
		}
		correctPageOrientation(ctx, imagePath)
		pageText := fmt.Sprintf("Page %d (diagram or scanned image, no text recognized)", page)
		if ocrText, err := ocrImage(ctx, imagePath, ocrLanguage); err == nil && len(strings.TrimSpace(ocrText)) > 0 {
			pageText = documents.CleanOCRText(fmt.Sprintf("Page %d (OCR)\n\n%s", page, strings.TrimSpace(ocrText)))
		}
		pages = append(pages, documents.PageInput{Text: pageText, ImageURL: imageURL})
	}
	return pages, nil
}

// boilerplateLineThreshold: a line repeated verbatim on more than this
// fraction of a PDF's pages is running header/footer/watermark noise
// (e.g. a source site's "Downloaded from ..." stamp), not real page
// content. Confirmed live: a manualslib.com-sourced manual's diagram
// pages had no extractable text at all except that exact watermark
// line — which alone was longer than minPDFPageTextChars — so every
// diagram page in the document (including the one the user actually
// wanted, an engine parts exploded view) was misclassified as a text
// page and its real image content was never rendered or OCR'd at all.
const boilerplateLineThreshold = 0.5

// detectBoilerplateLines finds every line that appears verbatim (after
// trimming) on more than boilerplateLineThreshold of pageTexts — each
// page counted at most once per distinct line, so a line repeated
// within a single page's own content can't inflate the count. Requiring
// count >= 2 (not just the fraction) matters for a one-page document:
// without it, that page's only line would trivially be "more than half"
// of one page and get wrongly treated as boilerplate.
func detectBoilerplateLines(pageTexts []string) map[string]bool {
	counts := make(map[string]int, 8)
	for _, text := range pageTexts {
		seenOnThisPage := make(map[string]bool)
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seenOnThisPage[line] {
				continue
			}
			seenOnThisPage[line] = true
			counts[line]++
		}
	}
	threshold := float64(len(pageTexts)) * boilerplateLineThreshold
	boilerplate := make(map[string]bool, len(counts))
	for line, count := range counts {
		if count >= 2 && float64(count) > threshold {
			boilerplate[line] = true
		}
	}
	return boilerplate
}

// stripBoilerplateLines removes every line detectBoilerplateLines found,
// keeping the rest — applied before comparing a page's text against
// minPDFPageTextChars, so a repeated header/footer/watermark line can
// never by itself make a genuinely image-only page look like a text
// page.
func stripBoilerplateLines(text string, boilerplate map[string]bool) string {
	if len(boilerplate) == 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if boilerplate[strings.TrimSpace(line)] {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func pdfPageCount(ctx context.Context, pdfPath string) (int, error) {
	out, err := exec.CommandContext(ctx, "pdfinfo", pdfPath).Output()
	if err != nil {
		return 0, fmt.Errorf("pdfinfo: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "Pages:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				return 0, fmt.Errorf("parse page count: %w", err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("could not determine PDF page count")
}

func pdfPageText(ctx context.Context, pdfPath string, page int) (string, error) {
	out, err := exec.CommandContext(ctx, "pdftotext", "-layout",
		"-f", strconv.Itoa(page), "-l", strconv.Itoa(page), pdfPath, "-").Output()
	if err != nil {
		return "", fmt.Errorf("pdftotext page %d: %w", page, err)
	}
	return string(out), nil
}

// renderPDFPageImage returns both the on-disk path (for ocrImage) and the
// URL a client should use to fetch it (served by the /document-images/
// route in server.go).
func renderPDFPageImage(ctx context.Context, pdfPath string, page int, imagesDir, urlPrefix string) (path string, url string, err error) {
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create images dir: %w", err)
	}
	id, err := randomHex(8)
	if err != nil {
		return "", "", err
	}
	outBase := filepath.Join(imagesDir, id)
	cmd := exec.CommandContext(ctx, "pdftoppm", "-png", "-r", "150",
		"-f", strconv.Itoa(page), "-l", strconv.Itoa(page), pdfPath, outBase)
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("pdftoppm page %d: %w", page, err)
	}
	// pdftoppm appends a page-number suffix even for a single-page range.
	matches, err := filepath.Glob(outBase + "*.png")
	if err != nil || len(matches) == 0 {
		return "", "", fmt.Errorf("rendered image for page %d not found", page)
	}
	return matches[0], urlPrefix + filepath.Base(matches[0]), nil
}

// validOCRLanguage matches a tesseract -l argument: one or more 3-letter
// language codes joined by "+" (e.g. "eng", "rus", "eng+rus") — tesseract's
// own naming convention for its bundled language data files.
var validOCRLanguage = regexp.MustCompile(`^[a-z]{3}(\+[a-z]{3})*$`)

// defaultOCRLanguage is used when a document upload doesn't specify one.
// Not "eng+rus": running both on an English-only technical diagram
// measurably made things worse, not more permissive — tesseract's combined
// model frequently misread plain English glyphs as look-alike Cyrillic
// ones ("ILLUMINATION SWITCH" came out as "ИЕЦАЮНАТЮН SWITCH"), turning a
// real, searchable word into noise instead of just failing to recognize
// it. A manual that's actually in Russian (or any other language
// tesseract's image has data for) should pass that language explicitly at
// upload time instead.
const defaultOCRLanguage = "eng"

// ocrImage runs tesseract on an already-rendered image with the given
// language spec (validate with validOCRLanguage before calling; an invalid
// one just makes tesseract itself fail, but validating earlier gives a
// clearer error to the uploader).
func ocrImage(ctx context.Context, imagePath, language string) (string, error) {
	if language == "" {
		language = defaultOCRLanguage
	}
	out, err := exec.CommandContext(ctx, "tesseract", imagePath, "-", "-l", language).Output()
	if err != nil {
		return "", fmt.Errorf("tesseract: %w", err)
	}
	return string(out), nil
}

// rotateLineRE matches tesseract --psm 0's "Rotate: N" output line — the
// clockwise degrees needed to correct the image's orientation (always
// one of 0/90/180/270, tesseract's OSD never reports anything else).
var rotateLineRE = regexp.MustCompile(`(?m)^Rotate:\s*(-?\d+)`)

// detectRotation runs tesseract's orientation/script detection pass
// (--psm 0, needs the separate tesseract-ocr-data-osd package — see
// Dockerfile) and returns the clockwise degrees needed to correct
// imagePath, or 0 if detection didn't produce a usable answer. That's
// not a hard error: OSD needs enough recognizable glyph shapes to work
// at all, and routinely fails outright ("Too few characters...") on a
// diagram-heavy page with little or no text — exactly the kind of page
// this OCR path exists for — in which case "leave it as-is" is already
// the right behavior, the same as before rotation detection existed.
func detectRotation(ctx context.Context, imagePath string) int {
	out, err := exec.CommandContext(ctx, "tesseract", imagePath, "-", "-l", "osd", "--psm", "0").Output()
	if err != nil {
		return 0
	}
	match := rotateLineRE.FindSubmatch(out)
	if match == nil {
		return 0
	}
	degrees, err := strconv.Atoi(string(match[1]))
	if err != nil {
		return 0
	}
	return ((degrees % 360) + 360) % 360
}

// correctPageOrientation detects and corrects imagePath's rotation
// in-place before OCR/serving it — a rotated diagram (e.g. a wide
// landscape exploded view embedded sideways in a portrait-page PDF,
// confirmed to exist in real manuals this app has ingested) would
// otherwise both OCR poorly (tesseract's default page segmentation
// assumes roughly upright text) and display sideways to whoever views
// the served image_url. Best-effort: a detection or rotation failure
// just leaves the image as tesseract/the source produced it, matching
// how a failed OCR pass is already tolerated rather than aborting
// ingestion.
func correctPageOrientation(ctx context.Context, imagePath string) {
	rotation := detectRotation(ctx, imagePath)
	if rotation == 0 {
		return
	}
	_ = rotateImageFile(imagePath, rotation)
}

// rotateImageFile decodes the image at path, rotates it clockwise by
// degrees (must be a multiple of 90 — tesseract's OSD is the only
// caller, and never reports anything else), and overwrites path with
// the rotated image re-encoded as PNG. Pure Go (image/png), no external
// tool: a fixed 90/180/270 rotation is just an index transpose, nothing
// arbitrary-angle rotation's interpolation math is needed for.
func rotateImageFile(path string, degrees int) error {
	steps := (degrees / 90) % 4
	if steps == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open image to rotate: %w", err)
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("decode image to rotate: %w", err)
	}
	for i := 0; i < steps; i++ {
		img = rotateImage90CW(img)
	}
	out, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create rotated image: %w", err)
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		return fmt.Errorf("encode rotated image: %w", err)
	}
	return nil
}

func rotateImage90CW(img image.Image) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	rotated := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			rotated.Set(h-1-y, x, img.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return rotated
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// imageSniffs recognizes a standalone image upload (not embedded in a
// PDF) by its magic bytes — the same formats a browser's own image
// preview understands, and all three are things tesseract can OCR
// directly with no format conversion.
var imageSniffs = []struct {
	prefix []byte
	ext    string
}{
	{[]byte("\x89PNG\r\n\x1a\n"), ".png"},
	{[]byte{0xFF, 0xD8, 0xFF}, ".jpg"},
	{[]byte("GIF87a"), ".gif"},
	{[]byte("GIF89a"), ".gif"},
}

// sniffImageExt returns the file extension for a recognized image format,
// or "" if content doesn't match any of imageSniffs.
func sniffImageExt(content []byte) string {
	for _, s := range imageSniffs {
		if bytes.HasPrefix(content, s.prefix) {
			return s.ext
		}
	}
	return ""
}

// ingestStandaloneImage OCRs an image uploaded on its own (a scraped
// manual's diagram, a photographed fuse panel — anything that isn't a
// scanned page inside a PDF) and returns it as a one-page
// []documents.PageInput, the same {Text, ImageURL} shape a diagram-only
// PDF page gets from extractPDFPages/ocrImage — a standalone diagram
// deserves identical treatment (OCR'd once, served from imagesDir,
// findable by its recognized text) rather than a separate, divergent
// pipeline.
func ingestStandaloneImage(ctx context.Context, content []byte, ext, imagesDir, imageURLPrefix, ocrLanguage string) ([]documents.PageInput, error) {
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		return nil, fmt.Errorf("create images dir: %w", err)
	}
	id, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	imagePath := filepath.Join(imagesDir, id+ext)
	if err := os.WriteFile(imagePath, content, 0o600); err != nil {
		return nil, fmt.Errorf("write image: %w", err)
	}
	correctPageOrientation(ctx, imagePath)
	imageURL := imageURLPrefix + filepath.Base(imagePath)
	text := "Diagram (no text recognized)"
	if ocrText, err := ocrImage(ctx, imagePath, ocrLanguage); err == nil && len(strings.TrimSpace(ocrText)) > 0 {
		text = documents.CleanOCRText(strings.TrimSpace(ocrText))
	}
	return []documents.PageInput{{Text: text, ImageURL: imageURL}}, nil
}
