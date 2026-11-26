package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type Tenant struct {
	ID   uuid.UUID
	Name string
}

type ServerKeyAccess struct {
	TenantID uuid.UUID
	KeyID    uuid.UUID
	Scopes   []string
}

var (
	ErrInvalidServerKey     = errors.New("invalid server key")
	ErrInsufficientKeyScope = errors.New("insufficient server key scope")
	ErrInvalidKeyRequest    = errors.New("invalid server key request")
)

var serverKeyScopes = []string{
	"read", "send", "users:write", "devices:write", "topics:write", "keys:manage", "providers:manage",
}

// BootstrapTenant creates an app and its first server key together. The secret
// exists only in the return value; its digest is the only credential stored.
func (s *PostgresStore) BootstrapTenant(ctx context.Context, name string) (Tenant, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 160 {
		return Tenant{}, "", fmt.Errorf("tenant name must contain 1 to 160 bytes")
	}

	tenantID, err := uuid.NewRandom()
	if err != nil {
		return Tenant{}, "", fmt.Errorf("generate tenant ID: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Tenant{}, "", fmt.Errorf("begin tenant bootstrap: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO tenants(id, name) VALUES($1, $2)`, tenantID, name); err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return Tenant{}, "", Errors.AlreadyExists
		}
		return Tenant{}, "", fmt.Errorf("create tenant: %w", err)
	}
	_, key, err := createServerKey(ctx, tx, tenantID, serverKeyScopes, nil)
	if err != nil {
		return Tenant{}, "", err
	}

	if err := tx.Commit(); err != nil {
		return Tenant{}, "", fmt.Errorf("commit tenant bootstrap: %w", err)
	}

	return Tenant{ID: tenantID, Name: name}, key, nil
}

// AuthenticateServerKey checks the current database state on every request so
// revocation and tenant disabling take effect without a credential cache.
func (s *PostgresStore) AuthenticateServerKey(ctx context.Context, key string) (ServerKeyAccess, error) {
	idText, secretText, ok := strings.Cut(key, ".")
	if !ok || len(key) != 80 {
		return ServerKeyAccess{}, ErrInvalidServerKey
	}
	keyID, err := uuid.Parse(idText)
	if err != nil || keyID.String() != idText {
		return ServerKeyAccess{}, ErrInvalidServerKey
	}
	secret, err := base64.RawURLEncoding.DecodeString(secretText)
	if err != nil || len(secret) != 32 || base64.RawURLEncoding.EncodeToString(secret) != secretText {
		return ServerKeyAccess{}, ErrInvalidServerKey
	}

	var access ServerKeyAccess
	var storedHash []byte
	var revoked, expired, disabled bool
	err = s.db.QueryRowContext(ctx, `
		SELECT k.tenant_id, k.secret_hash, k.scopes,
			k.revoked_at IS NOT NULL,
			k.expires_at IS NOT NULL AND k.expires_at <= statement_timestamp(),
			t.disabled_at IS NOT NULL
		FROM tenant_api_keys k
		JOIN tenants t ON t.id = k.tenant_id
		WHERE k.id = $1`, keyID,
	).Scan(&access.TenantID, &storedHash, pq.Array(&access.Scopes), &revoked, &expired, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ServerKeyAccess{}, ErrInvalidServerKey
	}
	if err != nil {
		return ServerKeyAccess{}, fmt.Errorf("lookup server key: %w", err)
	}
	hash := sha256.Sum256(secret)
	if subtle.ConstantTimeCompare(storedHash, hash[:]) != 1 || revoked || expired || disabled {
		return ServerKeyAccess{}, ErrInvalidServerKey
	}
	access.KeyID = keyID
	return access, nil
}

type keyManagementAuthorization struct {
	scopes []string
	expiry sql.NullTime
	now    time.Time
}

// lockKeyManagement serializes management within an app before locking keys.
// The common tenant-first order also prevents reciprocal revocations deadlocking.
func lockKeyManagement(ctx context.Context, tx *sql.Tx, actor ServerKeyAccess) (keyManagementAuthorization, error) {
	var access keyManagementAuthorization
	var disabled bool
	err := tx.QueryRowContext(ctx, `SELECT disabled_at IS NOT NULL FROM tenants WHERE id = $1 FOR UPDATE`, actor.TenantID).Scan(&disabled)
	if errors.Is(err, sql.ErrNoRows) || err == nil && disabled {
		return access, ErrInvalidServerKey
	}
	if err != nil {
		return access, fmt.Errorf("lock key tenant: %w", err)
	}
	var revoked bool
	err = tx.QueryRowContext(ctx, `SELECT scopes, expires_at, revoked_at IS NOT NULL FROM tenant_api_keys WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, actor.TenantID, actor.KeyID).Scan(pq.Array(&access.scopes), &access.expiry, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return access, ErrInvalidServerKey
	}
	if err != nil {
		return access, fmt.Errorf("lock acting key: %w", err)
	}
	// Read the database clock after acquiring locks: a waiting transaction may
	// have started before the key expired.
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&access.now); err != nil {
		return access, fmt.Errorf("read key validation time: %w", err)
	}
	if revoked || access.expiry.Valid && !access.expiry.Time.After(access.now) {
		return access, ErrInvalidServerKey
	}
	if !slices.Contains(access.scopes, "keys:manage") {
		return access, ErrInsufficientKeyScope
	}
	return access, nil
}

