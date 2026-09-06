package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const DefaultTimestampTolerance = 5 * time.Minute

var (
	ErrMissingTimestamp          = errors.New("webhook timestamp missing")
	ErrInvalidTimestamp          = errors.New("webhook timestamp malformed")
	ErrTimestampOutsideTolerance = errors.New("webhook timestamp outside tolerance window")
)

func ValidateHMACSHA256(payload []byte, signature, secret string) error {
	if secret == "" {
		return fmt.Errorf("secret not configured")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedSignature := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(signature), []byte(expectedSignature)) {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

func ValidateTimestamp(timestamp string, tolerance time.Duration) error {
	if timestamp == "" {
		return ErrMissingTimestamp
	}

	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrInvalidTimestamp
	}

	drift := time.Since(time.Unix(seconds, 0))
	if drift < 0 {
		drift = -drift
	}
	if drift > tolerance {
		return ErrTimestampOutsideTolerance
	}

	return nil
}

func ValidateHMACSHA256WithTimestamp(payload []byte, signature, secret, timestamp string, tolerance time.Duration) error {
	if err := ValidateTimestamp(timestamp, tolerance); err != nil {
		return err
	}

	return ValidateHMACSHA256(append([]byte(timestamp), payload...), signature, secret)
}

func GenerateHMACSHA256(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
