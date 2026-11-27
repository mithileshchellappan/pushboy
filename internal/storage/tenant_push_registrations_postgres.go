package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/mithileshchellappan/pushboy/internal/model"
)

const MaxPushRegistrationTokenBytes = 4096

type TenantPushRegistration struct {
	ID                  uuid.UUID      `json:"id"`
	ProviderConfigID    uuid.UUID      `json:"providerConfigId"`
	Provider            model.Platform `json:"provider"`
	RegistrationVersion int64          `json:"registrationVersion"`
	Valid               bool           `json:"valid"`
}

type TenantPushRegistrationChange struct {
	OperationID                 uuid.UUID
	ProviderConfigID            uuid.UUID
	Token                       string
	ExpectedRegistrationID      *uuid.UUID
	ExpectedRegistrationVersion int64
}

var ErrPushRegistrationConflict = errors.New("push registration conflict")

func (s *PostgresStore) GetPushRegistration(ctx context.Context, tenantID, deviceID uuid.UUID) (TenantPushRegistration, error) {
	var registration TenantPushRegistration
	var origin string
	var retired bool
	var id, providerConfigID uuid.NullUUID
	err := s.db.QueryRowContext(ctx, `
		SELECT d.origin, d.retired_at IS NOT NULL, t.registration_id, t.provider_config_id,
		       COALESCE(t.platform, ''), COALESCE(t.registration_version, 0), COALESCE(NOT t.is_deleted, false)
		FROM tenant_devices d
		LEFT JOIN tokens t ON t.tenant_id = d.tenant_id AND t.device_id = d.id AND t.superseded_at IS NULL
		WHERE d.tenant_id = $1 AND d.id = $2
	`, tenantID, deviceID).Scan(&origin, &retired, &id, &providerConfigID,
		&registration.Provider, &registration.RegistrationVersion, &registration.Valid)
	if err := checkPushRegistrationDevice(origin, retired, err); err != nil {
		return TenantPushRegistration{}, err
	}
	if !id.Valid {
		return TenantPushRegistration{}, Errors.NotFound
	}
	registration.ID, registration.ProviderConfigID = id.UUID, providerConfigID.UUID
	return registration, nil
}