// createServerKey keeps bootstrap and delegated credentials on the same format
// and digest-only persistence path. The caller commits before exposing the key.
func createServerKey(ctx context.Context, tx *sql.Tx, tenantID uuid.UUID, scopes []string, expiresAt *time.Time) (uuid.UUID, string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("generate key ID: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return uuid.Nil, "", fmt.Errorf("generate server key: %w", err)
	}
	hash := sha256.Sum256(secret)
	if _, err := tx.ExecContext(ctx, `INSERT INTO tenant_api_keys(id, tenant_id, secret_hash, scopes, expires_at) VALUES($1, $2, $3, $4, $5)`, id, tenantID, hash[:], pq.Array(scopes), expiresAt); err != nil {
		return uuid.Nil, "", fmt.Errorf("create server key: %w", err)
	}
	return id, id.String() + "." + base64.RawURLEncoding.EncodeToString(secret), nil
}

// IssueServerKey creates an independent credential so callers can rotate with
// overlap, then explicitly revoke the old key once their backend has switched.
func (s *PostgresStore) IssueServerKey(ctx context.Context, actor ServerKeyAccess, scopes []string, expiresAt *time.Time) (uuid.UUID, string, error) {
	if len(scopes) == 0 {
		return uuid.Nil, "", ErrInvalidKeyRequest
	}
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if !slices.Contains(serverKeyScopes, scope) {
			return uuid.Nil, "", ErrInvalidKeyRequest
		}
		if seen[scope] {
			return uuid.Nil, "", ErrInvalidKeyRequest
		}
		seen[scope] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("begin key issuance: %w", err)
	}
	defer tx.Rollback()
	access, err := lockKeyManagement(ctx, tx, actor)
	if err != nil {
		return uuid.Nil, "", err
	}
	for _, scope := range scopes {
		if !slices.Contains(access.scopes, scope) {
			return uuid.Nil, "", ErrInsufficientKeyScope
		}
	}
	if expiresAt != nil && !expiresAt.After(access.now) {
		return uuid.Nil, "", ErrInvalidKeyRequest
	}
	if access.expiry.Valid && (expiresAt == nil || expiresAt.After(access.expiry.Time)) {
		return uuid.Nil, "", ErrInsufficientKeyScope
	}
	id, key, err := createServerKey(ctx, tx, actor.TenantID, scopes, expiresAt)
	if err != nil {
		return uuid.Nil, "", err
	}

	if err := tx.Commit(); err != nil {
		return uuid.Nil, "", fmt.Errorf("commit server key: %w", err)
	}
	return id, key, nil
}

// RevokeServerKey is idempotent for keys belonging to the acting app. A key
// belonging to another app is indistinguishable from a missing key.
func (s *PostgresStore) RevokeServerKey(ctx context.Context, actor ServerKeyAccess, id uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin key revocation: %w", err)
	}
	defer tx.Rollback()
	if _, err := lockKeyManagement(ctx, tx, actor); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tenant_api_keys SET revoked_at = COALESCE(revoked_at, clock_timestamp()) WHERE tenant_id = $1 AND id = $2`, actor.TenantID, id)
	if err != nil {
		return fmt.Errorf("revoke server key: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count revoked keys: %w", err)
	}
	if count == 0 {
		return Errors.NotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit key revocation: %w", err)
	}
	return nil
}
