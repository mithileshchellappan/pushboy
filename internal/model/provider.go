package model

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

var (
	ErrProviderAuthorization    = errors.New("provider authorization failed")
	ErrProviderUnavailable      = errors.New("provider unavailable before submission")
	ErrProviderRouteUnavailable = errors.New("tenant provider route unavailable")
	ErrProviderAdmission        = errors.New("provider delivery is no longer eligible")
	ErrProviderAdmissionCheck   = errors.New("provider delivery eligibility check failed")
	ErrProviderInvalidToken     = errors.New("provider rejected the registration token")
	ErrProviderFailed           = errors.New("provider request failed")
	ErrProviderOutcomeUnknown   = errors.New("provider delivery outcome unknown")
)

type ProviderRef struct {
	TenantID uuid.UUID
	RouteID  uuid.UUID
}

type ProviderAdmission func(context.Context) error

func (admit ProviderAdmission) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if admit == nil {
		return nil
	}
	if err := admit(ctx); err != nil {
		if errors.Is(err, ErrProviderAdmission) {
			return ErrProviderAdmission
		}
		return ErrProviderAdmissionCheck
	}
	return ctx.Err()
}

func SafeProviderError(err error) error {
	if err == nil {
		return nil
	}
	for _, safe := range []error{
		ErrProviderOutcomeUnknown, ErrProviderAdmission, ErrProviderAdmissionCheck, ErrProviderAuthorization,
		ErrProviderRouteUnavailable, ErrProviderUnavailable, ErrProviderInvalidToken,
	} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrProviderUnavailable
	}
	return ErrProviderFailed
}
