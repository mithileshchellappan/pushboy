package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func TestLazyLAChannelCreationAcrossHTTPInstances(t *testing.T) {
	databaseURL := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set PUSHBOY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	// The storage benchmark uses the same dedicated database. Run env-gated
	// package tests with `go test -p=1` so their connection pools do not overlap.

	stores := newLazyChannelIntegrationStores(t, databaseURL)
	inspectionDB, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatalf("open inspection database: %v", err)
	}
	t.Cleanup(func() { inspectionDB.Close() })

	t.Run("32 concurrent PUTs create one channel", func(t *testing.T) {
		topicID, activityID := createLazyChannelIntegrationScope(t, stores[0], inspectionDB)
		provider := &countedLAChannelProvider{delay: 25 * time.Millisecond}
		laPipeline := &recordingIntegrationPipeline[model.LAJobItem]{}
		routers := lazyChannelIntegrationRouters(stores, provider, laPipeline)

		responses := runConcurrentHTTPRequests(32, func(index int) (*http.Request, http.Handler) {
			request := httptest.NewRequest(
				http.MethodPut,
				"/v1/live-activity/channels/"+activityID,
				bytes.NewBufferString(fmt.Sprintf(`{"topicId":%q}`, topicID)),
			)
			return request, routers[index%len(routers)]
		})

		createdCount := 0
		type channelResponse struct {
			ActivityID string `json:"activityId"`
			TopicID    string `json:"topicId"`
			ChannelID  string `json:"channelId"`
			CreatedAt  string `json:"createdAt"`
		}
		var firstBody channelResponse
		for index, response := range responses {
			switch response.status {
			case http.StatusCreated:
				createdCount++
			case http.StatusOK:
			default:
				t.Fatalf("response %d status = %d, body = %s", index, response.status, response.body)
			}
			var body channelResponse
			if err := json.Unmarshal(response.body, &body); err != nil {
				t.Fatalf("decode response %d: %v", index, err)
			}
			if body.ActivityID != activityID ||
				body.TopicID != topicID ||
				body.ChannelID == "" ||
				body.CreatedAt == "" {
				t.Fatalf("response %d mapping = %+v, want complete matching mapping", index, body)
			}
			if index == 0 {
				firstBody = body
			} else if body != firstBody {
				t.Fatalf("response %d mapping = %+v, want %+v", index, body, firstBody)
			}
		}

		if createdCount != 1 {
			t.Fatalf("201 responses = %d, want 1", createdCount)
		}
		if provider.calls.Load() != 1 {
			t.Fatalf("APNs create calls = %d, want 1", provider.calls.Load())
		}
		assertLazyChannelIntegrationRows(t, inspectionDB, activityID, 1, 0, 0)
	})

	t.Run("32 concurrent starts share one channel and one dispatch", func(t *testing.T) {
		topicID, activityID := createLazyChannelIntegrationScope(t, stores[0], inspectionDB)
		provider := &countedLAChannelProvider{delay: 25 * time.Millisecond}
		laPipeline := &recordingIntegrationPipeline[model.LAJobItem]{}
		routers := lazyChannelIntegrationRouters(stores, provider, laPipeline)

		responses := runConcurrentHTTPRequests(32, func(index int) (*http.Request, http.Handler) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/live-activity/jobs",
				bytes.NewBufferString(fmt.Sprintf(
					`{"action":"start","activityId":%q,"activityType":"RaceAttributes","topicId":%q,"payload":{"lap":1}}`,
					activityID,
					topicID,
				)),
			)
			return request, routers[index%len(routers)]
		})

		acceptedCount := 0
		alreadyStartedCount := 0
		for index, response := range responses {
			var body struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(response.body, &body); err != nil {
				t.Fatalf("decode response %d: %v (body %s)", index, err, response.body)
			}
			switch {
			case response.status == http.StatusAccepted && body.Status == "started":
				acceptedCount++
			case response.status == http.StatusOK && body.Status == "already_started":
				alreadyStartedCount++
			default:
				t.Fatalf(
					"response %d = status %d, job status %q, body %s",
					index,
					response.status,
					body.Status,
					response.body,
				)
			}
		}

		if acceptedCount != 1 || alreadyStartedCount != 31 {
			t.Fatalf(
				"start responses = %d accepted, %d already_started; want 1 and 31",
				acceptedCount,
				alreadyStartedCount,
			)
		}
		if provider.calls.Load() != 1 {
			t.Fatalf("APNs create calls = %d, want 1", provider.calls.Load())
		}
		if laPipeline.len() != 1 {
			t.Fatalf("LA pipeline submissions = %d, want 1", laPipeline.len())
		}
		assertLazyChannelIntegrationRows(t, inspectionDB, activityID, 1, 1, 1)
	})

	t.Run("mixed PUT and start requests share one channel", func(t *testing.T) {
		topicID, activityID := createLazyChannelIntegrationScope(t, stores[0], inspectionDB)
		provider := &countedLAChannelProvider{delay: 25 * time.Millisecond}
		laPipeline := &recordingIntegrationPipeline[model.LAJobItem]{}
		routers := lazyChannelIntegrationRouters(stores, provider, laPipeline)

		responses := runConcurrentHTTPRequests(32, func(index int) (*http.Request, http.Handler) {
			if index < 16 {
				request := httptest.NewRequest(
					http.MethodPut,
					"/v1/live-activity/channels/"+activityID,
					bytes.NewBufferString(fmt.Sprintf(`{"topicId":%q}`, topicID)),
				)
				return request, routers[index%len(routers)]
			}
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/live-activity/jobs",
				bytes.NewBufferString(fmt.Sprintf(
					`{"action":"start","activityId":%q,"activityType":"RaceAttributes","topicId":%q,"payload":{"lap":1}}`,
					activityID,
					topicID,
				)),
			)
			return request, routers[index%len(routers)]
		})

		startedCount := 0
		for index, response := range responses {
			if response.status != http.StatusOK &&
				response.status != http.StatusCreated &&
				response.status != http.StatusAccepted {
				t.Fatalf("response %d status = %d, body = %s", index, response.status, response.body)
			}
			var body map[string]any
			if err := json.Unmarshal(response.body, &body); err != nil {
				t.Fatalf("decode response %d: %v", index, err)
			}
			if body["status"] == "started" {
				startedCount++
			}
		}

		if startedCount != 1 {
			t.Fatalf("started responses = %d, want 1", startedCount)
		}
		if provider.calls.Load() != 1 {
			t.Fatalf("APNs create calls = %d, want 1", provider.calls.Load())
		}
		if laPipeline.len() != 1 {
			t.Fatalf("LA pipeline submissions = %d, want 1", laPipeline.len())
		}
		assertLazyChannelIntegrationRows(t, inspectionDB, activityID, 1, 1, 1)
	})

	t.Run("different-topic retry after token fallback returns conflict", func(t *testing.T) {
		jobTopicID, activityID := createLazyChannelIntegrationScope(t, stores[0], inspectionDB)
		requestTopicID := "topic-" + uuid.NewString()
		if err := stores[0].CreateTopic(
			context.Background(),
			&storage.Topic{ID: requestTopicID, Name: requestTopicID},
		); err != nil {
			t.Fatalf("create retry topic: %v", err)
		}
		t.Cleanup(func() {
			if _, err := inspectionDB.ExecContext(
				context.Background(),
				`DELETE FROM live_activity_channels WHERE activity_id = $1`,
				activityID,
			); err != nil {
				t.Errorf("delete retry channel mapping: %v", err)
			}
			if _, err := inspectionDB.ExecContext(
				context.Background(),
				`DELETE FROM topics WHERE id = $1`,
				requestTopicID,
			); err != nil {
				t.Errorf("delete retry topic: %v", err)
			}
		})

		laPipeline := &recordingIntegrationPipeline[model.LAJobItem]{}
		tokenOnlyRouter := New(
			service.NewPushBoyService(stores[0], ""),
			&recordingIntegrationPipeline[model.JobItem]{},
			laPipeline,
		).setupRouter()

		firstRequest := httptest.NewRequest(
			http.MethodPost,
			"/v1/live-activity/jobs",
			bytes.NewBufferString(fmt.Sprintf(
				`{"action":"start","activityId":%q,"activityType":"RaceAttributes","topicId":%q,"payload":{"lap":1}}`,
				activityID,
				jobTopicID,
			)),
		)
		firstResponse := httptest.NewRecorder()
		tokenOnlyRouter.ServeHTTP(firstResponse, firstRequest)
		if firstResponse.Code != http.StatusAccepted {
			t.Fatalf(
				"token-only start status = %d, want %d, body = %s",
				firstResponse.Code,
				http.StatusAccepted,
				firstResponse.Body.String(),
			)
		}

		provider := &countedLAChannelProvider{}
		channelRouter := New(
			service.NewPushBoyService(stores[1], "", service.WithLAChannels(provider)),
			&recordingIntegrationPipeline[model.JobItem]{},
			laPipeline,
		).setupRouter()
		retryRequest := httptest.NewRequest(
			http.MethodPost,
			"/v1/live-activity/jobs",
			bytes.NewBufferString(fmt.Sprintf(
				`{"action":"start","activityId":%q,"activityType":"RaceAttributes","topicId":%q,"payload":{"lap":2}}`,
				activityID,
				requestTopicID,
			)),
		)
		retryResponse := httptest.NewRecorder()
		channelRouter.ServeHTTP(retryResponse, retryRequest)

		if retryResponse.Code != http.StatusConflict {
			t.Fatalf(
				"retry status = %d, want %d, body = %s",
				retryResponse.Code,
				http.StatusConflict,
				retryResponse.Body.String(),
			)
		}
		if provider.calls.Load() != 0 {
			t.Fatalf("APNs create calls = %d, want 0", provider.calls.Load())
		}
		if laPipeline.len() != 1 {
			t.Fatalf("LA pipeline submissions = %d, want 1", laPipeline.len())
		}
		assertLazyChannelIntegrationRows(t, inspectionDB, activityID, 0, 1, 1)

		var storedTopicID string
		if err := inspectionDB.QueryRowContext(
			context.Background(),
			`SELECT topic_id FROM live_activity_jobs WHERE activity_id = $1`,
			activityID,
		).Scan(&storedTopicID); err != nil {
			t.Fatalf("read stored job topic: %v", err)
		}
		if storedTopicID != jobTopicID {
			t.Fatalf("stored job topic = %q, want %q", storedTopicID, jobTopicID)
		}
	})
}

