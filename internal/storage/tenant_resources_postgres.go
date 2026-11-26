package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type TenantUser struct {
	UID  uuid.UUID `json:"uid"`
	Kind string    `json:"kind"`
}

type TenantUserIdentity struct {
	Namespace  string `json:"namespace"`
	ExternalID string `json:"externalId"`
}

type TenantTopic struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var ErrInvalidTenantResource = errors.New("invalid tenant resource")

func validTenantResourceText(value string, maxRunes int) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) &&
		strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= maxRunes
}

func (s *PostgresStore) CreateTenantUser(ctx context.Context, tenantID uuid.UUID, identity *TenantUserIdentity) (TenantUser, error) {
	kind := "anonymous"
	if identity != nil {
		if !validTenantResourceText(identity.Namespace, 128) || strings.EqualFold(identity.Namespace, "legacy") {
			return TenantUser{}, fmt.Errorf("%w: invalid identity namespace", ErrInvalidTenantResource)
		}
		if !validTenantResourceText(identity.ExternalID, 512) {
			return TenantUser{}, fmt.Errorf("%w: invalid external ID", ErrInvalidTenantResource)
		}
		kind = "identified"
	}

	uid, err := uuid.NewRandom()
	if err != nil {
		return TenantUser{}, fmt.Errorf("generate user UID: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TenantUser{}, fmt.Errorf("begin tenant user creation: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO tenant_users (tenant_id, uid, kind) VALUES ($1, $2, $3)`, tenantID, uid, kind); err != nil {
		return TenantUser{}, fmt.Errorf("create tenant user: %w", err)
	}
	if identity != nil {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO tenant_user_identities (tenant_id, user_uid, namespace, external_id)
			VALUES ($1, $2, $3, $4)`, tenantID, uid, identity.Namespace, identity.ExternalID)
		if err != nil {
			var pqErr *pq.Error
			if errors.As(err, &pqErr) && pqErr.Code == "23505" {
				return TenantUser{}, Errors.AlreadyExists
			}
			return TenantUser{}, fmt.Errorf("create tenant user identity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return TenantUser{}, fmt.Errorf("commit tenant user creation: %w", err)
	}

	return TenantUser{UID: uid, Kind: kind}, nil
}

func (s *PostgresStore) GetTenantUser(ctx context.Context, tenantID, uid uuid.UUID) (TenantUser, error) {
	user := TenantUser{UID: uid}
	err := s.db.QueryRowContext(ctx, `SELECT kind FROM tenant_users WHERE tenant_id = $1 AND uid = $2`, tenantID, uid).Scan(&user.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantUser{}, Errors.NotFound
	}
	if err != nil {
		return TenantUser{}, fmt.Errorf("get tenant user: %w", err)
	}
	return user, nil
}

func (s *PostgresStore) CreateTenantTopic(ctx context.Context, tenantID uuid.UUID, id, name string) (TenantTopic, error) {
	if !validTenantResourceText(id, 128) || id == "." || id == ".." {
		return TenantTopic{}, fmt.Errorf("%w: invalid topic ID", ErrInvalidTenantResource)
	}
	if !validTenantResourceText(name, 255) {
		return TenantTopic{}, fmt.Errorf("%w: invalid topic name", ErrInvalidTenantResource)
	}

	_, err := s.db.ExecContext(ctx, `INSERT INTO tenant_topics (tenant_id, id, name) VALUES ($1, $2, $3)`, tenantID, id, name)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" {
			return TenantTopic{}, Errors.AlreadyExists
		}
		return TenantTopic{}, fmt.Errorf("create tenant topic: %w", err)
	}
	return TenantTopic{ID: id, Name: name}, nil
}

func (s *PostgresStore) GetTenantTopic(ctx context.Context, tenantID uuid.UUID, id string) (TenantTopic, error) {
	topic := TenantTopic{ID: id}
	err := s.db.QueryRowContext(ctx, `SELECT name FROM tenant_topics WHERE tenant_id = $1 AND id = $2`, tenantID, id).Scan(&topic.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantTopic{}, Errors.NotFound
	}
	if err != nil {
		return TenantTopic{}, fmt.Errorf("get tenant topic: %w", err)
	}
	return topic, nil
}
