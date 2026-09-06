package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/malwarebo/conductor/internal/convert"
	"github.com/malwarebo/conductor/internal/crypto"
	"github.com/malwarebo/conductor/models"
	xendit "github.com/xendit/xendit-go/v7"
	"github.com/xendit/xendit-go/v7/customer"
	"github.com/xendit/xendit-go/v7/invoice"
	"github.com/xendit/xendit-go/v7/payment_method"
	paymentrequest "github.com/xendit/xendit-go/v7/payment_request"
	"github.com/xendit/xendit-go/v7/payout"
	"github.com/xendit/xendit-go/v7/refund"
)

const (
	xenditBaseURL    = "https://api.xendit.co"
	xenditAPIVersion = "2024-11-11"
)

type XenditProvider struct {
	apiKey        string
	webhookSecret string
	client        *xendit.APIClient
	httpClient    *http.Client
}

func CreateXenditProvider(apiKey string) *XenditProvider {
	client := xendit.NewClient(apiKey)
	return &XenditProvider{
		apiKey:     apiKey,
		client:     client,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func CreateXenditProviderWithWebhook(apiKey, webhookSecret string) *XenditProvider {
	client := xendit.NewClient(apiKey)
	return &XenditProvider{
		apiKey:        apiKey,
		webhookSecret: webhookSecret,
		client:        client,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}
}

type xenditRecurringSchedule struct {
	ReferenceID                string `json:"reference_id"`
	Interval                   string `json:"interval"`
	IntervalCount              int    `json:"interval_count"`
	TotalRecurrence            *int   `json:"total_recurrence,omitempty"`
	AnchorDate                 string `json:"anchor_date,omitempty"`
	RetryInterval              string `json:"retry_interval,omitempty"`
	RetryIntervalCount         int    `json:"retry_interval_count,omitempty"`
	FailedAttemptNotifications []int  `json:"failed_attempt_notifications,omitempty"`
}

type xenditRecurringPaymentMethod struct {
	PaymentMethodID string `json:"payment_method_id"`
	Rank            int    `json:"rank"`
}

type xenditCreateRecurringPlanRequest struct {
	ReferenceID         string                         `json:"reference_id"`
	CustomerID          string                         `json:"customer_id"`
	RecurringAction     string                         `json:"recurring_action"`
	Currency            string                         `json:"currency"`
	Amount              float64                        `json:"amount"`
	Schedule            xenditRecurringSchedule        `json:"schedule"`
	PaymentMethods      []xenditRecurringPaymentMethod `json:"payment_methods,omitempty"`
	ImmediateActionType string                         `json:"immediate_action_type,omitempty"`
	FailedCycleAction   string                         `json:"failed_cycle_action,omitempty"`
	Description         string                         `json:"description,omitempty"`
	Metadata            map[string]interface{}         `json:"metadata,omitempty"`
}

type xenditRecurringPlanResponse struct {
	ID              string                  `json:"id"`
	ReferenceID     string                  `json:"reference_id"`
	CustomerID      string                  `json:"customer_id"`
	RecurringAction string                  `json:"recurring_action"`
	Currency        string                  `json:"currency"`
	Amount          float64                 `json:"amount"`
	Status          string                  `json:"status"`
	Schedule        xenditRecurringSchedule `json:"schedule"`
	Created         string                  `json:"created"`
	Updated         string                  `json:"updated"`
	Description     string                  `json:"description,omitempty"`
	Metadata        map[string]interface{}  `json:"metadata,omitempty"`
	Actions         []xenditAction          `json:"actions,omitempty"`
}

type xenditAction struct {
	Action string `json:"action"`
	URL    string `json:"url"`
	Method string `json:"method"`
}

type xenditUpdateRecurringPlanRequest struct {
	CustomerID     string                         `json:"customer_id,omitempty"`
	Currency       string                         `json:"currency,omitempty"`
	Amount         float64                        `json:"amount,omitempty"`
	PaymentMethods []xenditRecurringPaymentMethod `json:"payment_methods,omitempty"`
	Description    string                         `json:"description,omitempty"`
	Metadata       map[string]interface{}         `json:"metadata,omitempty"`
	Status         string                         `json:"status,omitempty"`
}

type xenditRecurringPlanListResponse struct {
	Data    []xenditRecurringPlanResponse `json:"data"`
	HasMore bool                          `json:"has_more"`
}

type xenditTransaction struct {
	ID              string  `json:"id"`
	ProductID       string  `json:"product_id"`
	Type            string  `json:"type"`
	Status          string  `json:"status"`
	ChannelCategory string  `json:"channel_category"`
	ChannelCode     string  `json:"channel_code"`
	ReferenceID     string  `json:"reference_id"`
	AccountID       string  `json:"account_identifier"`
	Currency        string  `json:"currency"`
	Amount          float64 `json:"amount"`
	Cashflow        string  `json:"cashflow"`
	BusinessID      string  `json:"business_id"`
	Created         string  `json:"created"`
	Updated         string  `json:"updated"`
}

type xenditTransactionListResponse struct {
	Data    []xenditTransaction `json:"data"`
	HasMore bool                `json:"has_more"`
	Links   map[string]string   `json:"links"`
}

func (p *XenditProvider) doRequest(ctx context.Context, method, path string, body interface{}) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, xenditBaseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(p.apiKey, "")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-version", xenditAPIVersion)

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
		return nil, fmt.Errorf("xendit API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return respBody, nil
}

func (p *XenditProvider) buildListPath(basePath string, params map[string]string) string {
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

func (p *XenditProvider) Name() string {
	return "xendit"
}

func (p *XenditProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		SupportsInvoices:        true,
		SupportsPayouts:         true,
		SupportsPaymentSessions: true,
		Supports3DS:             true,
		SupportsManualCapture:   true,
		SupportsBalance:         true,
		SupportedCurrencies:     []string{"IDR", "PHP", "VND", "THB", "MYR", "SGD", "USD", "HKD", "AUD", "GBP", "EUR", "JPY", "MXN"},
		SupportedPaymentMethods: []models.PaymentMethodType{models.PMTypeCard, models.PMTypeEWallet, models.PMTypeVirtualAccount, models.PMTypeQRCode, models.PMTypeDirectDebit, models.PMTypeRetail},
	}
}

func (p *XenditProvider) Charge(ctx context.Context, req *models.ChargeRequest) (*models.ChargeResponse, error) {
	currency, err := p.getCurrency(req.Currency)
	if err != nil {
		return nil, fmt.Errorf("unsupported currency: %w", err)
	}

	paymentReq := paymentrequest.NewPaymentRequestParameters(currency)
	paymentReq.SetAmount(float64(req.Amount))
	paymentReq.SetReferenceId(req.CustomerID)
	paymentReq.SetDescription(req.Description)

	if req.PaymentMethod != "" {
		paymentReq.SetPaymentMethodId(req.PaymentMethod)
	}

	captureMethod := models.CaptureMethodAutomatic
	if req.CaptureMethod == models.CaptureMethodManual || (req.Capture != nil && !*req.Capture) {
		captureMethod = models.CaptureMethodManual
		paymentReq.SetCaptureMethod(paymentrequest.PAYMENTREQUESTCAPTUREMETHOD_MANUAL)
	}

	if req.Metadata != nil {
		paymentReq.SetMetadata(req.Metadata)
	}

	pr, _, sdkErr := p.client.PaymentRequestApi.CreatePaymentRequest(ctx).PaymentRequestParameters(*paymentReq).Execute()
	if sdkErr != nil {
		return nil, fmt.Errorf("xendit payment request creation failed: %w", sdkErr)
	}

	status := p.mapPaymentStatus(string(pr.GetStatus()))

	response := &models.ChargeResponse{
		ID:               pr.GetId(),
		CustomerID:       req.CustomerID,
		Amount:           req.Amount,
		Currency:         req.Currency,
		Status:           status,
		PaymentMethod:    req.PaymentMethod,
		Description:      req.Description,
		ProviderName:     "xendit",
		ProviderChargeID: pr.GetId(),
		CaptureMethod:    captureMethod,
		Metadata:         req.Metadata,
		CreatedAt:        time.Now(),
	}

	if actions := pr.GetActions(); len(actions) > 0 {
		response.RequiresAction = true
		for _, action := range actions {
			if action.GetAction() == "AUTH" {
				response.NextActionType = "redirect_to_url"
				response.NextActionURL = action.GetUrl()
			}
		}
	}

	return response, nil
}

func (p *XenditProvider) mapPaymentStatus(status string) models.PaymentStatus {
	statusMap := map[string]models.PaymentStatus{
		"SUCCEEDED":        models.PaymentStatusSuccess,
		"FAILED":           models.PaymentStatusFailed,
		"PENDING":          models.PaymentStatusPending,
		"AWAITING_CAPTURE": models.PaymentStatusRequiresCapture,
		"REQUIRES_ACTION":  models.PaymentStatusRequiresAction,
	}
	if s, ok := statusMap[status]; ok {
		return s
	}
	return models.PaymentStatusPending
}

func (p *XenditProvider) CapturePayment(ctx context.Context, paymentID string, amount int64) error {
	captureParams := paymentrequest.NewCaptureParameters(float64(amount))
	_, _, err := p.client.PaymentRequestApi.CapturePaymentRequest(ctx, paymentID).CaptureParameters(*captureParams).Execute()
	if err != nil {
		return fmt.Errorf("xendit capture failed: %w", err)
	}
	return nil
}

func (p *XenditProvider) VoidPayment(ctx context.Context, paymentID string) error {
	return fmt.Errorf("xendit does not support voiding payments directly, use refund instead")
}

func (p *XenditProvider) getCurrency(currency string) (paymentrequest.PaymentRequestCurrency, error) {
	curr, err := paymentrequest.NewPaymentRequestCurrencyFromValue(currency)
	if err != nil {
		return paymentrequest.PAYMENTREQUESTCURRENCY_IDR, fmt.Errorf("unsupported currency: %s", currency)
	}
	return *curr, nil
}

func (p *XenditProvider) CreatePaymentSession(ctx context.Context, req *models.CreatePaymentSessionRequest) (*models.PaymentSession, error) {
	currency, err := p.getCurrency(req.Currency)
	if err != nil {
		return nil, err
	}

	paymentReq := paymentrequest.NewPaymentRequestParameters(currency)
	paymentReq.SetAmount(float64(req.Amount))

	if req.ExternalID != "" {
		paymentReq.SetReferenceId(req.ExternalID)
	}

	if req.Description != "" {
		paymentReq.SetDescription(req.Description)
	}

	if req.PaymentMethodID != "" {
		paymentReq.SetPaymentMethodId(req.PaymentMethodID)
	}

	if req.CaptureMethod == models.CaptureMethodManual {
		paymentReq.SetCaptureMethod(paymentrequest.PAYMENTREQUESTCAPTUREMETHOD_MANUAL)
	}

	if req.Metadata != nil {
		paymentReq.SetMetadata(req.Metadata)
	}

	pr, _, sdkErr := p.client.PaymentRequestApi.CreatePaymentRequest(ctx).PaymentRequestParameters(*paymentReq).Execute()
	if sdkErr != nil {
		return nil, fmt.Errorf("xendit create payment session failed: %w", sdkErr)
	}

	return p.mapPaymentSession(pr), nil
}

func (p *XenditProvider) GetPaymentSession(ctx context.Context, sessionID string) (*models.PaymentSession, error) {
	pr, _, err := p.client.PaymentRequestApi.GetPaymentRequestByID(ctx, sessionID).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit get payment session failed: %w", err)
	}

	return p.mapPaymentSession(pr), nil
}

func (p *XenditProvider) UpdatePaymentSession(ctx context.Context, sessionID string, req *models.UpdatePaymentSessionRequest) (*models.PaymentSession, error) {
	return nil, ErrNotSupported
}

func (p *XenditProvider) ConfirmPaymentSession(ctx context.Context, sessionID string, req *models.ConfirmPaymentSessionRequest) (*models.PaymentSession, error) {
	pr, _, err := p.client.PaymentRequestApi.GetPaymentRequestByID(ctx, sessionID).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit confirm payment session failed: %w", err)
	}

	return p.mapPaymentSession(pr), nil
}

