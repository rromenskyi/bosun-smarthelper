package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/roman220/bosun-smarthelper/internal/filedump"
)

// fileDumpUploadHardLimit is a last-resort backstop against a runaway
// request, not a real limit — see docs/filedump.md. There's deliberately
// no smaller server-side cap: the client warns above a size threshold via
// confirm(), but anything the user actually commits to uploading is
// allowed through, unlike maxDocumentUploadBytes's hard 2MB cap on the
// old flat-dialog path this feature replaces.
const fileDumpUploadHardLimit = 4 << 30

// SetFileDumpStore wires in the raw file tree (see docs/filedump.md).
// Optional: nil (the default, or when filedump.path is unset) makes the
// /api/files endpoints report the feature as disabled, and GET /files/
// is never registered (see Handler).
func (s *Server) SetFileDumpStore(store *filedump.Store) {
	s.fileDumpStore = store
	if store != nil {
		s.fileDumpDir = store.Root()
	}
}

func (s *Server) handleFileDumpList(w http.ResponseWriter, r *http.Request) {
	if s.fileDumpStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "folders": []any{}, "files": []any{}})
		return
	}
	listing, err := s.fileDumpStore.List(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "folders": listing.Folders, "files": listing.Files})
}

type fileDumpFolderRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

func (s *Server) handleFileDumpFolder(w http.ResponseWriter, r *http.Request) {
	if s.fileDumpStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "file dump is not configured"})
		return
	}
	var request fileDumpFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := s.fileDumpStore.CreateFolder(request.Path, request.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created"})
}

type fileDumpMoveRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// handleFileDumpMove also keeps any RAG-linked documents.Record.SourcePath
// in sync — a folder move can carry many linked files with it at once
// (see filedump.Store.Move).
func (s *Server) handleFileDumpMove(w http.ResponseWriter, r *http.Request) {
	if s.fileDumpStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "file dump is not configured"})
		return
	}
	var request fileDumpMoveRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	updates, err := s.fileDumpStore.Move(request.From, request.To)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if s.documents != nil {
		for _, update := range updates {
			// SourcePath is always a folder, never a full file path — see
			// fileDumpFolderOf and handleFileDumpUpload's own use of it —
			// so a moved file's new folder must go through the same
			// derivation, not update.NewPath (a file path) directly.
			if err := s.documents.UpdateSourcePath(update.DocumentID, fileDumpFolderOf(update.NewPath)); err != nil {
				s.logger.Warn("update document source path after move", "document_id", update.DocumentID, "error", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "moved"})
}

// handleFileDumpDelete cascades to documents.Store.Delete for every
// RAG-linked file removed — a recursive folder delete can remove many at
// once (see filedump.Store.Delete). The client's confirm() dialog is
// expected to have already named this count via a prior GET /api/files
// listing; this endpoint doesn't re-confirm, it just reports what it did.
func (s *Server) handleFileDumpDelete(w http.ResponseWriter, r *http.Request) {
	if s.fileDumpStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "file dump is not configured"})
		return
	}
	targetPath := r.URL.Query().Get("path")
	recursive := r.URL.Query().Get("recursive") == "true"
	result, err := s.fileDumpStore.Delete(targetPath, recursive)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if s.documents != nil {
		for _, id := range result.DocumentIDs {
			if err := s.documents.Delete(id); err != nil {
				s.logger.Warn("delete document after file dump delete", "document_id", id, "error", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted_paths":        result.Paths,
		"deleted_document_ids": result.DocumentIDs,
	})
}

// readMultipartValue reads and closes a non-file multipart part.
func readMultipartValue(part *multipart.Part) (string, error) {
	defer part.Close()
	data, err := io.ReadAll(part)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// fileDumpFolderOf returns the folder portion of a tree-relative file
// path (forward-slash form, as produced by filedump.Store), for use as
// documents.Record.SourcePath — "" for a root-level file, matching
// cleanRelPath's own empty-root convention in internal/filedump.
func fileDumpFolderOf(relFilePath string) string {
	dir := path.Dir(relFilePath)
	if dir == "." {
		return ""
	}
	return dir
}

// handleFileDumpUpload streams the "file" part straight to disk via
// filedump.Store.OpenForWrite — never buffered in full via
// ParseMultipartForm, unlike the old handleDocumentUpload this feature
// replaces, since there's deliberately no small hard size cap here (see
// fileDumpUploadHardLimit). The metadata fields ("path", "add_to_rag",
// "title", "ocr_language") must arrive before the "file" part in the
// multipart stream — true for any FormData built by appending fields in
// that order (what internal/webui/static/filedump.js does), since a
// streaming reader can't look ahead.
//
// When add_to_rag is true, the file is read back after the raw write
// completes and run through PDF/image/plain-text extraction (whichever
// matches — see ingestFileDumpUpload), tagged with the file's folder as
// documents.Record.SourcePath. A failed ingestion (not a PDF, not a
// recognized image format, not valid UTF-8 text, extraction error) never
// rolls back the raw file write — it's reported back as a non-fatal
// rag_warning instead.
func (s *Server) handleFileDumpUpload(w http.ResponseWriter, r *http.Request) {
	if s.fileDumpStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "file dump is not configured"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, fileDumpUploadHardLimit)
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid upload"})
		return
	}

	var (
		targetPath  string
		addToRAG    bool
		title       string
		ocrLanguage = defaultOCRLanguage
		relFilePath string
		wrote       bool
	)

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			respondMultipartError(w, err)
			return
		}

		switch part.FormName() {
		case "path":
			targetPath, _ = readMultipartValue(part)
		case "add_to_rag":
			value, _ := readMultipartValue(part)
			addToRAG = value == "true"
		case "title":
			title, _ = readMultipartValue(part)
		case "ocr_language":
			if value, _ := readMultipartValue(part); value != "" {
				ocrLanguage = value
			}
		case "file":
			filename := filepath.Base(part.FileName())
			if filename == "" || filename == "." || filename == string(filepath.Separator) {
				part.Close()
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file name is required"})
				return
			}
			dest, rel, err := s.fileDumpStore.OpenForWrite(targetPath, filename)
			if err != nil {
				part.Close()
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			_, copyErr := io.Copy(dest, part)
			part.Close()
			if copyErr != nil {
				dest.Close()
				respondMultipartError(w, copyErr)
				return
			}
			if err := dest.Close(); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not write file"})
				return
			}
			relFilePath = rel
			if title == "" {
				title = filename
			}
			wrote = true
		default:
			part.Close()
		}
	}

	if !wrote {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file is required"})
		return
	}

	response := map[string]any{"path": relFilePath, "in_rag": false}
	if !addToRAG {
		writeJSON(w, http.StatusOK, response)
		return
	}
	if s.documents == nil {
		response["rag_warning"] = "document search is not configured; the file was saved but not added to search"
		writeJSON(w, http.StatusOK, response)
		return
	}
	if !validOCRLanguage.MatchString(ocrLanguage) {
		response["rag_warning"] = "ocr_language must look like a tesseract language code, e.g. eng, rus, or eng+rus; the file was saved but not added to search"
		writeJSON(w, http.StatusOK, response)
		return
	}

	s.ingestFileDumpUpload(r.Context(), relFilePath, title, ocrLanguage, response)
	writeJSON(w, http.StatusOK, response)
}

