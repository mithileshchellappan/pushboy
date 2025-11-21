package config

import (
	"os"
	"strconv"
)

type Config struct {
	ServerPort string

	WorkerCount int

	DatabaseDriver string
	DatabaseURL    string

	APNSKeyID   string
	APNSTeamID  string
	APNSTopicID string

	FCMProjectID      string
	FCMServiceAccount string
}

func Load() *Config {
	return &Config{
		ServerPort:        getEnv("SERVER_PORT", "8080"),
		WorkerCount:       getIntEnv("WORKER_COUNT", 5),
		DatabaseDriver:    getEnv("DATABASE_DRIVER", "sqlite"),
		DatabaseURL:       getEnv("DATABASE_URL", "./pushboy.db"),
		APNSKeyID:         getEnv("APNS_KEY_ID", ""),
		APNSTeamID:        getEnv("APNS_TEAM_ID", ""),
		APNSTopicID:       getEnv("APNS_TOPIC_ID", ""),
		FCMProjectID:      getEnv("FCM_PROJECT_ID", ""),
		FCMServiceAccount: getEnv("FCM_SERVICE_ACCOUNT", ""),
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
