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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
	"github.com/mithileshchellappan/pushboy/internal/workers"
)

func TestTenantUserSendIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	store := openTenantUserSendTestStore(t, ctx)
	providers, err := storage.NewTenantProviderStore(store, "test-key", `{"test-key":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`)
	if err != nil {
		t.Fatal(err)
	}

	jobs := pipeline.NewMemoryPipeline[model.JobItem](4)
	router := New(service.NewPushBoyService(store, "", nil), jobs, nil, store).setupRouter()

	var secrets []string
	request := func(method, path, bearer, body string, want int, response any) {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		for _, secret := range secrets {
			if strings.Contains(res.Body.String(), secret) {
				t.Fatalf("%s %s leaked provider or key material", method, path)
			}
		}
		if res.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, res.Code, want, res.Body)
		}
		if response != nil {
			if err := json.Unmarshal(res.Body.Bytes(), response); err != nil {
				t.Fatal(err)
			}
		}
	}

	const usersPath = "/v1/users"
	const identityBody = `{"identity":{"namespace":"account","externalId":"shared-customer"}}`
	const bundle = "com.example.shared"
	const lostTitle = "received-without-reply"
	const retryTitle = "ownership-changed-before-retry"
	type tenantFixture struct {
		id, uid, routeID uuid.UUID
		bearer, kid      string
		signingKey       *ecdsa.PrivateKey
	}
	createTenant := func(name, kid string) tenantFixture {
		t.Helper()
		f := tenantFixture{kid: kid}
		err := store.BootstrapTenant(ctx, name, func(tenant storage.Tenant, bearer string) error {
			f.id, f.bearer = tenant.ID, bearer
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, f.bearer, f.kid)
		var user storage.TenantUser
		request(http.MethodPost, usersPath, f.bearer, identityBody, http.StatusCreated, &user)
		if user.UID == uuid.Nil || user.Kind != "identified" {
			t.Fatalf("unexpected created user: %+v", user)
		}
		var fetched storage.TenantUser
		request(http.MethodGet, usersPath+"/"+user.UID.String(), f.bearer, "", http.StatusOK, &fetched)
		if fetched != user {
			t.Fatalf("fetched user %+v differs from created user %+v", fetched, user)
		}
		f.uid = user.UID
		f.signingKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		keyBytes, err := x509.MarshalECPrivateKey(f.signingKey)
		if err != nil {
			t.Fatal(err)
		}
		credentials, err := json.Marshal(storage.APNsCredentials{
			KeyID: kid, TeamID: "Z1Y2X3W4V5", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})),
		})
		if err != nil {
			t.Fatal(err)
		}
		route, err := providers.Create(ctx, f.id, storage.TenantProviderNamespace{
			Provider: "apns", Environment: "production", ApplicationID: bundle,
		}, true, credentials)
		if err != nil {
			t.Fatal(err)
		}
		f.routeID = route.ID
		secrets = append(secrets, route.ID.String())
		return f
	}
	a := createTenant("user-send-a", "AAAA000001")
	b := createTenant("user-send-b", "BBBB000002")
	if a.uid == b.uid {
		t.Fatal("tenants with the same external identity share a UID")
	}

	aUserPath := usersPath + "/" + a.uid.String()
	bUserPath := usersPath + "/" + b.uid.String()
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		request(method, aUserPath, b.bearer, "", http.StatusNotFound, nil)
		request(method, bUserPath, a.bearer, "", http.StatusNotFound, nil)
		request(method, usersPath+"/invalid-uid", a.bearer, "", http.StatusNotFound, nil)
		request(method, aUserPath, "", "", http.StatusUnauthorized, nil)
	}
	request(http.MethodGet, usersPath, "", "", http.StatusUnauthorized, nil)
	request(http.MethodPost, usersPath, "", "{}", http.StatusUnauthorized, nil)
	request(http.MethodPost, usersPath, a.bearer, identityBody, http.StatusConflict, nil)
	for _, body := range []string{
		`{"id":"chosen-id"}`,
		`{"uid":"chosen-uid"}`,
		`{"identity":{"namespace":"account","externalId":"customer","extra":true}}`,
		`{"identity":{"namespace":"account"}}`,
		`{} {}`,
		`null`,
	} {
		request(http.MethodPost, usersPath, a.bearer, body, http.StatusBadRequest, nil)
	}

	actor, err := store.AuthenticateServerKey(ctx, a.bearer)
	if err != nil {
		t.Fatal(err)
	}
	_, readKey, err := store.IssueServerKey(ctx, actor, []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, writeKey, err := store.IssueServerKey(ctx, actor, []string{"users:write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, readKey, writeKey)
	request(http.MethodGet, aUserPath, readKey, "", http.StatusOK, nil)
	request(http.MethodGet, aUserPath, writeKey, "", http.StatusForbidden, nil)
	request(http.MethodGet, usersPath, writeKey, "", http.StatusForbidden, nil)
	request(http.MethodPost, usersPath, readKey, "{}", http.StatusForbidden, nil)
	request(http.MethodDelete, aUserPath, readKey, "", http.StatusForbidden, nil)

	var anonymous storage.TenantUser
	request(http.MethodPost, usersPath, writeKey, "{}", http.StatusCreated, &anonymous)
	if anonymous.Kind != "anonymous" || anonymous.UID == uuid.Nil {
		t.Fatalf("unexpected anonymous user: %+v", anonymous)
	}

	for _, body := range []string{"", `{"identity":null}`} {
		var user storage.TenantUser
		request(http.MethodPost, usersPath, writeKey, body, http.StatusCreated, &user)
		if user.Kind != "anonymous" || user.UID == uuid.Nil || user.UID == anonymous.UID {
			t.Fatalf("unexpected anonymous user: %+v", user)
		}
		path := usersPath + "/" + user.UID.String()
		request(http.MethodDelete, path, writeKey, "", http.StatusNoContent, nil)
		request(http.MethodGet, path, readKey, "", http.StatusNotFound, nil)
		request(http.MethodDelete, path, writeKey, "", http.StatusNotFound, nil)
	}

	var firstPage, secondPage listResponse[storage.TenantUser]
	request(http.MethodGet, usersPath+"?limit=1", readKey, "", http.StatusOK, &firstPage)
	if len(firstPage.Items) != 1 || firstPage.Items[0] != anonymous || !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("unexpected first user page: %+v", firstPage)
	}
	nextUsers := usersPath + "?limit=1&cursor=" + url.QueryEscape(firstPage.NextCursor)
	request(http.MethodGet, nextUsers, readKey, "", http.StatusOK, &secondPage)
	if len(secondPage.Items) != 1 || secondPage.Items[0].UID != a.uid || secondPage.HasMore || secondPage.NextCursor != "" {
		t.Fatalf("unexpected second user page: %+v", secondPage)
	}
	request(http.MethodGet, nextUsers, b.bearer, "", http.StatusBadRequest, nil)
	request(http.MethodGet, usersPath+"?cursor=not-a-cursor", readKey, "", http.StatusBadRequest, nil)

	enroll := func(f tenantFixture, deviceID uuid.UUID, token string) (storage.TenantInstallation, storage.TenantPushRegistration) {
		t.Helper()
		os := "iOS"
		device, _, err := store.EnrollInstallation(ctx, f.id, deviceID, storage.InstallationObservations{OS: &os})
		if err != nil {
			t.Fatal(err)
		}
		device, err = store.IdentifyInstallation(ctx, f.id, deviceID, storage.InstallationOwnershipChange{
			OperationID: uuid.New(), ExpectedOwnershipVersion: device.OwnershipVersion,
		}, storage.InstallationIdentification{UID: &f.uid})
		if err != nil {
			t.Fatal(err)
		}
		registration, err := store.RegisterPush(ctx, f.id, deviceID, storage.TenantPushRegistrationChange{
			OperationID: uuid.New(), ProviderConfigID: f.routeID, Token: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		return device, registration
	}
	phoneID, tabletID := uuid.New(), uuid.New()
	aPhoneToken, aTabletToken, bPhoneToken := strings.Repeat("a1", 32), strings.Repeat("a2", 32), strings.Repeat("b1", 32)
	aPhone, aPhoneReg := enroll(a, phoneID, aPhoneToken)
	aTablet, aTabletReg := enroll(a, tabletID, aTabletToken)
	_, bPhoneReg := enroll(b, phoneID, bPhoneToken)
	secrets = append(secrets, aPhoneToken, aTabletToken, bPhoneToken)
	request(http.MethodDelete, aUserPath, writeKey, "", http.StatusConflict, nil)

	switchOwner := func(ctx context.Context, device storage.TenantInstallation) error {
		_, err := store.IdentifyInstallation(ctx, a.id, device.ID, storage.InstallationOwnershipChange{
			OperationID: uuid.New(), ExpectedOwnershipVersion: device.OwnershipVersion,
		}, storage.InstallationIdentification{Identity: &storage.TenantUserIdentity{
			Namespace: "account", ExternalID: "new-owner",
		}})
		return err
	}
	deliveryKeys := map[string]tenantFixture{
		"/3/device/" + aTabletToken: a,
		"/3/device/" + bPhoneToken:  b,
	}
	var invalidSince atomic.Int64
	calls := make(chan string, 10)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected, ok := deliveryKeys[r.URL.Path]
		if !ok || r.Method != http.MethodPost || r.Header.Get("apns-topic") != bundle {
			t.Error("unexpected APNs path, method, or topic")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, err := jwt.Parse(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), func(token *jwt.Token) (any, error) {
			if token.Header["kid"] != expected.kid {
				return nil, fmt.Errorf("JWT key does not match the token's tenant")
			}
			return &expected.signingKey.PublicKey, nil
		}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuer("Z1Y2X3W4V5"))
		if err != nil {
			t.Errorf("verify tenant APNs signature: %v", err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var payload struct {
			APS struct {
				Alert *struct {
					Title string `json:"title"`
				} `json:"alert"`
				ContentAvailable int `json:"content-available"`
			} `json:"aps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode APNs payload: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var call string
		switch {
		case expected.id == b.id && payload.APS.Alert != nil && payload.APS.Alert.Title == "expired":
			w.WriteHeader(http.StatusGone)
			if ts := invalidSince.Load(); ts > 0 {
				fmt.Fprintf(w, `{"reason":"Unregistered","timestamp":%d}`, ts)
			} else {
				fmt.Fprint(w, `{"reason":"Unregistered"}`)
			}
			return
		case expected.id == a.id && payload.APS.Alert != nil && payload.APS.Alert.Title == "tenant A":
			call = "tenant A tablet"
		case expected.id == a.id && payload.APS.Alert != nil && payload.APS.Alert.Title == retryTitle:
			call = "rate-limited tablet"
		case expected.id == b.id && payload.APS.Alert == nil && payload.APS.ContentAvailable == 1 && r.Header.Get("apns-push-type") == "background":
			call = "tenant B phone"
		case expected.id == b.id && payload.APS.Alert != nil && payload.APS.Alert.Title == lostTitle:
			call = "lost reply"
		default:
			t.Error("APNs payload went to the wrong tenant or used the wrong push type")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case calls <- call:
		default:
			t.Error("unexpected extra APNs request")
		}
		switch call {
		case "lost reply":
			panic(http.ErrAbortHandler)
		case "rate-limited tablet":
			if err := switchOwner(r.Context(), aTablet); err != nil {
				t.Errorf("switch tablet owner before returning 429: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer stub.Close()

	routes := dispatch.NewTenantChannelRouter(providers, &config.Config{
		APNSEndpoint: stub.URL, APNSClientPool: 1, APNSMaxConcurrent: 1, MaxRetryNotification: 1,
	})
	userSendPath := func(uid string) string { return "/v1/users/" + uid + "/send" }
	jobPath := func(jobID string) string { return "/v1/notifications/" + jobID }
	diagnoseJob := func(jobID, bearer string) publishJobResponse {
		t.Helper()
		var result publishJobResponse
		request(http.MethodGet, jobPath(jobID), bearer, "", http.StatusOK, &result)
		return result
	}
	submit := func(f tenantFixture, body string, recipients int) model.JobItem {
		t.Helper()
		var queue struct {
			ID string `json:"job_id"`
		}
		request(http.MethodPost, userSendPath(f.uid.String()), f.bearer, body, http.StatusAccepted, &queue)
		d, err := jobs.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		job := d.Get()
		if _, err := uuid.Parse(queue.ID); err != nil || job.ID != queue.ID || job.TenantID != f.id || job.UserID != f.uid.String() {
			t.Fatal("HTTP response and queued tenant job disagree")
		}
		status := diagnoseJob(job.ID, f.bearer)
		if status.Status != model.NotificationJobStatusQueued || status.TotalCount != recipients {
			t.Fatalf("queued tenant job has status %s and %d recipients, want QUEUED and %d", status.Status, status.TotalCount, recipients)
		}
		return job
	}
	fanout := func(job model.JobItem, recipients int) []model.SendTask {
		t.Helper()
		var tasks []model.SendTask
		err := workers.FanoutTenantPush(ctx, store, 1, job, func(_ context.Context, task model.SendTask) error {
			tasks = append(tasks, task)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != recipients {
			t.Fatalf("fanout emitted %d tasks, want %d", len(tasks), recipients)
		}
		return tasks
	}
	dispatchTasks := func(tasks []model.SendTask) {
		t.Helper()
		outcomes := pipeline.NewMemoryPipeline[model.SendOutcome](len(tasks))
		for _, task := range tasks {
			if err := workers.DispatchTenantPush(ctx, store, routes, task, outcomes); err != nil {
				t.Fatal(err)
			}
		}
		var receipts []model.DeliveryReceipt
		for range tasks {
			d, err := outcomes.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			receipts = append(receipts, d.Get().Receipt)
		}
		if err := store.ApplyPushOutcomeBatch(ctx, receipts); err != nil {
			t.Fatal(err)
		}
	}

	validBody := `{"title":"tenant A"}`
	request(http.MethodPost, userSendPath(a.uid.String()), a.bearer, `{}`, http.StatusBadRequest, nil)
	request(http.MethodPost, userSendPath(a.uid.String()), "", validBody, http.StatusUnauthorized, nil)
	request(http.MethodPost, userSendPath(a.uid.String()), "invalid-key", validBody, http.StatusUnauthorized, nil)
	request(http.MethodPost, userSendPath(b.uid.String()), a.bearer, validBody, http.StatusNotFound, nil)
	request(http.MethodPost, userSendPath(uuid.NewString()), a.bearer, validBody, http.StatusNotFound, nil)
	request(http.MethodPost, userSendPath("invalid-uid"), a.bearer, validBody, http.StatusNotFound, nil)

	aJob := submit(a, validBody, 2)
	bJob := submit(b, `{"silent":true,"data":{"event":"refresh"}}`, 1)
	aTasks, bTasks := fanout(aJob, 2), fanout(bJob, 1)
	if err := switchOwner(ctx, aPhone); err != nil {
		t.Fatal(err)
	}
	dispatchTasks(append(aTasks, bTasks...))
	lostJob := submit(b, fmt.Sprintf(`{"title":%q}`, lostTitle), 1)
	dispatchTasks(fanout(lostJob, 1))
	retryJob := submit(a, fmt.Sprintf(`{"title":%q}`, retryTitle), 1)
	dispatchTasks(fanout(retryJob, 1))

	request(http.MethodDelete, aUserPath, writeKey, "", http.StatusConflict, nil)
	request(http.MethodGet, aUserPath, readKey, "", http.StatusOK, nil)

	counts := make(map[string]int)
	for len(calls) > 0 {
		counts[<-calls]++
	}
	for _, want := range []string{"tenant A tablet", "tenant B phone", "lost reply", "rate-limited tablet"} {
		if counts[want] != 1 {
			t.Errorf("APNs saw %d requests for %s, want one", counts[want], want)
		}
	}
	for _, check := range []struct {
		job              model.JobItem
		bearer           string
		success, skipped int
		unknown          int
	}{
		{aJob, a.bearer, 1, 1, 0},
		{bJob, b.bearer, 1, 0, 0},
		{lostJob, b.bearer, 0, 0, 1},
		{retryJob, a.bearer, 0, 1, 0},
	} {
		status := diagnoseJob(check.job.ID, check.bearer)
		if status.Status != model.NotificationJobStatusCompleted || status.CompletedAt == "" ||
			status.SuccessCount != check.success || status.SkippedCount != check.skipped || status.UnknownCount != check.unknown || status.FailureCount != 0 {
			t.Errorf("job %s has unexpected status or delivery counters: %+v", check.job.ID, status)
		}
	}

	aReceipts := jobPath(aJob.ID) + "/receipts"
	var page1, page2 listResponse[storage.TenantJobReceipt]
	request(http.MethodGet, aReceipts+"?limit=1", a.bearer, "", http.StatusOK, &page1)
	if len(page1.Items) != 1 || !page1.HasMore || page1.NextCursor != page1.Items[0].ID {
		t.Fatalf("unexpected first receipt page: %+v", page1)
	}
	request(http.MethodGet, aReceipts+"?limit=1&cursor="+url.QueryEscape(page1.NextCursor), a.bearer, "", http.StatusOK, &page2)
	if len(page2.Items) != 1 || page2.HasMore || page2.NextCursor != "" || page1.Items[0].ID == page2.Items[0].ID {
		t.Fatalf("unexpected second receipt page: %+v", page2)
	}
	receipts := map[uuid.UUID]storage.TenantJobReceipt{
		page1.Items[0].DeviceID: page1.Items[0],
		page2.Items[0].DeviceID: page2.Items[0],
	}
	assertReceipt := func(receipt storage.TenantJobReceipt, deviceID, regID uuid.UUID, status model.DeliveryStatus, reason string) {
		t.Helper()
		if receipt.DeviceID != deviceID || receipt.RegistrationID != regID || receipt.Status != status ||
			receipt.StatusReason != reason || receipt.DispatchedAt == nil {
			t.Errorf("unexpected %s receipt: %+v", status, receipt)
		}
	}
	assertReceipt(receipts[phoneID], phoneID, aPhoneReg.ID, model.DeliveryStatusSkipped, model.TenantPushReasonRecipientStale)
	assertReceipt(receipts[tabletID], tabletID, aTabletReg.ID, model.DeliveryStatusSuccess, "")
	var retryReceipts listResponse[storage.TenantJobReceipt]
	request(http.MethodGet, jobPath(retryJob.ID)+"/receipts", a.bearer, "", http.StatusOK, &retryReceipts)
	if len(retryReceipts.Items) != 1 {
		t.Fatalf("unexpected retry job receipt count: %d", len(retryReceipts.Items))
	}
	assertReceipt(retryReceipts.Items[0], tabletID, aTabletReg.ID, model.DeliveryStatusSkipped, model.TenantPushReasonRecipientStale)
	for _, check := range []struct {
		id     string
		status model.DeliveryStatus
		reason string
	}{
		{bJob.ID, model.DeliveryStatusSuccess, ""},
		{lostJob.ID, model.DeliveryStatusUnknown, model.TenantPushReasonDeliveryUnknown},
	} {
		var page listResponse[storage.TenantJobReceipt]
		request(http.MethodGet, jobPath(check.id)+"/receipts", b.bearer, "", http.StatusOK, &page)
		if len(page.Items) != 1 {
			t.Fatalf("unexpected tenant B receipt count: %d", len(page.Items))
		}
		assertReceipt(page.Items[0], phoneID, bPhoneReg.ID, check.status, check.reason)
	}
	if registration, err := store.GetPushRegistration(ctx, b.id, phoneID); err != nil || !registration.Valid {
		t.Fatalf("unknown outcome changed tenant B registration validity: %v", err)
	}

	priorInvalidation := time.Now().UnixMilli()
	_, err = store.RegisterPush(ctx, b.id, phoneID, storage.TenantPushRegistrationChange{
		OperationID: uuid.New(), ProviderConfigID: b.routeID, Token: bPhoneToken,
		ExpectedRegistrationID: &bPhoneReg.ID, ExpectedRegistrationVersion: bPhoneReg.RegistrationVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmedBy := time.Now()
	assertGone := func(ts int64, preserve bool) {
		t.Helper()
		invalidSince.Store(ts)
		job := submit(b, `{"title":"expired"}`, 1)
		dispatchTasks(fanout(job, 1))
		current, err := store.GetPushRegistration(ctx, b.id, phoneID)
		if err != nil || current.Valid != preserve || diagnoseJob(job.ID, b.bearer).FailureCount != 1 {
			t.Fatalf("APNs 410 timestamp %d: valid=%t, want %t, err=%v", ts, current.Valid, preserve, err)
		}
	}
	assertGone(priorInvalidation, true)
	assertGone(0, true)
	time.Sleep(time.Until(confirmedBy.Truncate(time.Millisecond).Add(time.Millisecond)))
	assertGone(time.Now().UnixMilli(), false)

	for _, suffix := range []string{"", "/receipts"} {
		request(http.MethodGet, jobPath(aJob.ID)+suffix, b.bearer, "", http.StatusNotFound, nil)
		request(http.MethodGet, jobPath(bJob.ID)+suffix, a.bearer, "", http.StatusNotFound, nil)
		request(http.MethodGet, jobPath(uuid.NewString())+suffix, a.bearer, "", http.StatusNotFound, nil)
		request(http.MethodGet, jobPath(aJob.ID)+suffix, "invalid-key", "", http.StatusUnauthorized, nil)
	}
	request(http.MethodGet, jobPath(aJob.ID), "", "", http.StatusNotFound, nil)
	request(http.MethodGet, aReceipts, "", "", http.StatusUnauthorized, nil)
	request(http.MethodGet, aReceipts+"?cursor=not-a-uuid", a.bearer, "", http.StatusBadRequest, nil)
}

func openTenantUserSendTestStore(t *testing.T, ctx context.Context) *storage.PostgresStore {
	t.Helper()
	databaseURL := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set PUSHBOY_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	dsn, err := url.Parse(databaseURL)
	if err != nil || (dsn.Scheme != "postgres" && dsn.Scheme != "postgresql") {
		t.Fatal("PUSHBOY_TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	admin, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	schema := "tenant_user_send_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop isolated tenant send schema: %v", err)
		}
	})
	params := dsn.Query()
	params.Set("search_path", schema)
	dsn.RawQuery = params.Encode()
	return openLAChannelTestStores(t, dsn.String())[0]
}
