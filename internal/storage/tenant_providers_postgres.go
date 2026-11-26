package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrProviderEncryptionUnavailable = errors.New("provider encryption key unavailable")
	ErrProviderVersionConflict       = errors.New("provider credential version changed")
)

type TenantProvider struct {
	ID uuid.UUID `json:"id"`
	TenantProviderNamespace
	Enabled           bool      `json:"enabled"`
	CredentialVersion int64     `json:"credentialVersion"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	Ready             bool      `json:"ready"`
	ReadinessReason   string    `json:"readinessReason,omitempty"`
}

type TenantProviderStore struct {
	db   *sql.DB
	keys providerEncryptionKeys
}

func NewTenantProviderStore(store *PostgresStore, activeKeyID, keysJSON string) (*TenantProviderStore, error) {
	keys, err := parseProviderEncryptionKeys(activeKeyID, keysJSON)
	if err != nil {
		return nil, err
	}
	return &TenantProviderStore{db: store.db, keys: keys}, nil
}

func (s *TenantProviderStore) Create(ctx context.Context, tenantID uuid.UUID, namespace TenantProviderNamespace, enabled bool, credentials json.RawMessage) (TenantProvider, error) {
	plaintext, err := validateProviderCredentials(ctx, namespace, credentials)
	if err != nil {
		return TenantProvider{}, err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return TenantProvider{}, fmt.Errorf("generate provider config ID: %w", err)
	}
	route := TenantProvider{ID: id, TenantProviderNamespace: namespace, Enabled: enabled, CredentialVersion: 1}
	encrypted, err := s.keys.encrypt(tenantID, route, plaintext)
	if err != nil {
		return TenantProvider{}, err
	}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO provider_configs (tenant_id, id, provider, environment, application_id, enabled,
			credential_version, credential_key_id, credential_nonce, credential_ciphertext)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (tenant_id, provider, environment, application_id) DO NOTHING
		RETURNING created_at, updated_at`,
		tenantID, id, route.Provider, route.Environment, route.ApplicationID, enabled,
		route.CredentialVersion, encrypted.keyID, encrypted.nonce, encrypted.ciphertext,
	).Scan(&route.CreatedAt, &route.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantProvider{}, Errors.AlreadyExists
	}
	if err != nil {
		return TenantProvider{}, fmt.Errorf("create tenant provider: %w", err)
	}
	return route.withReadiness(""), nil
}

func (s *TenantProviderStore) Update(ctx context.Context, tenantID, id uuid.UUID, version int64, enabled *bool, credentials json.RawMessage) (TenantProvider, error) {
	route := TenantProvider{ID: id}
	err := s.db.QueryRowContext(ctx, `
		SELECT provider, environment, application_id, credential_version
		FROM provider_configs WHERE tenant_id = $1 AND id = $2`, tenantID, id,
	).Scan(&route.Provider, &route.Environment, &route.ApplicationID, &route.CredentialVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantProvider{}, Errors.NotFound
	}
	if err != nil {
		return TenantProvider{}, fmt.Errorf("get tenant provider namespace: %w", err)
	}
	if version != route.CredentialVersion {
		return TenantProvider{}, ErrProviderVersionConflict
	}
	if len(credentials) == 0 {
		if enabled == nil || *enabled {
			return TenantProvider{}, ErrInvalidTenantResource
		}
		route.Enabled = false
		err = s.db.QueryRowContext(ctx, `
			UPDATE provider_configs SET enabled = false, updated_at = CASE WHEN enabled THEN now() ELSE updated_at END
			WHERE tenant_id = $1 AND id = $2 AND credential_version = $3
			RETURNING created_at, updated_at`, tenantID, id, version,
		).Scan(&route.CreatedAt, &route.UpdatedAt)
	} else {
		plaintext, err := validateProviderCredentials(ctx, route.TenantProviderNamespace, credentials)
		if err != nil {
			return TenantProvider{}, err
		}
		route.CredentialVersion++
		encrypted, err := s.keys.encrypt(tenantID, route, plaintext)
		if err != nil {
			return TenantProvider{}, err
		}
		return s.replaceCredentials(ctx, tenantID, route, version, enabled, encrypted)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return TenantProvider{}, ErrProviderVersionConflict
	}
	if err != nil {
		return TenantProvider{}, fmt.Errorf("disable tenant provider: %w", err)
	}
	return route.withReadiness(""), nil
}

func (s *TenantProviderStore) replaceCredentials(ctx context.Context, tenantID uuid.UUID, route TenantProvider, version int64, enabled *bool, encrypted encryptedProviderCredentials) (TenantProvider, error) {
	err := s.db.QueryRowContext(ctx, `
		UPDATE provider_configs SET enabled = COALESCE($4, enabled), credential_version = $5, credential_key_id = $6,
			credential_nonce = $7, credential_ciphertext = $8, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND credential_version = $3
		RETURNING enabled, created_at, updated_at`,
		tenantID, route.ID, version, enabled, route.CredentialVersion, encrypted.keyID, encrypted.nonce, encrypted.ciphertext,
	).Scan(&route.Enabled, &route.CreatedAt, &route.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantProvider{}, ErrProviderVersionConflict
	}
	if err != nil {
		return TenantProvider{}, fmt.Errorf("rotate tenant provider credentials: %w", err)
	}
	return route.withReadiness(""), nil
}

const providerConfigSelect = `SELECT id, provider, environment, application_id, enabled,
	credential_version, created_at, updated_at, credential_key_id, credential_nonce, credential_ciphertext
	FROM provider_configs WHERE tenant_id = $1`

func (s *TenantProviderStore) Get(ctx context.Context, tenantID, id uuid.UUID) (TenantProvider, error) {
	route, err := s.providerWithReadiness(ctx, tenantID, s.db.QueryRowContext(ctx, providerConfigSelect+` AND id = $2`, tenantID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return TenantProvider{}, Errors.NotFound
	}
	if err != nil {
		return TenantProvider{}, fmt.Errorf("get tenant provider config: %w", err)
	}
	return route, nil
}

func (s *TenantProviderStore) List(ctx context.Context, tenantID uuid.UUID) ([]TenantProvider, error) {
	rows, err := s.db.QueryContext(ctx, providerConfigSelect+` ORDER BY provider, environment, application_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list tenant provider configs: %w", err)
	}
	defer rows.Close()

	routes := make([]TenantProvider, 0)
	for rows.Next() {
		route, err := s.providerWithReadiness(ctx, tenantID, rows)
		if err != nil {
			return nil, fmt.Errorf("read tenant provider config: %w", err)
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tenant provider configs: %w", err)
	}
	return routes, nil
}

func (s *TenantProviderStore) providerWithReadiness(ctx context.Context, tenantID uuid.UUID, row interface{ Scan(...any) error }) (TenantProvider, error) {
	var route TenantProvider
	var credentials encryptedProviderCredentials
	if err := row.Scan(&route.ID, &route.Provider, &route.Environment, &route.ApplicationID, &route.Enabled,
		&route.CredentialVersion, &route.CreatedAt, &route.UpdatedAt, &credentials.keyID, &credentials.nonce,
		&credentials.ciphertext); err != nil {
		return TenantProvider{}, err
	}
	return route.withReadiness(s.keys.readiness(ctx, tenantID, route, credentials)), nil
}

func (route TenantProvider) withReadiness(reason string) TenantProvider {
	if !route.Enabled {
		reason = "disabled"
	}
	route.Ready = reason == ""
	route.ReadinessReason = reason
	return route
}
