package providers

import (
	"context"
	"fmt"
	"time"

	"github.com/malwarebo/conductor/internal/convert"
	"github.com/malwarebo/conductor/models"
	"github.com/stripe/stripe-go/v86"
	stripeBalance "github.com/stripe/stripe-go/v86/balance"
	"github.com/stripe/stripe-go/v86/customer"
	"github.com/stripe/stripe-go/v86/dispute"
	stripeInvoice "github.com/stripe/stripe-go/v86/invoice"
	"github.com/stripe/stripe-go/v86/paymentintent"
	"github.com/stripe/stripe-go/v86/paymentmethod"
	"github.com/stripe/stripe-go/v86/payout"
	"github.com/stripe/stripe-go/v86/plan"
	"github.com/stripe/stripe-go/v86/refund"
	"github.com/stripe/stripe-go/v86/subscription"
	"github.com/stripe/stripe-go/v86/transfer"
	"github.com/stripe/stripe-go/v86/webhook"
)

type StripeProvider struct {
	apiKey        string
	webhookSecret string
}

func CreateStripeProvider(apiKey string) *StripeProvider {
	stripe.Key = apiKey
	return &StripeProvider{
		apiKey: apiKey,
	}
}

func CreateStripeProviderWithWebhook(apiKey, webhookSecret string) *StripeProvider {
	stripe.Key = apiKey
	return &StripeProvider{
		apiKey:        apiKey,
		webhookSecret: webhookSecret,
	}
}

func (p *StripeProvider) Name() string {
	return "stripe"
}

func (p *StripeProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		SupportsInvoices:        true,
		SupportsPayouts:         true,
		SupportsPaymentSessions: true,
		Supports3DS:             true,
		SupportsManualCapture:   true,
		SupportsBalance:         true,
		SupportedCurrencies:     []string{"USD", "EUR", "GBP", "CAD", "AUD", "JPY", "SGD", "HKD"},
		SupportedPaymentMethods: []models.PaymentMethodType{models.PMTypeCard, models.PMTypeBankAccount},
	}
}

func (p *StripeProvider) Charge(ctx context.Context, req *models.ChargeRequest) (*models.ChargeResponse, error) {
	params := &stripe.PaymentIntentParams{
		Amount:      stripe.Int64(req.Amount),
		Currency:    stripe.String(req.Currency),
		Description: stripe.String(req.Description),
		Customer:    stripe.String(req.CustomerID),
	}

	if req.PaymentMethod != "" {
		params.PaymentMethod = stripe.String(req.PaymentMethod)
		params.Confirm = stripe.Bool(true)
	}

	if req.CaptureMethod == models.CaptureMethodManual || (req.Capture != nil && !*req.Capture) {
		params.CaptureMethod = stripe.String("manual")
	}

	if req.ReturnURL != "" {
		params.ReturnURL = stripe.String(req.ReturnURL)
	}

	params.AutomaticPaymentMethods = &stripe.PaymentIntentAutomaticPaymentMethodsParams{
		Enabled:        stripe.Bool(true),
		AllowRedirects: stripe.String("always"),
	}

	if req.Metadata != nil {
		params.Metadata = ConvertMetadataToStringMap(req.Metadata)
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe payment intent creation failed: %w", err)
	}

	metadata := ConvertStringMapToMetadata(pi.Metadata)

	status := p.mapPaymentIntentStatus(pi.Status)
	captureMethod := models.CaptureMethodAutomatic
	if pi.CaptureMethod == stripe.PaymentIntentCaptureMethodManual {
		captureMethod = models.CaptureMethodManual
	}

	paymentMethodID := ""
	if pi.PaymentMethod != nil {
		paymentMethodID = pi.PaymentMethod.ID
	}

	response := &models.ChargeResponse{
		ID:               pi.ID,
		CustomerID:       req.CustomerID,
		Amount:           pi.Amount,
		Currency:         string(pi.Currency),
		Status:           status,
		PaymentMethod:    paymentMethodID,
		Description:      req.Description,
		ProviderName:     "stripe",
		ProviderChargeID: pi.ID,
		CaptureMethod:    captureMethod,
		CapturedAmount:   pi.AmountReceived,
		ClientSecret:     pi.ClientSecret,
		Metadata:         metadata,
		CreatedAt:        convert.UnixToTime(pi.Created),
	}

	if pi.NextAction != nil {
		response.RequiresAction = true
		response.NextActionType = string(pi.NextAction.Type)
		if pi.NextAction.RedirectToURL != nil {
			response.NextActionURL = pi.NextAction.RedirectToURL.URL
		}
		if pi.NextAction.UseStripeSDK != nil {
			response.NextActionType = "use_stripe_sdk"
		}
	}

	return response, nil
}

func (p *StripeProvider) mapPaymentIntentStatus(status stripe.PaymentIntentStatus) models.PaymentStatus {
	switch status {
	case stripe.PaymentIntentStatusSucceeded:
		return models.PaymentStatusSuccess
	case stripe.PaymentIntentStatusRequiresAction:
		return models.PaymentStatusRequiresAction
	case stripe.PaymentIntentStatusRequiresCapture:
		return models.PaymentStatusRequiresCapture
	case stripe.PaymentIntentStatusProcessing:
		return models.PaymentStatusProcessing
	case stripe.PaymentIntentStatusCanceled:
		return models.PaymentStatusCanceled
	case stripe.PaymentIntentStatusRequiresPaymentMethod:
		return models.PaymentStatusFailed
	default:
		return models.PaymentStatusPending
	}
}

func (p *StripeProvider) CapturePayment(ctx context.Context, paymentID string, amount int64) error {
	params := &stripe.PaymentIntentCaptureParams{}
	if amount > 0 {
		params.AmountToCapture = stripe.Int64(amount)
	}

	_, err := paymentintent.Capture(paymentID, params)
	if err != nil {
		return fmt.Errorf("stripe capture failed: %w", err)
	}
	return nil
}

func (p *StripeProvider) VoidPayment(ctx context.Context, paymentID string) error {
	params := &stripe.PaymentIntentCancelParams{
		CancellationReason: stripe.String("requested_by_customer"),
	}

	_, err := paymentintent.Cancel(paymentID, params)
	if err != nil {
		return fmt.Errorf("stripe void/cancel failed: %w", err)
	}
	return nil
}

