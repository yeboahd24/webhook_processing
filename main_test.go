package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"webhook-processor/internal/http/handlers"
	"webhook-processor/internal/queue"
	"webhook-processor/internal/service"
	"webhook-processor/internal/worker"
)

func TestWebhookHandler(t *testing.T) {
	// Create a test configuration
	config := Config{
		Port:        "8080",
		WorkerCount: 2,
		QueueSize:   10,
		MaxRetries:  3,
		Timeout:     5 * time.Second,
	}

	// Create a test logger
	logger := &testLogger{}

	// Create a test queue
	testQueue := queue.NewJobQueue(config.QueueSize)

	// Create a test service
	testService := service.NewWebhookService(service.Config{
		MaxRetries: config.MaxRetries,
		Timeout:    config.Timeout,
	}, logger)

	// Create a test worker pool
	testWorkerPool := worker.NewWorkerPool(config.WorkerCount, testQueue, testService, logger)
	if err := testWorkerPool.Start(); err != nil {
		t.Fatalf("Failed to start worker pool: %v", err)
	}

	// Create a test webhook handler
	handler := handlers.NewWebhookHandler(testQueue, config, logger)

	// Test webhook payload
	webhookPayload := map[string]any{
		"id":          "test_payment_123",
		"amount":      1000,
		"currency":    "USD",
		"status":      "succeeded",
		"customer_id": "test_customer_123",
	}

	payloadBytes, err := json.Marshal(webhookPayload)
	if err != nil {
		t.Fatalf("Failed to marshal webhook payload: %v", err)
	}

	// Create a test HTTP request
	req := httptest.NewRequest("POST", "/webhooks", bytes.NewReader(payloadBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Event-Type", "payment.succeeded")

	// Create a test response recorder
	w := httptest.NewRecorder()

	// Handle the webhook request
	handler.HandleWebhook(w, req)

	// Check the response
	if w.Code != http.StatusAccepted {
		t.Errorf("Expected status code %d, got %d", http.StatusAccepted, w.Code)
	}

	// Check the response body
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response["status"] != "queued" {
		t.Errorf("Expected status 'queued', got '%v'", response["status"])
	}

	if response["event"] != "payment.succeeded" {
		t.Errorf("Expected event 'payment.succeeded', got '%v'", response["event"])
	}

	// Check that the job was enqueued
	if testQueue.Size() == 0 {
		t.Error("Expected job to be enqueued, but queue is empty")
	}

	// Clean up
	testWorkerPool.Stop()
}

// testLogger is a simple logger for testing
type testLogger struct{}

func (l *testLogger) Println(v ...any) {
	fmt.Println(v...)
}

func TestHealthHandler(t *testing.T) {
	// Create a test worker pool
	testWorkerPool := &testWorkerPool{}
	logger := &testLogger{}

	// Create a test health handler
	handler := handlers.NewHealthHandler(testWorkerPool, logger)

	// Create a test HTTP request
	req := httptest.NewRequest("GET", "/health", nil)

	// Create a test response recorder
	w := httptest.NewRecorder()

	// Handle the health request
	handler.HandleHealth(w, req)

	// Check the response
	if w.Code != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, w.Code)
	}

	// Check the response body
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if response["status"] != "healthy" {
		t.Errorf("Expected status 'healthy', got '%v'", response["status"])
	}
}

// testWorkerPool is a test implementation of WorkerPool
type testWorkerPool struct{}

func (p *testWorkerPool) GetWorkerCount() int {
	return 2
}

func (p *testWorkerPool) IsStopped() bool {
	return false
}

func (p *testWorkerPool) GetQueueStats() (int, int, float64) {
	return 5, 10, 50.0
}

// Test the main function
func TestMain(m *testing.M) {
	// Run the tests
	code := m.Run()

	// Exit with the test code
	os.Exit(code)
}
