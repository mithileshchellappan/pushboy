package storage

import (
	"database/sql"
	"time"
)

func requiredTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

func optionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	t := value.UTC()
	return t
}

func cursorTimeArg(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}
