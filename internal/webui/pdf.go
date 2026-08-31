package webui

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

	pages := make([]documents.PageInput, 0, pageCount)
	for page := 1; page <= pageCount; page++ {
		text, err := pdfPageText(ctx, pdfPath, page)
		if err != nil {
			return nil, err
		}
		if len(strings.TrimSpace(text)) >= minPDFPageTextChars {
			pages = append(pages, documents.PageInput{Text: fmt.Sprintf("Page %d\n\n%s", page, strings.TrimSpace(text))})
			continue
		}
		imagePath, imageURL, err := renderPDFPageImage(ctx, pdfPath, page, imagesDir, imageURLPrefix)
		if err != nil {
			return nil, err
		}
		pageText := fmt.Sprintf("Page %d (diagram or scanned image, no text recognized)", page)
		if ocrText, err := ocrImage(ctx, imagePath, ocrLanguage); err == nil && len(strings.TrimSpace(ocrText)) > 0 {
			pageText = documents.CleanOCRText(fmt.Sprintf("Page %d (OCR)\n\n%s", page, strings.TrimSpace(ocrText)))
		}
		pages = append(pages, documents.PageInput{Text: pageText, ImageURL: imageURL})
	}
	return pages, nil
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
	imageURL := imageURLPrefix + filepath.Base(imagePath)
	text := "Diagram (no text recognized)"
	if ocrText, err := ocrImage(ctx, imagePath, ocrLanguage); err == nil && len(strings.TrimSpace(ocrText)) > 0 {
		text = documents.CleanOCRText(strings.TrimSpace(ocrText))
	}
	return []documents.PageInput{{Text: text, ImageURL: imageURL}}, nil
}
