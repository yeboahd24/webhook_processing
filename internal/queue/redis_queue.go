package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisQueue implements durable queue using Redis Streams
// We use Streams for durability, consumer groups, and pending message tracking
// This provides better reliability than Lists for failure scenarios
type RedisQueue struct {
	client     *redis.Client
	streamName string
	groupName  string
	consumerID string
	logger     *log.Logger
	closed     bool
}

// NewRedisQueue creates a new Redis-backed queue
func NewRedisQueue(client *redis.Client, streamName string, consumerID string, logger *log.Logger) *RedisQueue {
	groupName := "webhook_workers" // Consumer group name

	q := &RedisQueue{
		client:     client,
		streamName: streamName,
		groupName:  groupName,
		consumerID: consumerID,
		logger:     logger,
		closed:     false,
	}

	// Initialize the stream and consumer group during construction
	// This ensures everything is ready before workers start
	if err := q.ensureConsumerGroup(); err != nil {
		logger.Printf("warning: failed to initialize Redis stream: %v", err)
	}

	return q
}

// Init performs explicit initialization of the stream and consumer group
// This should be called once at application startup
func (rq *RedisQueue) Init() error {
	rq.logger.Printf("initializing Redis stream %s with group %s", rq.streamName, rq.groupName)
	return rq.ensureConsumerGroup()
}

// ensureConsumerGroup creates the consumer group if it doesn't exist
func (rq *RedisQueue) ensureConsumerGroup() error {
	ctx := context.Background()
	rq.logger.Printf("ensuring consumer group %s for stream %s", rq.groupName, rq.streamName)

	// Check if stream exists
	streamExists := false
	streamInfo, err := rq.client.XInfoStream(ctx, rq.streamName).Result()
	if err == nil {
		// Stream exists
		streamExists = true
		rq.logger.Printf("stream exists (length: %d)", streamInfo.Length)
	} else if err == redis.Nil {
		// Stream definitely doesn't exist
		rq.logger.Printf("stream doesn't exist")
	} else {
		// Some other error from XInfoStream - treat as stream doesn't exist
		// We'll try to create it with MKSTREAM
		rq.logger.Printf("XInfoStream error (treating as stream doesn't exist): %v", err)
		streamExists = false
	}

	if streamExists {
		// Stream exists - check if group exists
		rq.logger.Printf("checking for existing groups")
		groups, groupErr := rq.client.XInfoGroups(ctx, rq.streamName).Result()
		if groupErr == nil {
			for _, g := range groups {
				rq.logger.Printf("found group: %s", g.Name)
				if g.Name == rq.groupName {
					rq.logger.Printf("consumer group already exists")
					return nil
				}
			}
			// Group doesn't exist - create it
			rq.logger.Printf("creating consumer group for existing stream")
			createErr := rq.client.XGroupCreate(ctx, rq.streamName, rq.groupName, "0").Err()
			if createErr != nil {
				return fmt.Errorf("failed to create group on existing stream: %w", createErr)
			}
			rq.logger.Printf("created consumer group for existing stream")
			return nil
		} else if groupErr == redis.Nil {
			// No groups exist, create the first one
			rq.logger.Printf("no groups exist, creating first group")
			createErr := rq.client.XGroupCreate(ctx, rq.streamName, rq.groupName, "0").Err()
			if createErr != nil {
				return fmt.Errorf("failed to create first group: %w", createErr)
			}
			rq.logger.Printf("created first consumer group")
			return nil
		} else {
			// Other error getting groups
			return fmt.Errorf("failed to get groups: %w", groupErr)
		}
	}

	// Stream doesn't exist - create it with XGroupCreateMkStream
	// This is the Redis-recommended way to create stream with group atomically
	rq.logger.Printf("stream doesn't exist, using MKSTREAM to create")

	createErr := rq.client.XGroupCreateMkStream(ctx, rq.streamName, rq.groupName, "0").Err()
	if createErr != nil {
		errStr := createErr.Error()
		rq.logger.Printf("MKSTREAM result: %s", errStr)

		// Check if it's because the group already exists (race condition)
		if strings.Contains(errStr, "BUSYGROUP") || strings.Contains(errStr, "Group already exists") {
			rq.logger.Printf("group was created by another process")
			return nil
		}

		// Check if stream was created by another process
		if strings.Contains(errStr, "NOGROUP") || strings.Contains(errStr, "No such key") {
			// Stream exists now, try to create just the group
			rq.logger.Printf("stream was created, trying to create group")
			secondErr := rq.client.XGroupCreate(ctx, rq.streamName, rq.groupName, "0").Err()
			if secondErr != nil {
				if strings.Contains(secondErr.Error(), "BUSYGROUP") || strings.Contains(secondErr.Error(), "Group already exists") {
					return nil
				}
				return fmt.Errorf("failed to create group after stream: %w", secondErr)
			}
			return nil
		}

		return fmt.Errorf("failed to create stream with group: %w", createErr)
	}

	rq.logger.Printf("successfully created stream and consumer group with MKSTREAM")
	return nil
}

