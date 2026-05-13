package config

import (
	"os"
	"testing"
)

func isolateEnv(t *testing.T) {
	t.Helper()

	previous := os.Environ()
	os.Clearenv()
	t.Cleanup(func() {
		os.Clearenv()
		for _, entry := range previous {
			key, value, ok := splitEnv(entry)
			if ok {
				_ = os.Setenv(key, value)
			}
		}
	})
}

func splitEnv(entry string) (string, string, bool) {
	for i := range entry {
		if entry[i] == '=' {
			return entry[:i], entry[i+1:], true
		}
	}
	return "", "", false
}

func TestLoadDefaults(t *testing.T) {
	isolateEnv(t)

	cfg := Load()

	if cfg.ServerPort != ":8080" {
		t.Fatalf("ServerPort = %q, want :8080", cfg.ServerPort)
	}
	if cfg.WorkerCount != 10 || cfg.SenderCount != 200 || cfg.JobQueueSize != 1000 || cfg.BatchSize != 5000 {
		t.Fatalf("worker defaults = %d/%d/%d/%d", cfg.WorkerCount, cfg.SenderCount, cfg.JobQueueSize, cfg.BatchSize)
	}
	if cfg.MaxRetryNotification != 3 {
		t.Fatalf("MaxRetryNotification = %d, want 3", cfg.MaxRetryNotification)
	}
	if cfg.DatabaseURL != "postgres://localhost:5432/pushboy?sslmode=disable" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.APNSUseSandbox {
		t.Fatalf("APNSUseSandbox = true, want false")
	}
	if cfg.FCMKeyPath != "keys/service-account.json" {
		t.Fatalf("FCMKeyPath = %q", cfg.FCMKeyPath)
	}
	if cfg.BroadcastTopicName != "broadcast" {
		t.Fatalf("BroadcastTopicName = %q", cfg.BroadcastTopicName)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	isolateEnv(t)
	setEnv(t, map[string]string{
		"SERVER_PORT":            ":9090",
		"WORKER_COUNT":           "2",
		"SENDER_COUNT":           "3",
		"JOB_QUEUE_SIZE":         "4",
		"BATCH_SIZE":             "5",
		"MAX_RETRY_NOTIFICATION": "6",
		"DATABASE_URL":           "postgres://example/pushboy",
		"APNS_KEY_ID":            "kid",
		"APNS_TEAM_ID":           "team",
		"APNS_BUNDLE_ID":         "bundle",
		"APNS_KEY_PATH":          "keys/apns.p8",
		"APNS_USE_SANDBOX":       "true",
		"FCM_PROJECT_ID":         "project",
		"FCM_SERVICE_ACCOUNT":    "service-account-json",
		"FCM_KEY_PATH":           "keys/fcm.json",
		"BROADCAST_TOPIC_NAME":   "everyone",
	})

	cfg := Load()

	if cfg.ServerPort != ":9090" ||
		cfg.WorkerCount != 2 ||
		cfg.SenderCount != 3 ||
		cfg.JobQueueSize != 4 ||
		cfg.BatchSize != 5 ||
		cfg.MaxRetryNotification != 6 ||
		cfg.DatabaseURL != "postgres://example/pushboy" ||
		cfg.APNSKeyID != "kid" ||
		cfg.APNSTeamID != "team" ||
		cfg.APNSBundleID != "bundle" ||
		cfg.APNSKeyPath != "keys/apns.p8" ||
		!cfg.APNSUseSandbox ||
		cfg.FCMProjectID != "project" ||
		cfg.FCMServiceAccount != "service-account-json" ||
		cfg.FCMKeyPath != "keys/fcm.json" ||
		cfg.BroadcastTopicName != "everyone" {
		t.Fatalf("Load() did not apply expected env overrides: %+v", cfg)
	}
}

func TestLoadInvalidEnvFallsBack(t *testing.T) {
	isolateEnv(t)
	setEnv(t, map[string]string{
		"WORKER_COUNT":     "not-an-int",
		"SENDER_COUNT":     "",
		"JOB_QUEUE_SIZE":   "1.5",
		"BATCH_SIZE":       " ",
		"APNS_USE_SANDBOX": "not-a-bool",
	})

	cfg := Load()

	if cfg.WorkerCount != 10 || cfg.SenderCount != 200 || cfg.JobQueueSize != 1000 || cfg.BatchSize != 5000 {
		t.Fatalf("invalid integer envs should fall back, got %d/%d/%d/%d", cfg.WorkerCount, cfg.SenderCount, cfg.JobQueueSize, cfg.BatchSize)
	}
	if cfg.APNSUseSandbox {
		t.Fatalf("invalid bool env should fall back to false")
	}
}

func TestAPNSBundleIDPrecedence(t *testing.T) {
	t.Run("bundle id wins over topic id", func(t *testing.T) {
		isolateEnv(t)
		setEnv(t, map[string]string{
			"APNS_BUNDLE_ID": "bundle",
			"APNS_TOPIC_ID":  "legacy-topic",
		})

		if got := Load().APNSBundleID; got != "bundle" {
			t.Fatalf("APNSBundleID = %q, want bundle", got)
		}
	})

	t.Run("topic id is fallback", func(t *testing.T) {
		isolateEnv(t)
		setEnv(t, map[string]string{
			"APNS_TOPIC_ID": "legacy-topic",
		})

		if got := Load().APNSBundleID; got != "legacy-topic" {
			t.Fatalf("APNSBundleID = %q, want legacy-topic", got)
		}
	})

	t.Run("empty bundle id is explicit override", func(t *testing.T) {
		isolateEnv(t)
		setEnv(t, map[string]string{
			"APNS_BUNDLE_ID": "",
			"APNS_TOPIC_ID":  "legacy-topic",
		})

		if got := Load().APNSBundleID; got != "" {
			t.Fatalf("APNSBundleID = %q, want empty explicit override", got)
		}
	})
}

func setEnv(t *testing.T, values map[string]string) {
	t.Helper()
	for key, value := range values {
		if err := os.Setenv(key, value); err != nil {
			t.Fatalf("set env %s: %v", key, err)
		}
	}
}
