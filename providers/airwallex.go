package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/malwarebo/conductor/internal/convert"
	"github.com/malwarebo/conductor/internal/crypto"
	"github.com/malwarebo/conductor/models"
)

const (
	airwallexProdURL    = "https://api.airwallex.com"
	airwallexDemoURL    = "https://api-demo.airwallex.com"
	airwallexAPIVersion = "2026-02-27"
)

type AirwallexProvider struct {
	clientID      string
	apiKey        string
	webhookSecret string
	baseURL       string
	httpClient    *http.Client
	accessToken   string
	tokenExpiry   time.Time
	tokenMu       sync.RWMutex
}

func CreateAirwallexProvider(clientID, apiKey string, useSandbox bool) *AirwallexProvider {
	baseURL := airwallexProdURL
	if useSandbox {
		baseURL = airwallexDemoURL
	}
	return &AirwallexProvider{
		clientID:   clientID,
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func CreateAirwallexProviderWithWebhook(clientID, apiKey, webhookSecret string, useSandbox bool) *AirwallexProvider {
	p := CreateAirwallexProvider(clientID, apiKey, useSandbox)
	p.webhookSecret = webhookSecret
	return p
}

type awxAuthResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

type awxPaymentIntentRequest struct {
	RequestID       string                 `json:"request_id"`
	Amount          float64                `json:"amount"`
	Currency        string                 `json:"currency"`
	MerchantOrderID string                 `json:"merchant_order_id,omitempty"`
	CustomerID      string                 `json:"customer_id,omitempty"`
	Descriptor      string                 `json:"descriptor,omitempty"`
	ReturnURL       string                 `json:"return_url,omitempty"`
	CaptureMethod   string                 `json:"capture_method,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

type awxPaymentIntentResponse struct {
	ID                   string                 `json:"id"`
	RequestID            string                 `json:"request_id"`
	Amount               float64                `json:"amount"`
	Currency             string                 `json:"currency"`
	MerchantOrderID      string                 `json:"merchant_order_id"`
	CustomerID           string                 `json:"customer_id"`
	Status               string                 `json:"status"`
	CapturedAmount       float64                `json:"captured_amount"`
	ClientSecret         string                 `json:"client_secret"`
	Descriptor           string                 `json:"descriptor"`
	CreatedAt            string                 `json:"created_at"`
	UpdatedAt            string                 `json:"updated_at"`
	NextAction           *awxNextAction         `json:"next_action,omitempty"`
	LatestPaymentAttempt *awxPaymentAttempt     `json:"latest_payment_attempt,omitempty"`
	Metadata             map[string]interface{} `json:"metadata,omitempty"`
}

type awxNextAction struct {
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
}

type awxPaymentAttempt struct {
	ID              string `json:"id"`
	PaymentMethodID string `json:"payment_method_id"`
	Status          string `json:"status"`
}

type awxRefundRequest struct {
	RequestID       string                 `json:"request_id"`
	PaymentIntentID string                 `json:"payment_intent_id"`
	Amount          float64                `json:"amount,omitempty"`
	Reason          string                 `json:"reason,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

type awxRefundResponse struct {
	ID              string                 `json:"id"`
	RequestID       string                 `json:"request_id"`
	PaymentIntentID string                 `json:"payment_intent_id"`
	Amount          float64                `json:"amount"`
	Currency        string                 `json:"currency"`
	Status          string                 `json:"status"`
	Reason          string                 `json:"reason"`
	CreatedAt       string                 `json:"created_at"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

type awxCustomerRequest struct {
	RequestID          string                 `json:"request_id"`
	MerchantCustomerID string                 `json:"merchant_customer_id"`
	Email              string                 `json:"email,omitempty"`
	FirstName          string                 `json:"first_name,omitempty"`
	LastName           string                 `json:"last_name,omitempty"`
	PhoneNumber        string                 `json:"phone_number,omitempty"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

type awxCustomerResponse struct {
	ID                 string                 `json:"id"`
	RequestID          string                 `json:"request_id"`
	MerchantCustomerID string                 `json:"merchant_customer_id"`
	Email              string                 `json:"email"`
	FirstName          string                 `json:"first_name"`
	LastName           string                 `json:"last_name"`
	PhoneNumber        string                 `json:"phone_number"`
	CreatedAt          string                 `json:"created_at"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

type awxTransferRequest struct {
	RequestID        string                 `json:"request_id"`
	SourceID         string                 `json:"source_id,omitempty"`
	BeneficiaryID    string                 `json:"beneficiary_id"`
	TransferAmount   float64                `json:"transfer_amount"`
	TransferCurrency string                 `json:"transfer_currency"`
	TransferMethod   string                 `json:"transfer_method"`
	Reason           string                 `json:"reason,omitempty"`
	Reference        string                 `json:"reference,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
}

type awxTransferResponse struct {
	ID            string                 `json:"id"`
	RequestID     string                 `json:"request_id"`
	Amount        float64                `json:"amount"`
	Currency      string                 `json:"currency"`
	Status        string                 `json:"status"`
	BeneficiaryID string                 `json:"beneficiary_id"`
	Reference     string                 `json:"reference"`
	CreatedAt     string                 `json:"created_at"`
	UpdatedAt     string                 `json:"updated_at"`
	FailureReason string                 `json:"failure_reason,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

type awxSubscriptionRequest struct {
	RequestID         string                 `json:"request_id"`
	BillingCustomerID string                 `json:"billing_customer_id"`
	CollectionMethod  string                 `json:"collection_method"`
	PaymentSourceID   string                 `json:"payment_source_id,omitempty"`
	Currency          string                 `json:"currency"`
	Duration          *awxDuration           `json:"duration,omitempty"`
	StartsAt          string                 `json:"starts_at,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

type awxDuration struct {
	Type  string `json:"type"`
	Value int    `json:"value,omitempty"`
}

type awxSubscriptionResponse struct {
	ID                    string                 `json:"id"`
	BillingCustomerID     string                 `json:"billing_customer_id"`
	Status                string                 `json:"status"`
	CollectionMethod      string                 `json:"collection_method"`
	Currency              string                 `json:"currency"`
	CurrentPeriodStartsAt string                 `json:"current_period_starts_at"`
	CurrentPeriodEndsAt   string                 `json:"current_period_ends_at"`
	TrialStartsAt         string                 `json:"trial_starts_at,omitempty"`
	TrialEndsAt           string                 `json:"trial_ends_at,omitempty"`
	EndsAt                string                 `json:"ends_at,omitempty"`
	CreatedAt             string                 `json:"created_at"`
	UpdatedAt             string                 `json:"updated_at"`
	Metadata              map[string]interface{} `json:"metadata,omitempty"`
}

type awxInvoiceRequest struct {
	RequestID         string                 `json:"request_id"`
	BillingCustomerID string                 `json:"billing_customer_id"`
	CollectionMethod  string                 `json:"collection_method,omitempty"`
	Currency          string                 `json:"currency"`
	DaysUntilDue      int                    `json:"days_until_due,omitempty"`
	Memo              string                 `json:"memo,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

type awxInvoiceResponse struct {
	ID                string                 `json:"id"`
	BillingCustomerID string                 `json:"billing_customer_id"`
	Number            string                 `json:"number"`
	Amount            float64                `json:"amount"`
	Currency          string                 `json:"currency"`
	Status            string                 `json:"status"`
	PaymentStatus     string                 `json:"payment_status"`
	Memo              string                 `json:"memo"`
	DueAt             string                 `json:"due_at"`
	PaidAt            string                 `json:"paid_at,omitempty"`
	FinalizedAt       string                 `json:"finalized_at,omitempty"`
	VoidedAt          string                 `json:"voided_at,omitempty"`
	HostedURL         string                 `json:"hosted_url"`
	PDFURL            string                 `json:"pdf_url"`
	CreatedAt         string                 `json:"created_at"`
	UpdatedAt         string                 `json:"updated_at"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

type awxBalanceResponse struct {
	AvailableAmount float64 `json:"available_amount"`
	PendingAmount   float64 `json:"pending_amount"`
	TotalAmount     float64 `json:"total_amount"`
	Currency        string  `json:"currency"`
}

type awxListResponse struct {
	Items      json.RawMessage `json:"items"`
	PageBefore string          `json:"page_before,omitempty"`
	PageAfter  string          `json:"page_after,omitempty"`
}

func (p *AirwallexProvider) authenticate(ctx context.Context) error {
	p.tokenMu.RLock()
	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		p.tokenMu.RUnlock()
		return nil
	}
	p.tokenMu.RUnlock()

	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()

	if p.accessToken != "" && time.Now().Before(p.tokenExpiry) {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/api/v1/authentication/login", nil)
	if err != nil {
		return fmt.Errorf("failed to create auth request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-client-id", p.clientID)
	req.Header.Set("x-api-key", p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("auth request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("authentication failed (status %d): %s", resp.StatusCode, string(body))
	}

	var authResp awxAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return fmt.Errorf("failed to parse auth response: %w", err)
	}

	p.accessToken = authResp.Token
	expiry, err := time.Parse(time.RFC3339, authResp.ExpiresAt)
	if err != nil {
		p.tokenExpiry = time.Now().Add(25 * time.Minute)
	} else {
		p.tokenExpiry = expiry.Add(-1 * time.Minute)
	}

	return nil
}

func (p *AirwallexProvider) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	if err := p.authenticate(ctx); err != nil {
		return nil, err
	}

	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	p.tokenMu.RLock()
	req.Header.Set("Authorization", "Bearer "+p.accessToken)
	p.tokenMu.RUnlock()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-version", airwallexAPIVersion)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("airwallex API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func (p *AirwallexProvider) requestID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func (p *AirwallexProvider) buildListPath(basePath string, params map[string]string) string {
	if len(params) == 0 {
		return basePath
	}
	q := url.Values{}
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	if encoded := q.Encode(); encoded != "" {
		return basePath + "?" + encoded
	}
	return basePath
}

func (p *AirwallexProvider) Name() string {
	return "airwallex"
}

func (p *AirwallexProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		SupportsInvoices:        true,
		SupportsPayouts:         true,
		SupportsPaymentSessions: true,
		Supports3DS:             true,
		SupportsManualCapture:   true,
		SupportsBalance:         true,
		SupportedCurrencies:     []string{"USD", "EUR", "GBP", "AUD", "NZD", "HKD", "SGD", "CNY", "JPY", "CAD", "CHF", "ILS", "THB", "MYR", "IDR", "PHP", "VND", "KRW", "INR"},
		SupportedPaymentMethods: []models.PaymentMethodType{models.PMTypeCard, models.PMTypeBankAccount, models.PMTypeEWallet, models.PMTypeQRCode},
	}
}

func (p *AirwallexProvider) Charge(ctx context.Context, req *models.ChargeRequest) (*models.ChargeResponse, error) {
	piReq := awxPaymentIntentRequest{
		RequestID:       p.requestID("pi"),
		Amount:          convert.CentsToFloat(req.Amount),
		Currency:        req.Currency,
		MerchantOrderID: req.CustomerID,
		CustomerID:      req.CustomerID,
		Descriptor:      req.Description,
		ReturnURL:       req.ReturnURL,
		CaptureMethod:   p.resolveCaptureMethod(req.CaptureMethod, req.Capture),
	}

	if req.Metadata != nil {
		piReq.Metadata = ConvertStringMapToMetadata(ConvertInterfaceMetadataToStringMap(req.Metadata))
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/pa/payment_intents/create", piReq)
	if err != nil {
		return nil, fmt.Errorf("charge failed: %w", err)
	}

	var piResp awxPaymentIntentResponse
	if err := json.Unmarshal(respBody, &piResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapChargeResponse(&piResp, req), nil
}

func (p *AirwallexProvider) resolveCaptureMethod(method models.CaptureMethod, capture *bool) string {
	if method == models.CaptureMethodManual || (capture != nil && !*capture) {
		return "manual"
	}
	return "automatic"
}

func (p *AirwallexProvider) mapChargeResponse(pi *awxPaymentIntentResponse, req *models.ChargeRequest) *models.ChargeResponse {
	captureMethod := models.CaptureMethodAutomatic
	if req != nil && (req.CaptureMethod == models.CaptureMethodManual || (req.Capture != nil && !*req.Capture)) {
		captureMethod = models.CaptureMethodManual
	}

	resp := &models.ChargeResponse{
		ID:               pi.ID,
		CustomerID:       pi.CustomerID,
		Amount:           convert.FloatToCents(pi.Amount),
		Currency:         pi.Currency,
		Status:           p.mapPaymentStatus(pi.Status),
		Description:      pi.Descriptor,
		ProviderName:     "airwallex",
		ProviderChargeID: pi.ID,
		CaptureMethod:    captureMethod,
		CapturedAmount:   convert.FloatToCents(pi.CapturedAmount),
		ClientSecret:     pi.ClientSecret,
		Metadata:         pi.Metadata,
		CreatedAt:        convert.ParseTime(pi.CreatedAt),
	}

	if pi.NextAction != nil {
		resp.RequiresAction = true
		resp.NextActionType = pi.NextAction.Type
		resp.NextActionURL = pi.NextAction.URL
	}

	return resp
}

func (p *AirwallexProvider) mapPaymentStatus(status string) models.PaymentStatus {
	statusMap := map[string]models.PaymentStatus{
		"SUCCEEDED":                models.PaymentStatusSuccess,
		"REQUIRES_PAYMENT_METHOD":  models.PaymentStatusRequiresAction,
		"REQUIRES_CUSTOMER_ACTION": models.PaymentStatusRequiresAction,
		"REQUIRES_CAPTURE":         models.PaymentStatusRequiresCapture,
		"PENDING":                  models.PaymentStatusProcessing,
		"PROCESSING":               models.PaymentStatusProcessing,
		"CANCELLED":                models.PaymentStatusCanceled,
		"FAILED":                   models.PaymentStatusFailed,
	}
	if s, ok := statusMap[status]; ok {
		return s
	}
	return models.PaymentStatusPending
}

func (p *AirwallexProvider) CapturePayment(ctx context.Context, paymentID string, amount int64) error {
	reqBody := map[string]interface{}{"request_id": p.requestID("cap")}
	if amount > 0 {
		reqBody["amount"] = convert.CentsToFloat(amount)
	}
	_, err := p.doRequest(ctx, "POST", "/api/v1/pa/payment_intents/"+paymentID+"/capture", reqBody)
	return err
}

func (p *AirwallexProvider) VoidPayment(ctx context.Context, paymentID string) error {
	reqBody := map[string]interface{}{
		"request_id":          p.requestID("void"),
		"cancellation_reason": "requested_by_customer",
	}
	_, err := p.doRequest(ctx, "POST", "/api/v1/pa/payment_intents/"+paymentID+"/cancel", reqBody)
	return err
}

func (p *AirwallexProvider) Create3DSSession(ctx context.Context, paymentID string, returnURL string) (*ThreeDSecureSession, error) {
	pi, err := p.getPaymentIntent(ctx, paymentID)
	if err != nil {
		return nil, err
	}

	session := &ThreeDSecureSession{
		PaymentID:    pi.ID,
		ClientSecret: pi.ClientSecret,
		Status:       pi.Status,
	}
	if pi.NextAction != nil {
		session.RedirectURL = pi.NextAction.URL
	}
	return session, nil
}

func (p *AirwallexProvider) Confirm3DSPayment(ctx context.Context, paymentID string) (*models.ChargeResponse, error) {
	pi, err := p.getPaymentIntent(ctx, paymentID)
	if err != nil {
		return nil, err
	}
	return p.mapChargeResponse(pi, nil), nil
}

func (p *AirwallexProvider) getPaymentIntent(ctx context.Context, id string) (*awxPaymentIntentResponse, error) {
	respBody, err := p.doRequest(ctx, "GET", "/api/v1/pa/payment_intents/"+id, nil)
	if err != nil {
		return nil, err
	}
	var pi awxPaymentIntentResponse
	if err := json.Unmarshal(respBody, &pi); err != nil {
		return nil, fmt.Errorf("failed to parse payment intent: %w", err)
	}
	return &pi, nil
}

func (p *AirwallexProvider) Refund(ctx context.Context, req *models.RefundRequest) (*models.RefundResponse, error) {
	refundReq := awxRefundRequest{
		RequestID:       p.requestID("ref"),
		PaymentIntentID: req.PaymentID,
		Amount:          convert.CentsToFloat(req.Amount),
		Reason:          req.Reason,
	}

	if req.Metadata != nil {
		refundReq.Metadata = ConvertStringMapToMetadata(ConvertInterfaceMetadataToStringMap(req.Metadata))
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/pa/refunds/create", refundReq)
	if err != nil {
		return nil, fmt.Errorf("refund failed: %w", err)
	}

	var refundResp awxRefundResponse
	if err := json.Unmarshal(respBody, &refundResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &models.RefundResponse{
		ID:               refundResp.ID,
		PaymentID:        req.PaymentID,
		Amount:           convert.FloatToCents(refundResp.Amount),
		Currency:         refundResp.Currency,
		Status:           p.mapRefundStatus(refundResp.Status),
		Reason:           refundResp.Reason,
		ProviderName:     "airwallex",
		ProviderRefundID: refundResp.ID,
		Metadata:         refundResp.Metadata,
		CreatedAt:        convert.ParseTime(refundResp.CreatedAt),
	}, nil
}

func (p *AirwallexProvider) mapRefundStatus(status string) string {
	statusMap := map[string]string{
		"SUCCEEDED": "settled",
		"SETTLED":   "settled",
		"PENDING":   "pending",
		"FAILED":    "failed",
	}
	if s, ok := statusMap[status]; ok {
		return s
	}
	return "pending"
}

func (p *AirwallexProvider) CreatePaymentSession(ctx context.Context, req *models.CreatePaymentSessionRequest) (*models.PaymentSession, error) {
	piReq := awxPaymentIntentRequest{
		RequestID:     p.requestID("ps"),
		Amount:        convert.CentsToFloat(req.Amount),
		Currency:      req.Currency,
		CustomerID:    req.CustomerID,
		Descriptor:    req.Description,
		ReturnURL:     req.ReturnURL,
		CaptureMethod: "automatic",
		Metadata:      req.Metadata,
	}

	if req.CaptureMethod == models.CaptureMethodManual {
		piReq.CaptureMethod = "manual"
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/pa/payment_intents/create", piReq)
	if err != nil {
		return nil, fmt.Errorf("create payment session failed: %w", err)
	}

	var piResp awxPaymentIntentResponse
	if err := json.Unmarshal(respBody, &piResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapPaymentSession(&piResp), nil
}

func (p *AirwallexProvider) GetPaymentSession(ctx context.Context, sessionID string) (*models.PaymentSession, error) {
	pi, err := p.getPaymentIntent(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get payment session failed: %w", err)
	}
	return p.mapPaymentSession(pi), nil
}

func (p *AirwallexProvider) UpdatePaymentSession(ctx context.Context, sessionID string, req *models.UpdatePaymentSessionRequest) (*models.PaymentSession, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) ConfirmPaymentSession(ctx context.Context, sessionID string, req *models.ConfirmPaymentSessionRequest) (*models.PaymentSession, error) {
	confirmReq := map[string]interface{}{"request_id": p.requestID("confirm")}
	if req.PaymentMethodID != "" {
		confirmReq["payment_method_id"] = req.PaymentMethodID
	}
	if req.ReturnURL != "" {
		confirmReq["return_url"] = req.ReturnURL
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/pa/payment_intents/"+sessionID+"/confirm", confirmReq)
	if err != nil {
		return nil, fmt.Errorf("confirm payment session failed: %w", err)
	}

	var piResp awxPaymentIntentResponse
	if err := json.Unmarshal(respBody, &piResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapPaymentSession(&piResp), nil
}

func (p *AirwallexProvider) CapturePaymentSession(ctx context.Context, sessionID string, amount *int64) (*models.PaymentSession, error) {
	captureReq := map[string]interface{}{"request_id": p.requestID("cap")}
	if amount != nil {
		captureReq["amount"] = convert.CentsToFloat(*amount)
	}

	if _, err := p.doRequest(ctx, "POST", "/api/v1/pa/payment_intents/"+sessionID+"/capture", captureReq); err != nil {
		return nil, fmt.Errorf("capture payment session failed: %w", err)
	}

	return p.GetPaymentSession(ctx, sessionID)
}

func (p *AirwallexProvider) CancelPaymentSession(ctx context.Context, sessionID string) (*models.PaymentSession, error) {
	cancelReq := map[string]interface{}{
		"request_id":          p.requestID("cancel"),
		"cancellation_reason": "requested_by_customer",
	}

	if _, err := p.doRequest(ctx, "POST", "/api/v1/pa/payment_intents/"+sessionID+"/cancel", cancelReq); err != nil {
		return nil, fmt.Errorf("cancel payment session failed: %w", err)
	}

	return p.GetPaymentSession(ctx, sessionID)
}

func (p *AirwallexProvider) ListPaymentSessions(ctx context.Context, req *models.ListPaymentSessionsRequest) ([]*models.PaymentSession, error) {
	path := p.buildListPath("/api/v1/pa/payment_intents", map[string]string{
		"customer_id": req.CustomerID,
	})

	respBody, err := p.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("list payment sessions failed: %w", err)
	}

	var listResp awxListResponse
	if err := json.Unmarshal(respBody, &listResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var items []awxPaymentIntentResponse
	if err := json.Unmarshal(listResp.Items, &items); err != nil {
		return nil, fmt.Errorf("failed to parse items: %w", err)
	}

	sessions := make([]*models.PaymentSession, 0, len(items))
	for i := range items {
		sessions = append(sessions, p.mapPaymentSession(&items[i]))
	}
	return sessions, nil
}

func (p *AirwallexProvider) mapPaymentSession(pi *awxPaymentIntentResponse) *models.PaymentSession {
	session := &models.PaymentSession{
		ProviderID:     pi.ID,
		ProviderName:   "airwallex",
		ExternalID:     pi.MerchantOrderID,
		Amount:         convert.FloatToCents(pi.Amount),
		Currency:       pi.Currency,
		Status:         p.mapPaymentStatus(pi.Status),
		CustomerID:     pi.CustomerID,
		Description:    pi.Descriptor,
		ClientSecret:   pi.ClientSecret,
		CapturedAmount: convert.FloatToCents(pi.CapturedAmount),
		Metadata:       pi.Metadata,
		CreatedAt:      convert.ParseTime(pi.CreatedAt),
		UpdatedAt:      convert.ParseTime(pi.UpdatedAt),
	}

	if pi.NextAction != nil {
		session.RequiresAction = true
		session.NextActionType = pi.NextAction.Type
		session.NextActionURL = pi.NextAction.URL
		session.NextAction = &models.NextAction{
			Type:        pi.NextAction.Type,
			RedirectURL: pi.NextAction.URL,
		}
	}

	if pi.LatestPaymentAttempt != nil {
		session.PaymentMethodID = pi.LatestPaymentAttempt.PaymentMethodID
	}

	return session
}

func (p *AirwallexProvider) CreateCustomer(ctx context.Context, req *models.CreateCustomerRequest) (string, error) {
	custReq := awxCustomerRequest{
		RequestID:          p.requestID("cust"),
		MerchantCustomerID: req.ExternalID,
		Email:              req.Email,
		FirstName:          req.Name,
		PhoneNumber:        req.Phone,
		Metadata:           req.Metadata,
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/pa/customers/create", custReq)
	if err != nil {
		return "", fmt.Errorf("create customer failed: %w", err)
	}

	var custResp awxCustomerResponse
	if err := json.Unmarshal(respBody, &custResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	return custResp.ID, nil
}

func (p *AirwallexProvider) UpdateCustomer(ctx context.Context, customerID string, req *models.UpdateCustomerRequest) error {
	updateReq := make(map[string]interface{})
	if req.Email != "" {
		updateReq["email"] = req.Email
	}
	if req.Name != "" {
		updateReq["first_name"] = req.Name
	}
	if req.Phone != "" {
		updateReq["phone_number"] = req.Phone
	}
	if req.Metadata != nil {
		updateReq["metadata"] = req.Metadata
	}

	_, err := p.doRequest(ctx, "POST", "/api/v1/pa/customers/"+customerID+"/update", updateReq)
	return err
}

func (p *AirwallexProvider) GetCustomer(ctx context.Context, customerID string) (*models.Customer, error) {
	respBody, err := p.doRequest(ctx, "GET", "/api/v1/pa/customers/"+customerID, nil)
	if err != nil {
		return nil, fmt.Errorf("get customer failed: %w", err)
	}

	var custResp awxCustomerResponse
	if err := json.Unmarshal(respBody, &custResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	name := custResp.FirstName
	if custResp.LastName != "" {
		name += " " + custResp.LastName
	}

	return &models.Customer{
		ExternalID: custResp.MerchantCustomerID,
		Email:      custResp.Email,
		Name:       name,
		Phone:      custResp.PhoneNumber,
		Metadata:   custResp.Metadata,
		CreatedAt:  convert.ParseTime(custResp.CreatedAt),
	}, nil
}

func (p *AirwallexProvider) DeleteCustomer(ctx context.Context, customerID string) error {
	return ErrNotSupported
}

func (p *AirwallexProvider) CreatePaymentMethod(ctx context.Context, req *models.CreatePaymentMethodRequest) (*models.PaymentMethod, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) GetPaymentMethod(ctx context.Context, paymentMethodID string) (*models.PaymentMethod, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) ListPaymentMethods(ctx context.Context, customerID string, pmType *models.PaymentMethodType) ([]*models.PaymentMethod, error) {
	return []*models.PaymentMethod{}, nil
}

func (p *AirwallexProvider) AttachPaymentMethod(ctx context.Context, paymentMethodID, customerID string) error {
	return ErrNotSupported
}

func (p *AirwallexProvider) DetachPaymentMethod(ctx context.Context, paymentMethodID string) error {
	return ErrNotSupported
}

func (p *AirwallexProvider) ExpirePaymentMethod(ctx context.Context, paymentMethodID string) (*models.PaymentMethod, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) CreateSubscription(ctx context.Context, req *models.CreateSubscriptionRequest) (*models.Subscription, error) {
	subReq := awxSubscriptionRequest{
		RequestID:         p.requestID("sub"),
		BillingCustomerID: req.CustomerID,
		CollectionMethod:  "AUTO_CHARGE",
		Currency:          "USD",
		Duration:          &awxDuration{Type: "FOREVER"},
	}

	if req.Metadata != nil {
		if metadata, ok := req.Metadata.(map[string]interface{}); ok {
			subReq.Metadata = metadata
			if currency, ok := metadata["currency"].(string); ok {
				subReq.Currency = currency
			}
			if collMethod, ok := metadata["collection_method"].(string); ok {
				subReq.CollectionMethod = collMethod
			}
		}
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/subscriptions/create", subReq)
	if err != nil {
		return nil, fmt.Errorf("create subscription failed: %w", err)
	}

	var subResp awxSubscriptionResponse
	if err := json.Unmarshal(respBody, &subResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapSubscription(&subResp, req.PlanID), nil
}

func (p *AirwallexProvider) UpdateSubscription(ctx context.Context, subscriptionID string, req *models.UpdateSubscriptionRequest) (*models.Subscription, error) {
	updateReq := make(map[string]interface{})
	if req.Metadata != nil {
		updateReq["metadata"] = req.Metadata
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/subscriptions/"+subscriptionID+"/update", updateReq)
	if err != nil {
		return nil, fmt.Errorf("update subscription failed: %w", err)
	}

	var subResp awxSubscriptionResponse
	if err := json.Unmarshal(respBody, &subResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	planID := ""
	if req.PlanID != nil {
		planID = *req.PlanID
	}
	return p.mapSubscription(&subResp, planID), nil
}

func (p *AirwallexProvider) CancelSubscription(ctx context.Context, subscriptionID string, req *models.CancelSubscriptionRequest) (*models.Subscription, error) {
	cancelReq := map[string]interface{}{"request_id": p.requestID("cancel")}
	if req.CancelAtPeriodEnd {
		cancelReq["cancel_at_period_end"] = true
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/subscriptions/"+subscriptionID+"/cancel", cancelReq)
	if err != nil {
		return nil, fmt.Errorf("cancel subscription failed: %w", err)
	}

	var subResp awxSubscriptionResponse
	if err := json.Unmarshal(respBody, &subResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapSubscription(&subResp, ""), nil
}

func (p *AirwallexProvider) GetSubscription(ctx context.Context, subscriptionID string) (*models.Subscription, error) {
	respBody, err := p.doRequest(ctx, "GET", "/api/v1/subscriptions/"+subscriptionID, nil)
	if err != nil {
		return nil, fmt.Errorf("get subscription failed: %w", err)
	}

	var subResp awxSubscriptionResponse
	if err := json.Unmarshal(respBody, &subResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapSubscription(&subResp, ""), nil
}

func (p *AirwallexProvider) ListSubscriptions(ctx context.Context, customerID string) ([]*models.Subscription, error) {
	path := p.buildListPath("/api/v1/subscriptions", map[string]string{
		"billing_customer_id": customerID,
	})

	respBody, err := p.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions failed: %w", err)
	}

	var listResp awxListResponse
	if err := json.Unmarshal(respBody, &listResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var items []awxSubscriptionResponse
	if err := json.Unmarshal(listResp.Items, &items); err != nil {
		return nil, fmt.Errorf("failed to parse items: %w", err)
	}

	subs := make([]*models.Subscription, 0, len(items))
	for i := range items {
		subs = append(subs, p.mapSubscription(&items[i], ""))
	}
	return subs, nil
}

func (p *AirwallexProvider) mapSubscription(sub *awxSubscriptionResponse, planID string) *models.Subscription {
	result := &models.Subscription{
		ID:                 sub.ID,
		CustomerID:         sub.BillingCustomerID,
		PlanID:             planID,
		Status:             p.mapSubscriptionStatus(sub.Status),
		CurrentPeriodStart: convert.ParseTime(sub.CurrentPeriodStartsAt),
		CurrentPeriodEnd:   convert.ParseTime(sub.CurrentPeriodEndsAt),
		Quantity:           1,
		ProviderName:       "airwallex",
		Metadata:           sub.Metadata,
		CreatedAt:          convert.ParseTime(sub.CreatedAt),
		UpdatedAt:          convert.ParseTime(sub.UpdatedAt),
		TrialEnd:           convert.ParseTimePtr(sub.TrialEndsAt),
		CanceledAt:         convert.ParseTimePtr(sub.EndsAt),
	}
	return result
}

func (p *AirwallexProvider) mapSubscriptionStatus(status string) models.SubscriptionStatus {
	statusMap := map[string]models.SubscriptionStatus{
		"ACTIVE":    models.SubscriptionStatusActive,
		"CANCELED":  models.SubscriptionStatusCanceled,
		"CANCELLED": models.SubscriptionStatusCanceled,
		"IN_TRIAL":  models.SubscriptionStatusTrialing,
		"TRIALING":  models.SubscriptionStatusTrialing,
		"UNPAID":    models.SubscriptionStatusPastDue,
		"PAST_DUE":  models.SubscriptionStatusPastDue,
		"PAUSED":    models.SubscriptionStatusPaused,
		"PENDING":   models.SubscriptionStatus("pending"),
	}
	if s, ok := statusMap[status]; ok {
		return s
	}
	return models.SubscriptionStatus("pending")
}

func (p *AirwallexProvider) CreatePlan(ctx context.Context, planReq *models.Plan) (*models.Plan, error) {
	now := time.Now()
	return &models.Plan{
		ID:            p.requestID("awx_plan"),
		Name:          planReq.Name,
		Description:   planReq.Description,
		Amount:        planReq.Amount,
		Currency:      planReq.Currency,
		BillingPeriod: planReq.BillingPeriod,
		PricingType:   planReq.PricingType,
		TrialDays:     planReq.TrialDays,
		Features:      planReq.Features,
		Metadata:      planReq.Metadata,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (p *AirwallexProvider) UpdatePlan(ctx context.Context, planID string, planReq *models.Plan) (*models.Plan, error) {
	return &models.Plan{
		ID:            planID,
		Name:          planReq.Name,
		Description:   planReq.Description,
		Amount:        planReq.Amount,
		Currency:      planReq.Currency,
		BillingPeriod: planReq.BillingPeriod,
		PricingType:   planReq.PricingType,
		TrialDays:     planReq.TrialDays,
		Features:      planReq.Features,
		Metadata:      planReq.Metadata,
		UpdatedAt:     time.Now(),
	}, nil
}

func (p *AirwallexProvider) DeletePlan(ctx context.Context, planID string) error {
	return nil
}

func (p *AirwallexProvider) GetPlan(ctx context.Context, planID string) (*models.Plan, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) ListPlans(ctx context.Context) ([]*models.Plan, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) CreateInvoice(ctx context.Context, req *models.CreateInvoiceRequest) (*models.Invoice, error) {
	invReq := awxInvoiceRequest{
		RequestID:         p.requestID("inv"),
		BillingCustomerID: req.CustomerID,
		Currency:          req.Currency,
		CollectionMethod:  "CHARGE_ON_CHECKOUT",
		Memo:              req.Description,
		Metadata:          req.Metadata,
	}

	if req.DueDate != nil {
		invReq.DaysUntilDue = int(time.Until(*req.DueDate).Hours() / 24)
		if invReq.DaysUntilDue < 0 {
			invReq.DaysUntilDue = 0
		}
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/invoices/create", invReq)
	if err != nil {
		return nil, fmt.Errorf("create invoice failed: %w", err)
	}

	var invResp awxInvoiceResponse
	if err := json.Unmarshal(respBody, &invResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapInvoice(&invResp), nil
}

func (p *AirwallexProvider) GetInvoice(ctx context.Context, invoiceID string) (*models.Invoice, error) {
	respBody, err := p.doRequest(ctx, "GET", "/api/v1/invoices/"+invoiceID, nil)
	if err != nil {
		return nil, fmt.Errorf("get invoice failed: %w", err)
	}

	var invResp awxInvoiceResponse
	if err := json.Unmarshal(respBody, &invResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapInvoice(&invResp), nil
}

func (p *AirwallexProvider) ListInvoices(ctx context.Context, req *models.ListInvoicesRequest) ([]*models.Invoice, error) {
	path := p.buildListPath("/api/v1/invoices", map[string]string{
		"billing_customer_id": req.CustomerID,
	})

	respBody, err := p.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("list invoices failed: %w", err)
	}

	var listResp awxListResponse
	if err := json.Unmarshal(respBody, &listResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var items []awxInvoiceResponse
	if err := json.Unmarshal(listResp.Items, &items); err != nil {
		return nil, fmt.Errorf("failed to parse items: %w", err)
	}

	invoices := make([]*models.Invoice, 0, len(items))
	for i := range items {
		invoices = append(invoices, p.mapInvoice(&items[i]))
	}
	return invoices, nil
}

func (p *AirwallexProvider) CancelInvoice(ctx context.Context, invoiceID string) (*models.Invoice, error) {
	respBody, err := p.doRequest(ctx, "POST", "/api/v1/invoices/"+invoiceID+"/void", nil)
	if err != nil {
		return nil, fmt.Errorf("void invoice failed: %w", err)
	}

	var invResp awxInvoiceResponse
	if err := json.Unmarshal(respBody, &invResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapInvoice(&invResp), nil
}

func (p *AirwallexProvider) mapInvoice(inv *awxInvoiceResponse) *models.Invoice {
	result := &models.Invoice{
		ProviderID:   inv.ID,
		ProviderName: "airwallex",
		CustomerID:   inv.BillingCustomerID,
		Amount:       convert.FloatToCents(inv.Amount),
		Currency:     inv.Currency,
		Status:       p.mapInvoiceStatus(inv.Status, inv.PaymentStatus),
		Description:  inv.Memo,
		InvoiceURL:   inv.HostedURL,
		Metadata:     inv.Metadata,
		CreatedAt:    convert.ParseTime(inv.CreatedAt),
		UpdatedAt:    convert.ParseTime(inv.UpdatedAt),
		DueDate:      convert.ParseTimePtr(inv.DueAt),
		PaidAt:       convert.ParseTimePtr(inv.PaidAt),
	}
	return result
}

func (p *AirwallexProvider) mapInvoiceStatus(status, paymentStatus string) models.InvoiceStatus {
	if status == "VOIDED" {
		return models.InvoiceStatusVoid
	}
	if paymentStatus == "PAID" {
		return models.InvoiceStatusPaid
	}
	statusMap := map[string]models.InvoiceStatus{
		"DRAFT":     models.InvoiceStatusDraft,
		"FINALIZED": models.InvoiceStatusPending,
		"OPEN":      models.InvoiceStatusPending,
	}
	if s, ok := statusMap[status]; ok {
		return s
	}
	return models.InvoiceStatusPending
}

func (p *AirwallexProvider) CreatePayout(ctx context.Context, req *models.CreatePayoutRequest) (*models.Payout, error) {
	transferReq := awxTransferRequest{
		RequestID:        p.requestID("transfer"),
		BeneficiaryID:    req.DestinationAccount,
		TransferAmount:   convert.CentsToFloat(req.Amount),
		TransferCurrency: req.Currency,
		TransferMethod:   "LOCAL",
		Reference:        req.ReferenceID,
		Reason:           req.Description,
		SourceID:         req.SourceAccount,
		Metadata:         req.Metadata,
	}

	respBody, err := p.doRequest(ctx, "POST", "/api/v1/transfers/create", transferReq)
	if err != nil {
		return nil, fmt.Errorf("create payout failed: %w", err)
	}

	var transferResp awxTransferResponse
	if err := json.Unmarshal(respBody, &transferResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapPayout(&transferResp, req.Description), nil
}

func (p *AirwallexProvider) GetPayout(ctx context.Context, payoutID string) (*models.Payout, error) {
	respBody, err := p.doRequest(ctx, "GET", "/api/v1/transfers/"+payoutID, nil)
	if err != nil {
		return nil, fmt.Errorf("get payout failed: %w", err)
	}

	var transferResp awxTransferResponse
	if err := json.Unmarshal(respBody, &transferResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapPayout(&transferResp, ""), nil
}

func (p *AirwallexProvider) ListPayouts(ctx context.Context, req *models.ListPayoutsRequest) ([]*models.Payout, error) {
	respBody, err := p.doRequest(ctx, "GET", "/api/v1/transfers", nil)
	if err != nil {
		return nil, fmt.Errorf("list payouts failed: %w", err)
	}

	var listResp awxListResponse
	if err := json.Unmarshal(respBody, &listResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	var items []awxTransferResponse
	if err := json.Unmarshal(listResp.Items, &items); err != nil {
		return nil, fmt.Errorf("failed to parse items: %w", err)
	}

	payouts := make([]*models.Payout, 0, len(items))
	for i := range items {
		payouts = append(payouts, p.mapPayout(&items[i], ""))
	}
	return payouts, nil
}

func (p *AirwallexProvider) CancelPayout(ctx context.Context, payoutID string) (*models.Payout, error) {
	respBody, err := p.doRequest(ctx, "POST", "/api/v1/transfers/"+payoutID+"/cancel", map[string]interface{}{
		"request_id": p.requestID("cancel"),
	})
	if err != nil {
		return nil, fmt.Errorf("cancel payout failed: %w", err)
	}

	var transferResp awxTransferResponse
	if err := json.Unmarshal(respBody, &transferResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return p.mapPayout(&transferResp, ""), nil
}

func (p *AirwallexProvider) GetPayoutChannels(ctx context.Context, currency string) ([]*models.PayoutChannel, error) {
	return []*models.PayoutChannel{
		{Code: "LOCAL", Name: "Local Bank Transfer", Category: "bank", Currency: currency},
		{Code: "SWIFT", Name: "SWIFT Transfer", Category: "bank", Currency: currency},
	}, nil
}

func (p *AirwallexProvider) mapPayout(t *awxTransferResponse, description string) *models.Payout {
	return &models.Payout{
		ProviderID:         t.ID,
		ProviderName:       "airwallex",
		ReferenceID:        t.Reference,
		Amount:             convert.FloatToCents(t.Amount),
		Currency:           t.Currency,
		Status:             p.mapPayoutStatus(t.Status),
		Description:        description,
		DestinationType:    models.DestinationBankAccount,
		DestinationAccount: t.BeneficiaryID,
		FailureReason:      t.FailureReason,
		Metadata:           t.Metadata,
		CreatedAt:          convert.ParseTime(t.CreatedAt),
		UpdatedAt:          convert.ParseTime(t.UpdatedAt),
	}
}

func (p *AirwallexProvider) mapPayoutStatus(status string) models.PayoutStatus {
	statusMap := map[string]models.PayoutStatus{
		"SUCCEEDED":   models.PayoutStatusSucceeded,
		"COMPLETED":   models.PayoutStatusSucceeded,
		"PAID":        models.PayoutStatusSucceeded,
		"FAILED":      models.PayoutStatusFailed,
		"CANCELLED":   models.PayoutStatusCanceled,
		"PENDING":     models.PayoutStatusProcessing,
		"IN_PROGRESS": models.PayoutStatusProcessing,
	}
	if s, ok := statusMap[status]; ok {
		return s
	}
	return models.PayoutStatusPending
}

func (p *AirwallexProvider) GetBalance(ctx context.Context, currency string) (*models.Balance, error) {
	path := p.buildListPath("/api/v1/balances/current", map[string]string{
		"currency": currency,
	})

	respBody, err := p.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("get balance failed: %w", err)
	}

	var balResp awxBalanceResponse
	if err := json.Unmarshal(respBody, &balResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &models.Balance{
		Available:    convert.FloatToCents(balResp.AvailableAmount),
		Pending:      convert.FloatToCents(balResp.PendingAmount),
		ProviderName: "airwallex",
		Currency:     balResp.Currency,
	}, nil
}

func (p *AirwallexProvider) CreateDispute(ctx context.Context, req *models.CreateDisputeRequest) (*models.Dispute, error) {
	return nil, fmt.Errorf("disputes are initiated by card networks")
}

func (p *AirwallexProvider) UpdateDispute(ctx context.Context, disputeID string, req *models.UpdateDisputeRequest) (*models.Dispute, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) AcceptDispute(ctx context.Context, disputeID string) (*models.Dispute, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) ContestDispute(ctx context.Context, disputeID string, evidence map[string]interface{}) (*models.Dispute, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) SubmitDisputeEvidence(ctx context.Context, disputeID string, req *models.SubmitEvidenceRequest) (*models.Evidence, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) GetDispute(ctx context.Context, disputeID string) (*models.Dispute, error) {
	return nil, ErrNotSupported
}

func (p *AirwallexProvider) ListDisputes(ctx context.Context, customerID string) ([]*models.Dispute, error) {
	return []*models.Dispute{}, nil
}

func (p *AirwallexProvider) GetDisputeStats(ctx context.Context) (*models.DisputeStats, error) {
	return &models.DisputeStats{}, nil
}

func (p *AirwallexProvider) ValidateWebhookSignature(payload []byte, signature, timestamp string) error {
	return crypto.ValidateHMACSHA256WithTimestamp(payload, signature, p.webhookSecret, timestamp, crypto.DefaultTimestampTolerance)
}

func (p *AirwallexProvider) IsAvailable(ctx context.Context) bool {
	if p.clientID == "" || p.apiKey == "" {
		return false
	}
	return p.authenticate(ctx) == nil
}
