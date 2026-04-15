package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vavallee/bindery/internal/db"
	"github.com/vavallee/bindery/internal/httpsec"
	"github.com/vavallee/bindery/internal/models"
	"github.com/vavallee/bindery/internal/notifier"
)

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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if n.Name == "" || n.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and url required"})
		return
	}
	policy := httpsec.PolicyFromEnv(httpsec.PolicyStrict, "BINDERY_NOTIFICATIONS_ALLOW_PRIVATE")
	if err := httpsec.ValidateOutboundURL(n.URL, policy); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := h.notifications.Create(r.Context(), &n); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
	if err != nil || existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "notification not found"})
		return
	}

	var n models.Notification
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if n.URL != "" {
		policy := httpsec.PolicyFromEnv(httpsec.PolicyStrict, "BINDERY_NOTIFICATIONS_ALLOW_PRIVATE")
		if err := httpsec.ValidateOutboundURL(n.URL, policy); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	n.ID = id
	if err := h.notifications.Update(r.Context(), &n); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
	if err != nil || n == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "notification not found"})
		return
	}
	if err := h.notifier.Test(r.Context(), n); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "ok"})
}
