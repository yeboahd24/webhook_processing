package idempotency

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Service handles idempotency checks using Redis
type Service struct {
	client *redis.Client
	ttl    time.Duration
}

// NewService creates a new idempotency service
func NewService(client *redis.Client, ttl time.Duration) *Service {
	return &Service{
		client: client,
		ttl:    ttl,
	}
}

// CheckAndSet checks if a key exists and sets it if not
// Returns (wasSet, error) where:
//
//	wasSet=true means key was created (first time, NOT a duplicate)
//	wasSet=false means key already existed (duplicate request)
func (s *Service) CheckAndSet(ctx context.Context, key string) (bool, error) {
	wasSet, err := s.client.SetNX(ctx, key, "1", s.ttl).Result()
	if err != nil {
		return false, fmt.Errorf("failed to check/set idempotency key: %w", err)
	}
	// Return true if key was just created (not duplicate), false if key existed (duplicate)
	return wasSet, nil
}

// GenerateKey generates an idempotency key from webhook payload
// This is a deterministic key based on relevant payload fields
func (s *Service) GenerateKey(payload map[string]any, eventType string) string {
	// Create a hash of the payload and event type
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%v", eventType, payload)))

	return fmt.Sprintf("idemp:%x", h.Sum(nil))
}

// Delete removes an idempotency key (useful for testing or cleanup)
func (s *Service) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}
