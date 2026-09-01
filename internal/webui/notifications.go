package webui

import (
	"encoding/json"
	"net/http"

	"github.com/roman220/bosun-smarthelper/internal/notifications"
)

// SetNotificationsStore wires in the persisted alert record (see
// internal/notifications and cmd/smarthelper/alerts.go's
// notificationStoreNotifier) for the web UI's notification zone.
// Optional: nil (the default) makes the /api/notifications endpoints
// report the feature as disabled.
func (s *Server) SetNotificationsStore(store *notifications.Store) {
	s.notificationsStore = store
}

func (s *Server) handleNotificationsList(w http.ResponseWriter, r *http.Request) {
	if s.notificationsStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "notifications": []any{}, "unread_count": 0})
		return
	}
	list, err := s.notificationsStore.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	unread := 0
	for _, n := range list {
		if !n.Read {
			unread++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "notifications": list, "unread_count": unread})
}

type notificationsMarkReadRequest struct {
	ID  string `json:"id"`
	All bool   `json:"all"`
}

func (s *Server) handleNotificationsMarkRead(w http.ResponseWriter, r *http.Request) {
	if s.notificationsStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "notifications are not configured"})
		return
	}
	var request notificationsMarkReadRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var err error
	switch {
	case request.All:
		err = s.notificationsStore.MarkAllRead()
	case request.ID != "":
		err = s.notificationsStore.MarkRead(request.ID)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id or all is required"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleNotificationsDelete(w http.ResponseWriter, r *http.Request) {
	if s.notificationsStore == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "notifications are not configured"})
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	if err := s.notificationsStore.Delete(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