func (p *StripeProvider) Create3DSSession(ctx context.Context, paymentID string, returnURL string) (*ThreeDSecureSession, error) {
	pi, err := paymentintent.Get(paymentID, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe get payment intent failed: %w", err)
	}

	session := &ThreeDSecureSession{
		PaymentID:    pi.ID,
		ClientSecret: pi.ClientSecret,
		Status:       string(pi.Status),
	}

	if pi.NextAction != nil && pi.NextAction.RedirectToURL != nil {
		session.RedirectURL = pi.NextAction.RedirectToURL.URL
	}

	return session, nil
}

func (p *StripeProvider) Confirm3DSPayment(ctx context.Context, paymentID string) (*models.ChargeResponse, error) {
	pi, err := paymentintent.Get(paymentID, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe get payment intent failed: %w", err)
	}

	status := p.mapPaymentIntentStatus(pi.Status)
	captureMethod := models.CaptureMethodAutomatic
	if pi.CaptureMethod == stripe.PaymentIntentCaptureMethodManual {
		captureMethod = models.CaptureMethodManual
	}

	paymentMethodID := ""
	if pi.PaymentMethod != nil {
		paymentMethodID = pi.PaymentMethod.ID
	}

	return &models.ChargeResponse{
		ID:               pi.ID,
		CustomerID:       pi.Customer.ID,
		Amount:           pi.Amount,
		Currency:         string(pi.Currency),
		Status:           status,
		PaymentMethod:    paymentMethodID,
		ProviderName:     "stripe",
		ProviderChargeID: pi.ID,
		CaptureMethod:    captureMethod,
		CapturedAmount:   pi.AmountReceived,
		ClientSecret:     pi.ClientSecret,
		CreatedAt:        time.Unix(pi.Created, 0),
	}, nil
}

func (p *StripeProvider) CreatePaymentSession(ctx context.Context, req *models.CreatePaymentSessionRequest) (*models.PaymentSession, error) {
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(req.Amount),
		Currency: stripe.String(req.Currency),
	}

	if req.CustomerID != "" {
		params.Customer = stripe.String(req.CustomerID)
	}

	if req.Description != "" {
		params.Description = stripe.String(req.Description)
	}

	if req.PaymentMethodID != "" {
		params.PaymentMethod = stripe.String(req.PaymentMethodID)
	}

	if req.CaptureMethod == models.CaptureMethodManual {
		params.CaptureMethod = stripe.String("manual")
	}

	if req.SetupFutureUsage != "" {
		params.SetupFutureUsage = stripe.String(req.SetupFutureUsage)
	}

	if req.ReturnURL != "" {
		params.ReturnURL = stripe.String(req.ReturnURL)
	}

	params.AutomaticPaymentMethods = &stripe.PaymentIntentAutomaticPaymentMethodsParams{
		Enabled: stripe.Bool(true),
	}

	if req.Metadata != nil {
		params.Metadata = ConvertMetadataToStringMap(req.Metadata)
	}

	pi, err := paymentintent.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe create payment session failed: %w", err)
	}

	return p.mapPaymentSession(pi), nil
}

func (p *StripeProvider) GetPaymentSession(ctx context.Context, sessionID string) (*models.PaymentSession, error) {
	pi, err := paymentintent.Get(sessionID, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe get payment session failed: %w", err)
	}

	return p.mapPaymentSession(pi), nil
}

func (p *StripeProvider) UpdatePaymentSession(ctx context.Context, sessionID string, req *models.UpdatePaymentSessionRequest) (*models.PaymentSession, error) {
	params := &stripe.PaymentIntentParams{}

	if req.Amount != nil {
		params.Amount = stripe.Int64(*req.Amount)
	}

	if req.Currency != nil {
		params.Currency = stripe.String(*req.Currency)
	}

	if req.Description != nil {
		params.Description = stripe.String(*req.Description)
	}

	if req.PaymentMethodID != nil {
		params.PaymentMethod = stripe.String(*req.PaymentMethodID)
	}

	if req.Metadata != nil {
		params.Metadata = ConvertMetadataToStringMap(req.Metadata)
	}

	pi, err := paymentintent.Update(sessionID, params)
	if err != nil {
		return nil, fmt.Errorf("stripe update payment session failed: %w", err)
	}

	return p.mapPaymentSession(pi), nil
}

func (p *StripeProvider) ConfirmPaymentSession(ctx context.Context, sessionID string, req *models.ConfirmPaymentSessionRequest) (*models.PaymentSession, error) {
	params := &stripe.PaymentIntentConfirmParams{}

	if req.PaymentMethodID != "" {
		params.PaymentMethod = stripe.String(req.PaymentMethodID)
	}

	if req.ReturnURL != "" {
		params.ReturnURL = stripe.String(req.ReturnURL)
	}

	pi, err := paymentintent.Confirm(sessionID, params)
	if err != nil {
		return nil, fmt.Errorf("stripe confirm payment session failed: %w", err)
	}

	return p.mapPaymentSession(pi), nil
}

func (p *StripeProvider) CapturePaymentSession(ctx context.Context, sessionID string, amount *int64) (*models.PaymentSession, error) {
	params := &stripe.PaymentIntentCaptureParams{}
	if amount != nil {
		params.AmountToCapture = stripe.Int64(*amount)
	}

	pi, err := paymentintent.Capture(sessionID, params)
	if err != nil {
		return nil, fmt.Errorf("stripe capture payment session failed: %w", err)
	}

	return p.mapPaymentSession(pi), nil
}

func (p *StripeProvider) CancelPaymentSession(ctx context.Context, sessionID string) (*models.PaymentSession, error) {
	params := &stripe.PaymentIntentCancelParams{
		CancellationReason: stripe.String("requested_by_customer"),
	}

	pi, err := paymentintent.Cancel(sessionID, params)
	if err != nil {
		return nil, fmt.Errorf("stripe cancel payment session failed: %w", err)
	}

	return p.mapPaymentSession(pi), nil
}

