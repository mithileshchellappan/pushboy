package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (s *PostgresStore) CreateLARegistration(ctx context.Context, registration *LARegistration) (*LARegistration, error) {
	query := `
		INSERT INTO la_registrations(id, user_id, platform, start_token, auto_start_enabled, enabled)
		VALUES($1, $2, $3, $4, $5, $6)`
	_, err := s.db.ExecContext(ctx, query,
		registration.ID,
		registration.UserID,
		registration.Platform,
		registration.StartToken,
		registration.AutoStartEnabled,
		registration.Enabled,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error creating LA registration: %w", err)
	}

	return s.GetLARegistration(ctx, registration.ID)
}

func (s *PostgresStore) UpdateLARegistration(ctx context.Context, registration *LARegistration) (*LARegistration, error) {
	query := `
		UPDATE la_registrations
		SET platform = $2,
			start_token = $3,
			auto_start_enabled = $4,
			enabled = $5,
			updated_at = NOW()
		WHERE id = $1`
	result, err := s.db.ExecContext(ctx, query,
		registration.ID,
		registration.Platform,
		registration.StartToken,
		registration.AutoStartEnabled,
		registration.Enabled,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error updating LA registration: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, Errors.NotFound
	}

	return s.GetLARegistration(ctx, registration.ID)
}

func (s *PostgresStore) GetLARegistration(ctx context.Context, laRegistrationID string) (*LARegistration, error) {
	query := `
		SELECT id, user_id, platform, start_token, auto_start_enabled, enabled, created_at, updated_at
		FROM la_registrations
		WHERE id = $1`
	row := s.db.QueryRowContext(ctx, query, laRegistrationID)

	registration, err := scanLARegistration(row)
	if err != nil {
		if err == Errors.NotFound {
			return nil, err
		}
		return nil, fmt.Errorf("error getting LA registration: %w", err)
	}

	return registration, nil
}

func (s *PostgresStore) GetLARegistrationsByUserID(ctx context.Context, userID string) ([]LARegistration, error) {
	query := `
		SELECT id, user_id, platform, start_token, auto_start_enabled, enabled, created_at, updated_at
		FROM la_registrations
		WHERE user_id = $1
		ORDER BY id`
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("error listing LA registrations: %w", err)
	}
	defer rows.Close()

	var registrations []LARegistration
	for rows.Next() {
		registration, err := scanLARegistration(rows)
		if err != nil {
			return nil, fmt.Errorf("error scanning LA registration: %w", err)
		}
		registrations = append(registrations, *registration)
	}

	return registrations, rows.Err()
}

