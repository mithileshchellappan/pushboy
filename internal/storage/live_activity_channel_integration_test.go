package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
)

// Keep the two test pools below PostgreSQL's common 100-connection default so
// the 128-caller benchmark measures pool waiting instead of server rejection.
// Run all PUSHBOY_TEST_DATABASE_URL packages with `go test -p=1` because the
// env-gated storage and HTTP suites intentionally share one dedicated database.
const laChannelIntegrationMaxOpenConnectionsPerStore = 32

func TestEnsureLAChannelSerializes32ConcurrentCreatorsAcrossStores(t *testing.T) {
	databaseURL := laChannelIntegrationDatabaseURL(t)
	stores := newLAChannelIntegrationStores(t, databaseURL)

	topicID := createLAChannelIntegrationTopic(t, stores[0])
	activityID := "activity-" + uuid.NewString()
	channelID := "channel-" + uuid.NewString()
	registerLAChannelIntegrationMappingCleanup(t, stores[0], activityID)

	var createCalls atomic.Int32
	create := func(context.Context) (string, error) {
		createCalls.Add(1)
		time.Sleep(50 * time.Millisecond)
		return channelID, nil
	}

	const callerCount = 32
	channels := make([]*LiveActivityChannel, callerCount)
	created := make([]bool, callerCount)
	errs := make([]error, callerCount)
	runConcurrently(callerCount, func(index int) {
		channels[index], created[index], errs[index] = stores[index%len(stores)].EnsureLAChannel(
			context.Background(),
			activityID,
			topicID,
			create,
		)
	})

	createdCount := 0
	for index, err := range errs {
		if err != nil {
			t.Fatalf("EnsureLAChannel call %d error = %v", index, err)
		}
		if created[index] {
			createdCount++
		}
		if channels[index].ActivityID != activityID {
			t.Errorf("call %d activity ID = %q, want %q", index, channels[index].ActivityID, activityID)
		}
		if channels[index].TopicID != topicID {
			t.Errorf("call %d topic ID = %q, want %q", index, channels[index].TopicID, topicID)
		}
		if channels[index].ChannelID != channelID {
			t.Errorf("call %d channel ID = %q, want %q", index, channels[index].ChannelID, channelID)
		}
	}
	if createCalls.Load() != 1 {
		t.Fatalf("APNs creator calls = %d, want 1", createCalls.Load())
	}
	if createdCount != 1 {
		t.Fatalf("created results = %d, want 1", createdCount)
	}

	stored, err := stores[1].GetLAChannelByActivityID(context.Background(), activityID)
	if err != nil {
		t.Fatalf("GetLAChannelByActivityID error = %v", err)
	}
	if stored.ChannelID != channelID {
		t.Fatalf("stored channel ID = %q, want %q", stored.ChannelID, channelID)
	}
}

func TestEnsureLAChannelPreexistingMappingSkipsCreator(t *testing.T) {
	databaseURL := laChannelIntegrationDatabaseURL(t)
	stores := newLAChannelIntegrationStores(t, databaseURL)

	topicID := createLAChannelIntegrationTopic(t, stores[0])
	activityID := "activity-" + uuid.NewString()
	channelID := "channel-" + uuid.NewString()
	registerLAChannelIntegrationMappingCleanup(t, stores[0], activityID)

	first, created, err := stores[0].EnsureLAChannel(
		context.Background(),
		activityID,
		topicID,
		func(context.Context) (string, error) { return channelID, nil },
	)
	if err != nil {
		t.Fatalf("initial EnsureLAChannel error = %v", err)
	}
	if !created {
		t.Fatal("initial EnsureLAChannel created = false, want true")
	}

	var createCalls atomic.Int32
	existing, created, err := stores[1].EnsureLAChannel(
		context.Background(),
		activityID,
		topicID,
		func(context.Context) (string, error) {
			createCalls.Add(1)
			return "unexpected-channel", nil
		},
	)
	if err != nil {
		t.Fatalf("EnsureLAChannel existing error = %v", err)
	}
	if created {
		t.Fatal("EnsureLAChannel existing created = true, want false")
	}
	if createCalls.Load() != 0 {
		t.Fatalf("APNs creator calls = %d, want 0", createCalls.Load())
	}
	if existing.ChannelID != first.ChannelID {
		t.Fatalf("existing channel ID = %q, want %q", existing.ChannelID, first.ChannelID)
	}

}

