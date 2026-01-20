package service

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"webhook-processor/internal/queue"
)

// Service defines the interface for webhook processing business logic
type Service interface {
	ProcessJob(job *queue.Job) error
	GetJobStats() JobStats
}

// Config holds service configuration
type Config struct {
	MaxRetries int
	Timeout    time.Duration
}

// WebhookService implements the Service interface
type WebhookService struct {
	config Config
	logger *log.Logger
}

// JobStats provides statistics about processed jobs
type JobStats struct {
	TotalProcessed    int64
	TotalFailed       int64
	CurrentRetries    int64
	AvgProcessingTime time.Duration
}

// NewWebhookService creates a new webhook service
func NewWebhookService(config Config, logger *log.Logger) *WebhookService {
	return &WebhookService{
		config: config,
		logger: logger,
	}
}

// ProcessJob processes a webhook job with retry logic
// Idempotency is handled at the HTTP level, not in workers
func (s *WebhookService) ProcessJob(job *queue.Job) error {
	startTime := time.Now()

	// Extract event type from payload
	eventType := s.extractEventType(job.Payload)

	// Validate the job payload
	if err := s.validateJob(job, eventType); err != nil {
		return fmt.Errorf("job validation failed: %w", err)
	}

	// Process the webhook payload
	err := s.processWebhookPayload(job, eventType)
	if err != nil {
		// Log the error but don't mark as failed yet - let retry logic handle it
		s.logger.Printf("failed to process job %s: %v", job.JobID, err)
		return err
	}

	// Log successful processing
	duration := time.Since(startTime)
	s.logger.Printf("successfully processed job %s in %v", job.JobID, duration)

	return nil
}

// extractEventType extracts the event type from payload
func (s *WebhookService) extractEventType(payload map[string]any) string {
	// Check for common event type fields in payload
	if eventType, exists := payload["event_type"]; exists {
		if eventTypeStr, ok := eventType.(string); ok {
			return eventTypeStr
		}
	}

	if eventType, exists := payload["type"]; exists {
		if eventTypeStr, ok := eventType.(string); ok {
			return eventTypeStr
		}
	}

	if eventType, exists := payload["event"]; exists {
		if eventTypeStr, ok := eventType.(string); ok {
			return eventTypeStr
		}
	}

	// Try to infer from payment-related fields
	if _, hasID := payload["id"]; hasID {
		if _, hasAmount := payload["amount"]; hasAmount {
			if _, hasCurrency := payload["currency"]; hasCurrency {
				return "payment"
			}
		}
		if _, hasStatus := payload["status"]; hasStatus {
			if status, ok := payload["status"].(string); ok {
				if strings.Contains(strings.ToLower(status), "success") {
					return "payment.succeeded"
				} else if strings.Contains(strings.ToLower(status), "fail") {
					return "payment.failed"
				}
			}
		}
	}

	// Try to infer from user-related fields
	if _, hasUserID := payload["user_id"]; hasUserID {
		return "user"
	}
	if _, hasEmail := payload["email"]; hasEmail {
		return "user.created"
	}

	// Try to infer from message-related fields
	if _, hasMessageID := payload["message_id"]; hasMessageID {
		return "message.sent"
	}
	if _, hasRecipient := payload["recipient"]; hasRecipient {
		return "message.sent"
	}

	return "unknown"
}

// validateJob validates the job payload and metadata
func (s *WebhookService) validateJob(job *queue.Job, eventType string) error {
	if job.Payload == nil {
		return errors.New("payload is required")
	}

	if eventType == "" {
		return errors.New("event type is required")
	}

	// Validate required fields based on event type
	if err := s.validateEventSpecificFields(job, eventType); err != nil {
		return err
	}

	return nil
}

// validateEventSpecificFields validates fields specific to event types
func (s *WebhookService) validateEventSpecificFields(job *queue.Job, eventType string) error {
	// Example validation for different event types
	switch eventType {
	case "payment.succeeded":
		return s.validatePaymentEvent(job.Payload)
	case "payment.failed":
		return s.validatePaymentEvent(job.Payload)
	case "user.created":
		return s.validateUserEvent(job.Payload)
	case "message.sent":
		return s.validateMessageEvent(job.Payload)
	default:
		// Unknown event type, but we'll still process it
		return nil
	}
}

// validatePaymentEvent validates payment-related webhook events
func (s *WebhookService) validatePaymentEvent(payload any) error {
	p, ok := payload.(map[string]any)
	if !ok {
		return errors.New("invalid payload format for payment event")
	}

	// Check for required payment fields
	if _, exists := p["id"]; !exists {
		return errors.New("payment id is required")
	}

	if _, exists := p["amount"]; !exists {
		return errors.New("payment amount is required")
	}

	if _, exists := p["currency"]; !exists {
		return errors.New("payment currency is required")
	}

	return nil
}