type lazyChannelHTTPResponse struct {
	status int
	body   []byte
}

func runConcurrentHTTPRequests(
	count int,
	request func(index int) (*http.Request, http.Handler),
) []lazyChannelHTTPResponse {
	responses := make([]lazyChannelHTTPResponse, count)
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	waitGroup.Add(count)
	for index := 0; index < count; index++ {
		go func(index int) {
			defer waitGroup.Done()
			httpRequest, handler := request(index)
			recorder := httptest.NewRecorder()
			<-start
			handler.ServeHTTP(recorder, httpRequest)
			responses[index] = lazyChannelHTTPResponse{
				status: recorder.Code,
				body:   append([]byte(nil), recorder.Body.Bytes()...),
			}
		}(index)
	}
	close(start)
	waitGroup.Wait()
	return responses
}

func newLazyChannelIntegrationStores(t *testing.T, databaseURL string) []*storage.PostgresStore {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test filename")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	previousWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatalf("change to repository root: %v", err)
	}
	defer func() {
		if err := os.Chdir(previousWorkingDirectory); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()

	stores := make([]*storage.PostgresStore, 2)
	for index := range stores {
		store, err := storage.NewPostgresStore(databaseURL)
		if err != nil {
			t.Fatalf("create PostgreSQL store %d: %v", index, err)
		}
		stores[index] = store
	}
	t.Cleanup(func() {
		for _, store := range stores {
			store.Close()
		}
	})
	return stores
}

