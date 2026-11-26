package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

type TenantLAChannelState string

const (
	TenantLAChannelProvisioning TenantLAChannelState = "provisioning"
	TenantLAChannelAmbiguous    TenantLAChannelState = "ambiguous"
	TenantLAChannelActive       TenantLAChannelState = "active"
)

type TenantLAChannel struct {
	ActivityID       string               `json:"activityId"`
	TopicID          string               `json:"topicId"`
	ProviderConfigID uuid.UUID            `json:"providerConfigId"`
	ProvisioningID   uuid.UUID            `json:"provisioningId"`
	ChannelID        string               `json:"channelId,omitempty"`
	State            TenantLAChannelState `json:"state"`
	CreatedAt        time.Time            `json:"createdAt"`
}

const tenantLAChannelColumns = `topic_id, provider_config_id, provisioning_id, COALESCE(channel_id, ''), state, created_at`
const selectTenantLAChannel = `SELECT ` + tenantLAChannelColumns + `
	FROM tenant_live_activity_channels WHERE tenant_id = $1 AND activity_id = $2`

func scanTenantLAChannel(row *sql.Row, activityID string) (TenantLAChannel, error) {
	ch := TenantLAChannel{ActivityID: activityID}
	err := row.Scan(&ch.TopicID, &ch.ProviderConfigID, &ch.ProvisioningID, &ch.ChannelID, &ch.State, &ch.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantLAChannel{}, Errors.NotFound
	}
	ch.CreatedAt = ch.CreatedAt.UTC()
	return ch, err
}

func (s *PostgresStore) GetTenantLAChannel(ctx context.Context, tenantID uuid.UUID, activityID string) (TenantLAChannel, error) {
	return scanTenantLAChannel(s.db.QueryRowContext(ctx, selectTenantLAChannel, tenantID, activityID), activityID)
}

func (s *PostgresStore) MatchTenantLAChannel(ctx context.Context, tenantID uuid.UUID, requested TenantLAChannel) (TenantLAChannel, error) {
	if !validTenantResourceText(requested.ActivityID, 128) || !validTenantResourceText(requested.TopicID, 128) ||
		requested.ProviderConfigID == uuid.Nil || requested.ActivityID == "." || requested.ActivityID == ".." {
		return TenantLAChannel{}, ErrInvalidTenantResource
	}
	existing, err := s.GetTenantLAChannel(ctx, tenantID, requested.ActivityID)
	if err != nil {
		return TenantLAChannel{}, err
	}
	if existing.TopicID != requested.TopicID || existing.ProviderConfigID != requested.ProviderConfigID {
		return TenantLAChannel{}, Errors.Conflict
	}
	return existing, nil
}

func (s *PostgresStore) ReserveTenantLAChannel(ctx context.Context, tenantID uuid.UUID, ch TenantLAChannel, credentialVersion int64) (TenantLAChannel, bool, error) {
	provisioningID, err := uuid.NewRandom()
	if err != nil {
		return TenantLAChannel{}, false, err
	}
	reserved, err := scanTenantLAChannel(s.db.QueryRowContext(ctx, `
		INSERT INTO tenant_live_activity_channels (tenant_id, activity_id, topic_id, provider_config_id, provisioning_id, state)
		SELECT $1, $2, topic.id, pc.id, $5, 'provisioning'
		FROM tenant_topics topic
		JOIN provider_configs pc ON pc.tenant_id = topic.tenant_id
		JOIN tenants t ON t.id = pc.tenant_id
		WHERE topic.tenant_id = $1 AND topic.id = $3 AND pc.id = $4 AND pc.provider = 'apns'
		  AND pc.credential_version = $6 AND pc.enabled AND NOT pc.authorization_failed AND t.disabled_at IS NULL
		ON CONFLICT (tenant_id, activity_id) DO NOTHING
		RETURNING `+tenantLAChannelColumns,
		tenantID, ch.ActivityID, ch.TopicID, ch.ProviderConfigID, provisioningID, credentialVersion,
	), ch.ActivityID)
	if err == nil {
		return reserved, true, nil
	}
	if !errors.Is(err, Errors.NotFound) {
		return TenantLAChannel{}, false, err
	}
	existing, err := s.MatchTenantLAChannel(ctx, tenantID, ch)
	if !errors.Is(err, Errors.NotFound) {
		return existing, false, err
	}
	if _, err := s.GetTenantTopic(ctx, tenantID, ch.TopicID); err != nil {
		return TenantLAChannel{}, false, err
	}
	return TenantLAChannel{}, false, ErrProviderRouteUnavailable
}

func (s *PostgresStore) RecordTenantLAChannel(ctx context.Context, tenantID uuid.UUID, ch TenantLAChannel, channelID string, state TenantLAChannelState) (TenantLAChannel, error) {
	return scanTenantLAChannel(s.db.QueryRowContext(ctx, `
		UPDATE tenant_live_activity_channels SET state = $5, channel_id = NULLIF($4, '')
		WHERE tenant_id = $1 AND activity_id = $2 AND provisioning_id = $3
		  AND (state = 'provisioning' OR (state = $5 AND channel_id IS NOT DISTINCT FROM NULLIF($4, '')))
		RETURNING `+tenantLAChannelColumns, tenantID, ch.ActivityID, ch.ProvisioningID, channelID, state,
	), ch.ActivityID)
}

func (s *PostgresStore) ReleaseTenantLAChannel(ctx context.Context, tenantID uuid.UUID, ch TenantLAChannel) error {
	return s.removeTenantLAChannel(ctx, tenantID, ch, TenantLAChannelProvisioning)
}

func (s *PostgresStore) AdmitTenantLAChannelOperation(ctx context.Context, tenantID uuid.UUID, ch TenantLAChannel, version int64) error {
	var admitted bool
	err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM tenant_live_activity_channels ch
			JOIN provider_configs pc ON pc.tenant_id = ch.tenant_id AND pc.id = ch.provider_config_id
			JOIN tenants t ON t.id = pc.tenant_id
			WHERE ch.tenant_id = $1 AND ch.activity_id = $2 AND ch.provisioning_id = $3
			  AND ch.state = $5 AND ch.channel_id IS NOT DISTINCT FROM NULLIF($6, '')
			  AND pc.credential_version = $4 AND pc.enabled
			  AND NOT pc.authorization_failed AND t.disabled_at IS NULL
		)`, tenantID, ch.ActivityID, ch.ProvisioningID, version, ch.State, ch.ChannelID).Scan(&admitted)
	if err == nil && !admitted {
		return ErrProviderRouteUnavailable
	}
	return err
}

func (s *PostgresStore) RemoveTenantLAChannel(ctx context.Context, tenantID uuid.UUID, ch TenantLAChannel) error {
	return s.removeTenantLAChannel(ctx, tenantID, ch, TenantLAChannelActive)
}

func (s *PostgresStore) removeTenantLAChannel(ctx context.Context, tenantID uuid.UUID, ch TenantLAChannel, state TenantLAChannelState) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM tenant_live_activity_channels
		WHERE tenant_id = $1 AND activity_id = $2 AND provisioning_id = $3 AND state = $4
		  AND channel_id IS NOT DISTINCT FROM NULLIF($5, '')`,
		tenantID, ch.ActivityID, ch.ProvisioningID, state, ch.ChannelID,
	)
	return err
}