// validateUserEvent validates user-related webhook events
func (s *WebhookService) validateUserEvent(payload any) error {
	p, ok := payload.(map[string]any)
	if !ok {
		return errors.New("invalid payload format for user event")
	}

	// Check for required user fields
	if _, exists := p["id"]; !exists {
		return errors.New("user id is required")
	}

	if _, exists := p["email"]; !exists {
		return errors.New("user email is required")
	}

	return nil
}

// validateMessageEvent validates message-related webhook events
func (s *WebhookService) validateMessageEvent(payload any) error {
	p, ok := payload.(map[string]any)
	if !ok {
		return errors.New("invalid payload format for message event")
	}

	// Check for required message fields
	if _, exists := p["message_id"]; !exists {
		return errors.New("message id is required")
	}

	if _, exists := p["recipient"]; !exists {
		return errors.New("message recipient is required")
	}

	return nil
}

// processWebhookPayload processes the actual webhook business logic
func (s *WebhookService) processWebhookPayload(job *queue.Job, eventType string) error {
	// Simulate processing time
	time.Sleep(10 * time.Millisecond)

	// Process based on event type
	switch eventType {
	case "payment.succeeded":
		return s.processPaymentSucceeded(job.Payload)
	case "payment.failed":
		return s.processPaymentFailed(job.Payload)
	case "user.created":
		return s.processUserCreated(job.Payload)
	case "message.sent":
		return s.processMessageSent(job.Payload)
	default:
		// Generic processing for unknown event types
		return s.processGenericEvent(eventType, job.Payload)
	}
}

// processPaymentSucceeded handles payment success events
func (s *WebhookService) processPaymentSucceeded(payload any) error {
	paymentID := payload.(map[string]any)["id"]
	amount := payload.(map[string]any)["amount"]
	currency := payload.(map[string]any)["currency"]

	s.logger.Printf("processing payment succeeded: id=%v, amount=%v, currency=%s",
		paymentID, amount, currency)

	// Here you would integrate with your business logic:
	// - Update user balance
	// - Send confirmation email
	// - Update order status
	// etc.

	return nil
}

// processPaymentFailed handles payment failure events
func (s *WebhookService) processPaymentFailed(payload any) error {
	p := payload.(map[string]any)
	paymentID := p["id"]
	amount := p["amount"]
	currency := p["currency"]
	reason, _ := p["reason"].(string)
	if reason == "" {
		reason = "unknown"
	}

	s.logger.Printf("processing payment failed: id=%v, amount=%v, currency=%s, reason=%s",
		paymentID, amount, currency, reason)

	// Here you would integrate with your business logic:
	// - Mark payment as failed
	// - Send failure notification
	// - Update order status
	// etc.

	return nil
}

// processUserCreated handles user creation events
func (s *WebhookService) processUserCreated(payload any) error {
	p := payload.(map[string]any)
	userID := p["id"]
	email := p["email"]
	name, _ := p["name"].(string)

	s.logger.Printf("processing user created: id=%v, email=%s, name=%s",
		userID, email, name)

	// Here you would integrate with your business logic:
	// - Send welcome email
	// - Initialize user preferences
	// - Create user profile
	// etc.

	return nil
}

// processMessageSent handles message sent events
func (s *WebhookService) processMessageSent(payload any) error {
	p := payload.(map[string]any)
	messageID := p["message_id"]
	recipient := p["recipient"]
	content, _ := p["content"].(string)

	s.logger.Printf("processing message sent: message_id=%v, recipient=%s, content=%s",
		messageID, recipient, content)

	// Here you would integrate with your business logic:
	// - Update message status
	// - Send delivery receipts
	// - Track analytics
	// etc.

	return nil
}

// processGenericEvent handles unknown event types
func (s *WebhookService) processGenericEvent(eventType string, payload any) error {
	s.logger.Printf("processing generic event: type=%s, payload=%v", eventType, payload)

	// Generic processing for unknown events
	// You might want to log these for later analysis

	return nil
}

// GetJobStats returns statistics about processed jobs
func (s *WebhookService) GetJobStats() JobStats {
	// In production, you would track these metrics properly
	// For this example, we'll return placeholder values
	return JobStats{
		TotalProcessed:    0,
		TotalFailed:       0,
		CurrentRetries:    0,
		AvgProcessingTime: 0,
	}
}
