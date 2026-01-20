# Webhook Processor

A production-grade webhook processing service built in Go that solves the real-world problem of handling high-volume webhook events from external providers (payment gateways, messaging platforms, SaaS integrations).

## Evolution: From In-Memory to Redis-Backed Durable Queuing

This service uses Redis for durable job queuing while preserving all existing worker pool guarantees. This incremental evolution ensures production safety and maintains the core requirements of fast HTTP responses, bounded concurrency, and backpressure protection.

## Features

- **Durable Redis Queuing**: Jobs survive restarts and crashes using Redis Streams
- **Bounded Worker Pool**: Fixed number of workers prevents CPU spikes and OOM
- **Backpressure via Local Limits**: Application-level limits protect against Redis unbounded growth
- **Graceful Shutdown**: No job loss, no goroutine leaks
- **Exponential Backoff Retry**: For transient failures with Redis-based tracking
- **Redis Idempotency**: Prevent duplicate processing with TTL-based keys
- **Panic Recovery**: Workers recover from panics and continue
- **Structured Logging**: Production-ready logging
- **Health Checks**: Comprehensive health endpoints including Redis connectivity
- **Configurable**: Environment-based configuration with reasonable defaults

## Architecture

```
webhook-processor/
├── cmd/server/main.go           # Application entry point
├── internal/
│   ├── config/                 # Configuration management
│   │   └── config.go
│   ├── http/                   # HTTP handlers and middleware
│   │   └── handlers.go
│   ├── idempotency/            # Redis-based idempotency service
│   │   └── service.go
│   ├── queue/                  # Redis-backed durable queue
│   │   ├── job.go             # Job data structures
│   │   └── redis_queue.go     # Redis Streams implementation
│   ├── worker/                 # Worker pool implementation
│   │   └── pool.go
│   ├── service/                # Business logic
│   │   └── webhook.go
│   └── shutdown/               # Graceful shutdown handling
│       └── handler.go
```
```

## Quick Start

### 1. Install Dependencies

```bash
go mod tidy
```

### 2. Run the Service

```bash
# Use default configuration
go run cmd/server/main.go

# Or with custom configuration
PORT=8080 WORKER_COUNT=20 QUEUE_SIZE=500 go run cmd/server/main.go
```

### 3. Send a Webhook

```bash
curl -X POST http://localhost:8080/webhooks \
  -H "Content-Type: application/json" \
  -H "X-Event-Type: payment.succeeded" \
  -d '{
    "id": "pay_1234567890",
    "amount": 1000,
    "currency": "USD",
    "status": "succeeded",
    "customer_id": "cus_1234567890"
  }'
