package shutdown

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"webhook-processor/internal/worker"
)

// ShutdownManager handles graceful shutdown of the application
type ShutdownManager struct {
	logger *log.Logger
}

// NewShutdownManager creates a new shutdown manager
func NewShutdownManager(logger *log.Logger) *ShutdownManager {
	return &ShutdownManager{
		logger: logger,
	}
}

// WaitForShutdown waits for shutdown signals and handles graceful shutdown
func (sm *ShutdownManager) WaitForShutdown(ctx context.Context, server *http.Server, workerPool any) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// Wait for signal
	sig := <-sigChan
	sm.logger.Printf("received signal: %v", sig)

	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Start shutdown process
	go func() {
		sm.gracefulShutdown(shutdownCtx, server, workerPool)
	}()

	// Wait for shutdown to complete or timeout
	select {
	case <-shutdownCtx.Done():
		sm.logger.Printf("shutdown completed")
	case <-time.After(30 * time.Second):
		sm.logger.Printf("shutdown timeout, forcing exit")
		os.Exit(1)
	}
}

// gracefulShutdown performs the actual graceful shutdown
func (sm *ShutdownManager) gracefulShutdown(ctx context.Context, server *http.Server, workerPool any) {
	sm.logger.Println("initiating graceful shutdown...")

	// Step 1: Stop accepting new requests
	sm.logger.Println("stopping HTTP server...")

	// Shutdown the HTTP server
	if err := server.Shutdown(ctx); err != nil {
		sm.logger.Printf("server shutdown error: %v", err)
	}

	// Step 2: Stop worker pool
	sm.logger.Println("stopping worker pool...")

	if pool, ok := workerPool.(*worker.WorkerPool); ok {
		pool.Stop()
	} else {
		sm.logger.Printf("unknown worker pool type: %T", workerPool)
	}

	// Step 3: Additional cleanup (if needed)
	sm.logger.Println("performing cleanup...")

	// Close any remaining resources
	sm.cleanup()

	sm.logger.Println("graceful shutdown completed")
}

// cleanup performs any additional cleanup operations
func (sm *ShutdownManager) cleanup() {
	// Close any database connections
	// Close any file handles
	// Flush any buffered logs
	// Release any other resources

	// For now, just log the cleanup
	sm.logger.Println("cleanup completed")
}

// HealthChecker defines the interface for health checks
type HealthChecker interface {
	IsHealthy() bool
}

// ReadyChecker defines the interface for readiness checks
type ReadyChecker interface {
	IsReady() bool
}

// LivenessHandler handles liveness checks
type LivenessHandler struct {
	healthChecker HealthChecker
	logger        *log.Logger
}

// NewLivenessHandler creates a new liveness handler
func NewLivenessHandler(healthChecker HealthChecker, logger *log.Logger) *LivenessHandler {
	return &LivenessHandler{
		healthChecker: healthChecker,
		logger:        logger,
	}
}

// HandleLiveness handles liveness check requests
func (h *LivenessHandler) HandleLiveness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := http.StatusOK
	if !h.healthChecker.IsHealthy() {
		status = http.StatusServiceUnavailable
	}

	w.WriteHeader(status)
	w.Header().Set("Content-Type", "application/json")

	response := map[string]any{
		"status":    "alive",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Printf("failed to encode liveness response: %v", err)
	}
}

// ReadinessHandler handles readiness checks
type ReadinessHandler struct {
	readyChecker ReadyChecker
	logger       *log.Logger
}

// NewReadinessHandler creates a new readiness handler
func NewReadinessHandler(readyChecker ReadyChecker, logger *log.Logger) *ReadinessHandler {
	return &ReadinessHandler{
		readyChecker: readyChecker,
		logger:       logger,
	}
}

// HandleReadiness handles readiness check requests
func (h *ReadinessHandler) HandleReadiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := http.StatusOK
	if !h.readyChecker.IsReady() {
		status = http.StatusServiceUnavailable
	}

	w.WriteHeader(status)
	w.Header().Set("Content-Type", "application/json")

	response := map[string]any{
		"status":    "ready",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		h.logger.Printf("failed to encode readiness response: %v", err)
	}
}
