package model

import (
	"errors"

	"github.com/google/uuid"
)

var ErrProviderAuthorization = errors.New("provider authorization failed")

type ProviderRef struct {
	TenantID uuid.UUID
	RouteID  uuid.UUID
}
