package providers

import (
	"context"
	"fmt"
	"time"

	"github.com/malwarebo/conductor/internal/convert"
	"github.com/malwarebo/conductor/internal/crypto"
	"github.com/malwarebo/conductor/models"
	razorpay "github.com/razorpay/razorpay-go"
)

type RazorpayProvider struct {
	keyID         string
	keySecret     string
	webhookSecret string
	client        *razorpay.Client
}

func CreateRazorpayProvider(keyID, keySecret string) *RazorpayProvider {
	client := razorpay.NewClient(keyID, keySecret)
	return &RazorpayProvider{
		keyID:     keyID,
		keySecret: keySecret,
		client:    client,
	}
}

func CreateRazorpayProviderWithWebhook(keyID, keySecret, webhookSecret string) *RazorpayProvider {
	client := razorpay.NewClient(keyID, keySecret)
	return &RazorpayProvider{
		keyID:         keyID,
		keySecret:     keySecret,
		webhookSecret: webhookSecret,
		client:        client,
	}
}

func (p *RazorpayProvider) Name() string {
	return "razorpay"
}

func (p *RazorpayProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		SupportsInvoices:        true,
		SupportsPayouts:         true,
		SupportsPaymentSessions: true,
		Supports3DS:             true,
		SupportsManualCapture:   true,
		SupportsBalance:         false,
		SupportedCurrencies:     []string{"INR", "USD", "EUR", "GBP", "SGD", "AED", "AUD", "CAD", "HKD", "JPY", "MYR", "SAR"},
		SupportedPaymentMethods: []models.PaymentMethodType{
			models.PMTypeCard,
			models.PMTypeUPI,
			models.PMTypeNetbanking,
			models.PMTypeWallet,
			models.PMTypeEMI,
			models.PMTypeCardlessEMI,
		},
	}
}

func (p *RazorpayProvider) Charge(ctx context.Context, req *models.ChargeRequest) (*models.ChargeResponse, error) {
	orderData := map[string]interface{}{
		"amount":   req.Amount,
		"currency": req.Currency,
		"receipt":  req.CustomerID,
	}

	if req.Metadata != nil {
		notes := make(map[string]interface{})
		for k, v := range req.Metadata {
			notes[k] = v
		}
		if req.Description != "" {
			notes["description"] = req.Description
		}
		orderData["notes"] = notes
	} else if req.Description != "" {
		orderData["notes"] = map[string]interface{}{
			"description": req.Description,
		}
	}

	order, err := p.client.Order.Create(orderData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay order creation failed: %w", err)
	}

	orderID := convert.StringFromMap(order, "id")
	status := p.mapOrderStatus(convert.StringFromMap(order, "status"))

	captureMethod := models.CaptureMethodAutomatic
	if req.CaptureMethod == models.CaptureMethodManual || (req.Capture != nil && !*req.Capture) {
		captureMethod = models.CaptureMethodManual
	}

	return &models.ChargeResponse{
		ID:               orderID,
		CustomerID:       req.CustomerID,
		Amount:           req.Amount,
		Currency:         req.Currency,
		Status:           status,
		PaymentMethod:    req.PaymentMethod,
		Description:      req.Description,
		ProviderName:     "razorpay",
		ProviderChargeID: orderID,
		CaptureMethod:    captureMethod,
		Metadata:         req.Metadata,
		CreatedAt:        time.Now(),
		RequiresAction:   true,
		NextActionType:   "razorpay_checkout",
		ClientSecret:     orderID,
	}, nil
}

var razorpayOrderStatusMap = map[string]models.PaymentStatus{
	"paid":      models.PaymentStatusSuccess,
	"attempted": models.PaymentStatusProcessing,
	"created":   models.PaymentStatusPending,
}

func (p *RazorpayProvider) mapOrderStatus(status string) models.PaymentStatus {
	if s, ok := razorpayOrderStatusMap[status]; ok {
		return s
	}
	return models.PaymentStatusPending
}

func (p *RazorpayProvider) CapturePayment(ctx context.Context, paymentID string, amount int64) error {
	captureData := map[string]interface{}{
		"amount":   amount,
		"currency": "INR",
	}

	_, err := p.client.Payment.Capture(paymentID, int(amount), captureData, nil)
	if err != nil {
		return fmt.Errorf("razorpay capture failed: %w", err)
	}
	return nil
}