func createLazyChannelIntegrationScope(
	t *testing.T,
	store *storage.PostgresStore,
	inspectionDB *sql.DB,
) (string, string) {
	t.Helper()

	topicID := "topic-" + uuid.NewString()
	activityID := "activity-" + uuid.NewString()
	if err := store.CreateTopic(
		context.Background(),
		&storage.Topic{ID: topicID, Name: topicID},
	); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	t.Cleanup(func() {
		if _, err := inspectionDB.ExecContext(
			context.Background(),
			`DELETE FROM live_activity_dispatches
			 WHERE live_activity_job_id IN (
				SELECT id FROM live_activity_jobs WHERE activity_id = $1
			 )`,
			activityID,
		); err != nil {
			t.Errorf("delete integration dispatches: %v", err)
		}
		if _, err := inspectionDB.ExecContext(
			context.Background(),
			`DELETE FROM live_activity_jobs WHERE activity_id = $1`,
			activityID,
		); err != nil {
			t.Errorf("delete integration job: %v", err)
		}
		if _, err := inspectionDB.ExecContext(
			context.Background(),
			`DELETE FROM live_activity_channels WHERE activity_id = $1`,
			activityID,
		); err != nil {
			t.Errorf("delete integration channel: %v", err)
		}
		if _, err := inspectionDB.ExecContext(
			context.Background(),
			`DELETE FROM topics WHERE id = $1`,
			topicID,
		); err != nil {
			t.Errorf("delete integration topic: %v", err)
		}
	})
	return topicID, activityID
}

