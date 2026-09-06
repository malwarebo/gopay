package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/malwarebo/conductor/internal/ctxkeys"
	"github.com/malwarebo/conductor/models"
	"github.com/malwarebo/conductor/providers"
	"github.com/malwarebo/conductor/stores"
)

var (
	ErrNoAvailableProvider    = errors.New("no available provider")
	ErrPaymentNotFound        = errors.New("payment not found")
	ErrInvalidCaptureAmount   = errors.New("capture amount exceeds authorized amount")
	ErrPaymentNotCapturable   = errors.New("payment is not in capturable state")
	ErrPaymentAlreadyCaptured = errors.New("payment already captured")
	ErrIdempotencyConflict    = errors.New("idempotency key conflict")
)

type PaymentService struct {
	paymentRepo      *stores.PaymentRepository
	idempotencyStore *stores.IdempotencyStore
	auditStore       *stores.AuditStore
	provider         providers.PaymentProvider
	executor         *providers.ProviderExecutor
	fraudService     FraudService
}

func CreatePaymentService(paymentRepo *stores.PaymentRepository, provider providers.PaymentProvider) *PaymentService {
	return &PaymentService{
		paymentRepo: paymentRepo,
		provider:    provider,
		executor:    providers.CreateProviderExecutor(providers.DefaultProviderExecutorConfig()),
	}
}

func CreatePaymentServiceFull(
	paymentRepo *stores.PaymentRepository,
	idempotencyStore *stores.IdempotencyStore,
	auditStore *stores.AuditStore,
	provider providers.PaymentProvider,
	fraudService FraudService,
) *PaymentService {
	return &PaymentService{
		paymentRepo:      paymentRepo,
		idempotencyStore: idempotencyStore,
		auditStore:       auditStore,
		provider:         provider,
		executor:         providers.CreateProviderExecutor(providers.DefaultProviderExecutorConfig()),
		fraudService:     fraudService,
	}
}

func (s *PaymentService) CreateCharge(ctx context.Context, req *models.ChargeRequest) (*models.ChargeResponse, error) {
	if err := s.validateChargeRequest(req); err != nil {
		return nil, err
	}

	if req.IdempotencyKey != "" && s.idempotencyStore != nil {
		result, err := s.checkIdempotency(ctx, req.IdempotencyKey, "/v1/charges", req)
		if err != nil {
			return nil, err
		}
		if !result.IsNew && result.ResponseCode != 0 {
			var resp models.ChargeResponse
			_ = json.Unmarshal(result.ResponseBody, &resp)
			return &resp, nil
		}
	}

	if req.FraudCheck != nil && *req.FraudCheck && s.fraudService != nil {
		fraudResp, err := s.runFraudCheck(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("fraud check failed: %w", err)
		}
		if !fraudResp.Allow {
			return nil, fmt.Errorf("payment declined: %s", fraudResp.Reason)
		}
	}

	providerName := s.selectProvider(ctx, req.Currency)
	if providerName == "" {
		return nil, ErrNoAvailableProvider
	}

	captureMethod := req.CaptureMethod
	if captureMethod == "" {
		captureMethod = models.CaptureMethodAutomatic
	}

	var payment *models.Payment
	var chargeResp *models.ChargeResponse
	var providerErr error

	err := s.executor.Execute(ctx, providerName, func() error {
		chargeResp, providerErr = s.provider.Charge(ctx, req)
		return providerErr
	})

	if err != nil {
		_ = s.completeIdempotency(ctx, req.IdempotencyKey, 500, nil)
		return nil, fmt.Errorf("failed to create charge with provider: %w", err)
	}

	tenantID := tenantIDFromContext(ctx)
	var tenantIDPtr *string
	if tenantID != "" {
		tenantIDPtr = &tenantID
	}

	payment = &models.Payment{
		ID:               chargeResp.ID,
		TenantID:         tenantIDPtr,
		Amount:           chargeResp.Amount,
		Currency:         chargeResp.Currency,
		Status:           chargeResp.Status,
		PaymentMethod:    req.PaymentMethod,
		CustomerID:       req.CustomerID,
		Description:      req.Description,
		ProviderName:     providerName,
		ProviderChargeID: chargeResp.ProviderChargeID,
		CaptureMethod:    captureMethod,
		CapturedAmount:   chargeResp.CapturedAmount,
		RequiresAction:   chargeResp.RequiresAction,
		NextActionType:   chargeResp.NextActionType,
		NextActionURL:    chargeResp.NextActionURL,
		ClientSecret:     chargeResp.ClientSecret,
		IdempotencyKey:   req.IdempotencyKey,
		Metadata:         req.Metadata,
		CreatedAt:        time.Now(),
	}

	response := s.buildChargeResponse(payment)

	err = s.paymentRepo.WithTransaction(ctx, func(txCtx context.Context) error {
		if err := s.paymentRepo.Create(txCtx, payment); err != nil {
			return err
		}
		return s.completeIdempotency(txCtx, req.IdempotencyKey, 200, response)
	})
	if err != nil {
		return nil, err
	}

	return response, nil
}