func (p *RazorpayProvider) VoidPayment(ctx context.Context, paymentID string) error {
	return fmt.Errorf("razorpay does not support voiding payments directly, use refund instead")
}

func (p *RazorpayProvider) Refund(ctx context.Context, req *models.RefundRequest) (*models.RefundResponse, error) {
	refundData := map[string]interface{}{
		"amount": req.Amount,
	}

	if req.Metadata != nil {
		notes := make(map[string]interface{})
		for k, v := range req.Metadata {
			notes[k] = v
		}
		if req.Reason != "" {
			notes["reason"] = req.Reason
		}
		refundData["notes"] = notes
	} else if req.Reason != "" {
		refundData["notes"] = map[string]interface{}{
			"reason": req.Reason,
		}
	}

	ref, err := p.client.Payment.Refund(req.PaymentID, int(req.Amount), refundData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay refund failed: %w", err)
	}

	refundID := convert.StringFromMap(ref, "id")
	status := convert.StringFromMap(ref, "status")
	if status == "" {
		status = "processed"
	}

	return &models.RefundResponse{
		ID:               refundID,
		PaymentID:        req.PaymentID,
		Amount:           req.Amount,
		Currency:         req.Currency,
		Status:           status,
		Reason:           req.Reason,
		ProviderName:     "razorpay",
		ProviderRefundID: refundID,
		Metadata:         req.Metadata,
		CreatedAt:        time.Now(),
	}, nil
}

func (p *RazorpayProvider) CreatePaymentSession(ctx context.Context, req *models.CreatePaymentSessionRequest) (*models.PaymentSession, error) {
	orderData := map[string]interface{}{
		"amount":   req.Amount,
		"currency": req.Currency,
	}

	if req.ExternalID != "" {
		orderData["receipt"] = req.ExternalID
	}

	if req.Metadata != nil {
		notes := make(map[string]interface{})
		for k, v := range req.Metadata {
			notes[k] = v
		}
		if req.Description != "" {
			notes["description"] = req.Description
		}
		orderData["notes"] = notes
	}

	order, err := p.client.Order.Create(orderData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay create payment session failed: %w", err)
	}

	return p.mapOrderToPaymentSession(order, req.CustomerID), nil
}

func (p *RazorpayProvider) GetPaymentSession(ctx context.Context, sessionID string) (*models.PaymentSession, error) {
	order, err := p.client.Order.Fetch(sessionID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get payment session failed: %w", err)
	}

	return p.mapOrderToPaymentSession(order, ""), nil
}

func (p *RazorpayProvider) UpdatePaymentSession(ctx context.Context, sessionID string, req *models.UpdatePaymentSessionRequest) (*models.PaymentSession, error) {
	return nil, ErrNotSupported
}

func (p *RazorpayProvider) ConfirmPaymentSession(ctx context.Context, sessionID string, req *models.ConfirmPaymentSessionRequest) (*models.PaymentSession, error) {
	order, err := p.client.Order.Fetch(sessionID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay confirm payment session failed: %w", err)
	}

	return p.mapOrderToPaymentSession(order, ""), nil
}

func (p *RazorpayProvider) CapturePaymentSession(ctx context.Context, sessionID string, amount *int64) (*models.PaymentSession, error) {
	payments, err := p.client.Order.Payments(sessionID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get order payments failed: %w", err)
	}

	items, ok := payments["items"].([]interface{})
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("no payments found for order")
	}

	payment, ok := items[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("razorpay invalid payment response")
	}
	paymentID := convert.StringFromMap(payment, "id")

	captureAmount := int64(0)
	if amount != nil {
		captureAmount = *amount
	} else if amountVal, ok := payment["amount"].(float64); ok {
		captureAmount = int64(amountVal)
	}

	err = p.CapturePayment(ctx, paymentID, captureAmount)
	if err != nil {
		return nil, err
	}

	return p.GetPaymentSession(ctx, sessionID)
}

func (p *RazorpayProvider) CancelPaymentSession(ctx context.Context, sessionID string) (*models.PaymentSession, error) {
	return nil, ErrNotSupported
}

func (p *RazorpayProvider) ListPaymentSessions(ctx context.Context, req *models.ListPaymentSessionsRequest) ([]*models.PaymentSession, error) {
	options := map[string]interface{}{}
	if req.Limit > 0 {
		options["count"] = req.Limit
	}

	orders, err := p.client.Order.All(options, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay list payment sessions failed: %w", err)
	}

	items, ok := orders["items"].([]interface{})
	if !ok {
		return []*models.PaymentSession{}, nil
	}

	var sessions []*models.PaymentSession
	for _, item := range items {
		orderMap, ok := item.(map[string]interface{})
		if ok {
			sessions = append(sessions, p.mapOrderToPaymentSession(orderMap, ""))
		}
	}

	return sessions, nil
}