func (p *XenditProvider) CapturePaymentSession(ctx context.Context, sessionID string, amount *int64) (*models.PaymentSession, error) {
	captureAmount := float64(0)
	if amount != nil {
		captureAmount = float64(*amount)
	}

	captureParams := paymentrequest.NewCaptureParameters(captureAmount)
	_, _, err := p.client.PaymentRequestApi.CapturePaymentRequest(ctx, sessionID).CaptureParameters(*captureParams).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit capture payment session failed: %w", err)
	}

	return p.GetPaymentSession(ctx, sessionID)
}

func (p *XenditProvider) CancelPaymentSession(ctx context.Context, sessionID string) (*models.PaymentSession, error) {
	return nil, ErrNotSupported
}

func (p *XenditProvider) ListPaymentSessions(ctx context.Context, req *models.ListPaymentSessionsRequest) ([]*models.PaymentSession, error) {
	apiReq := p.client.PaymentRequestApi.GetAllPaymentRequests(ctx)

	if req.Limit > 0 && req.Limit <= math.MaxInt32 {
		apiReq = apiReq.Limit(int32(req.Limit))
	} else if req.Limit > math.MaxInt32 {
		apiReq = apiReq.Limit(math.MaxInt32)
	}

	resp, _, err := apiReq.Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit list payment sessions failed: %w", err)
	}

	var sessions []*models.PaymentSession
	for _, pr := range resp.GetData() {
		sessions = append(sessions, p.mapPaymentSession(&pr))
	}

	return sessions, nil
}