func (p *StripeProvider) ListPaymentSessions(ctx context.Context, req *models.ListPaymentSessionsRequest) ([]*models.PaymentSession, error) {
	params := &stripe.PaymentIntentListParams{}

	if req.CustomerID != "" {
		params.Customer = stripe.String(req.CustomerID)
	}

	if req.Limit > 0 {
		params.Limit = stripe.Int64(int64(req.Limit))
	}

	i := paymentintent.List(params)
	var sessions []*models.PaymentSession

	for i.Next() {
		sessions = append(sessions, p.mapPaymentSession(i.PaymentIntent()))
	}

	return sessions, nil
}

func (p *StripeProvider) mapPaymentSession(pi *stripe.PaymentIntent) *models.PaymentSession {
	session := &models.PaymentSession{
		ProviderID:     pi.ID,
		ProviderName:   "stripe",
		Amount:         pi.Amount,
		Currency:       string(pi.Currency),
		Status:         p.mapPaymentIntentStatus(pi.Status),
		ClientSecret:   pi.ClientSecret,
		CaptureMethod:  models.CaptureMethod(pi.CaptureMethod),
		CapturedAmount: pi.AmountReceived,
		CreatedAt:      time.Unix(pi.Created, 0),
		UpdatedAt:      time.Unix(pi.Created, 0),
	}

	if pi.Customer != nil {
		session.CustomerID = pi.Customer.ID
	}

	if pi.PaymentMethod != nil {
		session.PaymentMethodID = pi.PaymentMethod.ID
	}

	if pi.Description != "" {
		session.Description = pi.Description
	}

	if pi.NextAction != nil {
		session.RequiresAction = true
		session.NextActionType = string(pi.NextAction.Type)
		if pi.NextAction.RedirectToURL != nil {
			session.NextActionURL = pi.NextAction.RedirectToURL.URL
			session.NextAction = &models.NextAction{
				Type:        string(pi.NextAction.Type),
				RedirectURL: pi.NextAction.RedirectToURL.URL,
			}
		}
	}

	if pi.Metadata != nil {
		session.Metadata = ConvertStringMapToMetadata(pi.Metadata)
	}

	return session
}

func (p *StripeProvider) CreateInvoice(ctx context.Context, req *models.CreateInvoiceRequest) (*models.Invoice, error) {
	params := &stripe.InvoiceParams{
		AutoAdvance: stripe.Bool(true),
	}

	if req.CustomerID != "" {
		params.Customer = stripe.String(req.CustomerID)
	}

	if req.Description != "" {
		params.Description = stripe.String(req.Description)
	}

	if req.DueDate != nil {
		params.DueDate = stripe.Int64(req.DueDate.Unix())
	}

	if req.Metadata != nil {
		params.Metadata = ConvertMetadataToStringMap(req.Metadata)
	}

	inv, err := stripeInvoice.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe create invoice failed: %w", err)
	}

	return p.mapInvoice(inv), nil
}

func (p *StripeProvider) GetInvoice(ctx context.Context, invoiceID string) (*models.Invoice, error) {
	inv, err := stripeInvoice.Get(invoiceID, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe get invoice failed: %w", err)
	}

	return p.mapInvoice(inv), nil
}

func (p *StripeProvider) ListInvoices(ctx context.Context, req *models.ListInvoicesRequest) ([]*models.Invoice, error) {
	params := &stripe.InvoiceListParams{}

	if req.CustomerID != "" {
		params.Customer = stripe.String(req.CustomerID)
	}

	if req.Limit > 0 {
		params.Limit = stripe.Int64(int64(req.Limit))
	}

	i := stripeInvoice.List(params)
	var invoices []*models.Invoice

	for i.Next() {
		invoices = append(invoices, p.mapInvoice(i.Invoice()))
	}

	return invoices, nil
}

func (p *StripeProvider) CancelInvoice(ctx context.Context, invoiceID string) (*models.Invoice, error) {
	inv, err := stripeInvoice.VoidInvoice(invoiceID, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe cancel invoice failed: %w", err)
	}

	return p.mapInvoice(inv), nil
}

func (p *StripeProvider) mapInvoice(inv *stripe.Invoice) *models.Invoice {
	result := &models.Invoice{
		ProviderID:   inv.ID,
		ProviderName: "stripe",
		Amount:       inv.AmountDue,
		Currency:     string(inv.Currency),
		Status:       p.mapInvoiceStatus(inv.Status),
		Description:  inv.Description,
		InvoiceURL:   inv.HostedInvoiceURL,
		CreatedAt:    time.Unix(inv.Created, 0),
		UpdatedAt:    time.Unix(inv.Created, 0),
	}

	if inv.Customer != nil {
		result.CustomerID = inv.Customer.ID
		result.CustomerEmail = inv.Customer.Email
	}

	if inv.DueDate > 0 {
		dueDate := time.Unix(inv.DueDate, 0)
		result.DueDate = &dueDate
	}

	if inv.StatusTransitions != nil && inv.StatusTransitions.PaidAt > 0 {
		paidAt := time.Unix(inv.StatusTransitions.PaidAt, 0)
		result.PaidAt = &paidAt
	}

	if inv.Metadata != nil {
		result.Metadata = ConvertStringMapToMetadata(inv.Metadata)
	}

	return result
}

func (p *StripeProvider) mapInvoiceStatus(status stripe.InvoiceStatus) models.InvoiceStatus {
	switch status {
	case stripe.InvoiceStatusDraft:
		return models.InvoiceStatusDraft
	case stripe.InvoiceStatusOpen:
		return models.InvoiceStatusPending
	case stripe.InvoiceStatusPaid:
		return models.InvoiceStatusPaid
	case stripe.InvoiceStatusVoid:
		return models.InvoiceStatusVoid
	case stripe.InvoiceStatusUncollectible:
		return models.InvoiceStatusCanceled
	default:
		return models.InvoiceStatusPending
	}
}