```

### 4. Check Health

```bash
curl http://localhost:8080/health
```

## Configuration

| Environment Variable | Default | Description |
|----------------------|---------|-------------|
| `PORT` | `8080` | HTTP server port |
| `WORKER_COUNT` | `10` | Number of worker goroutines |
| `QUEUE_SIZE` | `1000` | Local backpressure limit (not Redis queue size) |
| `MAX_RETRIES` | `3` | Maximum retry attempts |
| `TIMEOUT` | `30s` | Job processing timeout |
| `REDIS_ADDR` | `localhost:6379` | Redis server address |
| `REDIS_PASSWORD` | `""` | Redis password |
| `REDIS_DB` | `0` | Redis database number |
| `QUEUE_NAME` | `webhook_jobs` | Redis stream name for jobs |
| `IDEMPOTENCY_TTL` | `24h` | TTL for idempotency keys |

## API Endpoints

### Webhook Endpoint

**POST** `/webhooks`

Accepts webhook events and queues them for processing.

**Headers:**
- `Content-Type: application/json`
- `X-Event-Type: <event-type>` (optional, can be inferred from payload)

**Body:**
```json
{
  "id": "unique_event_id",
  "amount": 1000,
  "currency": "USD",
  "status": "succeeded",
  "customer_id": "customer_123"
}
```

**Response:** `202 Accepted`
```json
{
  "message": "Webhook accepted for processing",
  "id": "job_id_here",
  "event": "payment.succeeded",
  "status": "queued"
}
```

### Health Endpoint

**GET** `/health`

Returns service health status including worker, Redis queue, and DLQ metrics.

**Response:**
```json
{
  "status": "healthy",
  "queue_size": 5,
  "pending": 2,
  "dlq_size": 0,
  "workers": 10,
  "is_stopped": false
}
```

## Supported Event Types

The service automatically detects and processes the following event types:

### Payment Events
- `payment.succeeded`
- `payment.failed`

### User Events
- `user.created`
- `user.updated`

### Message Events
- `message.sent`
- `message.delivered`

### Generic Events
Any unknown event type is processed as a generic webhook.

## Why Redis Was Introduced

### The Problem with In-Memory Queues
- **Job Loss on Crashes**: In-memory queues lose all jobs when the service restarts
- **No Persistence**: Jobs disappear during deployments or system failures
- **Single Instance Limitation**: Cannot distribute work across multiple instances
- **Memory Pressure**: Large queues consume RAM that could be used for processing

### Redis Provides Durability
- **Crash Recovery**: Jobs survive service restarts and crashes
- **Persistence**: Jobs are stored in Redis until processed or moved to DLQ
- **Horizontal Scaling**: Multiple instances can share the same Redis queue
- **Operational Visibility**: Queue state is inspectable via Redis CLI/tools

### Worker Pool Guarantees Preserved
The evolution maintains all existing worker pool guarantees:

#### 1. Prevents CPU Spikes and OOM
- **Unchanged**: Fixed number of workers limits concurrent processing
- **Benefit**: Predictable resource usage and stable performance

#### 2. Provides Backpressure
- **Evolution**: Local `QUEUE_SIZE` limits in-flight jobs per instance
- **Why Redis Alone Insufficient**: Redis can grow unbounded, so we enforce local limits
- **Benefit**: System rejects requests when overloaded instead of crashing

#### 3. Avoids Goroutine Leaks
- **Unchanged**: Managed worker pool with proper lifecycle
- **Benefit**: Clean shutdown and resource cleanup

#### 4. Enables Retry Logic
- **Enhanced**: Redis Streams track pending messages and retry state
- **Benefit**: Improved reliability with durable retry tracking

## Failure Scenarios

### 1. Redis Unavailable
- **Symptoms**: `503 Service Unavailable` responses, health check fails
- **Cause**: Redis connection lost, network issues, or Redis down
- **Solution**: Check Redis connectivity, restart Redis, or failover to replica
- **Impact**: New webhooks rejected, existing jobs continue processing from memory

### 2. Local Queue Full (Backpressure)
- **Symptoms**: `503 Service Unavailable` responses
- **Cause**: Too many in-flight jobs per instance (local QUEUE_SIZE limit)
- **Solution**: Scale horizontally (add more instances) or increase QUEUE_SIZE
- **Why This Design**: Redis alone can't provide backpressure per instance

### 3. Worker Exhaustion
- **Symptoms**: Increased processing times, degraded health
- **Cause**: All workers busy processing long-running jobs
- **Solution**: Optimize job processing or add more workers

### 4. Duplicate Webhooks
- **Symptoms**: Idempotency check returns 202 for duplicate
- **Cause**: Same webhook sent multiple times within TTL window
- **Solution**: Expected behavior - client should handle 202 as success
- **TTL Cleanup**: Idempotency keys auto-expire after IDEMPOTENCY_TTL

### 5. Service Crashes/Restarts
- **Symptoms**: Temporary unavailability during restart
- **Cause**: Deployment, crashes, or manual restart
- **Solution**: Jobs persist in Redis, workers resume processing on startup
- **No Data Loss**: Redis durability ensures jobs aren't lost

### 6. Permanent Job Failures
- **Symptoms**: Jobs moved to DLQ after MAX_RETRIES attempts
- **Cause**: Invalid payloads or permanent service issues
- **Solution**: Monitor DLQ size, inspect failed jobs, fix root causes

## Backpressure Protection

The system implements multiple layers of backpressure, evolved for Redis:

1. **HTTP Level**: Returns `503` when Redis unavailable or local limits hit
2. **Local Instance Level**: `QUEUE_SIZE` limits in-flight jobs per instance
3. **Redis Level**: Streams can grow but local limits prevent unbounded growth
4. **Worker Level**: Fixed concurrency prevents CPU overload
5. **Retry Level**: Exponential backoff prevents thundering herd

### Why Redis Alone is Insufficient for Backpressure

Redis Streams and Lists don't provide per-instance backpressure:
- **Unbounded Growth**: Redis can accept unlimited jobs
- **Memory Exhaustion**: Large queues consume Redis memory
- **No Instance Awareness**: Redis doesn't know about instance capacity
- **Solution**: Local `QUEUE_SIZE` provides instance-level protection

## Operational Features

### Health Monitoring
- **Liveness**: Service is running and responsive
- **Readiness**: Service is ready to accept requests
- **Metrics**: Queue size, utilization, worker status

### Logging
- **Structured Logging**: Consistent log format for parsing
- **Context Information**: Job IDs, event types, processing times
- **Error Tracking**: Detailed error context for debugging

### Metrics
- **Queue Metrics**: Size, capacity, utilization
- **Worker Metrics**: Active workers, processing rates
- **Job Metrics**: Success/failure rates, retry counts

## How This Design Avoids OOM and Goroutine Leaks

### Memory Safety
1. **Bounded Workers**: Fixed `WORKER_COUNT` prevents unlimited goroutines
2. **Local Backpressure**: `QUEUE_SIZE` limits in-flight jobs per instance
3. **No Dynamic Goroutines**: All goroutines are pre-allocated and managed
4. **Redis Streams**: Efficient storage without loading all jobs into memory

### Goroutine Leak Prevention
1. **Managed Worker Pool**: Workers are created once and reused
2. **Graceful Shutdown**: All goroutines are waited for during shutdown
3. **No Fire-and-Forget**: Every goroutine has a defined lifecycle
4. **Panic Recovery**: Workers recover from panics and continue running

### Redis Memory Management
1. **Stream Trimming**: Configure Redis to trim old processed streams
2. **DLQ Monitoring**: Failed jobs are moved to DLQ to prevent accumulation
3. **TTL on Idempotency**: Keys auto-expire to prevent unbounded growth
4. **Connection Pooling**: Redis client reuses connections efficiently

## Production Considerations

### 1. Persistence
- **Current**: Redis-backed durable queue with crash recovery
- **Production**: Configure Redis persistence (RDB/AOF) for data safety

### 2. Monitoring
- **Current**: Health checks with queue and DLQ metrics
- **Production**: Add Prometheus metrics, distributed tracing, Redis monitoring

### 3. Security
- **Current**: Basic CORS headers
- **Production**: Add webhook signature verification, rate limiting, Redis AUTH

### 4. Scalability
- **Current**: Horizontal scaling ready (shared Redis queue)
- **Production**: Load balancer, Redis cluster, read replicas for monitoring

## Development

### Running Tests
```bash
go test ./...
```

### Building
```bash
go build -o webhook-processor cmd/server/main.go
```

### Docker
```bash
# Build the image
docker build -t webhook-processor .

# Run with Docker Compose (includes Redis)
docker-compose up --build

# Or run manually with external Redis
docker run -p 8080:8080 -e REDIS_ADDR=host.docker.internal:6379 webhook-processor
```

### Key Architecture Decisions

- **Redis Streams over Lists**: Provides consumer groups, pending message tracking, and better failure handling
- **Local Backpressure**: QUEUE_SIZE prevents Redis unbounded growth while allowing horizontal scaling
- **HTTP-Level Idempotency**: Prevents duplicate jobs before they enter the queue
- **Panic Recovery**: Workers continue running after panics, maintaining fixed concurrency
- **Graceful Shutdown**: No job loss, clean Redis connection closure