func TestEnsureLAChannelRejectsTopicThatConflictsWithExistingJob(t *testing.T) {
	databaseURL := laChannelIntegrationDatabaseURL(t)
	stores := newLAChannelIntegrationStores(t, databaseURL)

	jobTopicID := createLAChannelIntegrationTopic(t, stores[0])
	requestTopicID := createLAChannelIntegrationTopic(t, stores[0])
	activityID := "activity-" + uuid.NewString()
	registerLAChannelIntegrationMappingCleanup(t, stores[0], activityID)

	now := time.Now().UTC()
	job, created, err := stores[0].CreateOrGetLAStartJob(context.Background(), &LiveActivityJob{
		ID:            uuid.NewString(),
		ActivityID:    activityID,
		ActivityType:  "RaceAttributes",
		TopicID:       jobTopicID,
		Status:        model.LiveActivityJobStatusActive,
		LatestPayload: json.RawMessage(`{"lap":1}`),
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if err != nil {
		t.Fatalf("CreateOrGetLAStartJob error = %v", err)
	}
	if !created {
		t.Fatal("CreateOrGetLAStartJob created = false, want true")
	}
	t.Cleanup(func() {
		if _, err := stores[0].db.ExecContext(
			context.Background(),
			`DELETE FROM live_activity_jobs WHERE id = $1`,
			job.ID,
		); err != nil {
			t.Errorf("delete live activity job: %v", err)
		}
	})

	var createCalls atomic.Int32
	_, _, err = stores[1].EnsureLAChannel(
		context.Background(),
		activityID,
		requestTopicID,
		func(context.Context) (string, error) {
			createCalls.Add(1)
			return "unexpected-channel", nil
		},
	)
	if !errors.Is(err, Errors.Conflict) {
		t.Fatalf("EnsureLAChannel error = %v, want conflict", err)
	}
	if createCalls.Load() != 0 {
		t.Fatalf("APNs creator calls = %d, want 0", createCalls.Load())
	}
	if _, err := stores[0].GetLAChannelByActivityID(context.Background(), activityID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("GetLAChannelByActivityID error = %v, want not found", err)
	}
}

func TestCreateLAStartJobRejectsTopicThatConflictsWithExistingChannel(t *testing.T) {
	databaseURL := laChannelIntegrationDatabaseURL(t)
	stores := newLAChannelIntegrationStores(t, databaseURL)

	channelTopicID := createLAChannelIntegrationTopic(t, stores[0])
	jobTopicID := createLAChannelIntegrationTopic(t, stores[0])
	activityID := "activity-" + uuid.NewString()
	registerLAChannelIntegrationMappingCleanup(t, stores[0], activityID)

	_, created, err := stores[0].EnsureLAChannel(
		context.Background(),
		activityID,
		channelTopicID,
		func(context.Context) (string, error) {
			return "channel-" + uuid.NewString(), nil
		},
	)
	if err != nil {
		t.Fatalf("EnsureLAChannel error = %v", err)
	}
	if !created {
		t.Fatal("EnsureLAChannel created = false, want true")
	}

	now := time.Now().UTC()
	_, _, err = stores[1].CreateOrGetLAStartJob(context.Background(), &LiveActivityJob{
		ID:            uuid.NewString(),
		ActivityID:    activityID,
		ActivityType:  "RaceAttributes",
		TopicID:       jobTopicID,
		Status:        model.LiveActivityJobStatusActive,
		LatestPayload: json.RawMessage(`{"lap":1}`),
		CreatedAt:     now,
		UpdatedAt:     now,
	})
	if !errors.Is(err, Errors.Conflict) {
		t.Fatalf("CreateOrGetLAStartJob error = %v, want conflict", err)
	}
	if _, err := stores[0].GetLAJobByActivityID(context.Background(), activityID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("GetLAJobByActivityID error = %v, want not found", err)
	}
}

func TestEnsureLAChannelSerializesAgainstConflictingStartJob(t *testing.T) {
	databaseURL := laChannelIntegrationDatabaseURL(t)
	stores := newLAChannelIntegrationStores(t, databaseURL)

	channelTopicID := createLAChannelIntegrationTopic(t, stores[0])
	userID := "user-" + uuid.NewString()
	if _, err := stores[0].CreateUser(context.Background(), &User{ID: userID}); err != nil {
		t.Fatalf("CreateUser error = %v", err)
	}
	t.Cleanup(func() {
		if err := stores[0].DeleteUser(context.Background(), userID); err != nil &&
			!errors.Is(err, Errors.NotFound) {
			t.Errorf("DeleteUser error = %v", err)
		}
	})

	activityID := "activity-" + uuid.NewString()
	channelID := "channel-" + uuid.NewString()
	registerLAChannelIntegrationMappingCleanup(t, stores[0], activityID)

	providerEntered := make(chan struct{})
	releaseProvider := make(chan struct{})
	channelDone := make(chan error, 1)
	go func() {
		_, _, err := stores[0].EnsureLAChannel(
			context.Background(),
			activityID,
			channelTopicID,
			func(context.Context) (string, error) {
				close(providerEntered)
				<-releaseProvider
				return channelID, nil
			},
		)
		channelDone <- err
	}()
	<-providerEntered

	now := time.Now().UTC()
	jobDone := make(chan error, 1)
	go func() {
		_, _, err := stores[1].CreateOrGetLAStartJob(context.Background(), &LiveActivityJob{
			ID:            uuid.NewString(),
			ActivityID:    activityID,
			ActivityType:  "RaceAttributes",
			UserID:        userID,
			Status:        model.LiveActivityJobStatusActive,
			LatestPayload: json.RawMessage(`{"lap":1}`),
			CreatedAt:     now,
			UpdatedAt:     now,
		})
		jobDone <- err
	}()

	waiting, err := waitForLAChannelIntegrationLockWaiter(stores[0], activityID, 2*time.Second)
	if err != nil {
		close(releaseProvider)
		<-channelDone
		<-jobDone
		t.Fatalf("observe start-job lock waiter: %v", err)
	}
	if !waiting {
		close(releaseProvider)
		<-channelDone
		<-jobDone
		t.Fatal("start job did not wait for the channel activity lock")
	}

	close(releaseProvider)
	if err := <-channelDone; err != nil {
		t.Fatalf("EnsureLAChannel error = %v", err)
	}
	if err := <-jobDone; !errors.Is(err, Errors.Conflict) {
		t.Fatalf("CreateOrGetLAStartJob error = %v, want conflict", err)
	}
	if _, err := stores[0].GetLAJobByActivityID(context.Background(), activityID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("GetLAJobByActivityID error = %v, want not found", err)
	}
	stored, err := stores[1].GetLAChannelByActivityID(context.Background(), activityID)
	if err != nil {
		t.Fatalf("GetLAChannelByActivityID error = %v", err)
	}
	if stored.TopicID != channelTopicID || stored.ChannelID != channelID {
		t.Fatalf("stored channel = %+v, want topic %q and channel %q", stored, channelTopicID, channelID)
	}
}

func TestEnsureLAChannelDifferentActivitiesCreateConcurrently(t *testing.T) {
	databaseURL := laChannelIntegrationDatabaseURL(t)
	stores := newLAChannelIntegrationStores(t, databaseURL)

	topicID := createLAChannelIntegrationTopic(t, stores[0])
	const callerCount = 8
	activityIDs := make([]string, callerCount)
	channelIDs := make([]string, callerCount)
	errs := make([]error, callerCount)
	entered := make(chan struct{}, callerCount)
	release := make(chan struct{})
	var currentProviderCalls atomic.Int32
	var maxProviderCalls atomic.Int32

	for index := range callerCount {
		activityIDs[index] = "activity-" + uuid.NewString()
		channelIDs[index] = "channel-" + uuid.NewString()
		registerLAChannelIntegrationMappingCleanup(t, stores[0], activityIDs[index])
	}

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		runConcurrently(callerCount, func(index int) {
			_, _, errs[index] = stores[index%len(stores)].EnsureLAChannel(
				context.Background(),
				activityIDs[index],
				topicID,
				func(context.Context) (string, error) {
					inFlight := currentProviderCalls.Add(1)
					raiseAtomicMax(&maxProviderCalls, inFlight)
					entered <- struct{}{}
					<-release
					currentProviderCalls.Add(-1)
					return channelIDs[index], nil
				},
			)
		})
	}()

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for entryCount := 0; entryCount < 2; entryCount++ {
		select {
		case <-entered:
		case <-timer.C:
			close(release)
			<-runDone
			t.Fatalf(
				"only %d provider callback(s) entered concurrently; activity locks may be global",
				entryCount,
			)
		}
	}
	close(release)
	<-runDone

	for index, err := range errs {
		if err != nil {
			t.Fatalf("EnsureLAChannel call %d error = %v", index, err)
		}
	}
	if maxProviderCalls.Load() < 2 {
		t.Fatalf("maximum provider concurrency = %d, want at least 2", maxProviderCalls.Load())
	}
}

func TestEnsureLAChannelCanceledLockWaiterDoesNotCreate(t *testing.T) {
	databaseURL := laChannelIntegrationDatabaseURL(t)
	stores := newLAChannelIntegrationStores(t, databaseURL)

	topicID := createLAChannelIntegrationTopic(t, stores[0])
	activityID := "activity-" + uuid.NewString()
	channelID := "channel-" + uuid.NewString()
	registerLAChannelIntegrationMappingCleanup(t, stores[0], activityID)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := stores[0].EnsureLAChannel(
			context.Background(),
			activityID,
			topicID,
			func(context.Context) (string, error) {
				close(firstEntered)
				<-releaseFirst
				return channelID, nil
			},
		)
		firstDone <- err
	}()
	<-firstEntered

	waiterContext, cancelWaiter := context.WithCancel(context.Background())
	var waiterCreateCalls atomic.Int32
	waiterDone := make(chan error, 1)
	go func() {
		_, _, err := stores[1].EnsureLAChannel(
			waiterContext,
			activityID,
			topicID,
			func(context.Context) (string, error) {
				waiterCreateCalls.Add(1)
				return "unexpected-channel", nil
			},
		)
		waiterDone <- err
	}()

	waiting, waitErr := waitForLAChannelIntegrationLockWaiter(
		stores[0],
		activityID,
		2*time.Second,
	)
	if waitErr != nil {
		close(releaseFirst)
		<-firstDone
		t.Fatalf("observe lock waiter: %v", waitErr)
	}
	if !waiting {
		close(releaseFirst)
		<-firstDone
		t.Fatal("waiter did not reach the advisory lock")
	}

	cancelWaiter()
	select {
	case err := <-waiterDone:
		if err == nil {
			t.Fatal("canceled lock waiter error = nil")
		}
	case <-time.After(2 * time.Second):
		close(releaseFirst)
		<-firstDone
		t.Fatal("canceled lock waiter did not return")
	}
	if waiterCreateCalls.Load() != 0 {
		t.Fatalf("canceled waiter creator calls = %d, want 0", waiterCreateCalls.Load())
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first EnsureLAChannel error = %v", err)
	}
}

