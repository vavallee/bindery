package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

// annotatePrivateNetworkError takes a webhook-save / webhook-test error and,
// if the failure is the SSRF policy refusing a private-network target,
// appends a hint pointing at BINDERY_NOTIFICATIONS_ALLOW_PRIVATE. Users with
// self-hosted ntfy / Gotify / Home Assistant on the same LAN hit this and
// the bare "url not allowed: points to private network" error was too cryptic
// to act on (#799).
func annotatePrivateNetworkError(err error) string {
	msg := err.Error()
	if !strings.Contains(msg, "points to private network") {
		return msg
	}
	return msg + " — to allow private-network webhooks (e.g. ntfy on your LAN), set BINDERY_NOTIFICATIONS_ALLOW_PRIVATE=1 on the Bindery container"
}

type NotificationHandler struct {
	notifications *db.NotificationRepo
	notifier      *notifier.Notifier
}

func NewNotificationHandler(notifications *db.NotificationRepo, notif *notifier.Notifier) *NotificationHandler {
	return &NotificationHandler{notifications: notifications, notifier: notif}
}

func (h *NotificationHandler) List(w http.ResponseWriter, r *http.Request) {
	notifications, err := h.notifications.List(r.Context())
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if notifications == nil {
		notifications = []models.Notification{}
	}
	writeJSON(w, http.StatusOK, notifications)
}

func (h *NotificationHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	n, err := h.notifications.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if n == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "notification not found"})
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (h *NotificationHandler) Create(w http.ResponseWriter, r *http.Request) {
	var n models.Notification
	if !decodeJSON(w, r, &n) {
		return
	}
	if n.Name == "" || n.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and url required"})
		return
	}
	policy := httpsec.PolicyFromEnv(httpsec.PolicyStrict, "BINDERY_NOTIFICATIONS_ALLOW_PRIVATE")
	if err := httpsec.ValidateOutboundURL(n.URL, policy); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": annotatePrivateNetworkError(err)})
		return
	}
	if err := h.notifications.Create(r.Context(), &n); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (h *NotificationHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	existing, err := h.notifications.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "notification not found"})
		return
	}

	// Decode over a copy of the stored row: JSON decoding only writes the keys
	// the client sent, so an omitted field keeps what is on disk. Previously a
	// body such as {"enabled":false} blanked the name, URL and headers and
	// cleared every on* flag.
	n := *existing
	if !decodeJSON(w, r, &n) {
		return
	}
	if n.Name == "" || n.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and url required"})
		return
	}
	if n.URL != existing.URL {
		policy := httpsec.PolicyFromEnv(httpsec.PolicyStrict, "BINDERY_NOTIFICATIONS_ALLOW_PRIVATE")
		if err := httpsec.ValidateOutboundURL(n.URL, policy); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": annotatePrivateNetworkError(err)})
			return
		}
	}
	n.ID = id
	n.CreatedAt = existing.CreatedAt
	if err := h.notifications.Update(r.Context(), &n); err != nil {
		writeServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (h *NotificationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := h.notifications.Delete(r.Context(), id); err != nil {
		writeServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *NotificationHandler) Test(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	n, err := h.notifications.GetByID(r.Context(), id)
	if err != nil {
		writeServerError(w, r, err)
		return
	}
	if n == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "notification not found"})
		return
	}
	if err := h.notifier.Test(r.Context(), n); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": annotatePrivateNetworkError(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}