func (p *XenditProvider) mapPaymentSession(pr *paymentrequest.PaymentRequest) *models.PaymentSession {
	desc := ""
	if pr.Description.IsSet() && pr.Description.Get() != nil {
		desc = *pr.Description.Get()
	}

	session := &models.PaymentSession{
		ProviderID:    pr.GetId(),
		ProviderName:  "xendit",
		ExternalID:    pr.GetReferenceId(),
		Amount:        int64(pr.GetAmount()),
		Currency:      string(pr.GetCurrency()),
		Status:        p.mapPaymentStatus(string(pr.GetStatus())),
		CaptureMethod: models.CaptureMethod(pr.GetCaptureMethod()),
		Description:   desc,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	pm := pr.GetPaymentMethod()
	if pm.Id != "" {
		session.PaymentMethodID = pm.Id
	}

	if actions := pr.GetActions(); len(actions) > 0 {
		session.RequiresAction = true
		for _, action := range actions {
			if action.GetAction() == "AUTH" {
				session.NextActionType = "redirect_to_url"
				session.NextActionURL = action.GetUrl()
				session.NextAction = &models.NextAction{
					Type:        "redirect",
					RedirectURL: action.GetUrl(),
				}
			}
		}
	}

	return session
}

func (p *XenditProvider) CreateInvoice(ctx context.Context, req *models.CreateInvoiceRequest) (*models.Invoice, error) {
	invoiceReq := invoice.NewCreateInvoiceRequest(req.ExternalID, float64(req.Amount))

	if req.Currency != "" {
		invoiceReq.SetCurrency(req.Currency)
	}
	if req.CustomerEmail != "" {
		invoiceReq.SetPayerEmail(req.CustomerEmail)
	}
	if req.Description != "" {
		invoiceReq.SetDescription(req.Description)
	}
	if req.DurationSeconds > 0 {
		invoiceReq.SetInvoiceDuration(float32(req.DurationSeconds))
	}
	if req.SuccessRedirectURL != "" {
		invoiceReq.SetSuccessRedirectUrl(req.SuccessRedirectURL)
	}
	if req.FailureRedirectURL != "" {
		invoiceReq.SetFailureRedirectUrl(req.FailureRedirectURL)
	}
	invoiceReq.SetShouldSendEmail(req.SendEmail)

	inv, _, err := p.client.InvoiceApi.CreateInvoice(ctx).CreateInvoiceRequest(*invoiceReq).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit create invoice failed: %w", err)
	}

	return p.mapInvoice(inv), nil
}

func (p *XenditProvider) GetInvoice(ctx context.Context, invoiceID string) (*models.Invoice, error) {
	inv, _, err := p.client.InvoiceApi.GetInvoiceById(ctx, invoiceID).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit get invoice failed: %w", err)
	}

	return p.mapInvoice(inv), nil
}

