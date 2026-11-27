package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type InstallationOwnershipChange struct {
	OperationID              uuid.UUID
	ExpectedOwnershipVersion int64
}

type InstallationIdentification struct {
	UID      *uuid.UUID
	Identity *TenantUserIdentity
}

var ErrInstallationOwnershipConflict = errors.New("installation ownership conflict")
var errRetryInstallationOwnership = errors.New("retry installation ownership")

func (s *PostgresStore) IdentifyInstallation(ctx context.Context, tenantID, deviceID uuid.UUID, change InstallationOwnershipChange, target InstallationIdentification) (TenantInstallation, error) {
	if (target.UID == nil) == (target.Identity == nil) || target.UID != nil && *target.UID == uuid.Nil {
		return TenantInstallation{}, ErrInvalidTenantResource
	}
	payload := ""
	if target.UID != nil {
		payload = "uid\x00" + target.UID.String()
	} else {
		identity := target.Identity
		if !validTenantResourceText(identity.Namespace, 128) || strings.EqualFold(identity.Namespace, "legacy") ||
			!validTenantResourceText(identity.ExternalID, 512) {
			return TenantInstallation{}, ErrInvalidTenantResource
		}
		payload = "identity\x00" + identity.Namespace + "\x00" + identity.ExternalID
	}
	return s.changeInstallationOwnership(ctx, tenantID, deviceID, change, &target, payload)
}

func (s *PostgresStore) LogoutInstallation(ctx context.Context, tenantID, deviceID uuid.UUID, change InstallationOwnershipChange) (TenantInstallation, error) {
	return s.changeInstallationOwnership(ctx, tenantID, deviceID, change, nil, "logout")
}

func (s *PostgresStore) changeInstallationOwnership(ctx context.Context, tenantID, deviceID uuid.UUID, change InstallationOwnershipChange, target *InstallationIdentification, payload string) (TenantInstallation, error) {
	if deviceID == uuid.Nil || change.OperationID == uuid.Nil || change.ExpectedOwnershipVersion < 1 {
		return TenantInstallation{}, ErrInvalidTenantResource
	}
	digest := sha256.Sum256(fmt.Appendf(nil, "%d\x00%s", change.ExpectedOwnershipVersion, payload))
	for ctx.Err() == nil {
		device, err := s.applyInstallationOwnership(ctx, tenantID, deviceID, change, target, digest[:])
		if !errors.Is(err, errRetryInstallationOwnership) {
			return device, err
		}
	}
	return TenantInstallation{}, ctx.Err()
}

func (s *PostgresStore) applyInstallationOwnership(ctx context.Context, tenantID, deviceID uuid.UUID, change InstallationOwnershipChange, target *InstallationIdentification, digest []byte) (TenantInstallation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return TenantInstallation{}, fmt.Errorf("begin installation ownership change: %w", err)
	}
	defer tx.Rollback()

	var owner uuid.UUID
	var retired bool
	err = tx.QueryRowContext(ctx, `
		SELECT user_uid, retired_at IS NOT NULL FROM tenant_devices WHERE tenant_id = $1 AND id = $2
	`, tenantID, deviceID).Scan(&owner, &retired)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantInstallation{}, Errors.NotFound
	}
	if err != nil {
		return TenantInstallation{}, fmt.Errorf("read installation owner: %w", err)
	}
	if retired {
		return TenantInstallation{}, ErrTenantInstallationRetired
	}

	var targetUID uuid.UUID
	if target != nil {
		if target.UID != nil {
			targetUID = *target.UID
		} else {
			targetUID, err = resolveOwnershipIdentity(ctx, tx, tenantID, target.Identity)
			if err != nil {
				return TenantInstallation{}, err
			}
		}
	}

	kinds, err := lockInstallationUsers(ctx, tx, tenantID, owner, targetUID)
	if err != nil {
		return TenantInstallation{}, err
	}

	device := TenantInstallation{ID: deviceID}
	var latest uuid.NullUUID
	var origin string
	err = tx.QueryRowContext(ctx, `
		SELECT user_uid, ownership_version, last_ownership_operation_id, retired_at IS NOT NULL, origin
		FROM tenant_devices WHERE tenant_id = $1 AND id = $2 FOR UPDATE
	`, tenantID, deviceID).Scan(&device.UID, &device.OwnershipVersion, &latest, &retired, &origin)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantInstallation{}, Errors.NotFound
	}
	if err != nil {
		return TenantInstallation{}, fmt.Errorf("lock installation owner: %w", err)
	}
	if retired {
		return TenantInstallation{}, ErrTenantInstallationRetired
	}
	if origin != "registered" {
		return TenantInstallation{}, Errors.NotFound
	}
	if owner != device.UID {
		return TenantInstallation{}, errRetryInstallationOwnership
	}

	var priorDigest []byte
	var priorVersion int64
	err = tx.QueryRowContext(ctx, `
		SELECT request_digest, ownership_version FROM tenant_device_ownership_operations
		WHERE tenant_id = $1 AND device_id = $2 AND operation_id = $3
	`, tenantID, deviceID, change.OperationID).Scan(&priorDigest, &priorVersion)
	if err == nil {
		if latest.Valid && latest.UUID == change.OperationID && priorVersion == device.OwnershipVersion && bytes.Equal(priorDigest, digest) {
			return device, nil
		}
		return TenantInstallation{}, ErrInstallationOwnershipConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TenantInstallation{}, fmt.Errorf("read installation ownership operation: %w", err)
	}
	if change.ExpectedOwnershipVersion != device.OwnershipVersion {
		return TenantInstallation{}, ErrInstallationOwnershipConflict
	}

	nextUID, promoted, err := chooseInstallationOwner(ctx, tx, tenantID, owner, targetUID, kinds, target)
	if err != nil {
		return TenantInstallation{}, err
	}
	device.UID = nextUID
	if target == nil || nextUID != owner || promoted {
		device.OwnershipVersion++
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tenant_device_ownership_operations (tenant_id, device_id, operation_id, request_digest, ownership_version)
		VALUES ($1, $2, $3, $4, $5)
	`, tenantID, deviceID, change.OperationID, digest, device.OwnershipVersion); err != nil {
		return TenantInstallation{}, fmt.Errorf("record installation ownership operation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tenant_devices SET user_uid = $3, ownership_version = $4, last_ownership_operation_id = $5
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, deviceID, device.UID, device.OwnershipVersion, change.OperationID); err != nil {
		return TenantInstallation{}, fmt.Errorf("update installation ownership: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TenantInstallation{}, fmt.Errorf("commit installation ownership change: %w", err)
	}
	return device, nil
}

func resolveOwnershipIdentity(ctx context.Context, tx *sql.Tx, tenantID uuid.UUID, identity *TenantUserIdentity) (uuid.UUID, error) {
	var uid uuid.UUID
	err := tx.QueryRowContext(ctx, `
		SELECT user_uid FROM tenant_user_identities WHERE tenant_id = $1 AND namespace = $2 AND external_id = $3
	`, tenantID, identity.Namespace, identity.ExternalID).Scan(&uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("resolve installation user identity: %w", err)
	}
	return uid, nil
}

func lockInstallationUsers(ctx context.Context, tx *sql.Tx, tenantID, owner, target uuid.UUID) (map[uuid.UUID]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT uid, kind FROM tenant_users WHERE tenant_id = $1 AND uid IN ($2, $3) ORDER BY uid FOR UPDATE
	`, tenantID, owner, target)
	if err != nil {
		return nil, fmt.Errorf("lock installation users: %w", err)
	}
	defer rows.Close()
	kinds := map[uuid.UUID]string{}
	for rows.Next() {
		var uid uuid.UUID
		var kind string
		if err := rows.Scan(&uid, &kind); err != nil {
			return nil, fmt.Errorf("read locked installation user: %w", err)
		}
		kinds[uid] = kind
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read locked installation users: %w", err)
	}
	return kinds, nil
}

