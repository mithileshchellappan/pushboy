package config

import (
	"os"
	"strconv"
)

type Config struct {
	ServerPort string

	// Worker pool configuration
	WorkerCount   int // Number of master (fanout) goroutines
	SenderCount   int // Number of sender goroutines; sends are synchronous, so this caps in-flight requests
	JobQueueSize  int // Size of the job queue buffer
	TaskQueueSize int // Size of the task queue buffer between fanout and senders
	DLQQueueSize  int // Size of the outcome queue buffer between senders and the outcome worker
	BatchSize     int // Number of tokens to fetch per batch from DB

	// Outcome persistence batching
	OutcomeBatchSize    int // Outcomes per DB flush
	OutcomeFlushSeconds int // Max seconds between DB flushes
	ShutdownTimeoutSecs int // Max seconds to drain in-memory work during shutdown

	// Live Activity worker pool configuration (separate lane from push notifications)
	LAWorkerCount   int // Number of LA master goroutines
	LASenderCount   int // Number of LA sender goroutines
	LAJobQueueSize  int // Size of the LA job queue buffer
	LATaskQueueSize int // Size of the LA task queue buffer between fanout and senders
	LADLQQueueSize  int // Size of the LA outcome queue buffer

	//Retry
	MaxRetryNotification int
	//TODO: LA Add separate retry count for LA

	DatabaseURL string

	APNSKeyID           string
	APNSTeamID          string
	APNSBundleID        string
	APNSKeyPath         string // Path to APNS key file (e.g., keys/AuthKey_XXX.p8)
	APNSUseSandbox      bool
	APNSEndpoint        string // test override; empty = real APNs
	APNSClientPool      int    // HTTP clients in the push APNs pool (~1000 streams each)
	APNSMaxConcurrent   int    // cap on in-flight push APNs requests; 0 = pool * 900
	LAAPNSClientPool    int    // HTTP clients in the Live Activity APNs pool
	LAAPNSMaxConcurrent int    // cap on in-flight Live Activity APNs requests; 0 = pool * 900

	FCMProjectID       string
	FCMServiceAccount  string
	FCMKeyPath         string
	FCMClientPool      int // HTTP clients in the push FCM pool (~100 streams each)
	FCMMaxConcurrent   int // cap on in-flight push FCM requests
	LAFCMClientPool    int // HTTP clients in the Live Activity FCM pool
	LAFCMMaxConcurrent int // cap on in-flight Live Activity FCM requests

	// Broadcast topic configuration
	BroadcastTopicName string // Name of the broadcast topic (all users auto-subscribe)
}