func TestEnsureLAChannelFailureWavesRetrySerially(t *testing.T) {
	databaseURL := laChannelIntegrationDatabaseURL(t)
	stores := newLAChannelIntegrationStores(t, databaseURL)

	testCases := []struct {
		name        string
		providerErr error
	}{
		{name: "provider failure", providerErr: errors.New("provider unavailable")},
		{name: "provider timeout", providerErr: context.DeadlineExceeded},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			topicID := createLAChannelIntegrationTopic(t, stores[0])
			activityID := "activity-" + uuid.NewString()
			registerLAChannelIntegrationMappingCleanup(t, stores[0], activityID)
			const callerCount = 8
			const providerDelay = 25 * time.Millisecond

			before := combinedDBStats(stores)
			stopPoolObserver := observeMaximumPoolUse(stores)
			var createCalls atomic.Int32
			var currentProviderCalls atomic.Int32
			var maxProviderCalls atomic.Int32
			errs := make([]error, callerCount)
			startedAt := time.Now()
			runConcurrently(callerCount, func(index int) {
				_, _, errs[index] = stores[index%len(stores)].EnsureLAChannel(
					context.Background(),
					activityID,
					topicID,
					func(context.Context) (string, error) {
						createCalls.Add(1)
						inFlight := currentProviderCalls.Add(1)
						raiseAtomicMax(&maxProviderCalls, inFlight)
						time.Sleep(providerDelay)
						currentProviderCalls.Add(-1)
						return "", testCase.providerErr
					},
				)
			})
			elapsed := time.Since(startedAt)
			maxPoolInUse := stopPoolObserver()
			after := combinedDBStats(stores)

			for index, err := range errs {
				if !errors.Is(err, testCase.providerErr) {
					t.Errorf("EnsureLAChannel call %d error = %v, want %v", index, err, testCase.providerErr)
				}
			}
			if createCalls.Load() != callerCount {
				t.Fatalf("provider calls = %d, want %d", createCalls.Load(), callerCount)
			}
			if maxProviderCalls.Load() != 1 {
				t.Fatalf("maximum provider concurrency = %d, want 1", maxProviderCalls.Load())
			}
			if _, err := stores[0].GetLAChannelByActivityID(context.Background(), activityID); !errors.Is(err, Errors.NotFound) {
				t.Fatalf("GetLAChannelByActivityID error = %v, want not found", err)
			}
			t.Logf(
				"%s wave: callers=%d elapsed=%s provider_calls=%d max_provider_concurrency=%d max_db_connections_in_use=%d db_wait_count=%d db_wait_duration=%s",
				testCase.name,
				callerCount,
				elapsed,
				createCalls.Load(),
				maxProviderCalls.Load(),
				maxPoolInUse,
				after.waitCount-before.waitCount,
				after.waitDuration-before.waitDuration,
			)
		})
	}
}

