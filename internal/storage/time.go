package storage

import (
	"fmt"
	"time"
)

type storageTimeScanner struct {
	dest *string
}

func scanStorageTime(dest *string) storageTimeScanner {
	return storageTimeScanner{dest: dest}
}

func (s storageTimeScanner) Scan(value any) error {
	if s.dest == nil {
		return fmt.Errorf("nil timestamp destination")
	}

	switch v := value.(type) {
	case nil:
		*s.dest = ""
		return nil
	case time.Time:
		*s.dest = formatStorageTime(v)
		return nil
	case string:
		return scanStorageTimeString(s.dest, v)
	case []byte:
		return scanStorageTimeString(s.dest, string(v))
	default:
		return fmt.Errorf("cannot scan timestamp value of type %T", value)
	}
}

func formatStorageTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func scanStorageTimeString(dest *string, value string) error {
	if value == "" {
		*dest = ""
		return nil
	}

	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return err
	}
	*dest = formatStorageTime(t)
	return nil
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