// Enqueue adds a job to the Redis stream
func (rq *RedisQueue) Enqueue(job *Job) error {
	if rq.closed {
		return ErrQueueClosed
	}

	// Serialize job to JSON
	jobData, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	// Add to stream
	args := &redis.XAddArgs{
		Stream: rq.streamName,
		ID:     "*", // Auto-generate ID
		Values: map[string]any{
			"job": string(jobData),
		},
	}

	_, err = rq.client.XAdd(context.Background(), args).Result()
	if err != nil {
		return fmt.Errorf("failed to add job to stream: %w", err)
	}

	rq.logger.Printf("enqueued job %s to Redis stream", job.JobID)
	return nil
}

// Dequeue retrieves a job from the Redis stream using consumer groups
// This is a blocking operation that waits for jobs
func (rq *RedisQueue) Dequeue() (*Job, error) {
	if rq.closed {
		return nil, ErrQueueClosed
	}

	rq.logger.Printf("worker %s attempting to dequeue", rq.consumerID)

	// Ensure consumer group exists (create it if necessary)
	if err := rq.ensureConsumerGroup(); err != nil {
		rq.logger.Printf("failed to ensure consumer group: %v", err)
		// Continue anyway, XREADGROUP might still work
	}

	rq.logger.Printf("worker %s reading from stream %s with group %s", rq.consumerID, rq.streamName, rq.groupName)

	// Read from pending messages first, then new messages
	// BLOCK 0 means block indefinitely until a message is available
	args := &redis.XReadGroupArgs{
		Group:    rq.groupName,
		Consumer: rq.consumerID,
		Streams:  []string{rq.streamName, ">"}, // ">" means new messages only
		Count:    1,
		Block:    0, // Block indefinitely
	}

	streams, err := rq.client.XReadGroup(context.Background(), args).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrQueueEmpty
		}
		rq.logger.Printf("XReadGroup error: %v", err)
		return nil, fmt.Errorf("failed to read from stream: %w", err)
	}

	rq.logger.Printf("worker %s received %d streams", rq.consumerID, len(streams))

	// Process the first message
	if len(streams) == 0 || len(streams[0].Messages) == 0 {
		return nil, ErrQueueEmpty
	}

	message := streams[0].Messages[0]
	jobData, ok := message.Values["job"].(string)
	if !ok {
		// Invalid message format, acknowledge and skip
		rq.client.XAck(context.Background(), rq.streamName, rq.groupName, message.ID)
		return nil, fmt.Errorf("invalid message format")
	}

	// Deserialize job
	var job Job
	if err := json.Unmarshal([]byte(jobData), &job); err != nil {
		// Failed to unmarshal, acknowledge and skip
		rq.client.XAck(context.Background(), rq.streamName, rq.groupName, message.ID)
		return nil, fmt.Errorf("failed to unmarshal job: %w", err)
	}

	// Store the stream message ID for later acknowledgment
	job.streamMessageID = message.ID

	rq.logger.Printf("dequeued job %s from Redis stream", job.JobID)
	return &job, nil
}