func (p *StripeProvider) CreatePayout(ctx context.Context, req *models.CreatePayoutRequest) (*models.Payout, error) {
	params := &stripe.TransferParams{
		Amount:   stripe.Int64(req.Amount),
		Currency: stripe.String(req.Currency),
	}

	if req.DestinationAccount != "" {
		params.Destination = stripe.String(req.DestinationAccount)
	}

	if req.Description != "" {
		params.Description = stripe.String(req.Description)
	}

	if req.Metadata != nil {
		params.Metadata = ConvertMetadataToStringMap(req.Metadata)
	}

	tr, err := transfer.New(params)
	if err != nil {
		return nil, fmt.Errorf("stripe create payout failed: %w", err)
	}

	return &models.Payout{
		ProviderID:         tr.ID,
		ProviderName:       "stripe",
		ReferenceID:        req.ReferenceID,
		Amount:             tr.Amount,
		Currency:           string(tr.Currency),
		Status:             models.PayoutStatusSucceeded,
		Description:        req.Description,
		DestinationType:    req.DestinationType,
		DestinationAccount: req.DestinationAccount,
		CreatedAt:          time.Unix(tr.Created, 0),
		UpdatedAt:          time.Unix(tr.Created, 0),
	}, nil
}

func (p *StripeProvider) GetPayout(ctx context.Context, payoutID string) (*models.Payout, error) {
	po, err := payout.Get(payoutID, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe get payout failed: %w", err)
	}

	return p.mapPayout(po), nil
}

func (p *StripeProvider) ListPayouts(ctx context.Context, req *models.ListPayoutsRequest) ([]*models.Payout, error) {
	params := &stripe.PayoutListParams{}

	if req.Limit > 0 {
		params.Limit = stripe.Int64(int64(req.Limit))
	}

	i := payout.List(params)
	var payouts []*models.Payout

	for i.Next() {
		payouts = append(payouts, p.mapPayout(i.Payout()))
	}

	return payouts, nil
}

func (p *StripeProvider) CancelPayout(ctx context.Context, payoutID string) (*models.Payout, error) {
	po, err := payout.Cancel(payoutID, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe cancel payout failed: %w", err)
	}

	return p.mapPayout(po), nil
}

func (p *StripeProvider) GetPayoutChannels(ctx context.Context, currency string) ([]*models.PayoutChannel, error) {
	return []*models.PayoutChannel{
		{Code: "bank_account", Name: "Bank Account", Category: "bank", Currency: currency},
		{Code: "card", Name: "Debit Card", Category: "card", Currency: currency},
	}, nil
}

func (p *StripeProvider) mapPayout(po *stripe.Payout) *models.Payout {
	status := models.PayoutStatusPending
	switch po.Status {
	case stripe.PayoutStatusPaid:
		status = models.PayoutStatusSucceeded
	case stripe.PayoutStatusFailed:
		status = models.PayoutStatusFailed
	case stripe.PayoutStatusCanceled:
		status = models.PayoutStatusCanceled
	case stripe.PayoutStatusInTransit:
		status = models.PayoutStatusProcessing
	}

	result := &models.Payout{
		ProviderID:      po.ID,
		ProviderName:    "stripe",
		Amount:          po.Amount,
		Currency:        string(po.Currency),
		Status:          status,
		Description:     po.Description,
		DestinationType: models.DestinationBankAccount,
		CreatedAt:       time.Unix(po.Created, 0),
		UpdatedAt:       time.Unix(po.Created, 0),
	}

	if po.ArrivalDate > 0 {
		arrival := time.Unix(po.ArrivalDate, 0)
		result.EstimatedArrival = &arrival
	}

	if po.FailureMessage != "" {
		result.FailureReason = po.FailureMessage
	}

	return result
}

func (p *StripeProvider) GetBalance(ctx context.Context, currency string) (*models.Balance, error) {
	bal, err := stripeBalance.Get(nil)
	if err != nil {
		return nil, fmt.Errorf("stripe get balance failed: %w", err)
	}

	result := &models.Balance{
		ProviderName: "stripe",
		Currency:     currency,
	}

	for _, a := range bal.Available {
		if currency == "" || string(a.Currency) == currency {
			result.Available = a.Amount
			result.Currency = string(a.Currency)
			break
		}
	}

	for _, p := range bal.Pending {
		if currency == "" || string(p.Currency) == currency {
			result.Pending = p.Amount
			break
		}
	}

	return result, nil
}

func (p *StripeProvider) Refund(ctx context.Context, req *models.RefundRequest) (*models.RefundResponse, error) {
	params := &stripe.RefundParams{
		PaymentIntent: stripe.String(req.PaymentID),
		Amount:        stripe.Int64(req.Amount),
		Reason:        stripe.String(req.Reason),
	}

	if req.Metadata != nil {
		params.Metadata = ConvertMetadataToStringMap(req.Metadata)
	}

	ref, err := refund.New(params)
	if err != nil {
		return nil, err
	}

	metadata := ConvertStringMapToMetadata(ref.Metadata)

	return &models.RefundResponse{
		ID:               ref.ID,
		PaymentID:        req.PaymentID,
		Amount:           ref.Amount,
		Currency:         string(ref.Currency),
		Status:           string(ref.Status),
		Reason:           req.Reason,
		ProviderName:     "stripe",
		ProviderRefundID: ref.ID,
		Metadata:         metadata,
		CreatedAt:        time.Unix(ref.Created, 0),
	}, nil
}

func (p *StripeProvider) ValidateWebhookSignature(payload []byte, signature, timestamp string) error {
	if p.webhookSecret == "" {
		return fmt.Errorf("webhook secret not configured")
	}

	_, err := webhook.ConstructEvent(payload, signature, p.webhookSecret)
	if err != nil {
		return fmt.Errorf("webhook signature verification failed: %w", err)
	}

	return nil
}

