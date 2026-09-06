package stores

import (
	"context"

	"github.com/malwarebo/conductor/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PaymentRepository struct {
	BaseStore
}

func CreatePaymentRepository(db *gorm.DB) *PaymentRepository {
	return &PaymentRepository{BaseStore: BaseStore{db: db}}
}

func (r *PaymentRepository) Create(ctx context.Context, payment *models.Payment) error {
	return r.GetDB(ctx).Create(payment).Error
}

func (r *PaymentRepository) Update(ctx context.Context, payment *models.Payment) error {
	return r.GetDB(ctx).Save(payment).Error
}

func (r *PaymentRepository) GetByID(ctx context.Context, id string) (*models.Payment, error) {
	var payment models.Payment
	if err := r.GetDB(ctx).Preload("Refunds").First(&payment, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &payment, nil
}

func (r *PaymentRepository) GetByIDForUpdate(ctx context.Context, id string) (*models.Payment, error) {
	var payment models.Payment
	if err := r.GetDB(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&payment, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &payment, nil
}

func (r *PaymentRepository) ListByCustomer(ctx context.Context, customerID string) ([]*models.Payment, error) {
	var payments []*models.Payment
	if err := r.GetDB(ctx).Preload("Refunds").Where("customer_id = ?", customerID).Find(&payments).Error; err != nil {
		return nil, err
	}
	return payments, nil
}

func (r *PaymentRepository) CreateRefund(ctx context.Context, refund *models.Refund) error {
	return r.GetDB(ctx).Create(refund).Error
}

func (r *PaymentRepository) GetRefundByID(ctx context.Context, id string) (*models.Refund, error) {
	var refund models.Refund
	if err := r.GetDB(ctx).First(&refund, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &refund, nil
}

func (r *PaymentRepository) ListRefundsByPayment(ctx context.Context, paymentID string) ([]*models.Refund, error) {
	var refunds []*models.Refund
	if err := r.GetDB(ctx).Where("payment_id = ?", paymentID).Find(&refunds).Error; err != nil {
		return nil, err
	}
	return refunds, nil
}

func (r *PaymentRepository) GetByProviderChargeID(ctx context.Context, providerChargeID string) (*models.Payment, error) {
	var payment models.Payment
	if err := r.GetDB(ctx).Where("provider_charge_id = ?", providerChargeID).First(&payment).Error; err != nil {
		return nil, err
	}
	return &payment, nil
}

func (r *PaymentRepository) GetByIdempotencyKey(ctx context.Context, idempotencyKey, tenantID string) (*models.Payment, error) {
	var payment models.Payment
	if err := r.GetDB(ctx).Where("idempotency_key = ? AND (tenant_id = ? OR (tenant_id IS NULL AND ? = ''))", idempotencyKey, tenantID, tenantID).First(&payment).Error; err != nil {
		return nil, err
	}
	return &payment, nil
}

func (r *PaymentRepository) ListByTenant(ctx context.Context, tenantID string, limit, offset int) ([]*models.Payment, error) {
	var payments []*models.Payment
	query := r.GetDB(ctx).Where("tenant_id = ?", tenantID).Order("created_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&payments).Error; err != nil {
		return nil, err
	}
	return payments, nil
}

func (r *PaymentRepository) UpdateStatus(ctx context.Context, id string, status models.PaymentStatus) error {
	return r.GetDB(ctx).Model(&models.Payment{}).Where("id = ?", id).Update("status", status).Error
}

func (r *PaymentRepository) UpdateCapture(ctx context.Context, id string, capturedAmount int64, status models.PaymentStatus) error {
	return r.GetDB(ctx).Model(&models.Payment{}).Where("id = ?", id).Updates(map[string]interface{}{
		"captured_amount": capturedAmount,
		"status":          status,
	}).Error
}