func (p *XenditProvider) ListInvoices(ctx context.Context, req *models.ListInvoicesRequest) ([]*models.Invoice, error) {
	apiReq := p.client.InvoiceApi.GetInvoices(ctx)
	if req.Limit > 0 {
		apiReq = apiReq.Limit(float32(req.Limit))
	}

	invoices, _, err := apiReq.Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit list invoices failed: %w", err)
	}

	result := make([]*models.Invoice, len(invoices))
	for i, inv := range invoices {
		result[i] = p.mapInvoice(&inv)
	}

	return result, nil
}

func (p *XenditProvider) CancelInvoice(ctx context.Context, invoiceID string) (*models.Invoice, error) {
	inv, _, err := p.client.InvoiceApi.ExpireInvoice(ctx, invoiceID).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit cancel invoice failed: %w", err)
	}

	return p.mapInvoice(inv), nil
}

func (p *XenditProvider) mapInvoice(inv *invoice.Invoice) *models.Invoice {
	result := &models.Invoice{
		ProviderID:         inv.GetId(),
		ProviderName:       "xendit",
		ExternalID:         inv.GetExternalId(),
		Amount:             int64(inv.GetAmount()),
		Currency:           string(inv.GetCurrency()),
		CustomerEmail:      inv.GetPayerEmail(),
		Description:        inv.GetDescription(),
		Status:             p.mapInvoiceStatus(inv.GetStatus()),
		InvoiceURL:         inv.GetInvoiceUrl(),
		SuccessRedirectURL: "",
		FailureRedirectURL: "",
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	if inv.SuccessRedirectUrl != nil {
		result.SuccessRedirectURL = *inv.SuccessRedirectUrl
	}

	dueDate := inv.GetExpiryDate()
	result.DueDate = &dueDate

	return result
}

func (p *XenditProvider) mapInvoiceStatus(status invoice.InvoiceStatus) models.InvoiceStatus {
	switch status {
	case invoice.INVOICESTATUS_PENDING:
		return models.InvoiceStatusPending
	case invoice.INVOICESTATUS_PAID:
		return models.InvoiceStatusPaid
	case invoice.INVOICESTATUS_EXPIRED:
		return models.InvoiceStatusExpired
	case invoice.INVOICESTATUS_SETTLED:
		return models.InvoiceStatusPaid
	default:
		return models.InvoiceStatusPending
	}
}

func (p *XenditProvider) CreatePayout(ctx context.Context, req *models.CreatePayoutRequest) (*models.Payout, error) {
	payoutReq := payout.NewCreatePayoutRequest(
		req.ReferenceID,
		req.DestinationChannel,
		payout.DigitalPayoutChannelProperties{},
		float32(req.Amount),
		req.Currency,
	)

	if req.Description != "" {
		payoutReq.SetDescription(req.Description)
	}
	if req.Metadata != nil {
		payoutReq.SetMetadata(req.Metadata)
	}

	po, _, err := p.client.PayoutApi.CreatePayout(ctx).CreatePayoutRequest(*payoutReq).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit create payout failed: %w", err)
	}

	return p.mapPayout(po), nil
}

func (p *XenditProvider) GetPayout(ctx context.Context, payoutID string) (*models.Payout, error) {
	po, _, err := p.client.PayoutApi.GetPayoutById(ctx, payoutID).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit get payout failed: %w", err)
	}

	return p.mapPayout(po), nil
}

func (p *XenditProvider) ListPayouts(ctx context.Context, req *models.ListPayoutsRequest) ([]*models.Payout, error) {
	apiReq := p.client.PayoutApi.GetPayouts(ctx)
	if req.ReferenceID != "" {
		apiReq = apiReq.ReferenceId(req.ReferenceID)
	}
	if req.Limit > 0 {
		apiReq = apiReq.Limit(float32(req.Limit))
	}

	resp, _, err := apiReq.Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit list payouts failed: %w", err)
	}

	data := resp.GetData()
	result := make([]*models.Payout, 0, len(data))
	for i := range data {
		if mapped := p.mapPayout(&data[i]); mapped != nil {
			result = append(result, mapped)
		}
	}

	return result, nil
}

func (p *XenditProvider) CancelPayout(ctx context.Context, payoutID string) (*models.Payout, error) {
	po, _, err := p.client.PayoutApi.CancelPayout(ctx, payoutID).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit cancel payout failed: %w", err)
	}

	return p.mapPayout(po), nil
}

func (p *XenditProvider) GetPayoutChannels(ctx context.Context, currency string) ([]*models.PayoutChannel, error) {
	apiReq := p.client.PayoutApi.GetPayoutChannels(ctx)
	if currency != "" {
		apiReq = apiReq.Currency(currency)
	}

	channels, _, err := apiReq.Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit get payout channels failed: %w", err)
	}

	result := make([]*models.PayoutChannel, len(channels))
	for i, ch := range channels {
		result[i] = &models.PayoutChannel{
			Code:     ch.GetChannelCode(),
			Category: string(ch.GetChannelCategory()),
			Currency: ch.GetCurrency(),
		}
	}

	return result, nil
}

func (p *XenditProvider) mapPayout(po *payout.GetPayouts200ResponseDataInner) *models.Payout {
	if po.Payout == nil {
		return nil
	}
	pyt := po.Payout

	statusMap := map[string]models.PayoutStatus{
		"SUCCEEDED": models.PayoutStatusSucceeded,
		"FAILED":    models.PayoutStatusFailed,
		"CANCELLED": models.PayoutStatusCanceled,
		"PENDING":   models.PayoutStatusPending,
		"ACCEPTED":  models.PayoutStatusPending,
	}
	status := models.PayoutStatusPending
	if s, ok := statusMap[pyt.Status]; ok {
		status = s
	}

	result := &models.Payout{
		ProviderID:         pyt.Id,
		ProviderName:       "xendit",
		ReferenceID:        pyt.ReferenceId,
		Amount:             int64(pyt.Amount),
		Currency:           pyt.Currency,
		Status:             status,
		DestinationType:    models.DestinationBankAccount,
		DestinationChannel: pyt.ChannelCode,
		CreatedAt:          pyt.Created,
		UpdatedAt:          pyt.Updated,
	}

	if pyt.Description != nil {
		result.Description = *pyt.Description
	}
	if pyt.FailureCode != nil {
		result.FailureReason = *pyt.FailureCode
	}

	return result
}

