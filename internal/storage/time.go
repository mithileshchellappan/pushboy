package storage

import "time"

func formatStorageTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseRequiredStorageTime(value string) (time.Time, error) {
	if value == "" {
		return time.Now().UTC(), nil
	}

	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func parseOptionalStorageTime(value string) (any, error) {
	if value == "" {
		return nil, nil
	}

	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	return t.UTC(), nil
}