func lazyChannelIntegrationRouters(
	stores []*storage.PostgresStore,
	provider *countedLAChannelProvider,
	laPipeline pipeline.Pipeline[model.LAJobItem],
) []http.Handler {
	routers := make([]http.Handler, len(stores))
	for index, store := range stores {
		routers[index] = New(
			service.NewPushBoyService(store, "", service.WithLAChannels(provider)),
			&recordingIntegrationPipeline[model.JobItem]{},
			laPipeline,
		).setupRouter()
	}
	return routers
}

func assertLazyChannelIntegrationRows(
	t *testing.T,
	inspectionDB *sql.DB,
	activityID string,
	wantChannels int,
	wantJobs int,
	wantDispatches int,
) {
	t.Helper()

	var channelCount int
	if err := inspectionDB.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM live_activity_channels WHERE activity_id = $1`,
		activityID,
	).Scan(&channelCount); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	var jobCount int
	if err := inspectionDB.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM live_activity_jobs WHERE activity_id = $1`,
		activityID,
	).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	var dispatchCount int
	if err := inspectionDB.QueryRowContext(
		context.Background(),
			`SELECT COUNT(*)
			 FROM live_activity_dispatches
			 WHERE live_activity_job_id IN (
				SELECT id FROM live_activity_jobs WHERE activity_id = $1
			 )`,
		activityID,
	).Scan(&dispatchCount); err != nil {
		t.Fatalf("count dispatches: %v", err)
	}

	if channelCount != wantChannels || jobCount != wantJobs || dispatchCount != wantDispatches {
		t.Fatalf(
			"stored rows = %d channels, %d jobs, %d dispatches; want %d, %d, %d",
			channelCount,
			jobCount,
			dispatchCount,
			wantChannels,
			wantJobs,
			wantDispatches,
		)
	}
}

type countedLAChannelProvider struct {
	delay time.Duration
	calls atomic.Int32
}

func (p *countedLAChannelProvider) CreateLiveActivityChannel(ctx context.Context) (string, error) {
	p.calls.Add(1)
	if p.delay > 0 {
		timer := time.NewTimer(p.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	return "channel-" + uuid.NewString(), nil
}

func (*countedLAChannelProvider) DeleteLiveActivityChannel(context.Context, string) error {
	return nil
}

type recordingIntegrationPipeline[T any] struct {
	mu    sync.Mutex
	items []T
}

func (p *recordingIntegrationPipeline[T]) Submit(ctx context.Context, item T) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items = append(p.items, item)
	return nil
}

func (*recordingIntegrationPipeline[T]) Receive(context.Context) (pipeline.Delivery[T], error) {
	return nil, pipeline.ErrClosed
}

func (*recordingIntegrationPipeline[T]) Close(context.Context) error {
	return nil
}

func (p *recordingIntegrationPipeline[T]) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items)
}