func (s *PostgresStore) GetEnabledLARegistrationsByUserID(ctx context.Context, userID string) ([]LARegistration, error) {
	query := `
		SELECT id, user_id, platform, start_token, auto_start_enabled, enabled, created_at, updated_at
		FROM la_registrations
		WHERE user_id = $1 AND enabled = TRUE
		ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("error listing enabled LA registrations: %w", err)
	}
	defer rows.Close()

	var registrations []LARegistration
	for rows.Next() {
		registration, err := scanLARegistration(rows)
		if err != nil {
			return nil, fmt.Errorf("error scanning enabled LA registration: %w", err)
		}
		registrations = append(registrations, *registration)
	}

	return registrations, rows.Err()
}

func (s *PostgresStore) GetEnabledLARegistrationsByTopicID(ctx context.Context, topicID string) ([]LARegistration, error) {
	query := `
		SELECT DISTINCT r.id, r.user_id, r.platform, r.start_token, r.auto_start_enabled, r.enabled, r.created_at, r.updated_at
		FROM la_registrations r
		INNER JOIN la_user_topic_preferences p ON p.user_id = r.user_id
		WHERE p.topic_id = $1 AND r.enabled = TRUE
		ORDER BY r.created_at ASC`
	rows, err := s.db.QueryContext(ctx, query, topicID)
	if err != nil {
		return nil, fmt.Errorf("error listing enabled LA registrations for topic: %w", err)
	}
	defer rows.Close()

	var registrations []LARegistration
	for rows.Next() {
		registration, err := scanLARegistration(rows)
		if err != nil {
			return nil, fmt.Errorf("error scanning LA registration for topic: %w", err)
		}
		registrations = append(registrations, *registration)
	}

	return registrations, rows.Err()
}

func (s *PostgresStore) DeleteLARegistration(ctx context.Context, laRegistrationID string) error {
	query := `DELETE FROM la_registrations WHERE id = $1`
	result, err := s.db.ExecContext(ctx, query, laRegistrationID)
	if err != nil {
		return fmt.Errorf("error deleting LA registration: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}

	return nil
}

func (s *PostgresStore) UpsertLAUserTopicPreference(ctx context.Context, preference *LAUserTopicPreference) (*LAUserTopicPreference, error) {
	query := `
		INSERT INTO la_user_topic_preferences(user_id, topic_id)
		VALUES($1, $2)
		ON CONFLICT (user_id, topic_id)
		DO UPDATE SET topic_id = EXCLUDED.topic_id`
	_, err := s.db.ExecContext(ctx, query, preference.UserID, preference.TopicID)
	if err != nil {
		return nil, fmt.Errorf("error upserting LA user topic preference: %w", err)
	}

	query = `
		SELECT user_id, topic_id, created_at
		FROM la_user_topic_preferences
		WHERE user_id = $1 AND topic_id = $2`
	row := s.db.QueryRowContext(ctx, query, preference.UserID, preference.TopicID)

	var createdAt time.Time
	if err := row.Scan(&preference.UserID, &preference.TopicID, &createdAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, fmt.Errorf("error reading LA user topic preference: %w", err)
	}

	preference.CreatedAt = formatTime(createdAt)
	return preference, nil
}

func (s *PostgresStore) DeleteLAUserTopicPreference(ctx context.Context, userID, topicID string) error {
	query := `DELETE FROM la_user_topic_preferences WHERE user_id = $1 AND topic_id = $2`
	result, err := s.db.ExecContext(ctx, query, userID, topicID)
	if err != nil {
		return fmt.Errorf("error deleting LA user topic preference: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}

	return nil
}

func (s *PostgresStore) GetLAUserTopicPreferences(ctx context.Context, userID string) ([]LAUserTopicPreference, error) {
	query := `
		SELECT user_id, topic_id, created_at
		FROM la_user_topic_preferences
		WHERE user_id = $1
		ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("error listing LA user topic preferences: %w", err)
	}
	defer rows.Close()

	var preferences []LAUserTopicPreference
	for rows.Next() {
		var preference LAUserTopicPreference
		var createdAt time.Time
		if err := rows.Scan(&preference.UserID, &preference.TopicID, &createdAt); err != nil {
			return nil, fmt.Errorf("error scanning LA user topic preference: %w", err)
		}
		preference.CreatedAt = formatTime(createdAt)
		preferences = append(preferences, preference)
	}

	return preferences, rows.Err()
}

func (s *PostgresStore) CreateLAActivity(ctx context.Context, activity *LAActivity) (*LAActivity, error) {
	startAttributes, err := marshalLAMap(activity.StartAttributes)
	if err != nil {
		return nil, err
	}
	currentState, err := marshalLAMap(activity.CurrentState)
	if err != nil {
		return nil, err
	}
	alert, err := marshalLAAlert(activity.Alert)
	if err != nil {
		return nil, err
	}

	query := `
		INSERT INTO la_activities(
			id, kind, audience_kind, user_id, topic_id, external_ref, status, pending_event,
			start_attributes, current_state, alert, state_version, claimed_version,
			claim_token, dispatch_needed, last_error
		)
		VALUES($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`
	_, err = s.db.ExecContext(ctx, query,
		activity.ID,
		activity.Kind,
		activity.AudienceKind,
		activity.UserID,
		activity.TopicID,
		activity.ExternalRef,
		activity.Status,
		activity.PendingEvent,
		startAttributes,
		currentState,
		alert,
		activity.StateVersion,
		activity.ClaimedVersion,
		activity.ClaimToken,
		activity.DispatchNeeded,
		activity.LastError,
	)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			return nil, Errors.AlreadyExists
		}
		return nil, fmt.Errorf("error creating LA activity: %w", err)
	}

	return s.GetLAActivity(ctx, activity.ID)
}

