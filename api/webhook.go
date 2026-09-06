package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/malwarebo/conductor/providers"
)

type WebhookHandler struct {
	stripeProvider *providers.StripeProvider
	xenditProvider *providers.XenditProvider
}

func CreateWebhookHandler(stripeProvider *providers.StripeProvider, xenditProvider *providers.XenditProvider) *WebhookHandler {
	return &WebhookHandler{
		stripeProvider: stripeProvider,
		xenditProvider: xenditProvider,
	}
}

func (h *WebhookHandler) HandleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read webhook payload", http.StatusBadRequest)
		return
	}

	signature := r.Header.Get("Stripe-Signature")
	if signature == "" {
		http.Error(w, "Missing Stripe signature", http.StatusUnauthorized)
		return
	}

	if err := h.stripeProvider.ValidateWebhookSignature(payload, signature, ""); err != nil {
		http.Error(w, "Invalid webhook signature", http.StatusUnauthorized)
		return
	}

	var event map[string]interface{}
	if err := json.Unmarshal(payload, &event); err != nil {
		http.Error(w, "Invalid webhook payload", http.StatusBadRequest)
		return
	}

	eventType, ok := event["type"].(string)
	if !ok {
		http.Error(w, "Missing event type", http.StatusBadRequest)
		return
	}

	h.processStripeEvent(eventType, event)

	response := map[string]interface{}{
		"received":   true,
		"event_type": eventType,
		"timestamp":  time.Now(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (h *WebhookHandler) HandleXenditWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read webhook payload", http.StatusBadRequest)
		return
	}

	signature := r.Header.Get("x-callback-token")
	if signature == "" {
		http.Error(w, "Missing Xendit signature", http.StatusUnauthorized)
		return
	}

	if err := h.xenditProvider.ValidateWebhookSignature(payload, signature, ""); err != nil {
		http.Error(w, "Invalid webhook signature", http.StatusUnauthorized)
		return
	}

	var event map[string]interface{}
	if err := json.Unmarshal(payload, &event); err != nil {
		http.Error(w, "Invalid webhook payload", http.StatusBadRequest)
		return
	}

	eventType, ok := event["event"].(string)
	if !ok {
		eventType = "unknown"
	}

	h.processXenditEvent(eventType, event)

	response := map[string]interface{}{
		"received":   true,
		"event_type": eventType,
		"timestamp":  time.Now(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (h *WebhookHandler) processStripeEvent(eventType string, event map[string]interface{}) {
	switch eventType {
	case "payment_intent.succeeded":
	case "payment_intent.payment_failed":
	case "payment_intent.requires_action":
	case "payment_intent.canceled":
	case "charge.refunded":
	case "charge.dispute.created":
	default:
	}
}

func (h *WebhookHandler) processXenditEvent(eventType string, event map[string]interface{}) {
	switch eventType {
	case "payment.succeeded":
	case "payment.failed":
	case "payment.pending":
	case "refund.succeeded":
	default:
	}
}

func (h *WebhookHandler) RegisterRoutes(router *mux.Router) {
	router.HandleFunc("/webhooks/stripe", h.HandleStripeWebhook).Methods("POST")
	router.HandleFunc("/webhooks/xendit", h.HandleXenditWebhook).Methods("POST")
}
