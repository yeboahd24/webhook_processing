package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all configuration for the application
type Config struct {
	Port           string
	WorkerCount    int
	QueueSize      int
	MaxRetries     int
	Timeout        time.Duration
	RedisAddr      string
	RedisPassword  string
	RedisDB        int
	QueueName      string
	IdempotencyTTL time.Duration
}

// Load loads configuration from environment variables with defaults
func Load() Config {
	return Config{
		Port:           getEnv("PORT", "8080"),
		WorkerCount:    getEnvInt("WORKER_COUNT", 10),
		QueueSize:      getEnvInt("QUEUE_SIZE", 1000), // This is now local backpressure limit
		MaxRetries:     getEnvInt("MAX_RETRIES", 3),
		Timeout:        getEnvDuration("TIMEOUT", 30*time.Second),
		RedisAddr:      getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:  getEnv("REDIS_PASSWORD", ""),
		RedisDB:        getEnvInt("REDIS_DB", 0),
		QueueName:      getEnv("QUEUE_NAME", "webhook_jobs"),
		IdempotencyTTL: getEnvDuration("IDEMPOTENCY_TTL", 24*time.Hour),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}