func (p *StripeProvider) CreateSubscription(ctx context.Context, req *models.CreateSubscriptionRequest) (*models.Subscription, error) {
	params := &stripe.SubscriptionParams{
		Customer: stripe.String(req.CustomerID),
	}

	if req.PlanID != "" {
		itemsParams := &stripe.SubscriptionItemsParams{
			Plan: stripe.String(req.PlanID),
		}

		if req.Quantity > 0 {
			itemsParams.Quantity = stripe.Int64(int64(req.Quantity))
		}

		params.Items = []*stripe.SubscriptionItemsParams{itemsParams}
	}

	if req.TrialDays != nil && *req.TrialDays > 0 {
		params.TrialPeriodDays = stripe.Int64(int64(*req.TrialDays))
	}

	if req.Metadata != nil {
		params.Metadata = ConvertInterfaceMetadataToStringMap(req.Metadata)
	}

	sub, err := subscription.New(params)
	if err != nil {
		return nil, err
	}

	result := &models.Subscription{
		ID:                 sub.ID,
		CustomerID:         req.CustomerID,
		PlanID:             req.PlanID,
		Status:             models.SubscriptionStatus(sub.Status),
		CurrentPeriodStart: time.Unix(sub.Created, 0),
		CurrentPeriodEnd:   time.Unix(sub.CanceledAt, 0),
		Quantity:           req.Quantity,
		ProviderName:       "stripe",
		CreatedAt:          time.Unix(sub.Created, 0),
		UpdatedAt:          time.Unix(sub.Created, 0),
	}

	if sub.TrialStart > 0 {
		trialStart := time.Unix(sub.TrialStart, 0)
		result.TrialStart = &trialStart
	}

	if sub.TrialEnd > 0 {
		trialEnd := time.Unix(sub.TrialEnd, 0)
		result.TrialEnd = &trialEnd
	}

	return result, nil
}

func (p *StripeProvider) UpdateSubscription(ctx context.Context, subscriptionID string, req *models.UpdateSubscriptionRequest) (*models.Subscription, error) {
	params := &stripe.SubscriptionParams{}

	if req.PlanID != nil && *req.PlanID != "" {
		itemsParams := &stripe.SubscriptionItemsParams{
			Plan: stripe.String(*req.PlanID),
		}

		if req.Quantity != nil && *req.Quantity > 0 {
			itemsParams.Quantity = stripe.Int64(int64(*req.Quantity))
		}

		params.Items = []*stripe.SubscriptionItemsParams{itemsParams}
	}

	if req.PaymentMethodID != nil && *req.PaymentMethodID != "" {
		params.DefaultPaymentMethod = stripe.String(*req.PaymentMethodID)
	}

	if req.Metadata != nil {
		params.Metadata = ConvertInterfaceMetadataToStringMap(req.Metadata)
	}

	sub, err := subscription.Update(subscriptionID, params)
	if err != nil {
		return nil, err
	}

	result := &models.Subscription{
		ID:                 sub.ID,
		CustomerID:         sub.Customer.ID,
		Status:             models.SubscriptionStatus(sub.Status),
		CurrentPeriodStart: time.Unix(sub.Created, 0),
		CurrentPeriodEnd:   time.Unix(sub.CanceledAt, 0),
		ProviderName:       "stripe",
		UpdatedAt:          time.Now(),
	}

	if req.Quantity != nil {
		result.Quantity = *req.Quantity
	}

	if sub.TrialStart > 0 {
		trialStart := time.Unix(sub.TrialStart, 0)
		result.TrialStart = &trialStart
	}

	if sub.TrialEnd > 0 {
		trialEnd := time.Unix(sub.TrialEnd, 0)
		result.TrialEnd = &trialEnd
	}

	return result, nil
}

func (p *StripeProvider) CancelSubscription(ctx context.Context, subscriptionID string, req *models.CancelSubscriptionRequest) (*models.Subscription, error) {
	params := &stripe.SubscriptionParams{}

	if req.CancelAtPeriodEnd {
		params.CancelAtPeriodEnd = stripe.Bool(true)
	}

	if req.Reason != "" {
		if params.Metadata == nil {
			params.Metadata = make(map[string]string)
		}
		params.Metadata["cancellation_reason"] = req.Reason
	}

	var sub *stripe.Subscription
	var err error

	if req.CancelAtPeriodEnd {
		sub, err = subscription.Update(subscriptionID, params)
	} else {
		cancelParams := &stripe.SubscriptionCancelParams{
			Prorate: stripe.Bool(true),
		}
		sub, err = subscription.Cancel(subscriptionID, cancelParams)
	}

	if err != nil {
		return nil, err
	}

	canceledAt := time.Now()
	if sub.CancelAt > 0 {
		canceledAt = time.Unix(sub.CancelAt, 0)
	}

	result := &models.Subscription{
		ID:                 sub.ID,
		CustomerID:         sub.Customer.ID,
		Status:             models.SubscriptionStatus(sub.Status),
		CurrentPeriodStart: time.Unix(sub.Created, 0),
		CurrentPeriodEnd:   time.Unix(sub.CanceledAt, 0),
		CanceledAt:         &canceledAt,
		ProviderName:       "stripe",
		UpdatedAt:          time.Now(),
	}

	return result, nil
}

func (p *StripeProvider) GetSubscription(ctx context.Context, subscriptionID string) (*models.Subscription, error) {
	params := &stripe.SubscriptionParams{}
	sub, err := subscription.Get(subscriptionID, params)
	if err != nil {
		return nil, err
	}

	result := &models.Subscription{
		ID:                 sub.ID,
		CustomerID:         sub.Customer.ID,
		Status:             models.SubscriptionStatus(sub.Status),
		CurrentPeriodStart: time.Unix(sub.Created, 0),
		CurrentPeriodEnd:   time.Unix(sub.CanceledAt, 0),
		CanceledAt:         nil,
		ProviderName:       "stripe",
	}

	if sub.CancelAt > 0 {
		canceledAt := time.Unix(sub.CancelAt, 0)
		result.CanceledAt = &canceledAt
	}

	return result, nil
}

func (p *StripeProvider) ListSubscriptions(ctx context.Context, customerID string) ([]*models.Subscription, error) {
	params := &stripe.SubscriptionListParams{
		Customer: stripe.String(customerID),
	}

	i := subscription.List(params)
	var subscriptions []*models.Subscription

	for i.Next() {
		sub := i.Subscription()
		result := &models.Subscription{
			ID:                 sub.ID,
			CustomerID:         sub.Customer.ID,
			Status:             models.SubscriptionStatus(sub.Status),
			CurrentPeriodStart: time.Unix(sub.Created, 0),
			CurrentPeriodEnd:   time.Unix(sub.CanceledAt, 0),
			CanceledAt:         nil,
			ProviderName:       "stripe",
		}

		if sub.CancelAt > 0 {
			canceledAt := time.Unix(sub.CancelAt, 0)
			result.CanceledAt = &canceledAt
		}

		subscriptions = append(subscriptions, result)
	}

	return subscriptions, nil
}