func (p *XenditProvider) GetBalance(ctx context.Context, currency string) (*models.Balance, error) {
	apiReq := p.client.BalanceApi.GetBalance(ctx)
	if currency != "" {
		apiReq = apiReq.AccountType(currency)
	}

	bal, _, err := apiReq.Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit get balance failed: %w", err)
	}

	return &models.Balance{
		Available:    int64(bal.GetBalance()),
		ProviderName: "xendit",
		Currency:     currency,
	}, nil
}

func (p *XenditProvider) Refund(ctx context.Context, req *models.RefundRequest) (*models.RefundResponse, error) {
	refundData := refund.NewCreateRefund()
	refundData.SetInvoiceId(req.PaymentID)
	refundData.SetAmount(float64(req.Amount))
	refundData.SetReason(req.Reason)

	if req.Metadata != nil {
		refundData.SetMetadata(req.Metadata)
	}

	ref, _, err := p.client.RefundApi.CreateRefund(ctx).CreateRefund(*refundData).Execute()
	if err != nil {
		return nil, err
	}

	return &models.RefundResponse{
		ID:               ref.GetId(),
		PaymentID:        req.PaymentID,
		Amount:           req.Amount,
		Currency:         req.Currency,
		Status:           "succeeded",
		Reason:           req.Reason,
		ProviderName:     "xendit",
		ProviderRefundID: ref.GetId(),
		Metadata:         req.Metadata,
		CreatedAt:        time.Now(),
	}, nil
}

func (p *XenditProvider) CreatePaymentMethod(ctx context.Context, req *models.CreatePaymentMethodRequest) (*models.PaymentMethod, error) {
	reusability := payment_method.PAYMENTMETHODREUSABILITY_ONE_TIME_USE
	if req.Reusable {
		reusability = payment_method.PAYMENTMETHODREUSABILITY_MULTIPLE_USE
	}

	pmReq := payment_method.NewPaymentMethodParameters(
		payment_method.PaymentMethodType(req.Type),
		reusability,
	)

	if req.CustomerID != "" {
		pmReq.SetCustomerId(req.CustomerID)
	}
	if req.Metadata != nil {
		pmReq.SetMetadata(req.Metadata)
	}

	pm, _, err := p.client.PaymentMethodApi.CreatePaymentMethod(ctx).PaymentMethodParameters(*pmReq).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit create payment method failed: %w", err)
	}

	return p.mapPaymentMethod(pm), nil
}

func (p *XenditProvider) GetPaymentMethod(ctx context.Context, paymentMethodID string) (*models.PaymentMethod, error) {
	pm, _, err := p.client.PaymentMethodApi.GetPaymentMethodByID(ctx, paymentMethodID).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit get payment method failed: %w", err)
	}

	return p.mapPaymentMethod(pm), nil
}

func (p *XenditProvider) ListPaymentMethods(ctx context.Context, customerID string, pmType *models.PaymentMethodType) ([]*models.PaymentMethod, error) {
	apiReq := p.client.PaymentMethodApi.GetAllPaymentMethods(ctx)

	resp, _, err := apiReq.Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit list payment methods failed: %w", err)
	}

	data := resp.GetData()
	var result []*models.PaymentMethod
	for i := range data {
		pm := p.mapPaymentMethod(&data[i])
		if customerID != "" && pm.CustomerID != customerID {
			continue
		}
		if pmType != nil && pm.Type != *pmType {
			continue
		}
		result = append(result, pm)
	}

	return result, nil
}

func (p *XenditProvider) AttachPaymentMethod(ctx context.Context, paymentMethodID, customerID string) error {
	return ErrNotSupported
}

func (p *XenditProvider) DetachPaymentMethod(ctx context.Context, paymentMethodID string) error {
	return ErrNotSupported
}

func (p *XenditProvider) ExpirePaymentMethod(ctx context.Context, paymentMethodID string) (*models.PaymentMethod, error) {
	pm, _, err := p.client.PaymentMethodApi.ExpirePaymentMethod(ctx, paymentMethodID).Execute()
	if err != nil {
		return nil, fmt.Errorf("xendit expire payment method failed: %w", err)
	}

	return p.mapPaymentMethod(pm), nil
}

func (p *XenditProvider) mapPaymentMethod(pm *payment_method.PaymentMethod) *models.PaymentMethod {
	result := &models.PaymentMethod{
		ProviderPaymentMethodID: pm.GetId(),
		ProviderName:            "xendit",
		Type:                    models.PaymentMethodType(pm.GetType()),
		Reusable:                pm.GetReusability() == payment_method.PAYMENTMETHODREUSABILITY_MULTIPLE_USE,
		Status:                  string(pm.GetStatus()),
		ChannelCode:             "",
		CreatedAt:               pm.GetCreated(),
		UpdatedAt:               pm.GetUpdated(),
	}

	if pm.CustomerId.IsSet() && pm.CustomerId.Get() != nil {
		result.CustomerID = *pm.CustomerId.Get()
	}

	if pm.Metadata != nil {
		result.Metadata = pm.Metadata
	}

	return result
}