func TestDurationPercentileUsesNearestRank(t *testing.T) {
	durations := []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		3 * time.Millisecond,
		4 * time.Millisecond,
		5 * time.Millisecond,
		6 * time.Millisecond,
		7 * time.Millisecond,
		8 * time.Millisecond,
	}

	if got := durationPercentile(durations, 0.50); got != 4*time.Millisecond {
		t.Fatalf("p50 = %s, want 4ms", got)
	}
	if got := durationPercentile(durations, 0.95); got != 8*time.Millisecond {
		t.Fatalf("p95 = %s, want 8ms", got)
	}
}

func BenchmarkEnsureLAChannel(b *testing.B) {
	databaseURL := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if databaseURL == "" {
		b.Skip("PUSHBOY_TEST_DATABASE_URL is not set")
	}
	stores := newLAChannelIntegrationStores(b, databaseURL)
	topicID := createLAChannelIntegrationTopic(b, stores[0])

	for _, concurrency := range []int{1, 8, 32, 64, 128} {
		for _, providerDelay := range []time.Duration{0, 100 * time.Millisecond} {
			name := fmt.Sprintf("concurrency=%d/provider_delay=%s", concurrency, providerDelay)
			b.Run(name, func(b *testing.B) {
				var providerCalls atomic.Int64
				var providerInFlight atomic.Int32
				var maxProviderConcurrency atomic.Int32
				var errorCount atomic.Int64
				latencies := make([]time.Duration, 0, b.N*concurrency)
				var requestWallElapsed time.Duration
				var poolWaitCount int64
				var poolWaitDuration time.Duration
				stopPoolObserver := observeMaximumPoolUse(stores)

				for wave := 0; wave < b.N; wave++ {
					activityID := fmt.Sprintf("benchmark-activity-%s-%d", uuid.NewString(), wave)
					channelID := "benchmark-channel-" + uuid.NewString()
					waveLatencies := make([]time.Duration, concurrency)
					channels := make([]*LiveActivityChannel, concurrency)
					created := make([]bool, concurrency)
					errs := make([]error, concurrency)
					beforeWave := combinedDBStats(stores)
					waveStartedAt := time.Now()
					runConcurrently(concurrency, func(index int) {
						requestStartedAt := time.Now()
						channels[index], created[index], errs[index] = stores[index%len(stores)].EnsureLAChannel(
							context.Background(),
							activityID,
							topicID,
							func(context.Context) (string, error) {
								providerCalls.Add(1)
								inFlight := providerInFlight.Add(1)
								raiseAtomicMax(&maxProviderConcurrency, inFlight)
								if providerDelay > 0 {
									time.Sleep(providerDelay)
								}
								providerInFlight.Add(-1)
								return channelID, nil
							},
						)
						waveLatencies[index] = time.Since(requestStartedAt)
					})
					requestWallElapsed += time.Since(waveStartedAt)
					afterWave := combinedDBStats(stores)
					poolWaitCount += afterWave.waitCount - beforeWave.waitCount
					poolWaitDuration += afterWave.waitDuration - beforeWave.waitDuration
					deleteLAChannelIntegrationMapping(b, stores[0], activityID)

					createdCount := 0
					for index, err := range errs {
						if err != nil {
							errorCount.Add(1)
							continue
						}
						if created[index] {
							createdCount++
						}
						if channels[index].ChannelID != channelID {
							b.Fatalf(
								"wave %d call %d channel ID = %q, want %q",
								wave,
								index,
								channels[index].ChannelID,
								channelID,
							)
						}
					}
					if createdCount != 1 {
						b.Fatalf("wave %d created results = %d, want 1", wave, createdCount)
					}
					latencies = append(latencies, waveLatencies...)
				}

				maxPoolInUse := stopPoolObserver()
				expectedProviderCalls := int64(b.N)
				if providerCalls.Load() != expectedProviderCalls {
					b.Fatalf("provider calls = %d, want %d", providerCalls.Load(), expectedProviderCalls)
				}
				if errorCount.Load() != 0 {
					b.Fatalf("errors = %d, want 0", errorCount.Load())
				}

				sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
				b.ReportMetric(
					float64(requestWallElapsed.Nanoseconds())/float64(b.N),
					"wall-ns/wave",
				)
				b.ReportMetric(float64(durationPercentile(latencies, 0.50).Nanoseconds()), "p50-ns/request")
				b.ReportMetric(float64(durationPercentile(latencies, 0.95).Nanoseconds()), "p95-ns/request")
				b.ReportMetric(float64(latencies[len(latencies)-1].Nanoseconds()), "max-ns/request")
				b.ReportMetric(float64(providerCalls.Load())/float64(b.N), "provider-calls/wave")
				b.ReportMetric(float64(maxProviderConcurrency.Load()), "max-provider-concurrency")
				b.ReportMetric(float64(errorCount.Load()), "errors")
				b.ReportMetric(float64(maxPoolInUse), "max-db-connections-in-use")
				b.ReportMetric(
					float64(poolWaitCount)/float64(b.N),
					"db-pool-waits/wave",
				)
				b.ReportMetric(
					float64(poolWaitDuration.Nanoseconds())/float64(b.N),
					"db-pool-wait-ns/wave",
				)
			})
		}
	}
}