func (s *PostgresStore) RegisterPush(ctx context.Context, tenantID, deviceID uuid.UUID, change TenantPushRegistrationChange) (TenantPushRegistration, error) {
	if deviceID == uuid.Nil || change.OperationID == uuid.Nil || change.ProviderConfigID == uuid.Nil ||
		change.ExpectedRegistrationVersion < 0 ||
		(change.ExpectedRegistrationID == nil) != (change.ExpectedRegistrationVersion == 0) ||
		(change.ExpectedRegistrationID != nil && *change.ExpectedRegistrationID == uuid.Nil) ||
		len(change.Token) == 0 || len(change.Token) > MaxPushRegistrationTokenBytes || !utf8.ValidString(change.Token) || strings.ContainsRune(change.Token, 0) {
		return TenantPushRegistration{}, ErrInvalidTenantResource
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return TenantPushRegistration{}, fmt.Errorf("begin push registration: %w", err)
	}
	defer tx.Rollback()

	var origin string
	var retired bool
	err = tx.QueryRowContext(ctx, `
		SELECT origin, retired_at IS NOT NULL FROM tenant_devices WHERE tenant_id = $1 AND id = $2 FOR UPDATE
	`, tenantID, deviceID).Scan(&origin, &retired)
	if err := checkPushRegistrationDevice(origin, retired, err); err != nil {
		return TenantPushRegistration{}, err
	}

	registration := TenantPushRegistration{ProviderConfigID: change.ProviderConfigID, Valid: true}
	err = tx.QueryRowContext(ctx, `SELECT provider FROM provider_configs WHERE tenant_id = $1 AND id = $2`,
		tenantID, change.ProviderConfigID).Scan(&registration.Provider)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantPushRegistration{}, Errors.NotFound
	}
	if err != nil {
		return TenantPushRegistration{}, fmt.Errorf("read push registration provider: %w", err)
	}
	token := change.Token
	if registration.Provider == model.APNS {
		if _, err := hex.DecodeString(token); err != nil {
			return TenantPushRegistration{}, ErrInvalidTenantResource
		}
		token = strings.ToLower(token)
	}
	fingerprint := sha256.Sum256([]byte(token))
	var expectedID uuid.UUID
	if change.ExpectedRegistrationID != nil {
		expectedID = *change.ExpectedRegistrationID
	}
	digest := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%s\x00%s",
		expectedID, change.ExpectedRegistrationVersion, change.ProviderConfigID, token))

	var current TenantPushRegistration
	var currentOperation uuid.UUID
	var currentFingerprint []byte
	var currentToken string
	err = tx.QueryRowContext(ctx, `
		SELECT registration_id, provider_config_id, platform, registration_version, NOT is_deleted,
		       registration_operation_id, token_fingerprint, token
		FROM tokens WHERE tenant_id = $1 AND device_id = $2 AND superseded_at IS NULL FOR UPDATE
	`, tenantID, deviceID).Scan(&current.ID, &current.ProviderConfigID, &current.Provider, &current.RegistrationVersion,
		&current.Valid, &currentOperation, &currentFingerprint, &currentToken)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TenantPushRegistration{}, fmt.Errorf("lock current push registration: %w", err)
	}

	var priorDigest []byte
	err = tx.QueryRowContext(ctx, `
		SELECT request_digest FROM tenant_push_registration_operations
		WHERE tenant_id = $1 AND device_id = $2 AND operation_id = $3
	`, tenantID, deviceID, change.OperationID).Scan(&priorDigest)
	if err == nil {
		if currentOperation == change.OperationID && bytes.Equal(priorDigest, digest[:]) {
			return current, nil
		}
		return TenantPushRegistration{}, ErrPushRegistrationConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TenantPushRegistration{}, fmt.Errorf("read push registration operation: %w", err)
	}
	if current.ID != expectedID || current.RegistrationVersion != change.ExpectedRegistrationVersion ||
		current.RegistrationVersion == math.MaxInt64 {
		return TenantPushRegistration{}, ErrPushRegistrationConflict
	}

	var otherDevice bool
	err = tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM tokens WHERE tenant_id = $1 AND provider_config_id = $2
		  AND token_fingerprint = $3 AND device_id <> $4 AND superseded_at IS NULL AND NOT is_deleted)
	`, tenantID, change.ProviderConfigID, fingerprint[:], deviceID).Scan(&otherDevice)
	if err != nil {
		return TenantPushRegistration{}, fmt.Errorf("check active push endpoint: %w", err)
	}
	sameEndpoint := current.ProviderConfigID == change.ProviderConfigID && bytes.Equal(currentFingerprint, fingerprint[:])
	if otherDevice || sameEndpoint && currentToken != token {
		return TenantPushRegistration{}, ErrPushRegistrationConflict
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tenant_push_registration_operations (tenant_id, device_id, operation_id, request_digest)
		VALUES ($1, $2, $3, $4)
	`, tenantID, deviceID, change.OperationID, digest[:]); err != nil {
		return TenantPushRegistration{}, fmt.Errorf("record push registration operation: %w", err)
	}
	registration.RegistrationVersion = current.RegistrationVersion + 1
	if sameEndpoint {
		registration.ID = current.ID
		_, err = tx.ExecContext(ctx, `
			UPDATE tokens SET registration_version = $3, registration_operation_id = $4, is_deleted = false,
			    registration_confirmed_at = clock_timestamp()
			WHERE tenant_id = $1 AND registration_id = $2
		`, tenantID, registration.ID, registration.RegistrationVersion, change.OperationID)
	} else {
		if current.ID != uuid.Nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE tokens SET superseded_at = clock_timestamp() WHERE tenant_id = $1 AND registration_id = $2
			`, tenantID, current.ID); err != nil {
				return TenantPushRegistration{}, fmt.Errorf("supersede push registration: %w", err)
			}
		}
		registration.ID, err = uuid.NewRandom()
		if err != nil {
			return TenantPushRegistration{}, fmt.Errorf("generate push registration ID: %w", err)
		}
		tokenID, idErr := uuid.NewRandom()
		if idErr != nil {
			return TenantPushRegistration{}, fmt.Errorf("generate push token ID: %w", idErr)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO tokens (id, tenant_id, device_id, provider_config_id, registration_id, platform, token,
			    token_fingerprint, registration_version, registration_operation_id, created_at, registration_confirmed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, statement_timestamp(), statement_timestamp())
		`, tokenID.String(), tenantID, deviceID, change.ProviderConfigID, registration.ID, registration.Provider, token,
			fingerprint[:], registration.RegistrationVersion, change.OperationID)
	}
	if err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.Constraint == "tokens_active_endpoint_idx" {
			return TenantPushRegistration{}, ErrPushRegistrationConflict
		}
		return TenantPushRegistration{}, fmt.Errorf("write push registration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TenantPushRegistration{}, fmt.Errorf("commit push registration: %w", err)
	}
	return registration, nil
}

func checkPushRegistrationDevice(origin string, retired bool, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return Errors.NotFound
	}
	if err != nil {
		return fmt.Errorf("read push registration installation: %w", err)
	}
	if retired {
		return ErrTenantInstallationRetired
	}
	if origin != "registered" {
		return Errors.NotFound
	}
	return nil
}
