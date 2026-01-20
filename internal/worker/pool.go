package worker

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"webhook-processor/internal/queue"
	"webhook-processor/internal/service"
)

// Worker represents a single worker that processes jobs
type Worker struct {
	id           int
	redisQueue   *queue.RedisQueue
	service      service.Service
	logger       *log.Logger
	stopChan     chan struct{}
	wg           *sync.WaitGroup
	shutdownChan chan struct{}
}

// NewWorker creates a new worker instance
func NewWorker(id int, redisQueue *queue.RedisQueue, service service.Service, logger *log.Logger) *Worker {
	return &Worker{
		id:           id,
		redisQueue:   redisQueue,
		service:      service,
		logger:       logger,
		stopChan:     make(chan struct{}),
		wg:           &sync.WaitGroup{},
		shutdownChan: make(chan struct{}),
	}
}

// Start begins processing jobs from the queue
func (w *Worker) Start() {
	w.wg.Add(1)
	go w.run()
}

// Stop gracefully shuts down the worker
func (w *Worker) Stop() {
	close(w.stopChan)
	w.wg.Wait()
}

// run is the main worker loop
func (w *Worker) run() {
	defer w.wg.Done()

	for {
		select {
		case <-w.stopChan:
			// Worker is being shut down, stop processing
			return

		default:
			// Dequeue a job (blocking call)
			job, err := w.redisQueue.Dequeue()
			if err != nil {
				if errors.Is(err, queue.ErrQueueClosed) {
					// Queue is closed, stop processing
					return
				}
				w.logger.Printf("worker %d: dequeue error: %v", w.id, err)
				time.Sleep(100 * time.Millisecond) // Backoff on errors
				continue
			}

			// Process the job
			w.processJob(job)
		}
	}
}

// processJob processes a single job with retry logic and panic recovery
func (w *Worker) processJob(job *queue.Job) {
	startTime := time.Now()

	// Log job start
	w.logger.Printf("worker %d: processing job %s (attempt: %d)",
		w.id, job.JobID, job.AttemptCount)

	defer func() {
		if r := recover(); r != nil {
			// Panic recovery - log and mark as failed
			err := fmt.Errorf("panic recovered: %v", r)
			w.logger.Printf("worker %d: panic processing job %s: %v", w.id, job.JobID, err)
			w.handleJobFailure(job, err)
			return
		}

		// Log job completion
		duration := time.Since(startTime)
		w.logger.Printf("worker %d: completed job %s in %v", w.id, job.JobID, duration)
	}()

	// Process the job with retry logic
	err := w.processJobWithRetry(job)
	if err != nil {
		w.handleJobFailure(job, err)
		return
	}

	// Mark as processed successfully and acknowledge
	job.MarkProcessed()
	w.redisQueue.Acknowledge(job)
}

// handleJobFailure handles job failure by either requeuing for retry or moving to DLQ
func (w *Worker) handleJobFailure(job *queue.Job, err error) {
	job.MarkFailed(err)
	job.IncrementRetry()

	if job.ShouldRetry() {
		// Requeue for retry (don't acknowledge, let it become pending again)
		w.redisQueue.Requeue(job)
		w.logger.Printf("worker %d: requeued job %s for retry", w.id, job.JobID)
	} else {
		// Move to dead letter queue
		if dlqErr := w.redisQueue.MoveToDLQ(job); dlqErr != nil {
			w.logger.Printf("worker %d: failed to move job %s to DLQ: %v", w.id, job.JobID, dlqErr)
		} else {
			w.logger.Printf("worker %d: moved job %s to DLQ", w.id, job.JobID)
		}
		// Acknowledge to remove from pending
		w.redisQueue.Acknowledge(job)
	}
}

// processJobWithRetry handles job processing with exponential backoff retry
func (w *Worker) processJobWithRetry(job *queue.Job) error {
	var lastError error

	for attempt := 0; attempt <= job.MaxRetries; attempt++ {
		if attempt > 0 {
			// Calculate exponential backoff
			backoff := time.Duration(attempt*attempt) * 100 * time.Millisecond
			w.logger.Printf("worker %d: retrying job %s in %v (attempt %d)",
				w.id, job.JobID, backoff, attempt)
			time.Sleep(backoff)
		}

		// Process the job
		err := w.service.ProcessJob(job)
		if err == nil {
			// Success
			return nil
		}

		lastError = err
		job.IncrementRetry()

		// Log the error
		w.logger.Printf("worker %d: job %s failed (attempt %d): %v",
			w.id, job.JobID, attempt, err)

		// If we're out of retries, break
		if !job.ShouldRetry() {
			break
		}
	}

	// All retries exhausted
	return fmt.Errorf("job failed after %d attempts, last error: %v",
		job.AttemptCount, lastError)
}

// WorkerPool manages a pool of workers
type WorkerPool struct {
	workers     []*Worker
	redisQueue  *queue.RedisQueue
	service     service.Service
	logger      *log.Logger
	workerCount int
	mu          sync.Mutex
	stopped     bool
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(workerCount int, redisQueue *queue.RedisQueue, service service.Service, logger *log.Logger) *WorkerPool {
	return &WorkerPool{
		workers:     make([]*Worker, workerCount),
		redisQueue:  redisQueue,
		service:     service,
		logger:      logger,
		workerCount: workerCount,
	}
}

// Start starts all workers in the pool
func (wp *WorkerPool) Start() error {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	if wp.stopped {
		return errors.New("worker pool is stopped")
	}

	wp.logger.Printf("starting worker pool with %d workers", wp.workerCount)

	for i := 0; i < wp.workerCount; i++ {
		worker := NewWorker(i, wp.redisQueue, wp.service, wp.logger)
		wp.workers[i] = worker
		worker.Start()
	}

	return nil
}

// Stop gracefully shuts down all workers
func (wp *WorkerPool) Stop() {
	wp.mu.Lock()
	defer wp.mu.Unlock()

	if wp.stopped {
		return
	}

	wp.logger.Println("stopping worker pool")

	// Stop all workers
	for _, worker := range wp.workers {
		worker.Stop()
	}

	wp.stopped = true
	wp.logger.Println("worker pool stopped")
}

// GetWorkerCount returns the number of workers in the pool
func (wp *WorkerPool) GetWorkerCount() int {
	return wp.workerCount
}

// IsStopped returns whether the worker pool has been stopped
func (wp *WorkerPool) IsStopped() bool {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	return wp.stopped
}

// GetQueueStats returns queue statistics
func (wp *WorkerPool) GetQueueStats() (int, int, int, int) {
	size := wp.redisQueue.Size()
	pending := wp.redisQueue.GetPendingCount()
	dlqSize := wp.redisQueue.GetDLQSize()
	capacity := wp.redisQueue.Capacity() // Always -1 for Redis

	return size, pending, dlqSize, capacity
}