func laChannelIntegrationDatabaseURL(t testing.TB) string {
	t.Helper()
	databaseURL := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PUSHBOY_TEST_DATABASE_URL is not set")
	}
	return databaseURL
}

func newLAChannelIntegrationStores(t testing.TB, databaseURL string) []*PostgresStore {
	t.Helper()
	firstStore := newLAChannelIntegrationStore(t, databaseURL)
	firstStore.db.SetMaxOpenConns(laChannelIntegrationMaxOpenConnectionsPerStore)
	firstStore.db.SetMaxIdleConns(laChannelIntegrationMaxOpenConnectionsPerStore / 2)
	t.Cleanup(func() {
		if err := firstStore.Close(); err != nil {
			t.Errorf("first store Close error = %v", err)
		}
	})
	secondStore := newLAChannelIntegrationStore(t, databaseURL)
	secondStore.db.SetMaxOpenConns(laChannelIntegrationMaxOpenConnectionsPerStore)
	secondStore.db.SetMaxIdleConns(laChannelIntegrationMaxOpenConnectionsPerStore / 2)
	t.Cleanup(func() {
		if err := secondStore.Close(); err != nil {
			t.Errorf("second store Close error = %v", err)
		}
	})
	return []*PostgresStore{firstStore, secondStore}
}

