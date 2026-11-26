package server

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
	"github.com/mithileshchellappan/pushboy/internal/service"
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

	bootstrap := func(kid string) (uuid.UUID, string, storage.TenantProvider) {
		t.Helper()
		var tenantID uuid.UUID
		var bearer string
		err := store.BootstrapTenant(ctx, "scoped-provider-"+uuid.NewString(), func(tenant storage.Tenant, key string) error {
			tenantID, bearer = tenant.ID, key
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
		return tenantID, bearer, route
	}
	tenantA, keyA, routeA := bootstrap("AAAA000001")
	tenantB, keyB, routeB := bootstrap("BBBB000002")
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
	channelService := service.NewTenantLAChannels(store, routes)
	httpRouter := New(nil, nil, nil, store, providers, channelService).setupRouter()
	const channelPath = "/v2/live-activity/channels/race%25day"
	sendTo := func(handler http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	send := func(method, path, key, body string) *httptest.ResponseRecorder {
		return sendTo(httpRouter, method, path, key, body)
	}
	request := func(method, path, key, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		res := send(method, path, key, body)
		if res.Code != want || strings.Contains(res.Body.String(), "rejected-token") {
			t.Fatalf("%s %s: got %d, want %d, body %s", method, path, res.Code, want, res.Body.String())
		}
		return res
	}
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
	publicChannel := func(topic string, routeID uuid.UUID) string {
		return `{"public":true,"topicId":"` + topic + `","providerConfigId":"` + routeID.String() + `"}`
	}
	assertDeleting := func(key string, routeID uuid.UUID) {
		t.Helper()
		for _, response := range []*httptest.ResponseRecorder{
			request(http.MethodGet, channelPath, key, "", http.StatusOK),
			request(http.MethodPut, channelPath, key, publicChannel("race", routeID), http.StatusConflict),
		} {
			var ch storage.TenantLAChannel
			if err := json.Unmarshal(response.Body.Bytes(), &ch); err != nil || ch.State != storage.TenantLAChannelDeleting ||
				ch.ProviderConfigID != routeID || strings.Contains(response.Body.String(), "channelId") {
				t.Fatalf("deleting channel exposed as usable: %s, %v", response.Body.String(), err)
			}
		}
	}

	refused := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	refused.Close()
	refusedRoutes := dispatch.NewTenantChannelRouter(providers, &config.Config{APNSEndpoint: refused.URL})
	refusedServer := New(nil, nil, nil, store, providers, service.NewTenantLAChannels(store, refusedRoutes)).setupRouter()
	refusedPath := "/v2/live-activity/channels/refused-provider-connection"
	refusedBody := publicChannel("race", refusedRoute.ID)
	response := sendTo(refusedServer, http.MethodPut, refusedPath, keyA, refusedBody)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), refused.URL) {
		t.Fatalf("refused APNs connection returned %d %s", response.Code, response.Body.String())
	}
	request(http.MethodGet, refusedPath, keyA, "", http.StatusNotFound)
	request(http.MethodPut, refusedPath, keyA, refusedBody, http.StatusCreated)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Refused/channels", kid: "CCCC000003"})

	request(http.MethodPut, channelPath, keyA, strings.Replace(publicChannel("race", routeA.ID), "true", "false", 1), http.StatusBadRequest)
	request(http.MethodPut, channelPath, keyB, publicChannel("only-a", routeB.ID), http.StatusNotFound)
	request(http.MethodPut, channelPath, keyB, publicChannel("race", routeA.ID), http.StatusServiceUnavailable)
	provisioned := make(chan int, 2)
	for range 2 {
		go func() {
			provisioned <- send(http.MethodPut, channelPath, keyA, publicChannel("race", routeA.ID)).Code
		}()
	}
	a, b := <-provisioned, <-provisioned
	if !(a == http.StatusCreated && (b == http.StatusOK || b == http.StatusConflict) ||
		b == http.StatusCreated && (a == http.StatusOK || a == http.StatusConflict)) {
		t.Fatalf("concurrent provision statuses = %d, %d", a, b)
	}
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Race/channels", kid: "AAAA000001"})
	request(http.MethodPut, channelPath, keyA, publicChannel("only-a", routeA.ID), http.StatusConflict)
	request(http.MethodGet, channelPath, keyB, "", http.StatusNotFound)
	request(http.MethodDelete, channelPath, keyB, "", http.StatusNotFound)
	request(http.MethodPut, channelPath, keyB, publicChannel("race", routeB.ID), http.StatusCreated)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Race/channels", kid: "BBBB000002"})
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Race/channels", kid: "BBBB000002"})
	if first, second := <-jwtHeaders, <-jwtHeaders; first == second {
		t.Fatal("expired JWT was reused")
	}
	if route, err := providers.Get(ctx, tenantB, routeB.ID); err != nil || !route.Ready {
		t.Fatalf("expired JWT marked route unready: %+v, %v", route, err)
	}

	lostPath := "/v2/live-activity/channels/lost-provider-response"
	lost := request(http.MethodPut, lostPath, keyA, publicChannel("race", lostRoute.ID), http.StatusConflict)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Lost/channels", kid: "CCCC000003"})
	var uncertain storage.TenantLAChannel
	if err := json.Unmarshal(lost.Body.Bytes(), &uncertain); err != nil || uncertain.State != storage.TenantLAChannelAmbiguous ||
		uncertain.ProvisioningID == uuid.Nil || uncertain.ChannelID != "" {
		t.Fatalf("lost response status = %+v, %v", uncertain, err)
	}
	if replay := request(http.MethodPut, lostPath, keyA, publicChannel("race", lostRoute.ID), http.StatusConflict); replay.Body.String() != lost.Body.String() {
		t.Fatalf("lost-response retry changed the reservation: %s", replay.Body.String())
	}
	request(http.MethodGet, lostPath, keyA, "", http.StatusOK)
	request(http.MethodGet, lostPath, keyB, "", http.StatusNotFound)

	retryPath := "/v2/live-activity/channels/safe-provider-retry"
	for range 2 {
		request(http.MethodPut, retryPath, keyA, publicChannel("race", retryRoute.ID), http.StatusBadGateway)
		expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Retry/channels", kid: "CCCC000003"})
		request(http.MethodGet, retryPath, keyA, "", http.StatusNotFound)
	}
	request(http.MethodPut, retryPath, keyA, publicChannel("race", retryRoute.ID), http.StatusCreated)
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
	knownPath := "/v2/live-activity/channels/known-provider-id"
	request(http.MethodPut, knownPath, keyA, publicChannel("race", knownRoute.ID), http.StatusConflict)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Known/channels", kid: "CCCC000003"})
	persisted, err := store.GetTenantLAChannel(ctx, tenantA, "known-provider-id")
	if err != nil || persisted.State != storage.TenantLAChannelAmbiguous || persisted.ChannelID != "known-provider-channel" {
		t.Fatalf("known provider channel ID was lost: %+v, %v", persisted, err)
	}
	request(http.MethodPut, knownPath, keyA, publicChannel("race", knownRoute.ID), http.StatusConflict)

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

	request(http.MethodDelete, channelPath, keyA, "", http.StatusBadGateway)
	expectProvider(providerCall{method: http.MethodDelete, path: "/1/apps/com.example.Race/channels", kid: "AAAA000001", channel: "shared-provider-channel"})
	active := request(http.MethodGet, channelPath, keyA, "", http.StatusOK)
	var restored storage.TenantLAChannel
	if err := json.Unmarshal(active.Body.Bytes(), &restored); err != nil || restored.State != storage.TenantLAChannelActive ||
		restored.ChannelID != "shared-provider-channel" {
		t.Fatalf("definite initial rejection did not restore the channel: %s, %v", active.Body.String(), err)
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
	request(http.MethodDelete, channelPath, keyA, "", http.StatusServiceUnavailable)
	disabled := false
	if _, err := providers.Update(ctx, tenantA, routeA.ID, 1, &disabled, nil); err != nil {
		t.Fatal(err)
	}
	replayed := request(http.MethodPut, channelPath, keyA, publicChannel("race", routeA.ID), http.StatusOK)
	var mapped storage.TenantLAChannel
	if err := json.Unmarshal(replayed.Body.Bytes(), &mapped); err != nil || mapped.State != storage.TenantLAChannelActive || mapped.ChannelID != "shared-provider-channel" {
		t.Fatalf("disabled route replay = %+v, %v", mapped, err)
	}
	request(http.MethodPut, channelPath, keyA, publicChannel("only-a", routeA.ID), http.StatusConflict)
	request(http.MethodPut, channelPath, keyA, publicChannel("race", routeB.ID), http.StatusConflict)
	bDelete := providerCall{method: http.MethodDelete, path: "/1/apps/com.example.Race/channels", kid: "BBBB000002", channel: "shared-provider-channel"}
	request(http.MethodDelete, channelPath, keyB, "", http.StatusBadGateway)
	expectProvider(bDelete)
	assertDeleting(keyB, routeB.ID)
	firstDeletion, err := store.GetTenantLAChannel(ctx, tenantB, "race%day")
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodDelete, channelPath, keyB, "", http.StatusBadGateway)
	expectProvider(bDelete)
	if err := store.RestoreTenantLAChannel(ctx, tenantB, firstDeletion); err != nil {
		t.Fatal(err)
	}
	assertDeleting(keyB, routeB.ID)
	if _, err := providers.Update(ctx, tenantB, routeB.ID, 1, nil, syntheticChannelCredentials(t, "BBBB000004")); err != nil {
		t.Fatal(err)
	}
	bDelete.kid = "BBBB000004"
	request(http.MethodDelete, channelPath, keyB, "", http.StatusBadGateway)
	expectProvider(bDelete)
	assertDeleting(keyB, routeB.ID)
	if route, err := providers.Get(ctx, tenantB, routeB.ID); err != nil || route.ReadinessReason != "provider_authorization_failed" {
		t.Fatalf("rotated route authorization failure = %+v, %v", route, err)
	}
	if _, err := providers.Update(ctx, tenantB, routeB.ID, 2, nil, syntheticChannelCredentials(t, "BBBB000005")); err != nil {
		t.Fatal(err)
	}
	bDelete.kid = "BBBB000005"
	request(http.MethodDelete, channelPath, keyB, "", http.StatusNoContent)
	expectProvider(bDelete)
	request(http.MethodGet, channelPath, keyB, "", http.StatusNotFound)

	request(http.MethodPut, channelPath, keyB, publicChannel("race", routeB.ID), http.StatusCreated)
	expectProvider(providerCall{method: http.MethodPost, path: "/1/apps/com.example.Race/channels", kid: "BBBB000005"})
	replacement, err := store.GetTenantLAChannel(ctx, tenantB, "race%day")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTenantLAChannelDeletion(ctx, tenantB, replacement, 3); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveTenantLAChannel(ctx, tenantB, firstDeletion); err != nil {
		t.Fatal(err)
	}
	assertDeleting(keyB, routeB.ID)
	request(http.MethodDelete, channelPath, keyB, "", http.StatusNoContent)
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
	request(http.MethodDelete, channelPath, keyA, "", http.StatusInternalServerError)
	expectProvider(aDelete)
	assertDeleting(keyA, routeA.ID)
	if _, err := db.Exec(`DROP TRIGGER fail_test_channel_removal ON tenant_live_activity_channels`); err != nil {
		t.Fatal(err)
	}
	request(http.MethodDelete, channelPath, keyA, "", http.StatusNoContent)
	expectProvider(aDelete)
	request(http.MethodGet, channelPath, keyA, "", http.StatusNotFound)

	if _, err := providers.Update(ctx, tenantA, routeA.ID, 2, &disabled, nil); err != nil {
		t.Fatal(err)
	}
	request(http.MethodPut, channelPath, keyA, publicChannel("race", routeA.ID), http.StatusServiceUnavailable)
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