func (p *XenditProvider) billingPeriodToInterval(period models.BillingPeriod) string {
	intervals := map[models.BillingPeriod]string{
		models.BillingPeriodDaily:   "DAY",
		models.BillingPeriodWeekly:  "WEEK",
		models.BillingPeriodMonthly: "MONTH",
		models.BillingPeriodYearly:  "YEAR",
	}
	if interval, ok := intervals[period]; ok {
		return interval
	}
	return "MONTH"
}

func (p *XenditProvider) CreateSubscription(ctx context.Context, req *models.CreateSubscriptionRequest) (*models.Subscription, error) {
	interval := p.billingPeriodToInterval(models.BillingPeriod(req.PlanID))

	planReq := xenditCreateRecurringPlanRequest{
		ReferenceID:     fmt.Sprintf("sub_%s_%d", req.CustomerID, time.Now().Unix()),
		CustomerID:      req.CustomerID,
		RecurringAction: "PAYMENT",
		Currency:        "IDR",
		Amount:          0,
		Schedule: xenditRecurringSchedule{
			ReferenceID:   fmt.Sprintf("sched_%d", time.Now().Unix()),
			Interval:      interval,
			IntervalCount: 1,
		},
		ImmediateActionType: "FULL_AMOUNT",
		FailedCycleAction:   "RESUME",
	}

	if req.Metadata != nil {
		if metadata, ok := req.Metadata.(map[string]interface{}); ok {
			planReq.Metadata = metadata
			if amount, ok := metadata["amount"].(float64); ok {
				planReq.Amount = amount
			}
			if currency, ok := metadata["currency"].(string); ok {
				planReq.Currency = currency
			}
		}
	}

	if req.TrialDays != nil && *req.TrialDays > 0 {
		anchorDate := time.Now().AddDate(0, 0, *req.TrialDays)
		planReq.Schedule.AnchorDate = anchorDate.Format(time.RFC3339)
	}

	respBody, err := p.doRequest(ctx, "POST", "/recurring/plans", planReq)
	if err != nil {
		return nil, fmt.Errorf("xendit create subscription failed: %w", err)
	}

	var planResp xenditRecurringPlanResponse
	if err := json.Unmarshal(respBody, &planResp); err != nil {
		return nil, fmt.Errorf("failed to parse subscription response: %w", err)
	}

	return p.mapRecurringPlanToSubscription(&planResp, req.PlanID), nil
}

func (p *XenditProvider) UpdateSubscription(ctx context.Context, subscriptionID string, req *models.UpdateSubscriptionRequest) (*models.Subscription, error) {
	updateReq := xenditUpdateRecurringPlanRequest{}

	if req.Quantity != nil && *req.Quantity > 0 {
		if updateReq.Metadata == nil {
			updateReq.Metadata = make(map[string]interface{})
		}
		updateReq.Metadata["quantity"] = *req.Quantity
	}

	if req.Metadata != nil {
		if metadata, ok := req.Metadata.(map[string]interface{}); ok {
			if updateReq.Metadata == nil {
				updateReq.Metadata = make(map[string]interface{})
			}
			for k, v := range metadata {
				updateReq.Metadata[k] = v
			}
		}
	}

	respBody, err := p.doRequest(ctx, "PATCH", "/recurring/plans/"+subscriptionID, updateReq)
	if err != nil {
		return nil, fmt.Errorf("xendit update subscription failed: %w", err)
	}

	var planResp xenditRecurringPlanResponse
	if err := json.Unmarshal(respBody, &planResp); err != nil {
		return nil, fmt.Errorf("failed to parse subscription response: %w", err)
	}

	planID := ""
	if req.PlanID != nil {
		planID = *req.PlanID
	}

	return p.mapRecurringPlanToSubscription(&planResp, planID), nil
}

func (p *XenditProvider) CancelSubscription(ctx context.Context, subscriptionID string, req *models.CancelSubscriptionRequest) (*models.Subscription, error) {
	updateReq := xenditUpdateRecurringPlanRequest{
		Status: "INACTIVE",
	}

	if req.Reason != "" {
		updateReq.Metadata = map[string]interface{}{
			"cancellation_reason": req.Reason,
		}
	}

	respBody, err := p.doRequest(ctx, "PATCH", "/recurring/plans/"+subscriptionID, updateReq)
	if err != nil {
		return nil, fmt.Errorf("xendit cancel subscription failed: %w", err)
	}

	var planResp xenditRecurringPlanResponse
	if err := json.Unmarshal(respBody, &planResp); err != nil {
		return nil, fmt.Errorf("failed to parse subscription response: %w", err)
	}

	sub := p.mapRecurringPlanToSubscription(&planResp, "")
	now := time.Now()
	sub.CanceledAt = &now

	return sub, nil
}

func (p *XenditProvider) GetSubscription(ctx context.Context, subscriptionID string) (*models.Subscription, error) {
	respBody, err := p.doRequest(ctx, "GET", "/recurring/plans/"+subscriptionID, nil)
	if err != nil {
		return nil, fmt.Errorf("xendit get subscription failed: %w", err)
	}

	var planResp xenditRecurringPlanResponse
	if err := json.Unmarshal(respBody, &planResp); err != nil {
		return nil, fmt.Errorf("failed to parse subscription response: %w", err)
	}

	return p.mapRecurringPlanToSubscription(&planResp, ""), nil
}