func newLAChannelIntegrationStore(t testing.TB, databaseURL string) *PostgresStore {
	t.Helper()

	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd error = %v", err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatalf("Chdir repository root error = %v", err)
	}
	store, storeErr := NewPostgresStore(databaseURL)
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatalf("restore working directory error = %v", err)
	}
	if storeErr != nil {
		t.Fatalf("NewPostgresStore error = %v", storeErr)
	}
	return store
}

func createLAChannelIntegrationTopic(t testing.TB, store *PostgresStore) string {
	t.Helper()
	topicID := "channel-lock-" + uuid.NewString()
	if err := store.CreateTopic(context.Background(), &Topic{
		ID:   topicID,
		Name: topicID,
	}); err != nil {
		t.Fatalf("CreateTopic error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.DeleteTopic(context.Background(), topicID); err != nil &&
			!errors.Is(err, Errors.NotFound) {
			t.Errorf("DeleteTopic error = %v", err)
		}
	})
	return topicID
}

func deleteLAChannelIntegrationMapping(
	t testing.TB,
	store *PostgresStore,
	activityID string,
) {
	t.Helper()
	if _, err := store.db.ExecContext(
		context.Background(),
		`DELETE FROM live_activity_channels WHERE activity_id = $1`,
		activityID,
	); err != nil {
		t.Errorf("delete live activity channel mapping: %v", err)
	}
}

