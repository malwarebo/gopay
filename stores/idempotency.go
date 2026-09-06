package stores

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/malwarebo/conductor/models"
	"gorm.io/gorm"
)

type IdempotencyStore struct {
	BaseStore
}

func CreateIdempotencyStore(db *gorm.DB) *IdempotencyStore {
	return &IdempotencyStore{BaseStore: BaseStore{db: db}}
}

func (s *IdempotencyStore) scopeToKey(ctx context.Context, key, tenantID string) *gorm.DB {
	return s.GetDB(ctx).Where("key = ? AND (tenant_id = ? OR (tenant_id IS NULL AND ? = ''))", key, tenantID, tenantID)
}

func (s *IdempotencyStore) GetOrCreate(ctx context.Context, key, tenantID, requestPath string, requestBody []byte, ttl time.Duration) (*models.IdempotencyResult, error) {
	requestHash := s.hashRequest(requestBody)
	now := time.Now()

	var existing models.IdempotencyKey
	err := s.scopeToKey(ctx, key, tenantID).First(&existing).Error

	if err == nil {
		if existing.RequestHash != requestHash {
			return nil, ErrIdempotencyMismatch
		}

		if existing.CompletedAt != nil && existing.ResponseCode != nil {
			responseBytes, _ := json.Marshal(existing.ResponseBody)
			return &models.IdempotencyResult{
				IsNew:        false,
				Key:          &existing,
				ResponseCode: *existing.ResponseCode,
				ResponseBody: responseBytes,
			}, nil
		}

		if existing.LockedAt != nil && time.Since(*existing.LockedAt) < time.Minute {
			return nil, ErrIdempotencyInProgress
		}

		err = s.GetDB(ctx).Model(&existing).Update("locked_at", now).Error
		if err != nil {
			return nil, err
		}

		return &models.IdempotencyResult{
			IsNew: false,
			Key:   &existing,
		}, nil
	}

	if err != gorm.ErrRecordNotFound {
		return nil, err
	}

	var tenantIDPtr *string
	if tenantID != "" {
		tenantIDPtr = &tenantID
	}

	newKey := &models.IdempotencyKey{
		Key:         key,
		TenantID:    tenantIDPtr,
		RequestPath: requestPath,
		RequestHash: requestHash,
		LockedAt:    &now,
		ExpiresAt:   now.Add(ttl),
	}

	if err := s.GetDB(ctx).Create(newKey).Error; err != nil {
		return nil, err
	}

	return &models.IdempotencyResult{
		IsNew: true,
		Key:   newKey,
	}, nil
}

func (s *IdempotencyStore) Complete(ctx context.Context, key, tenantID string, responseCode int, responseBody interface{}) error {
	now := time.Now()
	bodyJSON, err := json.Marshal(responseBody)
	if err != nil {
		return err
	}

	return s.scopeToKey(ctx, key, tenantID).
		Model(&models.IdempotencyKey{}).
		Updates(map[string]interface{}{
			"response_code": responseCode,
			"response_body": bodyJSON,
			"completed_at":  now,
			"locked_at":     nil,
		}).Error
}

func (s *IdempotencyStore) Unlock(ctx context.Context, key, tenantID string) error {
	return s.scopeToKey(ctx, key, tenantID).
		Model(&models.IdempotencyKey{}).
		Update("locked_at", nil).Error
}

func (s *IdempotencyStore) CleanupExpired(ctx context.Context) (int64, error) {
	result := s.GetDB(ctx).
		Where("expires_at < ?", time.Now()).
		Delete(&models.IdempotencyKey{})
	return result.RowsAffected, result.Error
}

func (s *IdempotencyStore) hashRequest(body []byte) string {
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

var (
	ErrIdempotencyMismatch   = gorm.ErrInvalidData
	ErrIdempotencyInProgress = gorm.ErrInvalidTransaction
)