func (p *StripeProvider) CreatePlan(ctx context.Context, planReq *models.Plan) (*models.Plan, error) {
	params := &stripe.PlanParams{
		Amount:   stripe.Int64(planReq.Amount),
		Currency: stripe.String(planReq.Currency),
		Interval: stripe.String(string(planReq.BillingPeriod)),
		Product: &stripe.PlanProductParams{
			Name: stripe.String(planReq.Name),
		},
	}

	if planReq.TrialDays > 0 {
		params.TrialPeriodDays = stripe.Int64(int64(planReq.TrialDays))
	}

	if planReq.Metadata != nil {
		params.Metadata = ConvertInterfaceMetadataToStringMap(planReq.Metadata)
	}

	stripePlan, err := plan.New(params)
	if err != nil {
		return nil, err
	}

	result := &models.Plan{
		ID:            stripePlan.ID,
		Name:          planReq.Name,
		Description:   planReq.Description,
		Amount:        stripePlan.Amount,
		Currency:      string(stripePlan.Currency),
		BillingPeriod: models.BillingPeriod(stripePlan.Interval),
		PricingType:   models.PricingTypeFixed,
		TrialDays:     planReq.TrialDays,
		Features:      planReq.Features,
		Metadata:      planReq.Metadata,
		CreatedAt:     time.Unix(stripePlan.Created, 0),
		UpdatedAt:     time.Unix(stripePlan.Created, 0),
	}

	return result, nil
}

func (p *StripeProvider) UpdatePlan(ctx context.Context, planID string, planReq *models.Plan) (*models.Plan, error) {
	params := &stripe.PlanParams{}

	if planReq.Name != "" {
		params.Product = &stripe.PlanProductParams{
			Name: stripe.String(planReq.Name),
		}
	}

	if planReq.Metadata != nil {
		params.Metadata = ConvertInterfaceMetadataToStringMap(planReq.Metadata)
	}

	stripePlan, err := plan.Update(planID, params)
	if err != nil {
		return nil, err
	}

	result := &models.Plan{
		ID:            stripePlan.ID,
		Name:          planReq.Name,
		Description:   planReq.Description,
		Amount:        stripePlan.Amount,
		Currency:      string(stripePlan.Currency),
		BillingPeriod: models.BillingPeriod(stripePlan.Interval),
		PricingType:   models.PricingTypeFixed,
		TrialDays:     planReq.TrialDays,
		Features:      planReq.Features,
		Metadata:      planReq.Metadata,
		UpdatedAt:     time.Now(),
	}

	return result, nil
}

func (p *StripeProvider) DeletePlan(ctx context.Context, planID string) error {
	_, err := plan.Del(planID, nil)
	return err
}

func (p *StripeProvider) GetPlan(ctx context.Context, planID string) (*models.Plan, error) {
	stripePlan, err := plan.Get(planID, nil)
	if err != nil {
		return nil, err
	}

	result := &models.Plan{
		ID:            stripePlan.ID,
		Name:          stripePlan.Product.Name,
		Description:   stripePlan.Product.Description,
		Amount:        stripePlan.Amount,
		Currency:      string(stripePlan.Currency),
		BillingPeriod: models.BillingPeriod(stripePlan.Interval),
		PricingType:   models.PricingTypeFixed,
		TrialDays:     int(stripePlan.TrialPeriodDays),
		Features:      []string{},
		Metadata:      nil,
		CreatedAt:     time.Unix(stripePlan.Created, 0),
		UpdatedAt:     time.Unix(stripePlan.Created, 0),
	}

	if stripePlan.Metadata != nil {
		result.Metadata = ConvertStringMapToMetadata(stripePlan.Metadata)
	}

	return result, nil
}

func (p *StripeProvider) ListPlans(ctx context.Context) ([]*models.Plan, error) {
	params := &stripe.PlanListParams{}
	i := plan.List(params)
	var plans []*models.Plan

	for i.Next() {
		stripePlan := i.Plan()
		result := &models.Plan{
			ID:            stripePlan.ID,
			Name:          stripePlan.Product.Name,
			Description:   stripePlan.Product.Description,
			Amount:        stripePlan.Amount,
			Currency:      string(stripePlan.Currency),
			BillingPeriod: models.BillingPeriod(stripePlan.Interval),
			PricingType:   models.PricingTypeFixed,
			TrialDays:     int(stripePlan.TrialPeriodDays),
			Features:      []string{},
			Metadata:      nil,
			CreatedAt:     time.Unix(stripePlan.Created, 0),
			UpdatedAt:     time.Unix(stripePlan.Created, 0),
		}

		if stripePlan.Metadata != nil {
			result.Metadata = ConvertStringMapToMetadata(stripePlan.Metadata)
		}

		plans = append(plans, result)
	}

	return plans, nil
}

func (p *StripeProvider) CreateDispute(ctx context.Context, req *models.CreateDisputeRequest) (*models.Dispute, error) {
	return nil, fmt.Errorf("stripe: disputes are created automatically from charges, cannot create manually")
}

func (p *StripeProvider) UpdateDispute(ctx context.Context, disputeID string, req *models.UpdateDisputeRequest) (*models.Dispute, error) {
	params := &stripe.DisputeParams{}

	if req.Metadata != nil {
		params.Metadata = ConvertMetadataToStringMap(req.Metadata)
	}

	stripeDispute, err := dispute.Update(disputeID, params)
	if err != nil {
		return nil, err
	}

	result := &models.Dispute{
		ID:        stripeDispute.ID,
		Status:    models.DisputeStatus(stripeDispute.Status),
		Metadata:  req.Metadata,
		UpdatedAt: time.Now(),
	}

	return result, nil
}

func (p *StripeProvider) AcceptDispute(ctx context.Context, disputeID string) (*models.Dispute, error) {
	params := &stripe.DisputeParams{}
	stripeDispute, err := dispute.Close(disputeID, params)
	if err != nil {
		return nil, fmt.Errorf("stripe close dispute failed: %w", err)
	}

	return &models.Dispute{
		ID:            stripeDispute.ID,
		TransactionID: stripeDispute.Charge.ID,
		Amount:        stripeDispute.Amount,
		Currency:      string(stripeDispute.Currency),
		Reason:        string(stripeDispute.Reason),
		Status:        models.DisputeStatus(stripeDispute.Status),
		CreatedAt:     time.Unix(stripeDispute.Created, 0),
		UpdatedAt:     time.Now(),
	}, nil
}