func (s *PaymentService) Authorize(ctx context.Context, req *models.AuthorizeRequest) (*models.ChargeResponse, error) {
	chargeReq := &models.ChargeRequest{
		CustomerID:     req.CustomerID,
		Amount:         req.Amount,
		Currency:       req.Currency,
		PaymentMethod:  req.PaymentMethod,
		Description:    req.Description,
		CaptureMethod:  models.CaptureMethodManual,
		Capture:        boolPtr(false),
		ReturnURL:      req.ReturnURL,
		IdempotencyKey: req.IdempotencyKey,
		Metadata:       req.Metadata,
	}

	return s.CreateCharge(ctx, chargeReq)
}

func (s *PaymentService) Capture(ctx context.Context, req *models.CaptureRequest) (*models.CaptureResponse, error) {
	payment, err := s.paymentRepo.GetByID(ctx, req.PaymentID)
	if err != nil {
		return nil, ErrPaymentNotFound
	}

	if payment.Status != models.PaymentStatusRequiresCapture {
		if payment.CapturedAmount > 0 {
			return nil, ErrPaymentAlreadyCaptured
		}
		return nil, ErrPaymentNotCapturable
	}

	captureAmount := req.Amount
	if captureAmount == 0 {
		captureAmount = payment.Amount
	}

	if captureAmount > payment.Amount {
		return nil, ErrInvalidCaptureAmount
	}

	var captureErr error
	err = s.executor.Execute(ctx, payment.ProviderName, func() error {
		captureErr = s.captureWithProvider(ctx, payment.ProviderChargeID, captureAmount)
		return captureErr
	})

	if err != nil {
		return nil, fmt.Errorf("failed to capture payment: %w", err)
	}

	err = s.paymentRepo.WithTransaction(ctx, func(txCtx context.Context) error {
		locked, err := s.paymentRepo.GetByIDForUpdate(txCtx, req.PaymentID)
		if err != nil {
			return err
		}
		locked.CapturedAmount = captureAmount
		locked.Status = models.PaymentStatusSuccess
		return s.paymentRepo.Update(txCtx, locked)
	})
	if err != nil {
		return nil, err
	}

	payment.CapturedAmount = captureAmount
	payment.Status = models.PaymentStatusSuccess

	return &models.CaptureResponse{
		ID:           payment.ID,
		PaymentID:    payment.ID,
		Amount:       captureAmount,
		Status:       payment.Status,
		ProviderName: payment.ProviderName,
		CapturedAt:   time.Now(),
	}, nil
}

func (s *PaymentService) Void(ctx context.Context, req *models.VoidRequest) (*models.VoidResponse, error) {
	payment, err := s.paymentRepo.GetByID(ctx, req.PaymentID)
	if err != nil {
		return nil, ErrPaymentNotFound
	}

	if payment.Status != models.PaymentStatusRequiresCapture && payment.Status != models.PaymentStatusPending {
		return nil, fmt.Errorf("cannot void payment with status: %s", payment.Status)
	}

	var voidErr error
	err = s.executor.Execute(ctx, payment.ProviderName, func() error {
		voidErr = s.voidWithProvider(ctx, payment.ProviderChargeID)
		return voidErr
	})

	if err != nil {
		return nil, fmt.Errorf("failed to void payment: %w", err)
	}

	err = s.paymentRepo.WithTransaction(ctx, func(txCtx context.Context) error {
		locked, err := s.paymentRepo.GetByIDForUpdate(txCtx, req.PaymentID)
		if err != nil {
			return err
		}
		locked.Status = models.PaymentStatusCanceled
		return s.paymentRepo.Update(txCtx, locked)
	})
	if err != nil {
		return nil, err
	}

	payment.Status = models.PaymentStatusCanceled

	return &models.VoidResponse{
		ID:           payment.ID,
		PaymentID:    payment.ID,
		Status:       payment.Status,
		ProviderName: payment.ProviderName,
		VoidedAt:     time.Now(),
	}, nil
}

func (s *PaymentService) Confirm3DS(ctx context.Context, req *models.Confirm3DSRequest) (*models.ChargeResponse, error) {
	payment, err := s.paymentRepo.GetByID(ctx, req.PaymentID)
	if err != nil {
		return nil, ErrPaymentNotFound
	}

	if payment.Status != models.PaymentStatusRequiresAction {
		return nil, fmt.Errorf("payment does not require 3DS confirmation")
	}

	return s.buildChargeResponse(payment), nil
}

