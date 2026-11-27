package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

func (s *TenantProviderStore) ChannelCredentialVersion(ctx context.Context, ref model.ProviderRef) (int64, error) {
	var version int64
	err := s.db.QueryRowContext(ctx, `
		SELECT pc.credential_version
		FROM provider_configs pc JOIN tenants t ON t.id = pc.tenant_id
		WHERE pc.tenant_id = $1 AND pc.id = $2 AND pc.provider = 'apns'
		  AND pc.enabled AND NOT pc.authorization_failed AND t.disabled_at IS NULL`,
		ref.TenantID, ref.RouteID,
	).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrProviderRouteUnavailable
	}
	if err != nil {
		return 0, fmt.Errorf("read tenant provider version: %w", err)
	}
	return version, nil
}

func (s *TenantProviderStore) ChannelCredentials(ctx context.Context, ref model.ProviderRef, version int64) (TenantProviderNamespace, []byte, error) {
	route := TenantProvider{ID: ref.RouteID, CredentialVersion: version, Enabled: true}
	var secret encryptedProviderCredentials
	err := s.db.QueryRowContext(ctx, `
		SELECT pc.provider, pc.environment, pc.application_id, pc.credential_key_id, pc.credential_nonce, pc.credential_ciphertext
		FROM provider_configs pc JOIN tenants t ON t.id = pc.tenant_id
		WHERE pc.tenant_id = $1 AND pc.id = $2 AND pc.provider = 'apns' AND pc.credential_version = $3
		  AND pc.enabled AND NOT pc.authorization_failed AND t.disabled_at IS NULL`,
		ref.TenantID, ref.RouteID, version,
	).Scan(&route.Provider, &route.Environment, &route.ApplicationID, &secret.keyID, &secret.nonce, &secret.ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantProviderNamespace{}, nil, ErrProviderRouteUnavailable
	}
	if err != nil {
		return TenantProviderNamespace{}, nil, fmt.Errorf("read tenant provider credentials: %w", err)
	}
	plaintext, reason := s.keys.open(ctx, ref.TenantID, route, secret)
	if reason != "" {
		return TenantProviderNamespace{}, nil, ErrProviderRouteUnavailable
	}
	return route.TenantProviderNamespace, plaintext, nil
}

func (s *TenantProviderStore) MarkAuthorizationFailed(ctx context.Context, ref model.ProviderRef, version int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE provider_configs SET authorization_failed = true
		WHERE tenant_id = $1 AND id = $2 AND credential_version = $3 AND NOT authorization_failed`,
		ref.TenantID, ref.RouteID, version,
	)
	return err
}