func (s *PostgresStore) GetLAActivity(ctx context.Context, laID string) (*LAActivity, error) {
	query := `
		SELECT id, kind, audience_kind, user_id, topic_id, external_ref, status, pending_event,
		       start_attributes, current_state, alert, state_version, claimed_version,
		       claim_token, claim_until, dispatch_needed, dispatch_after, last_dispatch_at,
		       last_error, created_at, updated_at
		FROM la_activities
		WHERE id = $1`
	row := s.db.QueryRowContext(ctx, query, laID)

	activity, err := scanLAActivity(row)
	if err != nil {
		if err == Errors.NotFound {
			return nil, err
		}
		return nil, fmt.Errorf("error getting LA activity: %w", err)
	}

	return activity, nil
}

func (s *PostgresStore) GetActiveLAActivitiesByTopicID(ctx context.Context, topicID string) ([]LAActivity, error) {
	query := `
		SELECT id, kind, audience_kind, user_id, topic_id, external_ref, status, pending_event,
		       start_attributes, current_state, alert, state_version, claimed_version,
		       claim_token, claim_until, dispatch_needed, dispatch_after, last_dispatch_at,
		       last_error, created_at, updated_at
		FROM la_activities
		WHERE topic_id = $1 AND status IN ('starting', 'active', 'ending')
		ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, query, topicID)
	if err != nil {
		return nil, fmt.Errorf("error listing active LA activities: %w", err)
	}
	defer rows.Close()

	var activities []LAActivity
	for rows.Next() {
		activity, err := scanLAActivity(rows)
		if err != nil {
			return nil, fmt.Errorf("error scanning active LA activity: %w", err)
		}
		activities = append(activities, *activity)
	}

	return activities, rows.Err()
}

func (s *PostgresStore) UpdateLAActivity(ctx context.Context, activity *LAActivity) (*LAActivity, error) {
	startAttributes, err := marshalLAMap(activity.StartAttributes)
	if err != nil {
		return nil, err
	}
	currentState, err := marshalLAMap(activity.CurrentState)
	if err != nil {
		return nil, err
	}
	alert, err := marshalLAAlert(activity.Alert)
	if err != nil {
		return nil, err
	}
	claimUntil, err := parseRFC3339ToNullableTime(activity.ClaimUntil)
	if err != nil {
		return nil, err
	}
	dispatchAfter, err := parseRFC3339ToNullableTime(activity.DispatchAfter)
	if err != nil {
		return nil, err
	}
	lastDispatchAt, err := parseRFC3339ToNullableTime(activity.LastDispatchAt)
	if err != nil {
		return nil, err
	}

	query := `
		UPDATE la_activities
		SET kind = $2,
			audience_kind = $3,
			user_id = NULLIF($4, ''),
			topic_id = NULLIF($5, ''),
			external_ref = NULLIF($6, ''),
			status = $7,
			pending_event = $8,
			start_attributes = $9,
			current_state = $10,
			alert = $11,
			state_version = $12,
			claimed_version = $13,
			claim_token = $14,
			claim_until = $15,
			dispatch_needed = $16,
			dispatch_after = COALESCE($17, dispatch_after),
			last_dispatch_at = $18,
			last_error = $19,
			updated_at = NOW()
		WHERE id = $1`
	result, err := s.db.ExecContext(ctx, query,
		activity.ID,
		activity.Kind,
		activity.AudienceKind,
		activity.UserID,
		activity.TopicID,
		activity.ExternalRef,
		activity.Status,
		activity.PendingEvent,
		startAttributes,
		currentState,
		alert,
		activity.StateVersion,
		activity.ClaimedVersion,
		activity.ClaimToken,
		claimUntil,
		activity.DispatchNeeded,
		dispatchAfter,
		lastDispatchAt,
		activity.LastError,
	)
	if err != nil {
		return nil, fmt.Errorf("error updating LA activity: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, Errors.NotFound
	}

	return s.GetLAActivity(ctx, activity.ID)
}

func (s *PostgresStore) ClaimReadyLAActivities(ctx context.Context, claimToken string, claimUntil time.Time, limit int) ([]LAActivity, error) {
	query := `
		WITH candidates AS (
			SELECT id
			FROM la_activities
			WHERE dispatch_needed = TRUE
			  AND dispatch_after <= NOW()
			  AND (claim_until IS NULL OR claim_until < NOW())
			ORDER BY dispatch_after ASC, created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $3
		)
		UPDATE la_activities AS a
		SET claim_token = $1,
			claim_until = $2,
			claimed_version = a.state_version,
			updated_at = NOW()
		FROM candidates
		WHERE a.id = candidates.id
		RETURNING
			a.id,
			a.kind,
			a.audience_kind,
			a.user_id,
			a.topic_id,
			a.external_ref,
			a.status,
			a.pending_event,
			a.start_attributes,
			a.current_state,
			a.alert,
			a.state_version,
			a.claimed_version,
			a.claim_token,
			a.claim_until,
			a.dispatch_needed,
			a.dispatch_after,
			a.last_dispatch_at,
			a.last_error,
			a.created_at,
			a.updated_at`

	rows, err := s.db.QueryContext(ctx, query, claimToken, claimUntil, limit)
	if err != nil {
		return nil, fmt.Errorf("error claiming ready LA activities: %w", err)
	}
	defer rows.Close()

	var activities []LAActivity
	for rows.Next() {
		activity, err := scanLAActivity(rows)
		if err != nil {
			return nil, fmt.Errorf("error scanning active LA activity: %w", err)
		}
		activities = append(activities, *activity)
	}

	return activities, rows.Err()
}

func (s *PostgresStore) CompleteLAActivityDispatch(ctx context.Context, laID, claimToken string, claimedVersion int64, status LAActivityStatus, lastError string) (*LAActivity, error) {
	query := `
		UPDATE la_activities
		SET status = CASE WHEN state_version = $3 THEN $4 ELSE status END,
			dispatch_needed = CASE WHEN state_version > $3 THEN TRUE ELSE FALSE END,
			dispatch_after = CASE WHEN state_version > $3 THEN NOW() ELSE dispatch_after END,
			last_dispatch_at = NOW(),
			last_error = $5,
			claim_token = '',
			claim_until = NULL,
			updated_at = NOW()
		WHERE id = $1 AND claim_token = $2
		RETURNING
			id, kind, audience_kind, user_id, topic_id, external_ref, status, pending_event,
			start_attributes, current_state, alert, state_version, claimed_version,
			claim_token, claim_until, dispatch_needed, dispatch_after, last_dispatch_at,
			last_error, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, query, laID, claimToken, claimedVersion, status, lastError)

	activity, err := scanLAActivity(row)
	if err != nil {
		if err == Errors.NotFound {
			return nil, err
		}
		return nil, fmt.Errorf("error completing LA activity dispatch: %w", err)
	}

	return activity, nil
}

