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

func TestConcurrentLazyLAStartsAcrossInstances(t *testing.T) {
	databaseURL := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set PUSHBOY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}

	stores := openLAChannelTestStores(t, databaseURL)
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		t.Fatalf("open inspection database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	topicID := "topic-" + uuid.NewString()
	activityID := "activity-" + uuid.NewString()
	if err := stores[0].CreateTopic(context.Background(), &storage.Topic{
		ID: topicID, Name: topicID,
	}); err != nil {
		t.Fatalf("create topic: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(
			context.Background(),
			`DELETE FROM live_activity_dispatches
			 WHERE live_activity_job_id IN (
				SELECT id FROM live_activity_jobs WHERE activity_id = $1
			 )`,
			activityID,
		); err != nil {
			t.Errorf("delete dispatches: %v", err)
		}
		if _, err := db.ExecContext(
			context.Background(),
			`DELETE FROM live_activity_jobs WHERE activity_id = $1`,
			activityID,
		); err != nil {
			t.Errorf("delete job: %v", err)
		}
		if _, err := db.ExecContext(
			context.Background(),
			`DELETE FROM live_activity_channels WHERE activity_id = $1`,
			activityID,
		); err != nil {
			t.Errorf("delete channel: %v", err)
		}
		if _, err := db.ExecContext(
			context.Background(),
			`DELETE FROM topics WHERE id = $1`,
			topicID,
		); err != nil {
			t.Errorf("delete topic: %v", err)
		}
	})

	provider := &countingHTTPChannelProvider{
		channelID: "channel-" + uuid.NewString(),
		delay:     25 * time.Millisecond,
	}
	laPipeline := &recordingHTTPChannelPipeline{}
	routers := make([]http.Handler, len(stores))
	for index, store := range stores {
		routers[index] = New(
			service.NewPushBoyService(store, "", provider),
			pipeline.NewMemoryPipeline[model.JobItem](1),
			laPipeline,
		).setupRouter()
	}

	type response struct {
		status int
		body   []byte
	}
	const requestCount = 32
	responses := make([]response, requestCount)
	start := make(chan struct{})
	var requests sync.WaitGroup
	requests.Add(requestCount)
	for index := range responses {
		go func(index int) {
			defer requests.Done()
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/live-activity/jobs",
				bytes.NewBufferString(fmt.Sprintf(
					`{"action":"start","activityId":%q,"activityType":"RaceAttributes","topicId":%q,"payload":{"lap":1}}`,
					activityID,
					topicID,
				)),
			)
			recorder := httptest.NewRecorder()
			<-start
			routers[index%len(routers)].ServeHTTP(recorder, request)
			responses[index] = response{
				status: recorder.Code,
				body:   append([]byte(nil), recorder.Body.Bytes()...),
			}
		}(index)
	}
	close(start)
	requests.Wait()

	var (
		started        int
		alreadyStarted int
		dispatchID     string
	)
	for index, result := range responses {
		var body struct {
			ActivityID string `json:"activityId"`
			Status     string `json:"status"`
			DispatchID string `json:"dispatchId"`
		}
		if err := json.Unmarshal(result.body, &body); err != nil {
			t.Fatalf("decode response %d: %v", index, err)
		}
		if body.ActivityID != activityID {
			t.Fatalf("response %d activity ID = %q, want %q", index, body.ActivityID, activityID)
		}
		switch {
		case result.status == http.StatusAccepted && body.Status == "started":
			started++
			dispatchID = body.DispatchID
		case result.status == http.StatusOK && body.Status == "already_started":
			alreadyStarted++
		default:
			t.Fatalf(
				"response %d status = %d, job status = %q, body = %s",
				index,
				result.status,
				body.Status,
				result.body,
			)
		}
	}

	if started != 1 || alreadyStarted != requestCount-1 || dispatchID == "" {
		t.Fatalf(
			"responses = %d started, %d already_started, dispatch ID %q",
			started,
			alreadyStarted,
			dispatchID,
		)
	}
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("APNs create calls = %d, want 1", calls)
	}

	items := laPipeline.snapshot()
	if len(items) != 1 {
		t.Fatalf("LA pipeline submissions = %d, want 1", len(items))
	}
	if item := items[0]; item.ActivityID != activityID ||
		item.TopicID != topicID ||
		item.ChannelID != provider.channelID ||
		item.DispatchID != dispatchID {
		t.Fatalf("LA pipeline item = %+v", item)
	}

	stored, err := stores[1].GetLAChannelByActivityID(context.Background(), activityID)
	if err != nil ||
		stored.TopicID != topicID ||
		stored.ChannelID != provider.channelID {
		t.Fatalf("stored mapping = %+v, err = %v", stored, err)
	}
	var jobs, dispatches int
	if err := db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(DISTINCT jobs.id), COUNT(dispatches.id)
		 FROM live_activity_jobs jobs
		 LEFT JOIN live_activity_dispatches dispatches
		   ON dispatches.live_activity_job_id = jobs.id
		 WHERE jobs.activity_id = $1`,
		activityID,
	).Scan(&jobs, &dispatches); err != nil {
		t.Fatalf("count stored start rows: %v", err)
	}
	if jobs != 1 || dispatches != 1 {
		t.Fatalf("stored rows = %d jobs, %d dispatches; want 1, 1", jobs, dispatches)
	}
}

func openLAChannelTestStores(t *testing.T, databaseURL string) []*storage.PostgresStore {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test filename")
	}
	previousDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatalf("change to repository root: %v", err)
	}
	defer func() {
		if err := os.Chdir(previousDirectory); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()

	stores := make([]*storage.PostgresStore, 2)
	for index := range stores {
		store, err := storage.NewPostgresStore(databaseURL)
		if err != nil {
			t.Fatalf("create store %d: %v", index, err)
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

type countingHTTPChannelProvider struct {
	channelID string
	delay     time.Duration
	calls     atomic.Int32
}

type recordingHTTPChannelPipeline struct {
	mu    sync.Mutex
	items []model.LAJobItem
}

func (p *recordingHTTPChannelPipeline) Submit(_ context.Context, item model.LAJobItem) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.items = append(p.items, item)
	return nil
}

func (*recordingHTTPChannelPipeline) Receive(context.Context) (pipeline.Delivery[model.LAJobItem], error) {
	return nil, pipeline.ErrClosed
}

func (*recordingHTTPChannelPipeline) Close(context.Context) error {
	return nil
}

func (p *recordingHTTPChannelPipeline) snapshot() []model.LAJobItem {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]model.LAJobItem(nil), p.items...)
}

func (p *countingHTTPChannelProvider) CreateLiveActivityChannel(ctx context.Context) (string, error) {
	p.calls.Add(1)
	timer := time.NewTimer(p.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return p.channelID, nil
	}
}

func (*countingHTTPChannelProvider) DeleteLiveActivityChannel(context.Context, string) error {
	return nil
}
