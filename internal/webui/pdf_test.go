package webui

import (
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// onePixelPNG is a minimal valid 1x1 transparent PNG — enough for
// sniffImageExt/ingestStandaloneImage to treat it as a real image without
// needing a real diagram scan.
var onePixelPNG = mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")

func mustDecodeBase64(s string) []byte {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return data
}

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

// twoPageBoilerplatePDF mimics a real incident: every page's only
// extractable text is a source site's download-stamp watermark, with no
// other content at all (the real page content is a diagram this hand
// -written PDF doesn't attempt to draw). At 58 characters, that
// watermark alone clears minPDFPageTextChars, so before boilerplate
// stripping both pages were misclassified as text pages and their
// (real-world) diagrams never got rendered or OCR'd.
const twoPageBoilerplatePDF = `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R 6 0 R]/Count 2>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/Resources<</Font<</F1 4 0 R>>>>/MediaBox[0 0 400 200]/Contents 5 0 R>>endobj
4 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj
5 0 obj<</Length 90>>
stream
BT /F1 10 Tf 20 100 Td (Downloaded from www.Manualslib.com manuals search engine) Tj ET
endstream
endobj
6 0 obj<</Type/Page/Parent 2 0 R/Resources<</Font<</F1 4 0 R>>>>/MediaBox[0 0 400 200]/Contents 7 0 R>>endobj
7 0 obj<</Length 90>>
stream
BT /F1 10 Tf 20 100 Td (Downloaded from www.Manualslib.com manuals search engine) Tj ET
endstream
endobj
trailer<</Size 8/Root 1 0 R>>
%%EOF
`

func TestExtractPDFPagesStripsRepeatedBoilerplateBeforeClassifying(t *testing.T) {
	requirePoppler(t)
	imagesDir := filepath.Join(t.TempDir(), "images")

	pages, err := extractPDFPages(context.Background(), []byte(twoPageBoilerplatePDF), imagesDir, "/document-images/", "eng")
	if err != nil {
		t.Fatalf("extractPDFPages: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	for i, p := range pages {
		if p.ImageURL == "" {
			t.Errorf("page %d: image_url = %q, want a rendered image — a repeated watermark line alone shouldn't count as real page text", i, p.ImageURL)
		}
		if strings.Contains(p.Text, "Manualslib") {
			t.Errorf("page %d text = %q, want the watermark stripped instead of stored as if it were real content", i, p.Text)
		}
	}
}

func TestDetectBoilerplateLinesRequiresRepetitionAcrossPages(t *testing.T) {
	lines := detectBoilerplateLines([]string{
		"Downloaded from www.Manualslib.com manuals search engine",
		"Downloaded from www.Manualslib.com manuals search engine",
		"Downloaded from www.Manualslib.com manuals search engine",
	})
	if !lines["Downloaded from www.Manualslib.com manuals search engine"] {
		t.Error("expected the line repeated on every page to be detected as boilerplate")
	}
}

// TestDetectBoilerplateLinesIgnoresSinglePageContent guards against a
// trivial false positive: without the count >= 2 requirement, a
// one-page document's own unique content would be "more than half" of
// one page and get wrongly flagged as boilerplate.
func TestDetectBoilerplateLinesIgnoresSinglePageContent(t *testing.T) {
	lines := detectBoilerplateLines([]string{"Hello World, this is a much longer line of test text"})
	if len(lines) != 0 {
		t.Errorf("boilerplate = %#v, want none for a single-page document", lines)
	}
}

func TestStripBoilerplateLinesPreservesRealContent(t *testing.T) {
	boilerplate := map[string]bool{"Downloaded from www.Manualslib.com manuals search engine": true}
	text := "Exploded View - V-Twin Engine Parts\nDownloaded from www.Manualslib.com manuals search engine"
	got := stripBoilerplateLines(text, boilerplate)
	if strings.Contains(got, "Manualslib") {
		t.Errorf("stripped text = %q, want the watermark line removed", got)
	}
	if !strings.Contains(got, "Exploded View") {
		t.Errorf("stripped text = %q, want the real content line kept", got)
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

func TestSniffImageExtRecognizesFormats(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		want    string
	}{
		{"png", onePixelPNG, ".png"},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, ".jpg"},
		{"gif87", []byte("GIF87a" + "rest"), ".gif"},
		{"gif89", []byte("GIF89a" + "rest"), ".gif"},
		{"pdf", []byte("%PDF-1.4\n..."), ""},
		{"plain text", []byte("just some text"), ""},
		{"empty", nil, ""},
	}
	for _, c := range cases {
		if got := sniffImageExt(c.content); got != c.want {
			t.Errorf("sniffImageExt(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestIngestStandaloneImageWritesFileAndReturnsPage(t *testing.T) {
	imagesDir := filepath.Join(t.TempDir(), "images")

	pages, err := ingestStandaloneImage(context.Background(), onePixelPNG, ".png", imagesDir, "/document-images/", "eng")
	if err != nil {
		t.Fatalf("ingestStandaloneImage: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(pages))
	}
	if pages[0].Text == "" {
		t.Error("expected non-empty page text (either OCR text or the no-text-recognized fallback)")
	}
	if !strings.HasPrefix(pages[0].ImageURL, "/document-images/") {
		t.Errorf("image_url = %q, want the /document-images/ prefix", pages[0].ImageURL)
	}
	if !strings.HasSuffix(pages[0].ImageURL, ".png") {
		t.Errorf("image_url = %q, want a .png suffix", pages[0].ImageURL)
	}
	saved := filepath.Join(imagesDir, strings.TrimPrefix(pages[0].ImageURL, "/document-images/"))
	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("saved image not found at %s: %v", saved, err)
	}
	if string(data) != string(onePixelPNG) {
		t.Error("saved image bytes don't match the uploaded content")
	}
}

func TestRotateLineRERecognizesTesseractOSDOutput(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "no rotation needed",
			out:  "Page number: 0\nOrientation in degrees: 0\nRotate: 0\nOrientation confidence: 8.72\nScript: Latin\nScript confidence: 2.93\n",
			want: "0",
		},
		{
			name: "rotated 90 detected",
			out:  "Page number: 0\nOrientation in degrees: 90\nRotate: 270\nOrientation confidence: 5.81\nScript: Latin\nScript confidence: 3.79\n",
			want: "270",
		},
	}
	for _, c := range cases {
		match := rotateLineRE.FindStringSubmatch(c.out)
		if match == nil {
			t.Fatalf("%s: rotateLineRE found no match in %q", c.name, c.out)
		}
		if match[1] != c.want {
			t.Errorf("%s: rotateLineRE captured %q, want %q", c.name, match[1], c.want)
		}
	}
}

func TestRotateImage90CWRotatesPixelsAndSwapsDimensions(t *testing.T) {
	// A 2x1 image: left pixel red, right pixel blue. Rotating 90 clockwise
	// should produce a 1x2 image with red on top, blue on bottom.
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	src.Set(1, 0, color.RGBA{B: 255, A: 255})

	rotated := rotateImage90CW(src)

	bounds := rotated.Bounds()
	if bounds.Dx() != 1 || bounds.Dy() != 2 {
		t.Fatalf("rotated bounds = %v, want 1x2", bounds)
	}
	r, g, b, a := rotated.At(0, 0).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 || a>>8 != 255 {
		t.Errorf("top pixel = (%d,%d,%d,%d), want red", r>>8, g>>8, b>>8, a>>8)
	}
	r, g, b, a = rotated.At(0, 1).RGBA()
	if r>>8 != 0 || g>>8 != 0 || b>>8 != 255 || a>>8 != 255 {
		t.Errorf("bottom pixel = (%d,%d,%d,%d), want blue", r>>8, g>>8, b>>8, a>>8)
	}
}

func TestRotateImageFileEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rotate.png")

	// 2x1: left red, right blue — same fixture as above, written to disk.
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.RGBA{R: 255, A: 255})
	src.Set(1, 0, color.RGBA{B: 255, A: 255})
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := png.Encode(f, src); err != nil {
		f.Close()
		t.Fatalf("encode: %v", err)
	}
	f.Close()

	if err := rotateImageFile(path, 90); err != nil {
		t.Fatalf("rotateImageFile: %v", err)
	}

	f, err = os.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer f.Close()
	got, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode rotated file: %v", err)
	}
	bounds := got.Bounds()
	if bounds.Dx() != 1 || bounds.Dy() != 2 {
		t.Fatalf("rotated file bounds = %v, want 1x2", bounds)
	}
	r, _, _, _ := got.At(0, 0).RGBA()
	if r>>8 != 255 {
		t.Errorf("top pixel red channel = %d, want 255 (red on top after 90CW)", r>>8)
	}
}

func TestRotateImageFileDegreesZeroIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "norotate.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := png.Encode(f, onePixelImage()); err != nil {
		f.Close()
		t.Fatalf("encode: %v", err)
	}
	f.Close()

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	if err := rotateImageFile(path, 0); err != nil {
		t.Fatalf("rotateImageFile: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Error("rotateImageFile(0) modified the file; want a no-op")
	}
}

func onePixelImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	return img
}

func TestValidatePDFPageCount(t *testing.T) {
	if err := validatePDFPageCount(0); err == nil {
		t.Error("expected an error for a PDF with no pages")
	}
	if err := validatePDFPageCount(1); err != nil {
		t.Errorf("validatePDFPageCount(1) = %v, want nil", err)
	}
	if err := validatePDFPageCount(maxPDFPages); err != nil {
		t.Errorf("validatePDFPageCount(maxPDFPages) = %v, want nil (the limit itself is allowed)", err)
	}
	if err := validatePDFPageCount(maxPDFPages + 1); err == nil {
		t.Error("expected an error for a PDF one page over the limit")
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