func (s *PaymentService) CreateRefund(ctx context.Context, req *models.RefundRequest) (*models.RefundResponse, error) {
	if err := s.validateRefundRequest(req); err != nil {
		return nil, err
	}

	payment, err := s.paymentRepo.GetByID(ctx, req.PaymentID)
	if err != nil {
		return nil, fmt.Errorf("payment not found: %v", err)
	}

	if payment.Status != models.PaymentStatusSuccess && payment.Status != models.PaymentStatusPartiallyRefunded {
		return nil, fmt.Errorf("cannot refund payment with status: %s", payment.Status)
	}

	var refundResp *models.RefundResponse
	var refundErr error

	err = s.executor.Execute(ctx, payment.ProviderName, func() error {
		refundResp, refundErr = s.provider.Refund(ctx, req)
		return refundErr
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create refund with provider: %w", err)
	}

	refund := &models.Refund{
		ID:               refundResp.ID,
		PaymentID:        req.PaymentID,
		Amount:           refundResp.Amount,
		Status:           refundResp.Status,
		Reason:           req.Reason,
		ProviderName:     refundResp.ProviderName,
		ProviderRefundID: refundResp.ProviderRefundID,
		Metadata:         req.Metadata,
		CreatedAt:        time.Now(),
	}

	err = s.paymentRepo.WithTransaction(ctx, func(txCtx context.Context) error {
		locked, err := s.paymentRepo.GetByIDForUpdate(txCtx, req.PaymentID)
		if err != nil {
			return err
		}

		if err := s.paymentRepo.CreateRefund(txCtx, refund); err != nil {
			return err
		}

		if refund.Amount >= locked.Amount {
			locked.Status = models.PaymentStatusRefunded
		} else {
			locked.Status = models.PaymentStatusPartiallyRefunded
		}

		return s.paymentRepo.Update(txCtx, locked)
	})
	if err != nil {
		return nil, err
	}

	return refundResp, nil
}

func (s *PaymentService) GetPayment(ctx context.Context, id string) (*models.Payment, error) {
	return s.paymentRepo.GetByID(ctx, id)
}

func (s *PaymentService) ListPayments(ctx context.Context, customerID string) ([]*models.Payment, error) {
	return s.paymentRepo.ListByCustomer(ctx, customerID)
}

func (s *PaymentService) GetRefund(ctx context.Context, id string) (*models.Refund, error) {
	return s.paymentRepo.GetRefundByID(ctx, id)
}

func (s *PaymentService) ListRefunds(ctx context.Context, paymentID string) ([]*models.Refund, error) {
	return s.paymentRepo.ListRefundsByPayment(ctx, paymentID)
}

func (s *PaymentService) CreatePaymentSession(ctx context.Context, req *models.CreatePaymentSessionRequest) (*models.PaymentSession, error) {
	if sessionProvider, ok := s.provider.(providers.PaymentSessionProvider); ok {
		return sessionProvider.CreatePaymentSession(ctx, req)
	}
	return nil, errors.New("provider does not support payment sessions")
}

func (s *PaymentService) GetPaymentSession(ctx context.Context, id string) (*models.PaymentSession, error) {
	if sessionProvider, ok := s.provider.(providers.PaymentSessionProvider); ok {
		return sessionProvider.GetPaymentSession(ctx, id)
	}
	return nil, errors.New("provider does not support payment sessions")
}

func (s *PaymentService) UpdatePaymentSession(ctx context.Context, id string, req *models.UpdatePaymentSessionRequest) (*models.PaymentSession, error) {
	if sessionProvider, ok := s.provider.(providers.PaymentSessionProvider); ok {
		return sessionProvider.UpdatePaymentSession(ctx, id, req)
	}
	return nil, errors.New("provider does not support payment sessions")
}

func (s *PaymentService) ConfirmPaymentSession(ctx context.Context, id string, req *models.ConfirmPaymentSessionRequest) (*models.PaymentSession, error) {
	if sessionProvider, ok := s.provider.(providers.PaymentSessionProvider); ok {
		return sessionProvider.ConfirmPaymentSession(ctx, id, req)
	}
	return nil, errors.New("provider does not support payment sessions")
}

func (s *PaymentService) CapturePaymentSession(ctx context.Context, id string, amount *int64) (*models.PaymentSession, error) {
	if sessionProvider, ok := s.provider.(providers.PaymentSessionProvider); ok {
		return sessionProvider.CapturePaymentSession(ctx, id, amount)
	}
	return nil, errors.New("provider does not support payment sessions")
}

func (s *PaymentService) CancelPaymentSession(ctx context.Context, id string) (*models.PaymentSession, error) {
	if sessionProvider, ok := s.provider.(providers.PaymentSessionProvider); ok {
		return sessionProvider.CancelPaymentSession(ctx, id)
	}
	return nil, errors.New("provider does not support payment sessions")
}

func (s *PaymentService) ListPaymentSessions(ctx context.Context, req *models.ListPaymentSessionsRequest) ([]*models.PaymentSession, error) {
	if sessionProvider, ok := s.provider.(providers.PaymentSessionProvider); ok {
		return sessionProvider.ListPaymentSessions(ctx, req)
	}
	return nil, errors.New("provider does not support payment sessions")
}

func tenantIDFromContext(ctx context.Context) string {
	tenantID, _ := ctx.Value(ctxkeys.TenantID).(string)
	return tenantID
}

func (s *PaymentService) checkIdempotency(ctx context.Context, key, path string, req interface{}) (*models.IdempotencyResult, error) {
	if s.idempotencyStore == nil {
		return &models.IdempotencyResult{IsNew: true}, nil
	}

	reqBody, _ := json.Marshal(req)

	return s.idempotencyStore.GetOrCreate(ctx, key, tenantIDFromContext(ctx), path, reqBody, 24*time.Hour)
}

func (s *PaymentService) completeIdempotency(ctx context.Context, key string, code int, response interface{}) error {
	if s.idempotencyStore == nil || key == "" {
		return nil
	}
	return s.idempotencyStore.Complete(ctx, key, tenantIDFromContext(ctx), code, response)
}

func (s *PaymentService) validateChargeRequest(req *models.ChargeRequest) error {
	if req.Amount <= 0 {
		return errors.New("amount must be positive")
	}
	if req.Currency == "" {
		return errors.New("currency is required")
	}
	if req.PaymentMethod == "" {
		return errors.New("payment method is required")
	}
	return nil
}

func (s *PaymentService) validateRefundRequest(req *models.RefundRequest) error {
	if req.PaymentID == "" {
		return errors.New("payment ID is required")
	}
	if req.Amount <= 0 {
		return errors.New("amount must be positive")
	}
	return nil
}

func (s *PaymentService) selectProvider(ctx context.Context, currency string) string {
	if s.provider.IsAvailable(ctx) {
		return s.provider.Name()
	}
	return ""
}

func (s *PaymentService) captureWithProvider(ctx context.Context, providerChargeID string, amount int64) error {
	if capturer, ok := s.provider.(providers.CaptureProvider); ok {
		return capturer.CapturePayment(ctx, providerChargeID, amount)
	}
	return errors.New("provider does not support capture")
}

func (s *PaymentService) voidWithProvider(ctx context.Context, providerChargeID string) error {
	if voider, ok := s.provider.(providers.VoidProvider); ok {
		return voider.VoidPayment(ctx, providerChargeID)
	}
	return errors.New("provider does not support void")
}

func (s *PaymentService) buildChargeResponse(payment *models.Payment) *models.ChargeResponse {
	return &models.ChargeResponse{
		ID:               payment.ID,
		CustomerID:       payment.CustomerID,
		Amount:           payment.Amount,
		Currency:         payment.Currency,
		Status:           payment.Status,
		PaymentMethod:    payment.PaymentMethod,
		Description:      payment.Description,
		ProviderName:     payment.ProviderName,
		ProviderChargeID: payment.ProviderChargeID,
		CaptureMethod:    payment.CaptureMethod,
		CapturedAmount:   payment.CapturedAmount,
		RequiresAction:   payment.RequiresAction,
		NextActionType:   payment.NextActionType,
		NextActionURL:    payment.NextActionURL,
		ClientSecret:     payment.ClientSecret,
		Metadata:         payment.Metadata,
		CreatedAt:        payment.CreatedAt,
	}
}

func boolPtr(b bool) *bool {
	return &b
}

func (s *PaymentService) runFraudCheck(ctx context.Context, req *models.ChargeRequest) (*models.FraudAnalysisResponse, error) {
	ipAddress := req.IPAddress
	if ipAddress == "" {
		if ip := ctx.Value("client_ip"); ip != nil {
			ipAddress, _ = ip.(string)
		}
	}

	fraudReq := &models.FraudAnalysisRequest{
		TransactionID:       req.IdempotencyKey,
		UserID:              req.CustomerID,
		TransactionAmount:   float64(req.Amount) / 100,
		BillingCountry:      extractMetadataString(req.Metadata, "billing_country", "US"),
		ShippingCountry:     extractMetadataString(req.Metadata, "shipping_country", "US"),
		IPAddress:           ipAddress,
		TransactionVelocity: 1,
	}

	return s.fraudService.AnalyzeTransaction(ctx, fraudReq)
}

func extractMetadataString(metadata models.JSON, key, defaultVal string) string {
	if metadata == nil {
		return defaultVal
	}
	if v, ok := metadata[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}
