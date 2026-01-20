package queue

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// generateUUID generates a unique ID using crypto/rand
func generateUUID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp if crypto/rand fails
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

// Job represents a webhook processing job with metadata
type Job struct {
	JobID          string            `json:"job_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	Payload        map[string]any    `json:"payload"`
	AttemptCount   int               `json:"attempt_count"`
	CreatedAt      time.Time         `json:"created_at"`
	MaxRetries     int               `json:"max_retries,omitempty"`
	Timeout        time.Duration     `json:"timeout,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	ProcessedAt    *time.Time        `json:"processed_at,omitempty"`
	FailedAt       *time.Time        `json:"failed_at,omitempty"`
	Error          string            `json:"error,omitempty"`

	// Internal fields (not serialized)
	streamMessageID string // Redis stream message ID
}

// NewJob creates a new webhook job with default values
func NewJob(payload map[string]any, idempotencyKey string, headers map[string]string, maxRetries int, timeout time.Duration) *Job {
	return &Job{
		JobID:          generateUUID(),
		IdempotencyKey: idempotencyKey,
		Payload:        payload,
		AttemptCount:   0,
		CreatedAt:      time.Now(),
		MaxRetries:     maxRetries,
		Timeout:        timeout,
		Headers:        headers,
		ProcessedAt:    nil,
		FailedAt:       nil,
		Error:          "",
	}
}

// MarkProcessed marks the job as processed successfully
func (j *Job) MarkProcessed() {
	now := time.Now()
	j.ProcessedAt = &now
}

// MarkFailed marks the job as failed with an error
func (j *Job) MarkFailed(err error) {
	now := time.Now()
	j.FailedAt = &now
	j.Error = err.Error()
}

// ShouldRetry determines if the job should be retried
func (j *Job) ShouldRetry() bool {
	return j.AttemptCount < j.MaxRetries
}

// IncrementRetry increments the retry count
func (j *Job) IncrementRetry() {
	j.AttemptCount++
}

// Queue defines the interface for a job queue
type Queue interface {
	Enqueue(job *Job) error
	Dequeue() (*Job, error)
	Size() int
	Capacity() int
	Close() error
	IsFull() bool
	IsEmpty() bool
}

// JobQueue implements a bounded, buffered channel-based queue
type JobQueue struct {
	jobs     chan *Job
	capacity int
	closed   bool
}

// NewJobQueue creates a new job queue with specified capacity
func NewJobQueue(capacity int) *JobQueue {
	if capacity <= 0 {
		capacity = 100 // default capacity
	}

	return &JobQueue{
		jobs:     make(chan *Job, capacity),
		capacity: capacity,
		closed:   false,
	}
}

// Enqueue adds a job to the queue
// Returns error if queue is full or closed
func (q *JobQueue) Enqueue(job *Job) error {
	if q.closed {
		return ErrQueueClosed
	}

	select {
	case q.jobs <- job:
		return nil
	default:
		return ErrQueueFull
	}
}

// Dequeue retrieves a job from the queue
// Blocks until a job is available or queue is closed
func (q *JobQueue) Dequeue() (*Job, error) {
	if q.closed {
		return nil, ErrQueueClosed
	}

	select {
	case job, ok := <-q.jobs:
		if !ok {
			return nil, ErrQueueClosed
		}
		return job, nil
	default:
		// This should not happen as Dequeue is blocking
		return nil, ErrQueueEmpty
	}
}

// Size returns the current number of jobs in the queue
func (q *JobQueue) Size() int {
	return len(q.jobs)
}

// Capacity returns the maximum capacity of the queue
func (q *JobQueue) Capacity() int {
	return q.capacity
}

// Close closes the queue and stops accepting new jobs
// All existing jobs will be processed
func (q *JobQueue) Close() error {
	if q.closed {
		return nil
	}

	q.closed = true
	close(q.jobs)
	return nil
}

// IsFull checks if the queue is at capacity
func (q *JobQueue) IsFull() bool {
	return len(q.jobs) >= q.capacity
}

// IsEmpty checks if the queue has no jobs
func (q *JobQueue) IsEmpty() bool {
	return len(q.jobs) == 0
}

// Jobs returns the underlying channel for direct access
// This is used by the worker pool for testing purposes
func (q *JobQueue) Jobs() <-chan *Job {
	return q.jobs
}

// Queue errors
var (
	ErrQueueFull   = &QueueError{message: "queue is full"}
	ErrQueueEmpty  = &QueueError{message: "queue is empty"}
	ErrQueueClosed = &QueueError{message: "queue is closed"}
)

// QueueError represents a queue-related error
type QueueError struct {
	message string
}

func (e *QueueError) Error() string {
	return e.message
}
