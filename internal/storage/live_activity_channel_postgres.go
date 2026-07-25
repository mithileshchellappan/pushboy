package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const laChannelSelectColumns = `
	activity_id,
	topic_id,
	channel_id,
	created_at`

func scanLAChannel(scanner interface{ Scan(dest ...any) error }, channel *LiveActivityChannel) error {
	if err := scanner.Scan(
		&channel.ActivityID,
		&channel.TopicID,
		&channel.ChannelID,
		&channel.CreatedAt,
	); err != nil {
		return err
	}
	channel.CreatedAt = channel.CreatedAt.UTC()
	return nil
}

func (s *PostgresStore) CreateOrGetLAChannel(
	ctx context.Context,
	channel *LiveActivityChannel,
) (*LiveActivityChannel, bool, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO live_activity_channels(activity_id, topic_id, channel_id, created_at)
		VALUES($1, $2, $3, $4)
		ON CONFLICT (activity_id) DO NOTHING`,
		channel.ActivityID,
		channel.TopicID,
		channel.ChannelID,
		channel.CreatedAt,
	)
	if err != nil {
		return nil, false, fmt.Errorf("error creating live activity channel: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 1 {
		return channel, true, nil
	}

	existing, err := s.GetLAChannelByActivityID(ctx, channel.ActivityID)
	if err != nil {
		return nil, false, err
	}
	return existing, false, nil
}

func (s *PostgresStore) GetLAChannelByActivityID(
	ctx context.Context,
	activityID string,
) (*LiveActivityChannel, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+laChannelSelectColumns+`
		FROM live_activity_channels
		WHERE activity_id = $1`,
		activityID,
	)
	var channel LiveActivityChannel
	if err := scanLAChannel(row, &channel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error getting live activity channel: %w", err)
	}
	return &channel, nil
}

func (s *PostgresStore) DeleteLAChannel(
	ctx context.Context,
	activityID string,
	channelID string,
) error {
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM live_activity_channels
		WHERE activity_id = $1
		  AND channel_id = $2`,
		activityID,
		channelID,
	)
	if err != nil {
		return fmt.Errorf("error deleting live activity channel: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return Errors.NotFound
	}
	return nil
}