func (s *PostgresStore) UpsertLAUpdateTokenByUser(ctx context.Context, updateToken *LAUpdateToken) (*LAUpdateToken, error) {
	expiresAt, err := time.Parse(time.RFC3339, updateToken.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("error parsing LA update token expiry: %w", err)
	}

	query := `
		INSERT INTO la_update_tokens(id, user_id, platform, token, expires_at)
		VALUES($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, platform, token)
		DO UPDATE SET expires_at = EXCLUDED.expires_at, updated_at = NOW()
		RETURNING id, user_id, topic_id, platform, token, expires_at, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, query,
		updateToken.ID,
		updateToken.UserID,
		updateToken.Platform,
		updateToken.Token,
		expiresAt.UTC(),
	)

	return scanLAUpdateToken(row)
}

func (s *PostgresStore) UpsertLAUpdateTokenByTopic(ctx context.Context, updateToken *LAUpdateToken) (*LAUpdateToken, error) {
	expiresAt, err := time.Parse(time.RFC3339, updateToken.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("error parsing LA update token expiry: %w", err)
	}

	query := `
		INSERT INTO la_update_tokens(id, topic_id, platform, token, expires_at)
		VALUES($1, $2, $3, $4, $5)
		ON CONFLICT (topic_id, platform, token)
		DO UPDATE SET expires_at = EXCLUDED.expires_at, updated_at = NOW()
		RETURNING id, user_id, topic_id, platform, token, expires_at, created_at, updated_at`
	row := s.db.QueryRowContext(ctx, query,
		updateToken.ID,
		updateToken.TopicID,
		updateToken.Platform,
		updateToken.Token,
		expiresAt.UTC(),
	)

	return scanLAUpdateToken(row)
}

func (s *PostgresStore) GetLAUpdateTokensByUser(ctx context.Context, userID string) ([]LAUpdateToken, error) {
	query := `
		SELECT id, user_id, topic_id, platform, token, expires_at, created_at, updated_at
		FROM la_update_tokens
		WHERE user_id = $1 AND expires_at > NOW()
		ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("error getting LA update tokens by user: %w", err)
	}
	defer rows.Close()

	var tokens []LAUpdateToken
	for rows.Next() {
		updateToken, err := scanLAUpdateToken(rows)
		if err != nil {
			return nil, fmt.Errorf("error scanning LA update token: %w", err)
		}
		tokens = append(tokens, *updateToken)
	}

	return tokens, rows.Err()
}

