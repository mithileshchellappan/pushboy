package service

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/apns"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type TenantLAChannels struct {
	store  *storage.PostgresStore
	routes *dispatch.TenantChannelRouter
}

func NewTenantLAChannels(store *storage.PostgresStore, routes *dispatch.TenantChannelRouter) *TenantLAChannels {
	return &TenantLAChannels{store: store, routes: routes}
}

func (s *TenantLAChannels) Provision(ctx context.Context, tenantID uuid.UUID, requested storage.TenantLAChannel, public bool) (storage.TenantLAChannel, bool, error) {
	if !public {
		return storage.TenantLAChannel{}, false, storage.ErrInvalidTenantResource
	}
	existing, err := s.store.MatchTenantLAChannel(ctx, tenantID, requested)
	if !errors.Is(err, storage.Errors.NotFound) {
		return existing, false, err
	}
	ref := model.ProviderRef{TenantID: tenantID, RouteID: requested.ProviderConfigID}
	client, err := s.routes.Resolve(ctx, ref)
	if err != nil {
		return storage.TenantLAChannel{}, false, err
	}
	channel, reserved, err := s.store.ReserveTenantLAChannel(ctx, tenantID, requested, client.CredentialVersion())
	if err != nil || !reserved {
		return channel, false, err
	}

	createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), laChannelDeleteTimeout)
	defer cancel()
	if err := s.store.AdmitTenantLAChannelOperation(createCtx, tenantID, channel, client.CredentialVersion()); err != nil {
		return s.releaseProvision(ctx, tenantID, channel, err)
	}
	channelID, err := client.CreateLiveActivityChannel(createCtx)

	if errors.Is(err, apns.ErrChannelCreateRejected) {
		providerErr := s.routes.ObserveResult(createCtx, ref, client.CredentialVersion(), err)
		return s.releaseProvision(ctx, tenantID, channel, tenantLAChannelProviderError(providerErr))
	}

	if err != nil {
		return s.uncertainProvision(ctx, tenantID, channel, channelID), false, nil
	}

	for range 2 {
		saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		active, err := s.store.RecordTenantLAChannel(saveCtx, tenantID, channel, channelID, storage.TenantLAChannelActive)
		saveCancel()
		if err == nil {
			return active, true, nil
		}
	}
	recovered := s.uncertainProvision(ctx, tenantID, channel, channelID)
	return recovered, recovered.State == storage.TenantLAChannelActive, nil
}

func (s *TenantLAChannels) releaseProvision(ctx context.Context, tenantID uuid.UUID, channel storage.TenantLAChannel, result error) (storage.TenantLAChannel, bool, error) {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := s.store.ReleaseTenantLAChannel(saveCtx, tenantID, channel); err != nil {
		return s.uncertainProvision(ctx, tenantID, channel, ""), false, nil
	}
	return storage.TenantLAChannel{}, false, result
}

func (s *TenantLAChannels) uncertainProvision(ctx context.Context, tenantID uuid.UUID, channel storage.TenantLAChannel, channelID string) storage.TenantLAChannel {
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	recorded, err := s.store.RecordTenantLAChannel(saveCtx, tenantID, channel, channelID, storage.TenantLAChannelAmbiguous)
	if err == nil {
		return recorded
	}
	if current, err := s.store.GetTenantLAChannel(saveCtx, tenantID, channel.ActivityID); err == nil &&
		current.ProvisioningID == channel.ProvisioningID && current.State == storage.TenantLAChannelActive && current.ChannelID == channelID {
		return current
	}
	if channelID != "" {
		log.Printf("Could not record public APNs channel %q for tenant %s, route %s, activity %q, provisioning %s",
			channelID, tenantID, channel.ProviderConfigID, channel.ActivityID, channel.ProvisioningID)
	}
	channel.State = storage.TenantLAChannelAmbiguous
	channel.ChannelID = channelID
	return channel
}

func (s *TenantLAChannels) Get(ctx context.Context, tenantID uuid.UUID, activityID string) (storage.TenantLAChannel, error) {
	return s.store.GetTenantLAChannel(ctx, tenantID, activityID)
}

func (s *TenantLAChannels) Delete(ctx context.Context, tenantID uuid.UUID, activityID string) error {
	deleteCtx, cancel := laChannelDeleteContext(ctx)
	defer cancel()

	channel, err := s.store.GetTenantLAChannel(deleteCtx, tenantID, activityID)
	if err != nil {
		return err
	}
	if channel.State != storage.TenantLAChannelActive {
		return storage.Errors.Conflict
	}
	ref := model.ProviderRef{TenantID: tenantID, RouteID: channel.ProviderConfigID}
	client, err := s.routes.Resolve(deleteCtx, ref)
	if err != nil {
		return err
	}
	if err := s.store.AdmitTenantLAChannelOperation(deleteCtx, tenantID, channel, client.CredentialVersion()); err != nil {
		return err
	}
	if err := client.DeleteLiveActivityChannel(deleteCtx, channel.ChannelID); err != nil {
		return tenantLAChannelProviderError(s.routes.ObserveResult(deleteCtx, ref, client.CredentialVersion(), err))
	}
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(deleteCtx), 2*time.Second)
	defer saveCancel()
	return s.store.RemoveTenantLAChannel(saveCtx, tenantID, channel)
}

func tenantLAChannelProviderError(err error) error {
	if errors.Is(err, model.ErrProviderAuthorization) {
		return errors.Join(ErrLAChannelProviderFailed, err)
	}
	return ErrLAChannelProviderFailed
}
