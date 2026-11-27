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
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type TenantChannelRouter struct {
	providers *storage.TenantProviderStore
	config    *config.Config
	mu        sync.Mutex
	clients   map[model.ProviderRef]*TenantChannelClient
}

type TenantChannelClient struct {
	*apns.Client
	version int64
}

func NewTenantChannelRouter(providers *storage.TenantProviderStore, cfg *config.Config) *TenantChannelRouter {
	return &TenantChannelRouter{providers: providers, config: cfg, clients: make(map[model.ProviderRef]*TenantChannelClient)}
}

func (r *TenantChannelRouter) Resolve(ctx context.Context, ref model.ProviderRef) (*TenantChannelClient, error) {
	if ref.TenantID == uuid.Nil || ref.RouteID == uuid.Nil {
		return nil, storage.ErrProviderRouteUnavailable
	}
	version, err := r.providers.ChannelCredentialVersion(ctx, ref)
	if err != nil {
		return nil, storage.ErrProviderRouteUnavailable
	}
	r.mu.Lock()
	cached := r.clients[ref]
	r.mu.Unlock()
	if cached != nil && cached.version == version {
		return cached, nil
	}

	namespace, credential, err := r.providers.ChannelCredentials(ctx, ref, version)
	if err != nil {
		return nil, storage.ErrProviderRouteUnavailable
	}
	var creds storage.APNsCredentials
	if err := json.Unmarshal(credential, &creds); err != nil {
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
	client := apns.NewClient([]byte(creds.PrivateKey), creds.KeyID, creds.TeamID, namespace.ApplicationID,
		namespace.Environment == "sandbox", cfg.APNSEndpoint, cfg.LAAPNSClientPool, cfg.LAAPNSMaxConcurrent, cfg.MaxRetryNotification)
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
