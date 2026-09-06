package models

import (
	"time"
)

type PricingType string
type SubscriptionStatus string
type BillingPeriod string

const (
	PricingTypeFixed   PricingType = "fixed"
	PricingTypePerUnit PricingType = "per_unit"
	PricingTypeTiered  PricingType = "tiered"
	PricingTypeVolume  PricingType = "volume"

	SubscriptionStatusActive   SubscriptionStatus = "active"
	SubscriptionStatusCanceled SubscriptionStatus = "canceled"
	SubscriptionStatusPaused   SubscriptionStatus = "paused"
	SubscriptionStatusTrialing SubscriptionStatus = "trialing"
	SubscriptionStatusPastDue  SubscriptionStatus = "past_due"

	BillingPeriodDaily   BillingPeriod = "daily"
	BillingPeriodWeekly  BillingPeriod = "weekly"
	BillingPeriodMonthly BillingPeriod = "monthly"
	BillingPeriodYearly  BillingPeriod = "yearly"
)

type Plan struct {
	ID            string        `json:"id" gorm:"primaryKey;type:uuid;default:gen_random_uuid()"`
	Name          string        `json:"name" gorm:"not null"`
	Description   string        `json:"description"`
	Amount        int64         `json:"amount" gorm:"not null"`
	Currency      string        `json:"currency" gorm:"not null"`
	BillingPeriod BillingPeriod `json:"billing_period" gorm:"not null"`
	PricingType   PricingType   `json:"pricing_type" gorm:"not null"`
	TrialDays     int           `json:"trial_days"`
	Features      []string      `json:"features"`
	Metadata      interface{}   `json:"metadata" gorm:"type:jsonb"`
	CreatedAt     time.Time     `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt     time.Time     `json:"updated_at" gorm:"autoUpdateTime"`
}

type Subscription struct {
	ID                 string             `json:"id" gorm:"primaryKey;type:uuid;default:gen_random_uuid()"`
	CustomerID         string             `json:"customer_id" gorm:"not null;index"`
	PlanID             string             `json:"plan_id" gorm:"not null"`
	Plan               *Plan              `json:"plan" gorm:"foreignKey:PlanID"`
	Status             SubscriptionStatus `json:"status" gorm:"not null;default:'active'"`
	CurrentPeriodStart time.Time          `json:"current_period_start"`
	CurrentPeriodEnd   time.Time          `json:"current_period_end"`
	CanceledAt         *time.Time         `json:"canceled_at,omitempty"`
	TrialStart         *time.Time         `json:"trial_start,omitempty"`
	TrialEnd           *time.Time         `json:"trial_end,omitempty"`
	Quantity           int                `json:"quantity"`
	PaymentMethodID    string             `json:"payment_method_id"`
	ProviderName       string             `json:"provider_name"`
	Metadata           interface{}        `json:"metadata" gorm:"type:jsonb"`
	CreatedAt          time.Time          `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt          time.Time          `json:"updated_at" gorm:"autoUpdateTime"`
}

type CreateSubscriptionRequest struct {
	CustomerID string      `json:"customer_id" binding:"required"`
	PlanID     string      `json:"plan_id" binding:"required"`
	Quantity   int         `json:"quantity"`
	TrialDays  *int        `json:"trial_days,omitempty"`
	Metadata   interface{} `json:"metadata,omitempty"`
}

type UpdateSubscriptionRequest struct {
	Quantity        *int        `json:"quantity,omitempty"`
	PlanID          *string     `json:"plan_id,omitempty"`
	PaymentMethodID *string     `json:"payment_method_id,omitempty"`
	Metadata        interface{} `json:"metadata,omitempty"`
}

type CancelSubscriptionRequest struct {
	CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
	Reason            string `json:"reason,omitempty"`
}

type SubscriptionEvent struct {
	ID             string      `json:"id"`
	SubscriptionID string      `json:"subscription_id"`
	Type           string      `json:"type"` // created, updated, canceled, payment_failed, etc.
	Data           interface{} `json:"data"`
	CreatedAt      time.Time   `json:"created_at"`
}

type SubscriptionResponse struct {
	Subscription *Subscription `json:"subscription"`
}

type CreatePlanRequest struct {
	Name      string      `json:"name"`
	Amount    int64       `json:"amount"`
	Currency  string      `json:"currency"`
	Interval  string      `json:"interval"`
	TrialDays int         `json:"trial_days,omitempty"`
	Metadata  interface{} `json:"metadata,omitempty"`
}

type UpdatePlanRequest struct {
	Name      string      `json:"name,omitempty"`
	Amount    int64       `json:"amount,omitempty"`
	Currency  string      `json:"currency,omitempty"`
	Interval  string      `json:"interval,omitempty"`
	TrialDays int         `json:"trial_days,omitempty"`
	Active    *bool       `json:"active,omitempty"`
	Metadata  interface{} `json:"metadata,omitempty"`
}

type PlanResponse struct {
	Plan *Plan `json:"plan"`
}
