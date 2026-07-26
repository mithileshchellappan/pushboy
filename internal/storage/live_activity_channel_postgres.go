package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
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

func (s *PostgresStore) EnsureLAChannel(
	ctx context.Context,
	activityID string,
	topicID string,
	create func(context.Context) (string, error),
) (*LiveActivityChannel, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("error starting live activity channel transaction: %w", err)
	}
	defer tx.Rollback()

	if err := lockLAActivity(ctx, tx, activityID); err != nil {
		return nil, false, fmt.Errorf("error locking live activity channel: %w", err)
	}

	var jobTopicID sql.NullString
	err = tx.QueryRowContext(
		ctx,
		`SELECT topic_id
		 FROM live_activity_jobs
		 WHERE activity_id = $1`,
		activityID,
	).Scan(&jobTopicID)
	if err == nil && (!jobTopicID.Valid || jobTopicID.String != topicID) {
		return nil, false, fmt.Errorf(
			"live activity job topic conflicts with channel topic: %w",
			Errors.Conflict,
		)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("error getting live activity job for channel: %w", err)
	}

	var existing LiveActivityChannel
	err = scanLAChannel(
		tx.QueryRowContext(
			ctx,
			`SELECT `+laChannelSelectColumns+`
			 FROM live_activity_channels
			 WHERE activity_id = $1`,
			activityID,
		),
		&existing,
	)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("error committing existing live activity channel: %w", err)
		}
		return &existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("error getting live activity channel: %w", err)
	}

	var topicExists int
	if err := tx.QueryRowContext(
		ctx,
		`SELECT 1
		 FROM topics
		 WHERE id = $1
		 FOR KEY SHARE`,
		topicID,
	).Scan(&topicExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, Errors.NotFound
		}
		return nil, false, fmt.Errorf("error locking live activity channel topic: %w", err)
	}

	channelID, err := create(ctx)
	if err != nil {
		return nil, false, err
	}
	if channelID == "" {
		return nil, false, errors.New("live activity channel creator returned an empty channel ID")
	}

	channel := &LiveActivityChannel{
		ActivityID: activityID,
		TopicID:    topicID,
		ChannelID:  channelID,
		CreatedAt:  time.Now().UTC(),
	}
	if err := scanLAChannel(
		tx.QueryRowContext(
			ctx,
			`INSERT INTO live_activity_channels(activity_id, topic_id, channel_id, created_at)
			 VALUES($1, $2, $3, $4)
			 RETURNING `+laChannelSelectColumns,
			channel.ActivityID,
			channel.TopicID,
			channel.ChannelID,
			channel.CreatedAt,
		),
		channel,
	); err != nil {
		return nil, false, fmt.Errorf("error creating live activity channel: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("error committing live activity channel: %w", err)
	}
	return channel, true, nil
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