// ingestFileDumpUpload reads the just-written file back from disk and
// feeds it through document ingestion, mutating response in place — kept
// separate from handleFileDumpUpload so every early-return path there
// stays a plain writeJSON, not a nested tree of ingestion error handling.
func (s *Server) ingestFileDumpUpload(ctx context.Context, relFilePath, title, ocrLanguage string, response map[string]any) {
	absPath := filepath.Join(s.fileDumpDir, filepath.FromSlash(relFilePath))
	content, err := os.ReadFile(absPath)
	if err != nil {
		response["rag_warning"] = "could not read the file back for search ingestion: " + err.Error()
		return
	}

	ingestCtx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()
	sourcePath := fileDumpFolderOf(relFilePath)

	if bytes.HasPrefix(content, pdfMagic) {
		pages, err := extractPDFPages(ingestCtx, content, s.documentImagesDir, "/document-images/", ocrLanguage)
		if err != nil {
			response["rag_warning"] = "could not process PDF for search: " + err.Error()
			return
		}
		summary, err := s.documents.AddPages(ingestCtx, title, pages, sourcePath)
		if err != nil {
			response["rag_warning"] = err.Error()
			return
		}
		s.linkFileDumpDocument(relFilePath, summary.ID, response)
		return
	}

	if ext := sniffImageExt(content); ext != "" {
		pages, err := ingestStandaloneImage(ingestCtx, content, ext, s.documentImagesDir, "/document-images/", ocrLanguage)
		if err != nil {
			response["rag_warning"] = "could not process image for search: " + err.Error()
			return
		}
		summary, err := s.documents.AddPages(ingestCtx, title, pages, sourcePath)
		if err != nil {
			response["rag_warning"] = err.Error()
			return
		}
		s.linkFileDumpDocument(relFilePath, summary.ID, response)
		return
	}

	if !utf8.Valid(content) {
		response["rag_warning"] = "file must be plain UTF-8 text, a PDF, or an image (PNG/JPEG/GIF) to be added to search"
		return
	}
	summary, err := s.documents.Add(ingestCtx, title, string(content), sourcePath)
	if err != nil {
		response["rag_warning"] = err.Error()
		return
	}
	s.linkFileDumpDocument(relFilePath, summary.ID, response)
}

func (s *Server) linkFileDumpDocument(relFilePath, documentID string, response map[string]any) {
	if err := s.fileDumpStore.LinkDocument(relFilePath, documentID); err != nil {
		s.logger.Warn("link file dump document", "path", relFilePath, "document_id", documentID, "error", err)
		response["rag_warning"] = "added to search but could not record the link: " + err.Error()
		return
	}
	response["in_rag"] = true
	response["document_id"] = documentID
}

// respondMultipartError distinguishes the fileDumpUploadHardLimit backstop
// from any other malformed-upload error, so a genuinely oversized request
// gets 413 rather than a generic 400.
func respondMultipartError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "upload exceeds the server's hard size limit"})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid upload"})
}
