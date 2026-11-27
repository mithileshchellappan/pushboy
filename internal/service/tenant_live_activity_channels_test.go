package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/config"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func TestTenantLAChannelsRouteIsolation(t *testing.T) {
	dsn := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PUSHBOY_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	t.Chdir("../..")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	store, err := storage.NewPostgresStore(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	providers, err := storage.NewTenantProviderStore(store, "test-key", `{"test-key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)
	if err != nil {
		t.Fatal(err)
	}

	bootstrap := func(kid string) (uuid.UUID, storage.TenantProvider) {
		t.Helper()
		var tenantID uuid.UUID
		err := store.BootstrapTenant(ctx, "scoped-provider-"+uuid.NewString(), func(tenant storage.Tenant, _ string) error {
			tenantID = tenant.ID
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, table := range []string{"tenant_live_activity_channels", "tenant_topics", "provider_configs", "tenant_api_keys"} {
				if _, err := db.Exec("DELETE FROM "+table+" WHERE tenant_id = $1", tenantID); err != nil {
					t.Error(err)
				}
			}
			if _, err := db.Exec("DELETE FROM tenants WHERE id = $1", tenantID); err != nil {
				t.Error(err)
			}
		})
		if _, err := store.CreateTenantTopic(ctx, tenantID, "race", "Race"); err != nil {
			t.Fatal(err)
		}
		route, err := providers.Create(ctx, tenantID, storage.TenantProviderNamespace{Provider: "apns", Environment: "production", ApplicationID: "com.example.Race"}, true, syntheticChannelCredentials(t, kid))
		if err != nil {
			t.Fatal(err)
		}
		return tenantID, route
	}
	tenantA, routeA := bootstrap("AAAA000001")
	tenantB, routeB := bootstrap("BBBB000002")
	if _, err := store.CreateTenantTopic(ctx, tenantA, "only-a", "Only A"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO tenant_live_activity_channels (tenant_id, activity_id, topic_id, provider_config_id, channel_id, provisioning_id, state)
		VALUES ($1, 'bad-route', 'race', $2, 'copied-channel', $3, 'active')`, tenantB, routeA.ID, uuid.New(),
	); err == nil {
		t.Fatal("PostgreSQL accepted a cross-tenant provider relationship")
	}
	if _, err := db.Exec(`
		INSERT INTO tenant_live_activity_channels (tenant_id, activity_id, topic_id, provider_config_id, channel_id, provisioning_id, state)
		VALUES ($1, 'bad-topic', 'only-a', $2, 'copied-channel', $3, 'active')`, tenantB, routeB.ID, uuid.New(),
	); err == nil {
		t.Fatal("PostgreSQL accepted a cross-tenant topic relationship")
	}

	newRoute := func(bundle string) storage.TenantProvider {
		t.Helper()
		route, err := providers.Create(ctx, tenantA, storage.TenantProviderNamespace{
			Provider: "apns", Environment: "production", ApplicationID: bundle,
		}, true, syntheticChannelCredentials(t, "CCCC000003"))
		if err != nil {
			t.Fatal(err)
		}
		return route
	}
	lostRoute := newRoute("com.example.Lost")
	refusedRoute := newRoute("com.example.Refused")
	retryRoute := newRoute("com.example.Retry")
	knownRoute := newRoute("com.example.Known")
	featureRoute := newRoute("com.example.Feature")

	type providerCall struct {
		method, path, kid, topic, channel string
	}
	calls := make(chan providerCall, 32)
	var expired atomic.Bool
	var retryRequests, absentResponses, confirmedDeletes atomic.Int32
	jwtHeaders := make(chan string, 2)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _, err := jwt.NewParser().ParseUnverified(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), jwt.MapClaims{})
		if err != nil {
			t.Errorf("parse synthetic provider bearer token: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		kid, _ := token.Header["kid"].(string)
		calls <- providerCall{r.Method, r.URL.Path, kid, r.Header.Get("apns-topic"), r.Header.Get("apns-channel-id")}
		if r.Method == http.MethodPost && kid == "BBBB000002" {
			jwtHeaders <- r.Header.Get("Authorization")
		}
		switch {
		case r.URL.Path == "/1/apps/com.example.Lost/channels":
			ch, err := store.GetTenantLAChannel(r.Context(), tenantA, "lost-provider-response")
			if err != nil || ch.State != storage.TenantLAChannelProvisioning || ch.ProvisioningID == uuid.Nil {
				t.Errorf("durable reservation before APNs create = %+v, %v", ch, err)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("simulate lost provider response: %v", err)
				return
			}
			_ = conn.Close()
		case r.Method == http.MethodPost && kid == "BBBB000002" && expired.CompareAndSwap(false, true):
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"reason":"ExpiredProviderToken"}`))
		case r.URL.Path == "/1/apps/com.example.Retry/channels":
			switch retryRequests.Add(1) {
			case 1:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"reason":"FeatureNotEnabled"}`))
			case 2:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"reason":"CannotCreateChannelConfig"}`))
			default:
				w.Header().Set("apns-channel-id", "retried-provider-channel")
				w.WriteHeader(http.StatusCreated)
			}
		case r.URL.Path == "/1/apps/com.example.Known/channels":
			w.Header().Set("apns-channel-id", "known-provider-channel")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete && r.URL.Path == "/1/apps/com.example.Feature/channels":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"reason":"FeatureNotEnabled"}`))
		case r.Method == http.MethodDelete && kid == "AAAA000001":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"reason":"InvalidProviderToken","secret":"https://provider.invalid/3/device/rejected-token"}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(kid, "BBBB"):
			switch absentResponses.Add(1) {
			case 1:
				ch, err := store.GetTenantLAChannel(r.Context(), tenantB, "race%day")
				if err != nil || ch.State != storage.TenantLAChannelDeleting || ch.ChannelID != "shared-provider-channel" {
					t.Errorf("durable deletion before APNs = %+v, %v", ch, err)
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("simulate lost delete response: %v", err)
					return
				}
				_ = conn.Close()
			case 2:
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"reason":"ChannelNotRegistered"}`))
			case 3:
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"reason":"InvalidProviderToken"}`))
			case 4:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"reason":"FeatureNotEnabled"}`))
			default:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"reason":"ChannelNotRegistered"}`))
			}
		case r.Method == http.MethodDelete && kid == "AAAA000003" && confirmedDeletes.Add(1) > 1:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"reason":"ChannelNotRegistered"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/1/apps/"):
			w.Header().Set("apns-channel-id", "shared-provider-channel")
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(stub.Close)
	routes := dispatch.NewTenantChannelRouter(providers, &config.Config{
		APNSEndpoint: stub.URL, APNSMaxConcurrent: 1, LAAPNSMaxConcurrent: 1,
	})
	channels := NewTenantLAChannels(store, routes)
	const activity = "race%day"
	expectProvider := func(expected providerCall) {
		t.Helper()
		select {
		case got := <-calls:
			if got != expected {
				t.Fatalf("provider call = %+v, want %+v", got, expected)
			}
		case <-ctx.Done():
			t.Fatal("provider call missing")
		}
	}
	request := func(activityID, topicID string, route storage.TenantProvider) storage.TenantLAChannel {
		return storage.TenantLAChannel{ActivityID: activityID, TopicID: topicID, ProviderConfigID: route.ID}
	}
	checkError := func(err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
		if err != nil && (strings.Contains(err.Error(), "rejected-token") || strings.Contains(err.Error(), stub.URL)) {
			t.Fatalf("error exposed raw provider details: %v", err)
		}
	}
	provision := func(tenant uuid.UUID, requested storage.TenantLAChannel, wantState storage.TenantLAChannelState, wantCreated bool) storage.TenantLAChannel {
		t.Helper()
		ch, created, err := channels.Provision(ctx, tenant, requested, true)
		if err != nil || created != wantCreated || ch.State != wantState {
			t.Fatalf("provision %q = %+v (created %t), %v; want %s (created %t)", requested.ActivityID, ch, created, err, wantState, wantCreated)
		}
		return ch
	}
	get := func(tenant uuid.UUID, activityID string) storage.TenantLAChannel {
		t.Helper()
		ch, err := channels.Get(ctx, tenant, activityID)
		checkError(err, nil)
		return ch
	}
	assertDeleting := func(tenant uuid.UUID, route storage.TenantProvider) {
		t.Helper()
		ch := get(tenant, activity)
		replay := provision(tenant, request(activity, "race", route), storage.TenantLAChannelDeleting, false)
		if ch.State != storage.TenantLAChannelDeleting || ch.ProviderConfigID != route.ID || ch != replay {
			t.Fatalf("deleting channel changed: %+v versus %+v", ch, replay)
		}
	}

	refused := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	refused.Close()
	refusedRoutes := dispatch.NewTenantChannelRouter(providers, &config.Config{APNSEndpoint: refused.URL})
	refusedService := NewTenantLAChannels(store, refusedRoutes)
	refusedRequest := request("refused-provider-connection", "race", refusedRoute)
	_, _, err = refusedService.Provision(ctx, tenantA, refusedRequest, true)
	checkError(err, ErrLAChannelProviderFailed)
	if strings.Contains(err.Error(), refused.URL) {
		t.Fatalf("error exposed refused provider endpoint: %v", err)
	}
	_, err = channels.Get(ctx, tenantA, refusedRequest.ActivityID)
	checkError(err, storage.Errors.NotFound)
	provision(tenantA, refusedRequest, storage.TenantLAChannelActive, true)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Refused/channels", kid: "CCCC000003"})

	requestA, requestB := request(activity, "race", routeA), request(activity, "race", routeB)
	_, _, err = channels.Provision(ctx, tenantA, requestA, false)
	checkError(err, storage.ErrInvalidTenantResource)
	_, _, err = channels.Provision(ctx, tenantB, request(activity, "only-a", routeB), true)
	checkError(err, storage.Errors.NotFound)
	_, _, err = channels.Provision(ctx, tenantB, requestA, true)
	checkError(err, storage.ErrProviderRouteUnavailable)

	type result struct {
		channel storage.TenantLAChannel
		created bool
		err     error
	}
	provisioned := make(chan result, 2)
	for range 2 {
		go func() {
			ch, created, err := channels.Provision(ctx, tenantA, requestA, true)
			provisioned <- result{ch, created, err}
		}()
	}
	a, b := <-provisioned, <-provisioned
	if a.err != nil || b.err != nil || a.created == b.created || a.channel.ProvisioningID != b.channel.ProvisioningID {
		t.Fatalf("concurrent provision results = %+v, %+v", a, b)
	}
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Race/channels", kid: "AAAA000001"})
	_, _, err = channels.Provision(ctx, tenantA, request(activity, "only-a", routeA), true)
	checkError(err, storage.Errors.Conflict)
	_, err = channels.Get(ctx, tenantB, activity)
	checkError(err, storage.Errors.NotFound)
	checkError(channels.Delete(ctx, tenantB, activity), storage.Errors.NotFound)
	provision(tenantB, requestB, storage.TenantLAChannelActive, true)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Race/channels", kid: "BBBB000002"})
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Race/channels", kid: "BBBB000002"})
	if first, second := <-jwtHeaders, <-jwtHeaders; first == second {
		t.Fatal("expired JWT was reused")
	}
	if route, err := providers.Get(ctx, tenantB, routeB.ID); err != nil || !route.Ready {
		t.Fatalf("expired JWT marked route unready: %+v, %v", route, err)
	}

	lostRequest := request("lost-provider-response", "race", lostRoute)
	uncertain := provision(tenantA, lostRequest, storage.TenantLAChannelAmbiguous, false)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Lost/channels", kid: "CCCC000003"})
	if uncertain.ProvisioningID == uuid.Nil || uncertain.ChannelID != "" {
		t.Fatalf("lost response = %+v", uncertain)
	}
	if replay := provision(tenantA, lostRequest, storage.TenantLAChannelAmbiguous, false); replay != uncertain {
		t.Fatalf("lost-response retry changed the reservation: %+v", replay)
	}
	get(tenantA, lostRequest.ActivityID)
	_, err = channels.Get(ctx, tenantB, lostRequest.ActivityID)
	checkError(err, storage.Errors.NotFound)

	retryRequest := request("safe-provider-retry", "race", retryRoute)
	for range 2 {
		_, _, err := channels.Provision(ctx, tenantA, retryRequest, true)
		checkError(err, ErrLAChannelProviderFailed)
		expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Retry/channels", kid: "CCCC000003"})
		_, err = channels.Get(ctx, tenantA, retryRequest.ActivityID)
		checkError(err, storage.Errors.NotFound)
	}
	provision(tenantA, retryRequest, storage.TenantLAChannelActive, true)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Retry/channels", kid: "CCCC000003"})

	if _, err := db.Exec(`
		CREATE FUNCTION fail_test_channel_completion() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'synthetic channel completion failure'; END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER fail_test_channel_completion
		BEFORE UPDATE ON tenant_live_activity_channels FOR EACH ROW
		WHEN (NEW.activity_id = 'known-provider-id' AND NEW.state = 'active')
		EXECUTE FUNCTION fail_test_channel_completion();
	`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DROP TRIGGER fail_test_channel_completion ON tenant_live_activity_channels; DROP FUNCTION fail_test_channel_completion()`); err != nil {
			t.Error(err)
		}
	})
	knownRequest := request("known-provider-id", "race", knownRoute)
	provision(tenantA, knownRequest, storage.TenantLAChannelAmbiguous, false)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Known/channels", kid: "CCCC000003"})
	persisted := get(tenantA, knownRequest.ActivityID)
	if persisted.State != storage.TenantLAChannelAmbiguous || persisted.ChannelID != "known-provider-channel" {
		t.Fatalf("known provider channel ID was lost: %+v", persisted)
	}
	provision(tenantA, knownRequest, storage.TenantLAChannelAmbiguous, false)

	featureRequest := request("feature-disabled", "race", featureRoute)
	provision(tenantA, featureRequest, storage.TenantLAChannelActive, true)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Feature/channels", kid: "CCCC000003"})
	checkError(channels.Delete(ctx, tenantA, featureRequest.ActivityID), nil)
	expectProvider(providerCall{method: http.MethodDelete, path: "/1/apps/com.example.Feature/channels", kid: "CCCC000003", channel: "shared-provider-channel"})
	_, err = channels.Get(ctx, tenantA, featureRequest.ActivityID)
	checkError(err, storage.Errors.NotFound)

	refA := model.ProviderRef{TenantID: tenantA, RouteID: routeA.ID}
	refB := model.ProviderRef{TenantID: tenantB, RouteID: routeB.ID}
	cachedA, err := routes.Resolve(ctx, refA)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := routes.Resolve(ctx, refA); err != nil || again != cachedA {
		t.Fatalf("tenant channel client was not reused: %v", err)
	}
	if cachedB, err := routes.Resolve(ctx, refB); err != nil || cachedB == cachedA {
		t.Fatalf("another tenant reused the channel client: %v", err)
	}

	aInitialDelete := providerCall{method: http.MethodDelete, path: "/1/apps/com.example.Race/channels", kid: "AAAA000001", channel: "shared-provider-channel"}
	err = channels.Delete(ctx, tenantA, activity)
	checkError(err, ErrLAChannelProviderFailed)
	checkError(err, model.ErrProviderAuthorization)
	expectProvider(aInitialDelete)
	if restored := get(tenantA, activity); restored.State != storage.TenantLAChannelActive || restored.ChannelID != "shared-provider-channel" {
		t.Fatalf("definite authorization rejection did not restore the channel: %+v", restored)
	}

	if route, err := providers.Get(ctx, tenantA, routeA.ID); err != nil || route.Ready || route.ReadinessReason != "provider_authorization_failed" {
		t.Fatalf("authorization readiness = %+v, %v", route, err)
	}
	if _, err := routes.Resolve(ctx, refA); !errors.Is(err, storage.ErrProviderRouteUnavailable) {
		t.Fatalf("channel resolver reused failed credentials: %v", err)
	}
	if _, err := routes.Resolve(ctx, refB); err != nil {
		t.Fatalf("authorization failure affected another tenant: %v", err)
	}
	checkError(channels.Delete(ctx, tenantA, activity), storage.ErrProviderRouteUnavailable)
	disabled := false
	if _, err := providers.Update(ctx, tenantA, routeA.ID, 1, &disabled, nil); err != nil {
		t.Fatal(err)
	}
	if mapped := provision(tenantA, requestA, storage.TenantLAChannelActive, false); mapped.ChannelID != "shared-provider-channel" {
		t.Fatalf("disabled route replay = %+v", mapped)
	}
	_, _, err = channels.Provision(ctx, tenantA, request(activity, "only-a", routeA), true)
	checkError(err, storage.Errors.Conflict)
	_, _, err = channels.Provision(ctx, tenantA, requestB, true)
	checkError(err, storage.Errors.Conflict)

	bDelete := providerCall{method: http.MethodDelete, path: "/1/apps/com.example.Race/channels", kid: "BBBB000002", channel: "shared-provider-channel"}
	checkError(channels.Delete(ctx, tenantB, activity), ErrLAChannelProviderFailed)
	expectProvider(bDelete)
	assertDeleting(tenantB, routeB)
	firstDeletion := get(tenantB, activity)
	checkError(channels.Delete(ctx, tenantB, activity), ErrLAChannelProviderFailed)
	expectProvider(bDelete)
	if err := store.RestoreTenantLAChannel(ctx, tenantB, firstDeletion); err != nil {
		t.Fatal(err)
	}
	assertDeleting(tenantB, routeB)

	err = channels.Delete(ctx, tenantB, activity)
	checkError(err, ErrLAChannelProviderFailed)
	checkError(err, model.ErrProviderAuthorization)
	expectProvider(bDelete)
	assertDeleting(tenantB, routeB)
	if route, err := providers.Get(ctx, tenantB, routeB.ID); err != nil || route.ReadinessReason != "provider_authorization_failed" {
		t.Fatalf("route authorization failure = %+v, %v", route, err)
	}
	if _, err := providers.Update(ctx, tenantB, routeB.ID, 1, nil, syntheticChannelCredentials(t, "BBBB000004")); err != nil {
		t.Fatal(err)
	}
	bDelete.kid = "BBBB000004"
	// APNs confirms the broadcast feature is disabled and the old ID is absent.
	checkError(channels.Delete(ctx, tenantB, activity), nil)
	expectProvider(bDelete)
	_, err = channels.Get(ctx, tenantB, activity)
	checkError(err, storage.Errors.NotFound)
	if route, err := providers.Get(ctx, tenantB, routeB.ID); err != nil || !route.Ready {
		t.Fatalf("disabled broadcast marked current credentials unauthorized: %+v, %v", route, err)
	}
	if remaining := get(tenantA, activity); remaining.State != storage.TenantLAChannelActive || remaining.ChannelID != "shared-provider-channel" {
		t.Fatalf("tenant B deletion affected tenant A: %+v", remaining)
	}

	replacement := provision(tenantB, requestB, storage.TenantLAChannelActive, true)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Race/channels", kid: "BBBB000004"})
	if _, err := store.BeginTenantLAChannelDeletion(ctx, tenantB, replacement, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveTenantLAChannel(ctx, tenantB, firstDeletion); err != nil {
		t.Fatal(err)
	}
	assertDeleting(tenantB, routeB)
	checkError(channels.Delete(ctx, tenantB, activity), nil)
	expectProvider(bDelete)

	enabled := true
	if _, err := providers.Update(ctx, tenantA, routeA.ID, 1, &enabled, syntheticChannelCredentials(t, "AAAA000003")); err != nil {
		t.Fatal(err)
	}
	if rotated, err := routes.Resolve(ctx, refA); err != nil || rotated == cachedA {
		t.Fatalf("rotation did not replace the cached client: %v", err)
	}
	if err := providers.MarkAuthorizationFailed(ctx, refA, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TRIGGER fail_test_channel_removal
		BEFORE DELETE ON tenant_live_activity_channels FOR EACH ROW
		WHEN (OLD.activity_id = 'race%day' AND OLD.state = 'deleting')
		EXECUTE FUNCTION fail_test_channel_completion();
	`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS fail_test_channel_removal ON tenant_live_activity_channels`); err != nil {
			t.Error(err)
		}
	})
	aDelete := providerCall{method: http.MethodDelete, path: "/1/apps/com.example.Race/channels", kid: "AAAA000003", channel: "shared-provider-channel"}
	if err := channels.Delete(ctx, tenantA, activity); err == nil {
		t.Fatal("delete succeeded despite a database failure")
	}
	expectProvider(aDelete)
	assertDeleting(tenantA, routeA)
	if _, err := db.Exec(`DROP TRIGGER fail_test_channel_removal ON tenant_live_activity_channels`); err != nil {
		t.Fatal(err)
	}
	checkError(channels.Delete(ctx, tenantA, activity), nil)
	expectProvider(aDelete)
	_, err = channels.Get(ctx, tenantA, activity)
	checkError(err, storage.Errors.NotFound)

	if _, err := providers.Update(ctx, tenantA, routeA.ID, 2, &disabled, nil); err != nil {
		t.Fatal(err)
	}
	_, _, err = channels.Provision(ctx, tenantA, requestA, true)
	checkError(err, storage.ErrProviderRouteUnavailable)
	select {
	case got := <-calls:
		t.Fatalf("unexpected provider call: %+v", got)
	default:
	}
}

func syntheticChannelCredentials(t *testing.T, kid string) json.RawMessage {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := json.Marshal(storage.APNsCredentials{
		KeyID: kid, TeamID: "Z1Y2X3W4V5",
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return creds
}