func (p *RazorpayProvider) mapOrderToPaymentSession(order map[string]interface{}, customerID string) *models.PaymentSession {
	orderID := convert.StringFromMap(order, "id")
	status := p.mapOrderStatus(convert.StringFromMap(order, "status"))
	amount := convert.Int64FromMap(order, "amount")
	currency := convert.StringFromMap(order, "currency")
	receipt := convert.StringFromMap(order, "receipt")

	session := &models.PaymentSession{
		ProviderID:     orderID,
		ProviderName:   "razorpay",
		ExternalID:     receipt,
		Amount:         amount,
		Currency:       currency,
		Status:         status,
		CaptureMethod:  models.CaptureMethodAutomatic,
		CustomerID:     customerID,
		ClientSecret:   orderID,
		RequiresAction: status == models.PaymentStatusPending,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if status == models.PaymentStatusPending {
		session.NextActionType = "razorpay_checkout"
		session.NextAction = &models.NextAction{
			Type: "razorpay_checkout",
		}
	}

	return session
}

func (p *RazorpayProvider) CreateInvoice(ctx context.Context, req *models.CreateInvoiceRequest) (*models.Invoice, error) {
	invoiceData := map[string]interface{}{
		"type":     "invoice",
		"currency": req.Currency,
	}

	if req.CustomerID != "" {
		invoiceData["customer_id"] = req.CustomerID
	}

	if req.Description != "" {
		invoiceData["description"] = req.Description
	}

	if req.DueDate != nil {
		invoiceData["expire_by"] = req.DueDate.Unix()
	}

	lineItems := []map[string]interface{}{
		{
			"name":   "Payment",
			"amount": req.Amount,
		},
	}
	invoiceData["line_items"] = lineItems

	if req.CustomerEmail != "" {
		invoiceData["customer"] = map[string]interface{}{
			"email": req.CustomerEmail,
		}
	}

	inv, err := p.client.Invoice.Create(invoiceData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay create invoice failed: %w", err)
	}

	return p.mapInvoice(inv), nil
}

func (p *RazorpayProvider) GetInvoice(ctx context.Context, invoiceID string) (*models.Invoice, error) {
	inv, err := p.client.Invoice.Fetch(invoiceID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get invoice failed: %w", err)
	}

	return p.mapInvoice(inv), nil
}

func (p *RazorpayProvider) ListInvoices(ctx context.Context, req *models.ListInvoicesRequest) ([]*models.Invoice, error) {
	options := map[string]interface{}{}
	if req.Limit > 0 {
		options["count"] = req.Limit
	}

	invoices, err := p.client.Invoice.All(options, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay list invoices failed: %w", err)
	}

	items, ok := invoices["items"].([]interface{})
	if !ok {
		return []*models.Invoice{}, nil
	}

	var result []*models.Invoice
	for _, item := range items {
		invMap, ok := item.(map[string]interface{})
		if ok {
			result = append(result, p.mapInvoice(invMap))
		}
	}

	return result, nil
}

func (p *RazorpayProvider) CancelInvoice(ctx context.Context, invoiceID string) (*models.Invoice, error) {
	inv, err := p.client.Invoice.Cancel(invoiceID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay cancel invoice failed: %w", err)
	}

	return p.mapInvoice(inv), nil
}

func (p *RazorpayProvider) mapInvoice(inv map[string]interface{}) *models.Invoice {
	invoiceID := convert.StringFromMap(inv, "id")
	status := p.mapInvoiceStatus(convert.StringFromMap(inv, "status"))
	amount := convert.Int64FromMap(inv, "amount")
	currency := convert.StringFromMap(inv, "currency")
	description := convert.StringFromMap(inv, "description")
	shortURL := convert.StringFromMap(inv, "short_url")

	result := &models.Invoice{
		ProviderID:   invoiceID,
		ProviderName: "razorpay",
		Amount:       amount,
		Currency:     currency,
		Status:       status,
		Description:  description,
		InvoiceURL:   shortURL,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if customer, ok := inv["customer"].(map[string]interface{}); ok {
		result.CustomerID = convert.StringFromMap(customer, "id")
		result.CustomerEmail = convert.StringFromMap(customer, "email")
	}

	result.DueDate = convert.UnixToTimePtr(convert.Int64FromMap(inv, "expire_by"))
	result.PaidAt = convert.UnixToTimePtr(convert.Int64FromMap(inv, "paid_at"))

	return result
}

var razorpayInvoiceStatusMap = map[string]models.InvoiceStatus{
	"draft":          models.InvoiceStatusDraft,
	"issued":         models.InvoiceStatusPending,
	"partially_paid": models.InvoiceStatusPending,
	"paid":           models.InvoiceStatusPaid,
	"cancelled":      models.InvoiceStatusCanceled,
	"expired":        models.InvoiceStatusExpired,
}

func (p *RazorpayProvider) mapInvoiceStatus(status string) models.InvoiceStatus {
	if s, ok := razorpayInvoiceStatusMap[status]; ok {
		return s
	}
	return models.InvoiceStatusPending
}

func (p *RazorpayProvider) CreatePayout(ctx context.Context, req *models.CreatePayoutRequest) (*models.Payout, error) {
	payoutData := map[string]interface{}{
		"account_number":  req.SourceAccount,
		"fund_account_id": req.DestinationAccount,
		"amount":          req.Amount,
		"currency":        req.Currency,
		"mode":            req.DestinationChannel,
		"purpose":         "payout",
	}

	if req.ReferenceID != "" {
		payoutData["reference_id"] = req.ReferenceID
	}

	if req.Description != "" {
		payoutData["narration"] = req.Description
	}

	if req.Metadata != nil {
		payoutData["notes"] = req.Metadata
	}

	payout, err := p.client.Post("/v1/payouts", payoutData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay create payout failed: %w", err)
	}

	return p.mapPayout(payout), nil
}

func (p *RazorpayProvider) GetPayout(ctx context.Context, payoutID string) (*models.Payout, error) {
	payout, err := p.client.Payout.Fetch(payoutID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get payout failed: %w", err)
	}

	return p.mapPayout(payout), nil
}

func (p *RazorpayProvider) ListPayouts(ctx context.Context, req *models.ListPayoutsRequest) ([]*models.Payout, error) {
	options := map[string]interface{}{}

	if req.Limit > 0 {
		options["count"] = req.Limit
	}

	payouts, err := p.client.Payout.All(options, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay list payouts failed: %w", err)
	}

	items, ok := payouts["items"].([]interface{})
	if !ok {
		return []*models.Payout{}, nil
	}

	var result []*models.Payout
	for _, item := range items {
		payoutMap, ok := item.(map[string]interface{})
		if ok {
			result = append(result, p.mapPayout(payoutMap))
		}
	}

	return result, nil
}

func (p *RazorpayProvider) CancelPayout(ctx context.Context, payoutID string) (*models.Payout, error) {
	payout, err := p.client.Post("/v1/payouts/"+payoutID+"/cancel", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay cancel payout failed: %w", err)
	}

	return p.mapPayout(payout), nil
}

func (p *RazorpayProvider) GetPayoutChannels(ctx context.Context, currency string) ([]*models.PayoutChannel, error) {
	return []*models.PayoutChannel{
		{Code: "NEFT", Name: "NEFT Transfer", Category: "bank", Currency: "INR"},
		{Code: "RTGS", Name: "RTGS Transfer", Category: "bank", Currency: "INR"},
		{Code: "IMPS", Name: "IMPS Transfer", Category: "bank", Currency: "INR"},
		{Code: "UPI", Name: "UPI Transfer", Category: "upi", Currency: "INR"},
	}, nil
}

func (p *RazorpayProvider) mapPayout(po map[string]interface{}) *models.Payout {
	payoutID := convert.StringFromMap(po, "id")
	referenceID := convert.StringFromMap(po, "reference_id")
	amount := convert.Int64FromMap(po, "amount")
	currency := convert.StringFromMap(po, "currency")
	status := p.mapPayoutStatus(convert.StringFromMap(po, "status"))
	narration := convert.StringFromMap(po, "narration")
	mode := convert.StringFromMap(po, "mode")
	fundAccountID := convert.StringFromMap(po, "fund_account_id")

	createdAt := convert.UnixToTime(convert.Int64FromMap(po, "created_at"))
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	return &models.Payout{
		ProviderID:         payoutID,
		ProviderName:       "razorpay",
		ReferenceID:        referenceID,
		Amount:             amount,
		Currency:           currency,
		Status:             status,
		Description:        narration,
		DestinationType:    models.DestinationBankAccount,
		DestinationChannel: mode,
		DestinationAccount: fundAccountID,
		FailureReason:      convert.StringFromMap(po, "failure_reason"),
		CreatedAt:          createdAt,
		UpdatedAt:          createdAt,
	}
}

var razorpayPayoutStatusMap = map[string]models.PayoutStatus{
	"processed":  models.PayoutStatusSucceeded,
	"processing": models.PayoutStatusProcessing,
	"queued":     models.PayoutStatusPending,
	"pending":    models.PayoutStatusPending,
	"cancelled":  models.PayoutStatusCanceled,
	"failed":     models.PayoutStatusFailed,
	"rejected":   models.PayoutStatusFailed,
	"reversed":   models.PayoutStatusFailed,
}

func (p *RazorpayProvider) mapPayoutStatus(status string) models.PayoutStatus {
	if s, ok := razorpayPayoutStatusMap[status]; ok {
		return s
	}
	return models.PayoutStatusPending
}

func (p *RazorpayProvider) CreateSubscription(ctx context.Context, req *models.CreateSubscriptionRequest) (*models.Subscription, error) {
	subData := map[string]interface{}{
		"plan_id":         req.PlanID,
		"total_count":     12,
		"quantity":        req.Quantity,
		"customer_notify": 1,
	}

	if req.CustomerID != "" {
		subData["customer_id"] = req.CustomerID
	}

	if req.TrialDays != nil && *req.TrialDays > 0 {
		trialEnd := time.Now().AddDate(0, 0, *req.TrialDays)
		subData["start_at"] = trialEnd.Unix()
	}

	if req.Metadata != nil {
		subData["notes"] = req.Metadata
	}

	sub, err := p.client.Subscription.Create(subData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay subscription creation failed: %w", err)
	}

	return p.mapSubscription(sub), nil
}

func (p *RazorpayProvider) UpdateSubscription(ctx context.Context, subscriptionID string, req *models.UpdateSubscriptionRequest) (*models.Subscription, error) {
	updateData := map[string]interface{}{}

	if req.PlanID != nil && *req.PlanID != "" {
		updateData["plan_id"] = *req.PlanID
	}

	if req.Quantity != nil && *req.Quantity > 0 {
		updateData["quantity"] = *req.Quantity
	}

	if len(updateData) == 0 {
		return p.GetSubscription(ctx, subscriptionID)
	}

	sub, err := p.client.Subscription.Update(subscriptionID, updateData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay subscription update failed: %w", err)
	}

	return p.mapSubscription(sub), nil
}

func (p *RazorpayProvider) CancelSubscription(ctx context.Context, subscriptionID string, req *models.CancelSubscriptionRequest) (*models.Subscription, error) {
	cancelData := map[string]interface{}{
		"cancel_at_cycle_end": req.CancelAtPeriodEnd,
	}

	sub, err := p.client.Subscription.Cancel(subscriptionID, cancelData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay subscription cancellation failed: %w", err)
	}

	return p.mapSubscription(sub), nil
}

func (p *RazorpayProvider) GetSubscription(ctx context.Context, subscriptionID string) (*models.Subscription, error) {
	sub, err := p.client.Subscription.Fetch(subscriptionID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get subscription failed: %w", err)
	}

	return p.mapSubscription(sub), nil
}

func (p *RazorpayProvider) ListSubscriptions(ctx context.Context, customerID string) ([]*models.Subscription, error) {
	options := map[string]interface{}{}

	subs, err := p.client.Subscription.All(options, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay list subscriptions failed: %w", err)
	}

	items, ok := subs["items"].([]interface{})
	if !ok {
		return []*models.Subscription{}, nil
	}

	var result []*models.Subscription
	for _, item := range items {
		subMap, ok := item.(map[string]interface{})
		if ok {
			subscription := p.mapSubscription(subMap)
			if customerID == "" || subscription.CustomerID == customerID {
				result = append(result, subscription)
			}
		}
	}

	return result, nil
}

func (p *RazorpayProvider) mapSubscription(sub map[string]interface{}) *models.Subscription {
	quantity := int(convert.Int64FromMap(sub, "quantity"))
	if quantity == 0 {
		quantity = 1
	}

	return &models.Subscription{
		ID:                 convert.StringFromMap(sub, "id"),
		CustomerID:         convert.StringFromMap(sub, "customer_id"),
		PlanID:             convert.StringFromMap(sub, "plan_id"),
		Status:             p.mapSubscriptionStatus(convert.StringFromMap(sub, "status")),
		Quantity:           quantity,
		ProviderName:       "razorpay",
		CurrentPeriodStart: convert.UnixToTime(convert.Int64FromMap(sub, "start_at")),
		CurrentPeriodEnd:   convert.UnixToTime(convert.Int64FromMap(sub, "end_at")),
		CanceledAt:         convert.UnixToTimePtr(convert.Int64FromMap(sub, "cancelled_at")),
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}
}

var razorpaySubscriptionStatusMap = map[string]models.SubscriptionStatus{
	"created":       models.SubscriptionStatus("pending"),
	"authenticated": models.SubscriptionStatus("active"),
	"active":        models.SubscriptionStatus("active"),
	"pending":       models.SubscriptionStatus("pending"),
	"halted":        models.SubscriptionStatus("paused"),
	"cancelled":     models.SubscriptionStatus("canceled"),
	"completed":     models.SubscriptionStatus("canceled"),
	"expired":       models.SubscriptionStatus("canceled"),
}

func (p *RazorpayProvider) mapSubscriptionStatus(status string) models.SubscriptionStatus {
	if s, ok := razorpaySubscriptionStatusMap[status]; ok {
		return s
	}
	return models.SubscriptionStatus("pending")
}

var billingPeriodToRazorpay = map[models.BillingPeriod]string{
	models.BillingPeriodDaily:   "daily",
	models.BillingPeriodWeekly:  "weekly",
	models.BillingPeriodMonthly: "monthly",
	models.BillingPeriodYearly:  "yearly",
}

var razorpayToBillingPeriod = map[string]models.BillingPeriod{
	"daily":   models.BillingPeriodDaily,
	"weekly":  models.BillingPeriodWeekly,
	"monthly": models.BillingPeriodMonthly,
	"yearly":  models.BillingPeriodYearly,
}

func (p *RazorpayProvider) CreatePlan(ctx context.Context, planReq *models.Plan) (*models.Plan, error) {
	period := billingPeriodToRazorpay[planReq.BillingPeriod]
	if period == "" {
		period = "monthly"
	}

	planData := map[string]interface{}{
		"period":   period,
		"interval": 1,
		"item": map[string]interface{}{
			"name":     planReq.Name,
			"amount":   planReq.Amount,
			"currency": planReq.Currency,
		},
	}

	if planReq.Metadata != nil {
		planData["notes"] = planReq.Metadata
	}

	plan, err := p.client.Plan.Create(planData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay plan creation failed: %w", err)
	}

	return p.mapPlan(plan, planReq), nil
}

func (p *RazorpayProvider) UpdatePlan(ctx context.Context, planID string, planReq *models.Plan) (*models.Plan, error) {
	return nil, ErrNotSupported
}

func (p *RazorpayProvider) DeletePlan(ctx context.Context, planID string) error {
	return ErrNotSupported
}

func (p *RazorpayProvider) GetPlan(ctx context.Context, planID string) (*models.Plan, error) {
	plan, err := p.client.Plan.Fetch(planID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get plan failed: %w", err)
	}

	return p.mapPlan(plan, nil), nil
}

func (p *RazorpayProvider) ListPlans(ctx context.Context) ([]*models.Plan, error) {
	options := map[string]interface{}{}

	plans, err := p.client.Plan.All(options, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay list plans failed: %w", err)
	}

	items, ok := plans["items"].([]interface{})
	if !ok {
		return []*models.Plan{}, nil
	}

	var result []*models.Plan
	for _, item := range items {
		planMap, ok := item.(map[string]interface{})
		if ok {
			result = append(result, p.mapPlan(planMap, nil))
		}
	}

	return result, nil
}

func (p *RazorpayProvider) mapPlan(plan map[string]interface{}, originalReq *models.Plan) *models.Plan {
	planID := convert.StringFromMap(plan, "id")
	period := convert.StringFromMap(plan, "period")

	billingPeriod := razorpayToBillingPeriod[period]
	if billingPeriod == "" {
		billingPeriod = models.BillingPeriodMonthly
	}

	result := &models.Plan{
		ID:            planID,
		BillingPeriod: billingPeriod,
		PricingType:   models.PricingTypeFixed,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if item, ok := plan["item"].(map[string]interface{}); ok {
		result.Name = convert.StringFromMap(item, "name")
		result.Currency = convert.StringFromMap(item, "currency")
		result.Amount = convert.Int64FromMap(item, "amount")
	}

	if originalReq != nil {
		result.Description = originalReq.Description
		result.TrialDays = originalReq.TrialDays
		result.Features = originalReq.Features
		result.Metadata = originalReq.Metadata
	}

	return result
}

func (p *RazorpayProvider) CreateDispute(ctx context.Context, req *models.CreateDisputeRequest) (*models.Dispute, error) {
	return nil, fmt.Errorf("razorpay: disputes are created by customers through their banks, cannot create manually")
}

func (p *RazorpayProvider) UpdateDispute(ctx context.Context, disputeID string, req *models.UpdateDisputeRequest) (*models.Dispute, error) {
	dispute, err := p.client.Dispute.Fetch(disputeID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get dispute failed: %w", err)
	}

	return p.mapDispute(dispute), nil
}

func (p *RazorpayProvider) AcceptDispute(ctx context.Context, disputeID string) (*models.Dispute, error) {
	dispute, err := p.client.Dispute.Accept(disputeID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay accept dispute failed: %w", err)
	}

	return p.mapDispute(dispute), nil
}

func (p *RazorpayProvider) ContestDispute(ctx context.Context, disputeID string, evidence map[string]interface{}) (*models.Dispute, error) {
	contestData := map[string]interface{}{}
	if evidence != nil {
		contestData = evidence
	}

	dispute, err := p.client.Dispute.Contest(disputeID, contestData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay contest dispute failed: %w", err)
	}

	return p.mapDispute(dispute), nil
}

func (p *RazorpayProvider) SubmitDisputeEvidence(ctx context.Context, disputeID string, req *models.SubmitEvidenceRequest) (*models.Evidence, error) {
	documents := []map[string]interface{}{}
	for _, file := range req.Files {
		documents = append(documents, map[string]interface{}{
			"type": req.Type,
			"url":  file,
		})
	}

	contestData := map[string]interface{}{
		"documents": documents,
	}

	_, err := p.client.Dispute.Contest(disputeID, contestData, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay submit evidence failed: %w", err)
	}

	return &models.Evidence{
		ID:          fmt.Sprintf("evid_%s", disputeID),
		DisputeID:   disputeID,
		Type:        req.Type,
		Description: req.Description,
		Files:       req.Files,
		Metadata:    req.Metadata,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}, nil
}

func (p *RazorpayProvider) GetDispute(ctx context.Context, disputeID string) (*models.Dispute, error) {
	dispute, err := p.client.Dispute.Fetch(disputeID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get dispute failed: %w", err)
	}

	return p.mapDispute(dispute), nil
}

func (p *RazorpayProvider) ListDisputes(ctx context.Context, customerID string) ([]*models.Dispute, error) {
	options := map[string]interface{}{}

	disputes, err := p.client.Dispute.All(options, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay list disputes failed: %w", err)
	}

	items, ok := disputes["items"].([]interface{})
	if !ok {
		return []*models.Dispute{}, nil
	}

	var result []*models.Dispute
	for _, item := range items {
		disputeMap, ok := item.(map[string]interface{})
		if ok {
			result = append(result, p.mapDispute(disputeMap))
		}
	}

	return result, nil
}

func (p *RazorpayProvider) GetDisputeStats(ctx context.Context) (*models.DisputeStats, error) {
	disputes, err := p.client.Dispute.All(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get dispute stats failed: %w", err)
	}

	stats := &models.DisputeStats{}

	items, ok := disputes["items"].([]interface{})
	if !ok {
		return stats, nil
	}

	for _, item := range items {
		disputeMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		stats.Total++
		status := convert.StringFromMap(disputeMap, "status")

		switch status {
		case "open", "under_review":
			stats.Open++
		case "won":
			stats.Won++
		case "lost":
			stats.Lost++
		case "closed":
			stats.Canceled++
		}
	}

	return stats, nil
}

func (p *RazorpayProvider) mapDispute(d map[string]interface{}) *models.Dispute {
	createdAt := convert.UnixToTime(convert.Int64FromMap(d, "created_at"))
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	return &models.Dispute{
		ID:            convert.StringFromMap(d, "id"),
		TransactionID: convert.StringFromMap(d, "payment_id"),
		Amount:        convert.Int64FromMap(d, "amount"),
		Currency:      "INR",
		Reason:        convert.StringFromMap(d, "reason_code"),
		Status:        p.mapDisputeStatus(convert.StringFromMap(d, "status")),
		Evidence:      make(map[string]interface{}),
		DueBy:         convert.UnixToTime(convert.Int64FromMap(d, "respond_by")),
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
}

var razorpayDisputeStatusMap = map[string]models.DisputeStatus{
	"open":         models.DisputeStatusOpen,
	"under_review": models.DisputeStatusOpen,
	"won":          models.DisputeStatusWon,
	"lost":         models.DisputeStatusLost,
	"closed":       models.DisputeStatusCanceled,
	"accepted":     models.DisputeStatusCanceled,
}

func (p *RazorpayProvider) mapDisputeStatus(status string) models.DisputeStatus {
	if s, ok := razorpayDisputeStatusMap[status]; ok {
		return s
	}
	return models.DisputeStatusOpen
}

func (p *RazorpayProvider) CreateCustomer(ctx context.Context, req *models.CreateCustomerRequest) (string, error) {
	customerData := map[string]interface{}{
		"name":  req.Name,
		"email": req.Email,
	}

	if req.Phone != "" {
		customerData["contact"] = req.Phone
	}

	if req.Metadata != nil {
		customerData["notes"] = req.Metadata
	}

	customer, err := p.client.Customer.Create(customerData, nil)
	if err != nil {
		return "", fmt.Errorf("razorpay customer creation failed: %w", err)
	}

	return convert.StringFromMap(customer, "id"), nil
}

func (p *RazorpayProvider) UpdateCustomer(ctx context.Context, customerID string, req *models.UpdateCustomerRequest) error {
	updateData := map[string]interface{}{}

	if req.Name != "" {
		updateData["name"] = req.Name
	}
	if req.Email != "" {
		updateData["email"] = req.Email
	}
	if req.Phone != "" {
		updateData["contact"] = req.Phone
	}
	if req.Metadata != nil {
		updateData["notes"] = req.Metadata
	}

	_, err := p.client.Customer.Edit(customerID, updateData, nil)
	return err
}

func (p *RazorpayProvider) GetCustomer(ctx context.Context, customerID string) (*models.Customer, error) {
	customer, err := p.client.Customer.Fetch(customerID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("razorpay get customer failed: %w", err)
	}

	return &models.Customer{
		ExternalID: convert.StringFromMap(customer, "id"),
		Email:      convert.StringFromMap(customer, "email"),
		Name:       convert.StringFromMap(customer, "name"),
		Phone:      convert.StringFromMap(customer, "contact"),
		CreatedAt:  time.Now(),
	}, nil
}

func (p *RazorpayProvider) DeleteCustomer(ctx context.Context, customerID string) error {
	return ErrNotSupported
}

func (p *RazorpayProvider) CreatePaymentMethod(ctx context.Context, req *models.CreatePaymentMethodRequest) (*models.PaymentMethod, error) {
	return nil, ErrNotSupported
}

func (p *RazorpayProvider) GetPaymentMethod(ctx context.Context, paymentMethodID string) (*models.PaymentMethod, error) {
	return nil, ErrNotSupported
}

func (p *RazorpayProvider) ListPaymentMethods(ctx context.Context, customerID string, pmType *models.PaymentMethodType) ([]*models.PaymentMethod, error) {
	return []*models.PaymentMethod{}, nil
}

func (p *RazorpayProvider) AttachPaymentMethod(ctx context.Context, paymentMethodID, customerID string) error {
	return ErrNotSupported
}

func (p *RazorpayProvider) DetachPaymentMethod(ctx context.Context, paymentMethodID string) error {
	return ErrNotSupported
}

func (p *RazorpayProvider) ExpirePaymentMethod(ctx context.Context, paymentMethodID string) (*models.PaymentMethod, error) {
	return nil, ErrNotSupported
}

func (p *RazorpayProvider) ValidateWebhookSignature(payload []byte, signature, timestamp string) error {
	return crypto.ValidateHMACSHA256(payload, signature, p.webhookSecret)
}

func (p *RazorpayProvider) IsAvailable(ctx context.Context) bool {
	if p.keyID == "" || p.keySecret == "" {
		return false
	}

	_, err := p.client.Order.All(map[string]interface{}{"count": 1}, nil)
	return err == nil
}