func (p *StripeProvider) ContestDispute(ctx context.Context, disputeID string, evidence map[string]interface{}) (*models.Dispute, error) {
	params := &stripe.DisputeParams{
		Submit:   stripe.Bool(true),
		Evidence: &stripe.DisputeEvidenceParams{},
	}

	if uncategorizedText, ok := evidence["uncategorized_text"].(string); ok {
		params.Evidence.UncategorizedText = stripe.String(uncategorizedText)
	}
	if productDescription, ok := evidence["product_description"].(string); ok {
		params.Evidence.ProductDescription = stripe.String(productDescription)
	}
	if customerName, ok := evidence["customer_name"].(string); ok {
		params.Evidence.CustomerName = stripe.String(customerName)
	}
	if customerEmail, ok := evidence["customer_email_address"].(string); ok {
		params.Evidence.CustomerEmailAddress = stripe.String(customerEmail)
	}

	stripeDispute, err := dispute.Update(disputeID, params)
	if err != nil {
		return nil, fmt.Errorf("stripe contest dispute failed: %w", err)
	}

	return &models.Dispute{
		ID:            stripeDispute.ID,
		TransactionID: stripeDispute.Charge.ID,
		Amount:        stripeDispute.Amount,
		Currency:      string(stripeDispute.Currency),
		Reason:        string(stripeDispute.Reason),
		Status:        models.DisputeStatus(stripeDispute.Status),
		CreatedAt:     time.Unix(stripeDispute.Created, 0),
		UpdatedAt:     time.Now(),
	}, nil
}

func (p *StripeProvider) SubmitDisputeEvidence(ctx context.Context, disputeID string, req *models.SubmitEvidenceRequest) (*models.Evidence, error) {
	params := &stripe.DisputeEvidenceParams{}

	if req.Type != "" {
		switch req.Type {
		case "customer_email_address":
			params.CustomerEmailAddress = stripe.String(req.Description)
		case "customer_purchase_ip":
			params.CustomerPurchaseIP = stripe.String(req.Description)
		case "customer_signature":
			params.CustomerSignature = stripe.String(req.Description)
		case "billing_address":
			params.BillingAddress = stripe.String(req.Description)
		case "receipt":
			params.Receipt = stripe.String(req.Description)
		case "service_date":
			params.ServiceDate = stripe.String(req.Description)
		case "product_description":
			params.ProductDescription = stripe.String(req.Description)
		case "customer_name":
			params.CustomerName = stripe.String(req.Description)
		case "customer_communication":
			params.CustomerCommunication = stripe.String(req.Description)
		}
	}

	_, err := dispute.Update(disputeID, &stripe.DisputeParams{
		Evidence: params,
	})
	if err != nil {
		return nil, err
	}

	result := &models.Evidence{
		ID:          fmt.Sprintf("evid_%s", disputeID),
		DisputeID:   disputeID,
		Type:        req.Type,
		Description: req.Description,
		Files:       req.Files,
		Metadata:    req.Metadata,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	return result, nil
}

func (p *StripeProvider) GetDispute(ctx context.Context, disputeID string) (*models.Dispute, error) {
	stripeDispute, err := dispute.Get(disputeID, nil)
	if err != nil {
		return nil, err
	}

	result := &models.Dispute{
		ID:            stripeDispute.ID,
		TransactionID: stripeDispute.Charge.ID,
		Amount:        stripeDispute.Amount,
		Currency:      string(stripeDispute.Currency),
		Reason:        string(stripeDispute.Reason),
		Status:        models.DisputeStatus(stripeDispute.Status),
		Evidence:      make(map[string]interface{}),
		DueBy:         time.Unix(stripeDispute.EvidenceDetails.DueBy, 0),
		CreatedAt:     time.Unix(stripeDispute.Created, 0),
		UpdatedAt:     time.Unix(stripeDispute.Created, 0),
	}

	if stripeDispute.Metadata != nil {
		result.Metadata = ConvertStringMapToMetadata(stripeDispute.Metadata)
	}

	return result, nil
}

func (p *StripeProvider) ListDisputes(ctx context.Context, customerID string) ([]*models.Dispute, error) {
	params := &stripe.DisputeListParams{}
	i := dispute.List(params)
	var disputes []*models.Dispute

	for i.Next() {
		stripeDispute := i.Dispute()
		result := &models.Dispute{
			ID:            stripeDispute.ID,
			TransactionID: stripeDispute.Charge.ID,
			Amount:        stripeDispute.Amount,
			Currency:      string(stripeDispute.Currency),
			Reason:        string(stripeDispute.Reason),
			Status:        models.DisputeStatus(stripeDispute.Status),
			Evidence:      make(map[string]interface{}),
			DueBy:         time.Unix(stripeDispute.EvidenceDetails.DueBy, 0),
			CreatedAt:     time.Unix(stripeDispute.Created, 0),
			UpdatedAt:     time.Unix(stripeDispute.Created, 0),
		}

		if stripeDispute.Metadata != nil {
			result.Metadata = ConvertStringMapToMetadata(stripeDispute.Metadata)
		}

		disputes = append(disputes, result)
	}

	return disputes, nil
}

func (p *StripeProvider) GetDisputeStats(ctx context.Context) (*models.DisputeStats, error) {
	params := &stripe.DisputeListParams{}
	i := dispute.List(params)

	stats := &models.DisputeStats{}

	for i.Next() {
		stripeDispute := i.Dispute()
		stats.Total++

		switch stripeDispute.Status {
		case "needs_response":
			stats.Open++
		case "won":
			stats.Won++
		case "lost":
			stats.Lost++
		case "warning_needs_response", "warning_under_review", "under_review":
			stats.Open++
		case "charge_refunded":
			stats.Canceled++
		case "won_charge_refunded":
			stats.Won++
		case "lost_charge_refunded":
			stats.Lost++
		}
	}

	return stats, nil
}

func (p *StripeProvider) CreateCustomer(ctx context.Context, req *models.CreateCustomerRequest) (string, error) {
	params := &stripe.CustomerParams{
		Email: stripe.String(req.Email),
	}

	if req.Name != "" {
		params.Name = stripe.String(req.Name)
	}

	if req.Phone != "" {
		params.Phone = stripe.String(req.Phone)
	}

	if req.Metadata != nil {
		params.Metadata = ConvertMetadataToStringMap(req.Metadata)
	}

	cust, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("stripe customer creation failed: %w", err)
	}

	return cust.ID, nil
}

