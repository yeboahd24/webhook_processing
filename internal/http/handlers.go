package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"webhook-processor/internal/idempotency"
	"webhook-processor/internal/queue"
)

// WebhookHandler handles incoming webhook HTTP requests
type WebhookHandler struct {
	redisQueue  *queue.RedisQueue
	idempotency *idempotency.Service
	config      Config
	logger      *log.Logger
}

// Config holds handler configuration
type Config struct {
	Port        string
	WorkerCount int
	QueueSize   int
	MaxRetries  int
	Timeout     time.Duration
}

// NewWebhookHandler creates a new webhook handler
func NewWebhookHandler(redisQueue *queue.RedisQueue, idempotency *idempotency.Service, config Config, logger *log.Logger) *WebhookHandler {
	return &WebhookHandler{
		redisQueue:  redisQueue,
		idempotency: idempotency,
		config:      config,
		logger:      logger,
	}
}

// HandleWebhook handles incoming webhook requests
func (h *WebhookHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	// Only accept POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Set CORS headers for webhooks
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Event-Type, X-Signature")

	// Handle preflight requests
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Validate content type
	contentType := r.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		h.logger.Printf("invalid content type: %s", contentType)
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	// Read and parse request body
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.logger.Printf("failed to decode payload: %v", err)
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	// Generate idempotency key
	idempotencyKey := h.idempotency.GenerateKey(payload, "")

	// Check for duplicate webhook
	isDuplicate, err := h.idempotency.CheckAndSet(r.Context(), idempotencyKey)
	if err != nil {
		h.logger.Printf("idempotency check failed: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if isDuplicate {
		h.logger.Printf("duplicate webhook rejected: key=%s", idempotencyKey)
		// Return 202 for idempotent success
		w.WriteHeader(http.StatusAccepted)
		response := map[string]any{
			"message": "Webhook already processed",
			"key":     idempotencyKey,
			"status":  "duplicate",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// Extract headers
	headers := make(map[string]string)
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}

	// Create job
	job := queue.NewJob(payload, idempotencyKey, headers, h.config.MaxRetries, h.config.Timeout)

	// Try to enqueue the job
	err = h.redisQueue.Enqueue(job)
	if err != nil {
		h.logger.Printf("failed to enqueue job: %v", err)
		http.Error(w, "Service unavailable (Redis error)", http.StatusServiceUnavailable)
		return
	}

	// Log successful webhook reception
	h.logger.Printf("received webhook: id=%s, key=%s, size=%d",
		job.JobID, idempotencyKey, len(payload))

	// Return 202 Accepted immediately
	w.WriteHeader(http.StatusAccepted)
	response := map[string]any{
		"message": "Webhook accepted for processing",
		"id":      job.JobID,
		"key":     idempotencyKey,
		"status":  "queued",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Printf("failed to encode response: %v", err)
	}
}

// extractEventType extracts the event type from headers or payload
func (h *WebhookHandler) extractEventType(r *http.Request, payload map[string]any) string {
	// First, check X-Event-Type header
	if eventType := r.Header.Get("X-Event-Type"); eventType != "" {
		return eventType
	}

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

	return ""
}

// HealthHandler handles health check requests
type HealthHandler struct {
	workerPool WorkerPool
	logger     *log.Logger
}

// WorkerPool defines the interface for worker pool health checks
type WorkerPool interface {
	GetWorkerCount() int
	IsStopped() bool
	GetQueueStats() (int, int, float64)
}

// NewHealthHandler creates a new health handler
func NewHealthHandler(workerPool WorkerPool, logger *log.Logger) *HealthHandler {
	return &HealthHandler{
		workerPool: workerPool,
		logger:     logger,
	}
}

// HandleHealth handles health check requests
func (h *HealthHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	// Only accept GET requests
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get worker pool stats
	workerCount := h.workerPool.GetWorkerCount()
	isStopped := h.workerPool.IsStopped()
	queueSize, queueCapacity, queueUtilization := h.workerPool.GetQueueStats()

	// Determine health status
	status := "healthy"
	if isStopped {
		status = "unhealthy"
	} else if queueUtilization > 95.0 {
		status = "degraded"
	}

	// Build health response
	health := map[string]any{
		"status":    status,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"workers": map[string]any{
			"count":  workerCount,
			"active": !isStopped,
		},
		"queue": map[string]any{
			"size":        queueSize,
			"capacity":    queueCapacity,
			"utilization": fmt.Sprintf("%.2f%%", queueUtilization),
			"is_full":     queueSize >= queueCapacity,
			"is_empty":    queueSize == 0,
		},
	}

	// Set appropriate status code
	var statusCode int
	switch status {
	case "healthy":
		statusCode = http.StatusOK
	case "degraded":
		statusCode = http.StatusOK // Still operational but degraded
	case "unhealthy":
		statusCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(health); err != nil {
		h.logger.Printf("failed to encode health response: %v", err)
	}
}

// MetricsHandler handles metrics requests (for Prometheus integration)
type MetricsHandler struct {
	queue  queue.Queue
	logger *log.Logger
}

// NewMetricsHandler creates a new metrics handler
func NewMetricsHandler(queue queue.Queue, logger *log.Logger) *MetricsHandler {
	return &MetricsHandler{
		queue:  queue,
		logger: logger,
	}
}

// HandleMetrics handles metrics requests
func (h *MetricsHandler) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	// Only accept GET requests
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get queue metrics
	queueSize := h.queue.Size()
	queueCapacity := h.queue.Capacity()
	queueUtilization := float64(queueSize) / float64(queueCapacity) * 100

	// Build metrics response
	metrics := map[string]any{
		"queue_size":        queueSize,
		"queue_capacity":    queueCapacity,
		"queue_utilization": queueUtilization,
		"timestamp":         time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(metrics); err != nil {
		h.logger.Printf("failed to encode metrics response: %v", err)
	}
}
