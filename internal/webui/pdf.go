package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/roman220/ai-local-smarthelper/internal/documents"
)

// minPDFPageTextChars below this, a page is treated as a diagram/scanned
// image rather than text — there's no OCR here (see docs/memo-search.md),
// so such a page is only searchable by its generic "page N" label.
const minPDFPageTextChars = 40

// extractPDFPages shells out to poppler-utils (pdfinfo, pdftotext,
// pdftoppm — must be present in the runtime image) to turn a PDF into
// per-page PageInputs: real text when a page has an extractable text
// layer, otherwise a rendered page image.
func extractPDFPages(ctx context.Context, pdfBytes []byte, imagesDir, imageURLPrefix string) ([]documents.PageInput, error) {
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
		imageURL, err := renderPDFPageImage(ctx, pdfPath, page, imagesDir, imageURLPrefix)
		if err != nil {
			return nil, err
		}
		pages = append(pages, documents.PageInput{
			Text:     fmt.Sprintf("Page %d (diagram or scanned image, no extracted text)", page),
			ImageURL: imageURL,
		})
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

func renderPDFPageImage(ctx context.Context, pdfPath string, page int, imagesDir, urlPrefix string) (string, error) {
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		return "", fmt.Errorf("create images dir: %w", err)
	}
	id, err := randomHex(8)
	if err != nil {
		return "", err
	}
	outBase := filepath.Join(imagesDir, id)
	cmd := exec.CommandContext(ctx, "pdftoppm", "-png", "-r", "150",
		"-f", strconv.Itoa(page), "-l", strconv.Itoa(page), pdfPath, outBase)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdftoppm page %d: %w", page, err)
	}
	// pdftoppm appends a page-number suffix even for a single-page range.
	matches, err := filepath.Glob(outBase + "*.png")
	if err != nil || len(matches) == 0 {
		return "", fmt.Errorf("rendered image for page %d not found", page)
	}
	return urlPrefix + filepath.Base(matches[0]), nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
