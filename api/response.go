package api

import (
	"encoding/json"
	"net/http"
)

const maxPageLimit = 100

type ErrorResponse struct {
	Error string `json:"error"`
}

type WebhookValidator interface {
	ValidateWebhookSignature(payload []byte, signature, timestamp string) error
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
		return
	}
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > maxPageLimit {
		return maxPageLimit
	}
	return limit
}