func chooseInstallationOwner(ctx context.Context, tx *sql.Tx, tenantID, owner, targetUID uuid.UUID, kinds map[uuid.UUID]string, target *InstallationIdentification) (uuid.UUID, bool, error) {
	if kinds[owner] == "" {
		return uuid.Nil, false, Errors.NotFound
	}
	if target == nil {
		if kinds[owner] == "anonymous" {
			return owner, false, nil
		}
		uid, err := createInstallationOwner(ctx, tx, tenantID, "anonymous")
		return uid, false, err
	}
	if targetUID != uuid.Nil {
		if target.Identity != nil {
			resolved, err := resolveOwnershipIdentity(ctx, tx, tenantID, target.Identity)
			if err != nil {
				return uuid.Nil, false, err
			}
			if resolved != targetUID {
				return uuid.Nil, false, errRetryInstallationOwnership
			}
		}
		if kinds[targetUID] == "" {
			return uuid.Nil, false, Errors.NotFound
		}
		if kinds[targetUID] == "anonymous" {
			return uuid.Nil, false, ErrInstallationOwnershipConflict
		}
		return targetUID, false, nil
	}

	uid := owner
	promote := kinds[owner] == "anonymous"
	if promote {
		if _, err := tx.ExecContext(ctx, `UPDATE tenant_users SET kind = 'identified' WHERE tenant_id = $1 AND uid = $2`, tenantID, uid); err != nil {
			return uuid.Nil, false, fmt.Errorf("promote installation user: %w", err)
		}
	} else {
		var err error
		uid, err = createInstallationOwner(ctx, tx, tenantID, "identified")
		if err != nil {
			return uuid.Nil, false, err
		}
	}
	err := tx.QueryRowContext(ctx, `
		INSERT INTO tenant_user_identities (tenant_id, user_uid, namespace, external_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, namespace, external_id) DO NOTHING RETURNING user_uid
	`, tenantID, uid, target.Identity.Namespace, target.Identity.ExternalID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, errRetryInstallationOwnership
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("assign installation user identity: %w", err)
	}
	return uid, promote, nil
}

func createInstallationOwner(ctx context.Context, tx *sql.Tx, tenantID uuid.UUID, kind string) (uuid.UUID, error) {
	uid, err := uuid.NewRandom()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generate installation owner UID: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tenant_users (tenant_id, uid, kind) VALUES ($1, $2, $3)`, tenantID, uid, kind); err != nil {
		return uuid.Nil, fmt.Errorf("create installation owner: %w", err)
	}
	return uid, nil
}
