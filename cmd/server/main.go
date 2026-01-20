package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"webhook-processor/internal/config"
	"webhook-processor/internal/idempotency"
	"webhook-processor/internal/queue"
	"webhook-processor/internal/service"
	"webhook-processor/internal/shutdown"
	"webhook-processor/internal/worker"

	"github.com/redis/go-redis/v9"
)

// Simple inline handlers
type WebhookHandler struct {
	redisQueue  *queue.RedisQueue
	idempotency *idempotency.Service
	logger      *log.Logger
}

type HealthHandler struct {
	workerPool *worker.WorkerPool
	logger     *log.Logger
}

func (h *WebhookHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	// Simple implementation - parse JSON, check idempotency, enqueue
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Extract event type from header or payload
	eventType := r.Header.Get("X-Event-Type")
	if eventType == "" {
		// Try to infer from payload
		if et, ok := payload["event_type"].(string); ok {
			eventType = et
		} else if et, ok := payload["type"].(string); ok {
			eventType = et
		}
	}

	// Generate idempotency key including event type
	idempotencyKey := h.idempotency.GenerateKey(payload, eventType)
	h.logger.Printf("generated idempotency key: %s (event: %s)", idempotencyKey, eventType)

	wasSet, err := h.idempotency.CheckAndSet(r.Context(), idempotencyKey)
	h.logger.Printf("idempotency check: key=%s, wasSet=%v (keyWasNew), err=%v", idempotencyKey, wasSet, err)
	if err != nil {
		h.logger.Printf("idempotency check failed: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !wasSet {
		// Key already existed - this is a duplicate
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Webhook already processed",
			"key":     idempotencyKey,
			"status":  "duplicate",
		})
		return
	}

	// Key was just set - not a duplicate, accept and enqueue
	job := queue.NewJob(payload, idempotencyKey, nil, 3, 30*time.Second)
	if err := h.redisQueue.Enqueue(job); err != nil {
		h.logger.Printf("failed to enqueue: %v", err)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{
		"message": "Webhook accepted",
		"id":      job.JobID,
		"key":     idempotencyKey,
		"status":  "queued",
	})
}

func (h *HealthHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	size, pending, dlqSize, _ := h.workerPool.GetQueueStats()
	status := "healthy"
	if dlqSize > 0 {
		status = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":     status,
		"queue_size": size,
		"pending":    pending,
		"dlq_size":   dlqSize,
		"workers":    h.workerPool.GetWorkerCount(),
		"is_stopped": h.workerPool.IsStopped(),
	})
}

func main() {
	// Initialize structured logging
	logger := setupLogger()

	// Load configuration
	cfg := config.Load()

	// Create Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})

	// Test Redis connection
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		logger.Printf("failed to connect to Redis: %v", err)
		os.Exit(1)
	}
	logger.Printf("connected to Redis at %s", cfg.RedisAddr)

	// Create Redis queue
	redisQueue := queue.NewRedisQueue(rdb, cfg.QueueName, fmt.Sprintf("worker-%d", os.Getpid()), logger)

	// Explicitly initialize the stream and consumer group
	if err := redisQueue.Init(); err != nil {
		logger.Printf("warning: failed to initialize Redis queue: %v", err)
	} else {
		logger.Printf("Redis queue initialized successfully")
	}

	// Create idempotency service
	idempotencyService := idempotency.NewService(rdb, cfg.IdempotencyTTL)

	// Initialize webhook service with business logic
	webhookService := service.NewWebhookService(service.Config{
		MaxRetries: cfg.MaxRetries,
		Timeout:    cfg.Timeout,
	}, logger)

	// Create worker pool with fixed number of workers
	workerPool := worker.NewWorkerPool(cfg.WorkerCount, redisQueue, webhookService, logger)

	// Start worker pool
	if err := workerPool.Start(); err != nil {
		fmt.Printf("failed to start worker pool: %v\n", err)
		os.Exit(1)
	}

	// Initialize HTTP handlers
	webhookHandler := &WebhookHandler{
		redisQueue:  redisQueue,
		idempotency: idempotencyService,
		logger:      logger,
	}
	healthHandler := &HealthHandler{
		workerPool: workerPool,
		logger:     logger,
	}

	// Setup HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/webhooks", webhookHandler.HandleWebhook)
	mux.HandleFunc("/health", healthHandler.HandleHealth)

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	// Setup graceful shutdown
	shutdownManager := shutdown.NewShutdownManager(logger)

	// Start server in goroutine
	go func() {
		log.Printf("starting server on port %s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("server failed: %v", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	shutdownManager.WaitForShutdown(context.Background(), server, workerPool)
}

func setupLogger() *log.Logger {
	// In production, use structured logging with slog
	// For simplicity, using log.Logger here
	logger := log.New(os.Stdout, "", log.LstdFlags|log.Lshortfile)
	return logger
}
