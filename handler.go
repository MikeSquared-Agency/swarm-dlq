package dlq

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// NATSPublisher is the interface for publishing messages to NATS.
type NATSPublisher interface {
	Publish(subject string, data []byte) error
}

// Handler provides HTTP endpoints for DLQ management.
type Handler struct {
	store DataStore
	nc    NATSPublisher
}

// NewHandler creates a DLQ HTTP handler.
func NewHandler(store DataStore, nc NATSPublisher) *Handler {
	return &Handler{store: store, nc: nc}
}

// Routes returns a chi.Router with all DLQ endpoints mounted.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", h.handleList)
	r.Get("/stats", h.handleStats)
	r.Get("/{dlqID}", h.handleGet)
	r.Post("/{dlqID}/retry", h.handleRetry)
	r.Post("/{dlqID}/discard", h.handleDiscard)
	r.Post("/retry-all", h.handleRetryAll)
	return r
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	opts := ListOpts{}

	if v := r.URL.Query().Get("recovered"); v != "" {
		b := v == "true"
		opts.Recovered = &b
	}
	if v := r.URL.Query().Get("reason"); v != "" {
		opts.Reason = v
	}
	if v := r.URL.Query().Get("source"); v != "" {
		opts.Source = v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}

	entries, err := h.store.List(r.Context(), opts)
	if err != nil {
		slog.Error("list dlq failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if entries == nil {
		entries = []Entry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	dlqID := chi.URLParam(r, "dlqID")
	entry, err := h.store.Get(r.Context(), dlqID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "dlq entry not found"})
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (h *Handler) handleRetry(w http.ResponseWriter, r *http.Request) {
	dlqID := chi.URLParam(r, "dlqID")

	entry, err := h.store.Get(r.Context(), dlqID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "dlq entry not found"})
		return
	}

	if entry.Recovered {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already recovered"})
		return
	}

	// Republish original payload to the original subject.
	if err := h.nc.Publish(entry.OriginalSubject, entry.OriginalPayload); err != nil {
		slog.Error("failed to republish dlq entry", "dlq_id", dlqID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to republish"})
		return
	}

	if err := h.store.MarkRecovered(r.Context(), dlqID, "api-retry"); err != nil {
		slog.Error("failed to mark recovered", "dlq_id", dlqID, "error", err)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "retried", "dlq_id": dlqID})
}

func (h *Handler) handleDiscard(w http.ResponseWriter, r *http.Request) {
	dlqID := chi.URLParam(r, "dlqID")

	if err := h.store.MarkRecovered(r.Context(), dlqID, "manual-discard"); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("discard failed: %v", err)})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded", "dlq_id": dlqID})
}

func (h *Handler) handleRetryAll(w http.ResponseWriter, r *http.Request) {
	entries, err := h.store.ListRecoverable(r.Context())
	if err != nil {
		slog.Error("list recoverable failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	retried := 0
	failed := 0
	for _, entry := range entries {
		if err := h.nc.Publish(entry.OriginalSubject, entry.OriginalPayload); err != nil {
			slog.Error("retry-all: failed to republish", "dlq_id", entry.DLQID, "error", err)
			failed++
			continue
		}
		if err := h.store.MarkRecovered(r.Context(), entry.DLQID, "api-retry-all"); err != nil {
			slog.Error("retry-all: failed to mark recovered", "dlq_id", entry.DLQID, "error", err)
		}
		retried++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"retried": retried,
		"failed":  failed,
		"total":   len(entries),
	})
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.store.Stats(r.Context())
	if err != nil {
		slog.Error("dlq stats failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