func (s *PostgresStore) GetLAUpdateTokensByTopic(ctx context.Context, topicID string) ([]LAUpdateToken, error) {
	query := `
		SELECT id, user_id, topic_id, platform, token, expires_at, created_at, updated_at
		FROM la_update_tokens
		WHERE topic_id = $1 AND expires_at > NOW()
		ORDER BY created_at ASC`
	rows, err := s.db.QueryContext(ctx, query, topicID)
	if err != nil {
		return nil, fmt.Errorf("error getting LA update tokens by topic: %w", err)
	}
	defer rows.Close()

	var tokens []LAUpdateToken
	for rows.Next() {
		updateToken, err := scanLAUpdateToken(rows)
		if err != nil {
			return nil, fmt.Errorf("error scanning LA update token: %w", err)
		}
		tokens = append(tokens, *updateToken)
	}

	return tokens, rows.Err()
}

func (s *PostgresStore) DeleteLAUpdateToken(ctx context.Context, tokenID string) error {
	query := `DELETE FROM la_update_tokens WHERE id = $1`
	result, err := s.db.ExecContext(ctx, query, tokenID)
	if err != nil {
		return fmt.Errorf("error deleting LA update token: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return Errors.NotFound
	}

	return nil
}

type laRowScanner interface {
	Scan(dest ...any) error
}

func marshalLAMap(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("error marshaling LA map: %w", err)
	}
	return b, nil
}

func marshalLAAlert(alert *LAAlert) ([]byte, error) {
	if alert == nil {
		return nil, nil
	}
	b, err := json.Marshal(alert)
	if err != nil {
		return nil, fmt.Errorf("error marshaling LA alert: %w", err)
	}
	return b, nil
}

func parseRFC3339ToNullableTime(value string) (any, error) {
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("error parsing timestamp %q: %w", value, err)
	}
	return t.UTC(), nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func formatNullTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return formatTime(t.Time)
}

