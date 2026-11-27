package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/apns"
	"github.com/mithileshchellappan/pushboy/internal/config"
	"github.com/mithileshchellappan/pushboy/internal/fcm"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type TenantChannelRouter struct {
	providers   *storage.TenantProviderStore
	config      *config.Config
	mu          sync.Mutex
	clients     map[model.ProviderRef]*TenantChannelClient
	pushClients map[model.ProviderRef]tenantPushClient
}

type TenantChannelClient struct {
	*apns.Client
	version int64
}

func NewTenantChannelRouter(providers *storage.TenantProviderStore, cfg *config.Config) *TenantChannelRouter {
	return &TenantChannelRouter{
		providers:   providers,
		config:      cfg,
		clients:     make(map[model.ProviderRef]*TenantChannelClient),
		pushClients: make(map[model.ProviderRef]tenantPushClient),
	}
}

func (r *TenantChannelRouter) Resolve(ctx context.Context, ref model.ProviderRef) (*TenantChannelClient, error) {
	if r == nil || r.providers == nil || r.config == nil || ref.TenantID == uuid.Nil || ref.RouteID == uuid.Nil {
		return nil, storage.ErrProviderRouteUnavailable
	}
	version, err := r.providers.DispatchVersion(ctx, ref, model.APNS)
	if err != nil {
		return nil, storage.ErrProviderRouteUnavailable
	}
	r.mu.Lock()
	cached := r.clients[ref]
	r.mu.Unlock()
	if cached != nil && cached.version == version {
		return cached, nil
	}

	namespace, credential, err := r.providers.DispatchCredentials(ctx, ref, model.APNS, version)
	if err != nil {
		return nil, storage.ErrProviderRouteUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cached := r.clients[ref]; cached != nil {
		switch {
		case cached.version == version:
			return cached, nil
		case cached.version > version:
			return nil, storage.ErrProviderRouteUnavailable
		}
	}
	cfg := r.config
	client, err := r.newAPNsClient(namespace, credential, cfg.LAAPNSClientPool, cfg.LAAPNSMaxConcurrent)
	if err != nil {
		return nil, err
	}
	resolved := &TenantChannelClient{Client: client, version: version}
	r.clients[ref] = resolved
	return resolved, nil
}

func (c *TenantChannelClient) CredentialVersion() int64 {
	return c.version
}

func (r *TenantChannelRouter) ObserveResult(ctx context.Context, ref model.ProviderRef, version int64, err error) error {
	if errors.Is(err, model.ErrProviderAuthorization) {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if saveErr := r.providers.MarkAuthorizationFailed(saveCtx, ref, version); saveErr != nil {
			return fmt.Errorf("%w: could not record route readiness", model.ErrProviderAuthorization)
		}
	}
	return err
}

type TenantAdmission func(context.Context, model.ProviderRef, int64) error

type admittedPushDispatcher interface {
	SendWithAdmission(context.Context, string, *model.NotificationPayload, model.ProviderAdmission) error
}

type tenantPushClient struct {
	dispatcher admittedPushDispatcher
	version    int64
}

func (r *TenantChannelRouter) SendPush(ctx context.Context, ref model.ProviderRef, platform model.Platform, token string, payload *model.NotificationPayload, admit TenantAdmission) error {
	if admit == nil {
		return model.ErrProviderAdmissionCheck
	}
	client, err := r.resolvePush(ctx, ref, platform)
	if err != nil {
		return err
	}
	err = client.dispatcher.SendWithAdmission(ctx, token, payload, func(ctx context.Context) error {
		return admit(ctx, ref, client.version)
	})
	return r.ObserveResult(ctx, ref, client.version, err)
}

func (r *TenantChannelRouter) resolvePush(ctx context.Context, ref model.ProviderRef, platform model.Platform) (tenantPushClient, error) {
	if r == nil || r.providers == nil || r.config == nil || ref.TenantID == uuid.Nil || ref.RouteID == uuid.Nil ||
		(platform != model.APNS && platform != model.FCM) {
		return tenantPushClient{}, storage.ErrProviderRouteUnavailable
	}
	version, err := r.providers.DispatchVersion(ctx, ref, platform)
	if err != nil {
		return tenantPushClient{}, pushRouteError(err)
	}
	r.mu.Lock()
	cached := r.pushClients[ref]
	r.mu.Unlock()
	if cached.version == version && cached.dispatcher != nil {
		return cached, nil
	}

	namespace, credential, err := r.providers.DispatchCredentials(ctx, ref, platform, version)
	if err != nil {
		return tenantPushClient{}, pushRouteError(err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if cached := r.pushClients[ref]; cached.dispatcher != nil {
		switch {
		case cached.version == version:
			return cached, nil
		case cached.version > version:
			return tenantPushClient{}, storage.ErrProviderRouteUnavailable
		}
	}
	cfg := r.config
	var client admittedPushDispatcher
	switch platform {
	case model.APNS:
		client, err = r.newAPNsClient(namespace, credential, cfg.APNSClientPool, cfg.APNSMaxConcurrent)
	case model.FCM:
		client, err = fcm.NewClientForProject(ctx, credential, namespace.ApplicationID,
			cfg.FCMClientPool, cfg.FCMMaxConcurrent, cfg.MaxRetryNotification)
	}
	if err != nil {
		return tenantPushClient{}, storage.ErrProviderRouteUnavailable
	}
	resolved := tenantPushClient{dispatcher: client, version: version}
	r.pushClients[ref] = resolved
	return resolved, nil
}

func pushRouteError(err error) error {
	if errors.Is(err, storage.ErrProviderRouteUnavailable) {
		return storage.ErrProviderRouteUnavailable
	}
	return model.ErrProviderAdmissionCheck
}

func (r *TenantChannelRouter) newAPNsClient(namespace storage.TenantProviderNamespace, credential []byte, poolSize, maxConcurrent int) (*apns.Client, error) {
	var creds storage.APNsCredentials
	if err := json.Unmarshal(credential, &creds); err != nil {
		return nil, storage.ErrProviderRouteUnavailable
	}
	cfg := r.config
	return apns.NewClient([]byte(creds.PrivateKey), creds.KeyID, creds.TeamID, namespace.ApplicationID,
		namespace.Environment == "sandbox", cfg.APNSEndpoint, poolSize, maxConcurrent, cfg.MaxRetryNotification), nil
}