func (p *XenditProvider) ListSubscriptions(ctx context.Context, customerID string) ([]*models.Subscription, error) {
	path := p.buildListPath("/recurring/plans", map[string]string{
		"customer_id": customerID,
	})

	respBody, err := p.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("xendit list subscriptions failed: %w", err)
	}

	var listResp xenditRecurringPlanListResponse
	if err := json.Unmarshal(respBody, &listResp); err != nil {
		return nil, fmt.Errorf("failed to parse subscriptions response: %w", err)
	}

	subscriptions := make([]*models.Subscription, 0, len(listResp.Data))
	for i := range listResp.Data {
		subscriptions = append(subscriptions, p.mapRecurringPlanToSubscription(&listResp.Data[i], ""))
	}

	return subscriptions, nil
}

func (p *XenditProvider) mapRecurringPlanToSubscription(plan *xenditRecurringPlanResponse, planID string) *models.Subscription {
	status := p.mapRecurringPlanStatus(plan.Status)
	created := convert.ParseTime(plan.Created)
	updated := convert.ParseTime(plan.Updated)

	quantity := 1
	if plan.Metadata != nil {
		if q, ok := plan.Metadata["quantity"].(float64); ok {
			quantity = int(q)
		}
	}

	sub := &models.Subscription{
		ID:                 plan.ID,
		CustomerID:         plan.CustomerID,
		PlanID:             planID,
		Status:             status,
		CurrentPeriodStart: created,
		CurrentPeriodEnd:   created.AddDate(0, 1, 0),
		Quantity:           quantity,
		ProviderName:       "xendit",
		CreatedAt:          created,
		UpdatedAt:          updated,
	}

	if plan.Metadata != nil {
		sub.Metadata = plan.Metadata
	}

	return sub
}

func (p *XenditProvider) mapRecurringPlanStatus(status string) models.SubscriptionStatus {
	statusMap := map[string]models.SubscriptionStatus{
		"ACTIVE":          models.SubscriptionStatusActive,
		"INACTIVE":        models.SubscriptionStatusCanceled,
		"PENDING":         models.SubscriptionStatus("pending"),
		"REQUIRES_ACTION": models.SubscriptionStatus("pending"),
	}
	if s, ok := statusMap[status]; ok {
		return s
	}
	return models.SubscriptionStatus("pending")
}