func registerLAChannelIntegrationMappingCleanup(
	t testing.TB,
	store *PostgresStore,
	activityID string,
) {
	t.Helper()
	t.Cleanup(func() {
		deleteLAChannelIntegrationMapping(t, store, activityID)
	})
}

func waitForLAChannelIntegrationLockWaiter(
	store *PostgresStore,
	activityID string,
	timeout time.Duration,
) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var waiting bool
		err := store.db.QueryRowContext(
			context.Background(),
			`SELECT EXISTS (
				SELECT 1
				FROM pg_locks
				WHERE locktype = 'advisory'
				  AND NOT granted
				  AND classid::bigint = (
					hashtext('live-activity-channel')::bigint & 4294967295
				  )
				  AND objid::bigint = (
					hashtext($1)::bigint & 4294967295
				  )
			)`,
			activityID,
		).Scan(&waiting)
		if err != nil {
			return false, err
		}
		if waiting {
			return true, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false, nil
}

func runConcurrently(count int, run func(index int)) {
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	for index := 0; index < count; index++ {
		index := index
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			run(index)
		}()
	}
	close(start)
	waitGroup.Wait()
}

func raiseAtomicMax(maximum *atomic.Int32, candidate int32) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}

type laChannelDBStats struct {
	waitCount    int64
	waitDuration time.Duration
}

func combinedDBStats(stores []*PostgresStore) laChannelDBStats {
	var combined laChannelDBStats
	for _, store := range stores {
		stats := store.db.Stats()
		combined.waitCount += stats.WaitCount
		combined.waitDuration += stats.WaitDuration
	}
	return combined
}

func observeMaximumPoolUse(stores []*PostgresStore) func() int32 {
	var maximum atomic.Int32
	stop := make(chan struct{})
	done := make(chan struct{})
	sample := func() {
		var inUse int32
		for _, store := range stores {
			inUse += int32(store.db.Stats().InUse)
		}
		raiseAtomicMax(&maximum, inUse)
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sample()
			case <-stop:
				sample()
				return
			}
		}
	}()
	return func() int32 {
		close(stop)
		<-done
		return maximum.Load()
	}
}

func durationPercentile(durations []time.Duration, percentile float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	rank := int(math.Ceil(percentile * float64(len(durations))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(durations) {
		rank = len(durations)
	}
	return durations[rank-1]
}