func formatNullString(s sql.NullString) string {
	if !s.Valid {
		return ""
	}
	return s.String
}

func scanLARegistration(scanner laRowScanner) (*LARegistration, error) {
	var registration LARegistration
	var createdAt time.Time
	var updatedAt time.Time
	err := scanner.Scan(
		&registration.ID,
		&registration.UserID,
		&registration.Platform,
		&registration.StartToken,
		&registration.AutoStartEnabled,
		&registration.Enabled,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, err
	}

	registration.CreatedAt = formatTime(createdAt)
	registration.UpdatedAt = formatTime(updatedAt)
	return &registration, nil
}

func scanLAActivity(scanner laRowScanner) (*LAActivity, error) {
	var activity LAActivity
	var userID sql.NullString
	var topicID sql.NullString
	var externalRef sql.NullString
	var startAttributesRaw []byte
	var currentStateRaw []byte
	var alertRaw []byte
	var claimUntil sql.NullTime
	var dispatchAfter time.Time
	var lastDispatchAt sql.NullTime
	var createdAt time.Time
	var updatedAt time.Time

	err := scanner.Scan(
		&activity.ID,
		&activity.Kind,
		&activity.AudienceKind,
		&userID,
		&topicID,
		&externalRef,
		&activity.Status,
		&activity.PendingEvent,
		&startAttributesRaw,
		&currentStateRaw,
		&alertRaw,
		&activity.StateVersion,
		&activity.ClaimedVersion,
		&activity.ClaimToken,
		&claimUntil,
		&activity.DispatchNeeded,
		&dispatchAfter,
		&lastDispatchAt,
		&activity.LastError,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, err
	}

	activity.UserID = formatNullString(userID)
	activity.TopicID = formatNullString(topicID)
	activity.ExternalRef = formatNullString(externalRef)
	activity.ClaimUntil = formatNullTime(claimUntil)
	activity.DispatchAfter = formatTime(dispatchAfter)
	activity.LastDispatchAt = formatNullTime(lastDispatchAt)
	activity.CreatedAt = formatTime(createdAt)
	activity.UpdatedAt = formatTime(updatedAt)

	if len(startAttributesRaw) > 0 {
		if err := json.Unmarshal(startAttributesRaw, &activity.StartAttributes); err != nil {
			return nil, fmt.Errorf("error unmarshaling LA start attributes: %w", err)
		}
	}
	if activity.StartAttributes == nil {
		activity.StartAttributes = map[string]any{}
	}

	if len(currentStateRaw) > 0 {
		if err := json.Unmarshal(currentStateRaw, &activity.CurrentState); err != nil {
			return nil, fmt.Errorf("error unmarshaling LA current state: %w", err)
		}
	}
	if activity.CurrentState == nil {
		activity.CurrentState = map[string]any{}
	}

	if len(alertRaw) > 0 {
		var alert LAAlert
		if err := json.Unmarshal(alertRaw, &alert); err != nil {
			return nil, fmt.Errorf("error unmarshaling LA alert: %w", err)
		}
		activity.Alert = &alert
	}

	return &activity, nil
}

func scanLAUpdateToken(scanner laRowScanner) (*LAUpdateToken, error) {
	var updateToken LAUpdateToken
	var userID sql.NullString
	var topicID sql.NullString
	var expiresAt time.Time
	var createdAt time.Time
	var updatedAt time.Time

	err := scanner.Scan(
		&updateToken.ID,
		&userID,
		&topicID,
		&updateToken.Platform,
		&updateToken.Token,
		&expiresAt,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, Errors.NotFound
		}
		return nil, err
	}

	updateToken.UserID = formatNullString(userID)
	updateToken.TopicID = formatNullString(topicID)
	updateToken.ExpiresAt = formatTime(expiresAt)
	updateToken.CreatedAt = formatTime(createdAt)
	updateToken.UpdatedAt = formatTime(updatedAt)
	return &updateToken, nil
}
