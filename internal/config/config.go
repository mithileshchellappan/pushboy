package config

import (
	"os"
	"strconv"
)

type Config struct {
	ServerPort string

	// Worker pool configuration
	WorkerCount  int // Number of worker goroutines
	SenderCount  int // Number of sender goroutines per worker
	JobQueueSize int // Size of the job queue buffer

	DatabaseDriver string
	DatabaseURL    string

	APNSKeyID   string
	APNSTeamID  string
	APNSTopicID string
	APNSKeyPath string // Path to APNS key file (e.g., keys/AuthKey_XXX.p8)

	FCMProjectID      string
	FCMServiceAccount string
	FCMKeyPath        string // Path to FCM service account JSON file

	// Broadcast topic configuration
	BroadcastTopicName string // Name of the broadcast topic (all users auto-subscribe)
}

func Load() *Config {
	return &Config{
		ServerPort:         getEnv("SERVER_PORT", ":8080"),
		WorkerCount:        getIntEnv("WORKER_COUNT", 10),
		SenderCount:        getIntEnv("SENDER_COUNT", 200),
		JobQueueSize:       getIntEnv("JOB_QUEUE_SIZE", 1000),
		DatabaseDriver:     getEnv("DATABASE_DRIVER", "postgres"),
		DatabaseURL:        getEnv("DATABASE_URL", "./pushboy.db"),
		APNSKeyID:          getEnv("APNS_KEY_ID", ""),
		APNSTeamID:         getEnv("APNS_TEAM_ID", ""),
		APNSTopicID:        getEnv("APNS_TOPIC_ID", ""),
		APNSKeyPath:        getEnv("APNS_KEY_PATH", ""),
		FCMProjectID:       getEnv("FCM_PROJECT_ID", ""),
		FCMServiceAccount:  getEnv("FCM_SERVICE_ACCOUNT", ""),
		FCMKeyPath:         getEnv("FCM_KEY_PATH", "keys/service-account.json"),
		BroadcastTopicName: getEnv("BROADCAST_TOPIC_NAME", "broadcast"),
	}
}

func getEnv(key, defaultVal string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultVal
}

func getIntEnv(key string, defaultVal int) int {
	valueStr := getEnv(key, "")
	if value, err := strconv.Atoi(valueStr); err == nil {
		return value
	}
	return defaultVal
}