func (p *StripeProvider) UpdateCustomer(ctx context.Context, customerID string, req *models.UpdateCustomerRequest) error {
	params := &stripe.CustomerParams{}

	if req.Email != "" {
		params.Email = stripe.String(req.Email)
	}

	if req.Name != "" {
		params.Name = stripe.String(req.Name)
	}

	if req.Phone != "" {
		params.Phone = stripe.String(req.Phone)
	}

	if req.Metadata != nil {
		params.Metadata = ConvertMetadataToStringMap(req.Metadata)
	}

	_, err := customer.Update(customerID, params)
	return err
}

func (p *StripeProvider) GetCustomer(ctx context.Context, customerID string) (*models.Customer, error) {
	cust, err := customer.Get(customerID, nil)
	if err != nil {
		return nil, err
	}

	return &models.Customer{
		ExternalID: cust.ID,
		Email:      cust.Email,
		Name:       cust.Name,
		Phone:      cust.Phone,
		Metadata:   ConvertStringMapToMetadata(cust.Metadata),
		CreatedAt:  time.Unix(cust.Created, 0),
	}, nil
}

func (p *StripeProvider) DeleteCustomer(ctx context.Context, customerID string) error {
	_, err := customer.Del(customerID, nil)
	return err
}

func (p *StripeProvider) CreatePaymentMethod(ctx context.Context, req *models.CreatePaymentMethodRequest) (*models.PaymentMethod, error) {
	pm, err := paymentmethod.Get(req.CardToken, nil)
	if err != nil {
		return nil, fmt.Errorf("stripe payment method get failed: %w", err)
	}

	result := &models.PaymentMethod{
		CustomerID:              req.CustomerID,
		ProviderName:            "stripe",
		ProviderPaymentMethodID: pm.ID,
		Type:                    req.Type,
		Reusable:                req.Reusable,
		Status:                  "active",
		IsDefault:               req.IsDefault,
		Metadata:                req.Metadata,
		CreatedAt:               time.Unix(pm.Created, 0),
	}

	if pm.Card != nil {
		result.Last4 = pm.Card.Last4
		result.Brand = string(pm.Card.Brand)
		result.ExpMonth = int(pm.Card.ExpMonth)
		result.ExpYear = int(pm.Card.ExpYear)
	}

	return result, nil
}

func (p *StripeProvider) GetPaymentMethod(ctx context.Context, paymentMethodID string) (*models.PaymentMethod, error) {
	pm, err := paymentmethod.Get(paymentMethodID, nil)
	if err != nil {
		return nil, err
	}

	customerID := ""
	if pm.Customer != nil {
		customerID = pm.Customer.ID
	}

	result := &models.PaymentMethod{
		CustomerID:              customerID,
		ProviderName:            "stripe",
		ProviderPaymentMethodID: pm.ID,
		Type:                    models.PaymentMethodType(pm.Type),
		Status:                  "active",
		CreatedAt:               time.Unix(pm.Created, 0),
	}

	if pm.Card != nil {
		result.Last4 = pm.Card.Last4
		result.Brand = string(pm.Card.Brand)
		result.ExpMonth = int(pm.Card.ExpMonth)
		result.ExpYear = int(pm.Card.ExpYear)
	}

	return result, nil
}

func (p *StripeProvider) ListPaymentMethods(ctx context.Context, customerID string, pmType *models.PaymentMethodType) ([]*models.PaymentMethod, error) {
	params := &stripe.PaymentMethodListParams{
		Customer: stripe.String(customerID),
		Type:     stripe.String("card"),
	}

	if pmType != nil {
		params.Type = stripe.String(string(*pmType))
	}

	i := paymentmethod.List(params)
	var paymentMethods []*models.PaymentMethod

	for i.Next() {
		pm := i.PaymentMethod()
		result := &models.PaymentMethod{
			CustomerID:              customerID,
			ProviderName:            "stripe",
			ProviderPaymentMethodID: pm.ID,
			Type:                    models.PaymentMethodType(pm.Type),
			Status:                  "active",
			CreatedAt:               time.Unix(pm.Created, 0),
		}

		if pm.Card != nil {
			result.Last4 = pm.Card.Last4
			result.Brand = string(pm.Card.Brand)
			result.ExpMonth = int(pm.Card.ExpMonth)
			result.ExpYear = int(pm.Card.ExpYear)
		}

		paymentMethods = append(paymentMethods, result)
	}

	return paymentMethods, nil
}

func (p *StripeProvider) AttachPaymentMethod(ctx context.Context, paymentMethodID, customerID string) error {
	params := &stripe.PaymentMethodAttachParams{
		Customer: stripe.String(customerID),
	}
	_, err := paymentmethod.Attach(paymentMethodID, params)
	return err
}

func (p *StripeProvider) DetachPaymentMethod(ctx context.Context, paymentMethodID string) error {
	_, err := paymentmethod.Detach(paymentMethodID, nil)
	return err
}

func (p *StripeProvider) ExpirePaymentMethod(ctx context.Context, paymentMethodID string) (*models.PaymentMethod, error) {
	_, err := paymentmethod.Detach(paymentMethodID, nil)
	if err != nil {
		return nil, err
	}

	return &models.PaymentMethod{
		ProviderPaymentMethodID: paymentMethodID,
		ProviderName:            "stripe",
		Status:                  "expired",
	}, nil
}

func (p *StripeProvider) IsAvailable(ctx context.Context) bool {
	if p.apiKey == "" {
		return false
	}

	var account stripe.Account
	err := stripe.GetBackend(stripe.APIBackend).Call(
		"GET",
		"/v1/account",
		p.apiKey,
		nil,
		&account,
	)

	return err == nil
}