func (p *XenditProvider) CreatePlan(ctx context.Context, planReq *models.Plan) (*models.Plan, error) {
	planID := fmt.Sprintf("xnd_plan_%d", time.Now().UnixNano())
	interval := p.billingPeriodToInterval(planReq.BillingPeriod)

	metadata := make(map[string]interface{})
	if planReq.Metadata != nil {
		if m, ok := planReq.Metadata.(map[string]interface{}); ok {
			metadata = m
		}
	}
	metadata["xendit_interval"] = interval
	metadata["xendit_interval_count"] = 1

	result := &models.Plan{
		ID:            planID,
		Name:          planReq.Name,
		Description:   planReq.Description,
		Amount:        planReq.Amount,
		Currency:      planReq.Currency,
		BillingPeriod: planReq.BillingPeriod,
		PricingType:   planReq.PricingType,
		TrialDays:     planReq.TrialDays,
		Features:      planReq.Features,
		Metadata:      metadata,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	return result, nil
}

func (p *XenditProvider) UpdatePlan(ctx context.Context, planID string, planReq *models.Plan) (*models.Plan, error) {
	result := &models.Plan{
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
	}

	return result, nil
}

func (p *XenditProvider) DeletePlan(ctx context.Context, planID string) error {
	return nil
}

func (p *XenditProvider) GetPlan(ctx context.Context, planID string) (*models.Plan, error) {
	return nil, fmt.Errorf("xendit plans are stored locally, use the plan store to retrieve")
}

func (p *XenditProvider) ListPlans(ctx context.Context) ([]*models.Plan, error) {
	return nil, fmt.Errorf("xendit plans are stored locally, use the plan store to list")
}

func (p *XenditProvider) CreateDispute(ctx context.Context, req *models.CreateDisputeRequest) (*models.Dispute, error) {
	return nil, fmt.Errorf("xendit: disputes are initiated by card networks, cannot create via API")
}

func (p *XenditProvider) UpdateDispute(ctx context.Context, disputeID string, req *models.UpdateDisputeRequest) (*models.Dispute, error) {
	dispute, err := p.GetDispute(ctx, disputeID)
	if err != nil {
		return nil, err
	}

	if req.Metadata != nil {
		dispute.Metadata = req.Metadata
	}

	return dispute, nil
}

func (p *XenditProvider) AcceptDispute(ctx context.Context, disputeID string) (*models.Dispute, error) {
	return nil, fmt.Errorf("xendit: accepting disputes must be done via Xendit dashboard")
}

func (p *XenditProvider) ContestDispute(ctx context.Context, disputeID string, evidence map[string]interface{}) (*models.Dispute, error) {
	return nil, fmt.Errorf("xendit: contesting disputes must be done via Xendit dashboard after uploading evidence")
}

func (p *XenditProvider) SubmitDisputeEvidence(ctx context.Context, disputeID string, req *models.SubmitEvidenceRequest) (*models.Evidence, error) {
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("at least one evidence file URL is required")
	}

	evidence := &models.Evidence{
		ID:          fmt.Sprintf("xnd_evid_%s_%d", disputeID, time.Now().Unix()),
		DisputeID:   disputeID,
		Type:        req.Type,
		Description: req.Description,
		Files:       req.Files,
		Metadata:    req.Metadata,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	return evidence, nil
}

func (p *XenditProvider) GetDispute(ctx context.Context, disputeID string) (*models.Dispute, error) {
	respBody, err := p.doRequest(ctx, "GET", "/transactions/"+disputeID, nil)
	if err != nil {
		return nil, fmt.Errorf("xendit get dispute failed: %w", err)
	}

	var txn xenditTransaction
	if err := json.Unmarshal(respBody, &txn); err != nil {
		return nil, fmt.Errorf("failed to parse transaction response: %w", err)
	}

	if txn.Type != "CHARGEBACK" {
		return nil, fmt.Errorf("transaction %s is not a chargeback", disputeID)
	}

	return p.mapTransactionToDispute(&txn), nil
}

func (p *XenditProvider) ListDisputes(ctx context.Context, customerID string) ([]*models.Dispute, error) {
	path := "/transactions?types=CHARGEBACK"

	respBody, err := p.doRequest(ctx, "GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("xendit list disputes failed: %w", err)
	}

	var listResp xenditTransactionListResponse
	if err := json.Unmarshal(respBody, &listResp); err != nil {
		return nil, fmt.Errorf("failed to parse disputes response: %w", err)
	}

	var disputes []*models.Dispute
	for _, txn := range listResp.Data {
		if txn.Type == "CHARGEBACK" {
			disputes = append(disputes, p.mapTransactionToDispute(&txn))
		}
	}

	return disputes, nil
}

func (p *XenditProvider) GetDisputeStats(ctx context.Context) (*models.DisputeStats, error) {
	disputes, err := p.ListDisputes(ctx, "")
	if err != nil {
		return nil, err
	}

	stats := &models.DisputeStats{
		Total: int64(len(disputes)),
	}

	for _, d := range disputes {
		switch d.Status {
		case models.DisputeStatusOpen:
			stats.Open++
		case models.DisputeStatusWon:
			stats.Won++
		case models.DisputeStatusLost:
			stats.Lost++
		case models.DisputeStatusCanceled:
			stats.Canceled++
		}
	}

	return stats, nil
}

func (p *XenditProvider) mapTransactionToDispute(txn *xenditTransaction) *models.Dispute {
	created := convert.ParseTime(txn.Created)
	updated := convert.ParseTime(txn.Updated)

	statusMap := map[string]models.DisputeStatus{
		"SUCCESS": models.DisputeStatusLost,
		"SETTLED": models.DisputeStatusLost,
		"FAILED":  models.DisputeStatusWon,
		"VOIDED":  models.DisputeStatusWon,
		"PENDING": models.DisputeStatusOpen,
	}
	status := models.DisputeStatusOpen
	if s, ok := statusMap[txn.Status]; ok {
		status = s
	}

	return &models.Dispute{
		ID:            txn.ID,
		TransactionID: txn.ProductID,
		Amount:        int64(txn.Amount),
		Currency:      txn.Currency,
		Reason:        "chargeback",
		Status:        status,
		Evidence:      make(map[string]interface{}),
		DueBy:         created.AddDate(0, 0, 7),
		CreatedAt:     created,
		UpdatedAt:     updated,
	}
}

func (p *XenditProvider) ValidateWebhookSignature(payload []byte, signature, timestamp string) error {
	return crypto.ValidateHMACSHA256(payload, signature, p.webhookSecret)
}

func (p *XenditProvider) CreateCustomer(ctx context.Context, req *models.CreateCustomerRequest) (string, error) {
	customerReq := *customer.NewCustomerRequest(req.ExternalID)
	customerReq.SetEmail(req.Email)

	if req.Phone != "" {
		customerReq.SetMobileNumber(req.Phone)
	}

	if req.Metadata != nil {
		customerReq.SetMetadata(req.Metadata)
	}

	cust, _, err := p.client.CustomerApi.CreateCustomer(ctx).CustomerRequest(customerReq).Execute()
	if err != nil {
		return "", fmt.Errorf("xendit customer creation failed: %w", err)
	}

	return cust.GetId(), nil
}

func (p *XenditProvider) UpdateCustomer(ctx context.Context, customerID string, req *models.UpdateCustomerRequest) error {
	updateReq := customer.NewPatchCustomer()

	if req.Email != "" {
		updateReq.SetEmail(req.Email)
	}

	if req.Phone != "" {
		updateReq.SetMobileNumber(req.Phone)
	}

	if req.Metadata != nil {
		updateReq.SetMetadata(req.Metadata)
	}

	_, _, err := p.client.CustomerApi.UpdateCustomer(ctx, customerID).PatchCustomer(*updateReq).Execute()
	return err
}

func (p *XenditProvider) GetCustomer(ctx context.Context, customerID string) (*models.Customer, error) {
	cust, _, err := p.client.CustomerApi.GetCustomer(ctx, customerID).Execute()
	if err != nil {
		return nil, err
	}

	metadata := make(map[string]interface{})
	if custMetadata := cust.GetMetadata(); custMetadata != nil {
		metadata = custMetadata
	}

	result := &models.Customer{
		ExternalID: cust.GetReferenceId(),
		Email:      cust.GetEmail(),
		Metadata:   metadata,
		CreatedAt:  cust.GetCreated(),
	}

	if cust.MobileNumber.IsSet() {
		if phonePtr := cust.MobileNumber.Get(); phonePtr != nil {
			result.Phone = *phonePtr
		}
	}

	return result, nil
}

func (p *XenditProvider) DeleteCustomer(ctx context.Context, customerID string) error {
	return ErrNotSupported
}

func (p *XenditProvider) IsAvailable(ctx context.Context) bool {
	if p.apiKey == "" {
		return false
	}

	_, resp, err := p.client.InvoiceApi.GetInvoices(ctx).Execute()
	if err != nil && resp == nil {
		return false
	}

	return true
}