// Acknowledge marks a job as processed successfully
// This removes it from the pending messages
func (rq *RedisQueue) Acknowledge(job *Job) error {
	if job.streamMessageID == "" {
		return fmt.Errorf("job has no stream message ID")
	}

	err := rq.client.XAck(context.Background(), rq.streamName, rq.groupName, job.streamMessageID).Err()
	if err != nil {
		return fmt.Errorf("failed to acknowledge job: %w", err)
	}

	rq.logger.Printf("acknowledged job %s", job.JobID)
	return nil
}

// Requeue puts a failed job back into the stream for retry
// This is done by not acknowledging and letting the message become pending again
func (rq *RedisQueue) Requeue(job *Job) error {
	// For retries, we don't acknowledge the message, so it remains in pending
	// and will be redelivered after the visibility timeout
	rq.logger.Printf("requeued job %s for retry", job.JobID)
	return nil
}

// MoveToDLQ moves permanently failed jobs to a dead letter queue
func (rq *RedisQueue) MoveToDLQ(job *Job) error {
	dlqName := rq.streamName + "_dlq"

	jobData, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job for DLQ: %w", err)
	}

	args := &redis.XAddArgs{
		Stream: dlqName,
		ID:     "*",
		Values: map[string]any{
			"job":       string(jobData),
			"failed_at": time.Now().Format(time.RFC3339),
		},
	}

	_, err = rq.client.XAdd(context.Background(), args).Result()
	if err != nil {
		return fmt.Errorf("failed to add job to DLQ: %w", err)
	}

	// Now acknowledge the original message to remove it from pending
	if job.streamMessageID != "" {
		rq.client.XAck(context.Background(), rq.streamName, rq.groupName, job.streamMessageID)
	}

	rq.logger.Printf("moved job %s to DLQ", job.JobID)
	return nil
}

// Size returns the approximate number of jobs in the stream
// This is an estimate since Redis Streams don't have exact size
func (rq *RedisQueue) Size() int {
	// Get stream info
	info, err := rq.client.XInfoStream(context.Background(), rq.streamName).Result()
	if err != nil {
		rq.logger.Printf("failed to get stream info: %v", err)
		return 0
	}

	// Return length (total messages)
	return int(info.Length)
}

// Capacity returns -1 since Redis doesn't have a fixed capacity
// Backpressure is enforced locally by the application
func (rq *RedisQueue) Capacity() int {
	return -1 // Unlimited
}

// Close closes the queue (no-op for Redis)
func (rq *RedisQueue) Close() error {
	rq.closed = true
	rq.logger.Println("Redis queue closed")
	return nil
}

// IsFull always returns false since Redis can grow
// Backpressure is handled at the application level
func (rq *RedisQueue) IsFull() bool {
	return false
}

// IsEmpty checks if the stream has messages
func (rq *RedisQueue) IsEmpty() bool {
	return rq.Size() == 0
}

// GetPendingCount returns the number of pending messages for this consumer group
func (rq *RedisQueue) GetPendingCount() int {
	pending, err := rq.client.XPending(context.Background(), rq.streamName, rq.groupName).Result()
	if err != nil {
		rq.logger.Printf("failed to get pending count: %v", err)
		return 0
	}

	return int(pending.Count)
}

// GetDLQSize returns the size of the dead letter queue
func (rq *RedisQueue) GetDLQSize() int {
	dlqName := rq.streamName + "_dlq"
	info, err := rq.client.XInfoStream(context.Background(), dlqName).Result()
	if err != nil {
		return 0
	}
	return int(info.Length)
}