func Load() *Config {
	laJobQueueDefault := 1000
	laTaskQueueDefault := 5000
	laDLQQueueDefault := 50000
	if legacyQueueSize, ok := getOptionalNonNegativeIntEnv("LA_QUEUE_SIZE"); ok {
		laJobQueueDefault = legacyQueueSize
		laTaskQueueDefault = legacyQueueSize
		laDLQQueueDefault = legacyQueueSize
	}

	apnsClientPool := getPositiveIntEnv("APNS_CLIENT_POOL", 8)
	apnsMaxConcurrent := getNonNegativeIntEnv("APNS_MAX_CONCURRENT", 0)
	fcmClientPool := getPositiveIntEnv("FCM_CLIENT_POOL", 4)
	fcmMaxConcurrent := getNonNegativeIntEnv("FCM_MAX_CONCURRENT", 360)

	return &Config{
		ServerPort:           getEnv("SERVER_PORT", ":8080"),
		WorkerCount:          getIntEnv("WORKER_COUNT", 10),
		SenderCount:          getIntEnv("SENDER_COUNT", 2000),
		JobQueueSize:         getIntEnv("JOB_QUEUE_SIZE", 1000),
		TaskQueueSize:        getNonNegativeIntEnv("TASK_QUEUE_SIZE", 10000),
		DLQQueueSize:         getNonNegativeIntEnv("DLQ_QUEUE_SIZE", 50000),
		BatchSize:            getIntEnv("BATCH_SIZE", 5000),
		OutcomeBatchSize:     getPositiveIntEnv("OUTCOME_BATCH_SIZE", 1000),
		OutcomeFlushSeconds:  getPositiveIntEnv("OUTCOME_FLUSH_SECONDS", 10),
		ShutdownTimeoutSecs:  getPositiveIntEnv("SHUTDOWN_TIMEOUT_SECONDS", 120),
		LAWorkerCount:        getIntEnv("LA_WORKER_COUNT", 5),
		LASenderCount:        getIntEnv("LA_SENDER_COUNT", 300),
		LAJobQueueSize:       getNonNegativeIntEnv("LA_JOB_QUEUE_SIZE", laJobQueueDefault),
		LATaskQueueSize:      getNonNegativeIntEnv("LA_TASK_QUEUE_SIZE", laTaskQueueDefault),
		LADLQQueueSize:       getNonNegativeIntEnv("LA_DLQ_QUEUE_SIZE", laDLQQueueDefault),
		MaxRetryNotification: getNonNegativeIntEnv("MAX_RETRY_NOTIFICATION", 3),
		DatabaseURL:          getEnv("DATABASE_URL", "postgres://localhost:5432/pushboy?sslmode=disable"),
		APNSKeyID:            getEnv("APNS_KEY_ID", ""),
		APNSTeamID:           getEnv("APNS_TEAM_ID", ""),
		APNSBundleID:         getEnv("APNS_BUNDLE_ID", getEnv("APNS_TOPIC_ID", "")),
		APNSKeyPath:          getEnv("APNS_KEY_PATH", ""),
		APNSUseSandbox:       getBoolEnv("APNS_USE_SANDBOX", false),
		APNSEndpoint:         getEnv("APNS_ENDPOINT", ""),
		APNSClientPool:       apnsClientPool,
		APNSMaxConcurrent:    apnsMaxConcurrent,
		LAAPNSClientPool:     getPositiveIntEnv("LA_APNS_CLIENT_POOL", apnsClientPool),
		LAAPNSMaxConcurrent:  getNonNegativeIntEnv("LA_APNS_MAX_CONCURRENT", apnsMaxConcurrent),
		FCMProjectID:         getEnv("FCM_PROJECT_ID", ""),
		FCMServiceAccount:    getEnv("FCM_SERVICE_ACCOUNT", ""),
		FCMKeyPath:           getEnv("FCM_KEY_PATH", "keys/service-account.json"),
		FCMClientPool:        fcmClientPool,
		FCMMaxConcurrent:     fcmMaxConcurrent,
		LAFCMClientPool:      getPositiveIntEnv("LA_FCM_CLIENT_POOL", fcmClientPool),
		LAFCMMaxConcurrent:   getNonNegativeIntEnv("LA_FCM_MAX_CONCURRENT", fcmMaxConcurrent),
		BroadcastTopicName:   getEnv("BROADCAST_TOPIC_NAME", "broadcast"),
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

func getPositiveIntEnv(key string, defaultVal int) int {
	value := getIntEnv(key, defaultVal)
	if value < 1 {
		return defaultVal
	}
	return value
}

func getNonNegativeIntEnv(key string, defaultVal int) int {
	value := getIntEnv(key, defaultVal)
	if value < 0 {
		return defaultVal
	}
	return value
}

func getOptionalNonNegativeIntEnv(key string) (int, bool) {
	valueStr, ok := os.LookupEnv(key)
	if !ok {
		return 0, false
	}
	value, err := strconv.Atoi(valueStr)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func getBoolEnv(key string, defaultVal bool) bool {
	valueStr := getEnv(key, "")
	if value, err := strconv.ParseBool(valueStr); err == nil {
		return value
	}
	return defaultVal
}
