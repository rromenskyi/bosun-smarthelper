package webui

import (
	"io"
	"net/http"

	"github.com/roman220/bosun-smarthelper/internal/chatfiles"
)

// maxChatFileUploadBytes bounds one file attached directly to a chat
// message — generous for a photo or a short document, but this is a
// compose-bar attachment meant for the chat_file tool to read or hand off
// to RAG/a memo, not a bulk upload surface (internal/filedump already
// exists for that, with no small cap — see docs/filedump.md).
const maxChatFileUploadBytes = 25 << 20

// SetChatFilesStore wires in temporary storage for files attached
// directly to a chat message (see internal/chatfiles and the chat_file
// tool). Optional: nil (the default) makes the /api/chat/files endpoints
// report the feature as disabled.
func (s *Server) SetChatFilesStore(store *chatfiles.Store) {
	s.chatFilesStore = store
}

func (s *Server) handleChatFilesList(w http.ResponseWriter, r *http.Request) {
	if s.chatFilesStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "files": []any{}})
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if !validSessionID(sessionID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session_id"})
		return
	}
	files, err := s.chatFilesStore.List(sessionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	views := make([]map[string]any, len(files))
	for i, f := range files {
		views[i] = map[string]any{"name": f.Name, "size": f.Size}
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "files": views})
}

// handleChatFilesUpload expects a multipart body with a "session_id"
// field (must arrive before "file", same streaming-reader constraint
// handleFileDumpUpload already documents) and a "file" part. Unlike a
// filedump upload, there's no add_to_rag/title/etc. here — what happens
// to the file is entirely up to the model, via the chat_file tool, once
// the user's next message mentions it.
func (s *Server) handleChatFilesUpload(w http.ResponseWriter, r *http.Request) {
	if s.chatFilesStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "chat file attachments are not configured"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxChatFileUploadBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid upload"})
		return
	}

	var sessionID, savedName string
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
		case "session_id":
			sessionID, _ = readMultipartValue(part)
		case "file":
			filename := part.FileName()
			if filename == "" {
				part.Close()
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file name is required"})
				return
			}
			if !validSessionID(sessionID) {
				part.Close()
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session_id"})
				return
			}
			savedName, err = s.chatFilesStore.Save(sessionID, filename, part)
			part.Close()
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		default:
			part.Close()
		}
	}

	if savedName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file is required"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": savedName})
}

func (s *Server) handleChatFilesDelete(w http.ResponseWriter, r *http.Request) {
	if s.chatFilesStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "chat file attachments are not configured"})
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	name := r.URL.Query().Get("name")
	if !validSessionID(sessionID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid session_id"})
		return
	}
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if err := s.chatFilesStore.Forget(sessionID, name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
