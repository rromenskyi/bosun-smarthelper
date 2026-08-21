package webui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requirePoppler skips the test when poppler-utils isn't installed, so this
// suite doesn't fail in environments that never build a container image
// (only the Docker image is guaranteed to have it — see the Dockerfile).
func requirePoppler(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"pdfinfo", "pdftotext", "pdftoppm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed; skipping PDF extraction test", bin)
		}
	}
}

func requireTesseract(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not installed; skipping OCR test")
	}
}

// onePageTextPDF and onePageBlankPDF are minimal hand-written PDFs (no
// proper xref table) — poppler recovers from that heuristically, which is
// enough for these two well-known-content fixtures.
const onePageTextPDF = `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/Resources<</Font<</F1 4 0 R>>>>/MediaBox[0 0 800 200]/Contents 5 0 R>>endobj
4 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj
5 0 obj<</Length 83>>
stream
BT /F1 14 Tf 20 100 Td (Hello World, this is a much longer line of test text) Tj ET
endstream
endobj
trailer<</Size 6/Root 1 0 R>>
%%EOF
`

const onePageBlankPDF = `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/Resources<<>>/MediaBox[0 0 200 200]/Contents 4 0 R>>endobj
4 0 obj<</Length 0>>
stream
endstream
endobj
trailer<</Size 5/Root 1 0 R>>
%%EOF
`

func TestExtractPDFPagesTextPage(t *testing.T) {
	requirePoppler(t)
	imagesDir := filepath.Join(t.TempDir(), "images")

	pages, err := extractPDFPages(context.Background(), []byte(onePageTextPDF), imagesDir, "/document-images/", "eng")
	if err != nil {
		t.Fatalf("extractPDFPages: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	if !strings.Contains(pages[0].Text, "Hello World") {
		t.Errorf("page text = %q, want it to contain Hello World", pages[0].Text)
	}
	if pages[0].ImageURL != "" {
		t.Errorf("image_url = %q, want empty for a text page", pages[0].ImageURL)
	}
}

func TestExtractPDFPagesBlankPageRendersImage(t *testing.T) {
	requirePoppler(t)
	imagesDir := filepath.Join(t.TempDir(), "images")

	pages, err := extractPDFPages(context.Background(), []byte(onePageBlankPDF), imagesDir, "/document-images/", "eng")
	if err != nil {
		t.Fatalf("extractPDFPages: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	if pages[0].ImageURL == "" {
		t.Fatal("expected a rendered image URL for a page with no text layer")
	}
	if !strings.HasPrefix(pages[0].ImageURL, "/document-images/") {
		t.Errorf("image_url = %q, want the /document-images/ prefix", pages[0].ImageURL)
	}
	rendered := filepath.Join(imagesDir, strings.TrimPrefix(pages[0].ImageURL, "/document-images/"))
	if _, err := os.Stat(rendered); err != nil {
		t.Errorf("rendered image not found on disk at %s: %v", rendered, err)
	}
}

func TestExtractPDFPagesRejectsGarbage(t *testing.T) {
	requirePoppler(t)
	imagesDir := filepath.Join(t.TempDir(), "images")
	if _, err := extractPDFPages(context.Background(), []byte("not a pdf at all"), imagesDir, "/document-images/", "eng"); err == nil {
		t.Error("expected an error for content that isn't a valid PDF")
	}
}

func TestOCRImageRecognizesText(t *testing.T) {
	requirePoppler(t)
	requireTesseract(t)

	tempDir := t.TempDir()
	pdfPath := filepath.Join(tempDir, "input.pdf")
	if err := os.WriteFile(pdfPath, []byte(onePageTextPDF), 0o600); err != nil {
		t.Fatalf("write temp pdf: %v", err)
	}
	imagePath, _, err := renderPDFPageImage(context.Background(), pdfPath, 1, filepath.Join(tempDir, "images"), "/document-images/")
	if err != nil {
		t.Fatalf("render page image: %v", err)
	}

	text, err := ocrImage(context.Background(), imagePath, "eng")
	if err != nil {
		t.Fatalf("ocrImage: %v", err)
	}
	if !strings.Contains(text, "Hello World") {
		t.Errorf("OCR text = %q, want it to contain Hello World", text)
	}
}

func TestOCRImageDefaultsLanguageWhenEmpty(t *testing.T) {
	requirePoppler(t)
	requireTesseract(t)

	tempDir := t.TempDir()
	pdfPath := filepath.Join(tempDir, "input.pdf")
	if err := os.WriteFile(pdfPath, []byte(onePageTextPDF), 0o600); err != nil {
		t.Fatalf("write temp pdf: %v", err)
	}
	imagePath, _, err := renderPDFPageImage(context.Background(), pdfPath, 1, filepath.Join(tempDir, "images"), "/document-images/")
	if err != nil {
		t.Fatalf("render page image: %v", err)
	}

	text, err := ocrImage(context.Background(), imagePath, "")
	if err != nil {
		t.Fatalf("ocrImage: %v", err)
	}
	if !strings.Contains(text, "Hello World") {
		t.Errorf("OCR text = %q, want it to contain Hello World with the default language", text)
	}
}

func TestValidOCRLanguage(t *testing.T) {
	for _, valid := range []string{"eng", "rus", "eng+rus", "fra+deu+ita"} {
		if !validOCRLanguage.MatchString(valid) {
			t.Errorf("validOCRLanguage(%q) = false, want true", valid)
		}
	}
	for _, invalid := range []string{"", "english", "eng+", "eng rus", "eng; rm -rf /", "EN"} {
		if validOCRLanguage.MatchString(invalid) {
			t.Errorf("validOCRLanguage(%q) = true, want false", invalid)
		}
	}
}
