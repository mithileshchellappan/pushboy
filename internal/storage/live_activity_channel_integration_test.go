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

// Keep both pools below PostgreSQL's common 100-connection default. Run
// env-gated integration packages with -p=1 because they share one test database.
const laChannelTestMaxOpenConnections = 32

func TestEnsureLAChannelSerializesSameActivityAcrossStores(t *testing.T) {
	h := newLAChannelDBHarness(t)
	topicID, activityID := h.topic(), h.activity()
	channelID := "channel-" + uuid.NewString()
	var providerCalls atomic.Int32

	type result struct {
		channel *LiveActivityChannel
		created bool
		err     error
	}
	const callers = 32
	results := make([]result, callers)
	runLAChannelConcurrent(callers, func(i int) {
		results[i].channel, results[i].created, results[i].err = h.stores[i%2].EnsureLAChannel(
			context.Background(), activityID, topicID,
			func(context.Context) (string, error) {
				providerCalls.Add(1)
				time.Sleep(50 * time.Millisecond)
				return channelID, nil
			},
		)
	})

	created := 0
	for i, result := range results {
		if result.err != nil {
			t.Fatalf("caller %d: %v", i, result.err)
		}
		if result.created {
			created++
		}
		if result.channel.ActivityID != activityID ||
			result.channel.TopicID != topicID ||
			result.channel.ChannelID != channelID {
			t.Fatalf("caller %d channel = %+v", i, result.channel)
		}
	}
	if providerCalls.Load() != 1 || created != 1 {
		t.Fatalf("provider calls = %d, created results = %d; want 1, 1", providerCalls.Load(), created)
	}
	stored, err := h.stores[1].GetLAChannelByActivityID(context.Background(), activityID)
	if err != nil || stored.ChannelID != channelID {
		t.Fatalf("stored channel = %+v, err = %v", stored, err)
	}
}

func TestEnsureLAChannelPreexistingMappingSkipsProvider(t *testing.T) {
	h := newLAChannelDBHarness(t)
	topicID, activityID := h.topic(), h.activity()
	want := "channel-" + uuid.NewString()
	first, created, err := h.stores[0].EnsureLAChannel(
		context.Background(), activityID, topicID,
		func(context.Context) (string, error) { return want, nil },
	)
	if err != nil || !created {
		t.Fatalf("first ensure: created = %v, err = %v", created, err)
	}

	var calls atomic.Int32
	existing, created, err := h.stores[1].EnsureLAChannel(
		context.Background(), activityID, topicID,
		func(context.Context) (string, error) {
			calls.Add(1)
			return "unexpected", nil
		},
	)
	if err != nil || created || calls.Load() != 0 || existing.ChannelID != first.ChannelID {
		t.Fatalf(
			"existing ensure: channel = %+v, created = %v, calls = %d, err = %v",
			existing, created, calls.Load(), err,
		)
	}
}

func TestLAChannelAndJobRejectConflictingTopics(t *testing.T) {
	t.Run("channel after job", func(t *testing.T) {
		h := newLAChannelDBHarness(t)
		jobTopic, channelTopic, activityID := h.topic(), h.topic(), h.activity()
		h.createJob(activityID, jobTopic)

		var calls atomic.Int32
		_, _, err := h.stores[1].EnsureLAChannel(
			context.Background(), activityID, channelTopic,
			func(context.Context) (string, error) {
				calls.Add(1)
				return "unexpected", nil
			},
		)
		if !errors.Is(err, Errors.Conflict) || calls.Load() != 0 {
			t.Fatalf("ensure error = %v, provider calls = %d", err, calls.Load())
		}
		if _, err := h.stores[0].GetLAChannelByActivityID(
			context.Background(), activityID,
		); !errors.Is(err, Errors.NotFound) {
			t.Fatalf("channel lookup error = %v, want not found", err)
		}
	})

	t.Run("job after channel", func(t *testing.T) {
		h := newLAChannelDBHarness(t)
		channelTopic, jobTopic, activityID := h.topic(), h.topic(), h.activity()
		_, created, err := h.stores[0].EnsureLAChannel(
			context.Background(), activityID, channelTopic,
			func(context.Context) (string, error) { return "channel-" + uuid.NewString(), nil },
		)
		if err != nil || !created {
			t.Fatalf("ensure: created = %v, err = %v", created, err)
		}
		_, _, err = h.stores[1].CreateOrGetLAStartJob(
			context.Background(), testLAStartJob(activityID, jobTopic),
		)
		if !errors.Is(err, Errors.Conflict) {
			t.Fatalf("job error = %v, want conflict", err)
		}
		if _, err := h.stores[0].GetLAJobByActivityID(
			context.Background(), activityID,
		); !errors.Is(err, Errors.NotFound) {
			t.Fatalf("job lookup error = %v, want not found", err)
		}
	})
}

func TestLAChannelAndConflictingJobShareActivityLock(t *testing.T) {
	h := newLAChannelDBHarness(t)
	channelTopic, jobTopic, activityID := h.topic(), h.topic(), h.activity()
	channelID := "channel-" + uuid.NewString()
	providerEntered, releaseProvider := make(chan struct{}), make(chan struct{})
	channelDone, jobDone := make(chan error, 1), make(chan error, 1)

	go func() {
		_, _, err := h.stores[0].EnsureLAChannel(
			context.Background(), activityID, channelTopic,
			func(context.Context) (string, error) {
				close(providerEntered)
				<-releaseProvider
				return channelID, nil
			},
		)
		channelDone <- err
	}()
	<-providerEntered

	job := testLAStartJob(activityID, jobTopic)
	t.Cleanup(func() {
		if _, err := h.stores[0].db.ExecContext(
			context.Background(), `DELETE FROM live_activity_jobs WHERE id = $1`, job.ID,
		); err != nil {
			t.Errorf("delete concurrent start job: %v", err)
		}
	})
	go func() {
		_, _, err := h.stores[1].CreateOrGetLAStartJob(context.Background(), job)
		jobDone <- err
	}()

	waiting, waitErr := h.waitForLockWaiter(activityID, 2*time.Second)
	close(releaseProvider)
	channelErr, jobErr := <-channelDone, <-jobDone
	if waitErr != nil {
		t.Fatalf("observe start-job lock waiter: %v", waitErr)
	}
	if !waiting {
		t.Fatal("start job did not wait for the channel activity lock")
	}
	if channelErr != nil {
		t.Fatalf("ensure channel: %v", channelErr)
	}
	if !errors.Is(jobErr, Errors.Conflict) {
		t.Fatalf("start job error = %v, want conflict", jobErr)
	}
	stored, err := h.stores[1].GetLAChannelByActivityID(context.Background(), activityID)
	if err != nil || stored.TopicID != channelTopic || stored.ChannelID != channelID {
		t.Fatalf("stored channel = %+v, err = %v", stored, err)
	}
	if _, err := h.stores[0].GetLAJobByActivityID(
		context.Background(), activityID,
	); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("job lookup error = %v, want not found", err)
	}
}

func TestEnsureLAChannelDifferentActivitiesCreateConcurrently(t *testing.T) {
	h := newLAChannelDBHarness(t)
	topicID := h.topic()
	const callers = 8
	activities := make([]string, callers)
	errs := make([]error, callers)
	entered, release, done := make(chan struct{}, callers), make(chan struct{}), make(chan struct{})
	for i := range activities {
		activities[i] = h.activity()
	}

	go func() {
		defer close(done)
		runLAChannelConcurrent(callers, func(i int) {
			_, _, errs[i] = h.stores[i%2].EnsureLAChannel(
				context.Background(), activities[i], topicID,
				func(context.Context) (string, error) {
					entered <- struct{}{}
					<-release
					return "channel-" + uuid.NewString(), nil
				},
			)
		})
	}()

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-timer.C:
			close(release)
			<-done
			t.Fatalf("only %d provider callback(s) entered concurrently", i)
		}
	}
	close(release)
	<-done
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
}

func TestEnsureLAChannelCanceledWaiterDoesNotCallProvider(t *testing.T) {
	h := newLAChannelDBHarness(t)
	topicID, activityID := h.topic(), h.activity()
	firstEntered, releaseFirst := make(chan struct{}), make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := h.stores[0].EnsureLAChannel(
			context.Background(), activityID, topicID,
			func(context.Context) (string, error) {
				close(firstEntered)
				<-releaseFirst
				return "channel-" + uuid.NewString(), nil
			},
		)
		firstDone <- err
	}()
	<-firstEntered
	defer func() {
		close(releaseFirst)
		if err := <-firstDone; err != nil {
			t.Errorf("first ensure: %v", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	var waiterCalls atomic.Int32
	waiterDone := make(chan error, 1)
	go func() {
		_, _, err := h.stores[1].EnsureLAChannel(
			ctx, activityID, topicID,
			func(context.Context) (string, error) {
				waiterCalls.Add(1)
				return "unexpected", nil
			},
		)
		waiterDone <- err
	}()
	waiting, err := h.waitForLockWaiter(activityID, 2*time.Second)
	if err != nil {
		t.Fatalf("observe canceled lock waiter: %v", err)
	}
	if !waiting {
		t.Fatal("second caller did not wait for the advisory lock")
	}
	cancel()
	select {
	case err := <-waiterDone:
		if err == nil {
			t.Fatal("canceled waiter returned nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled waiter did not return")
	}
	if waiterCalls.Load() != 0 {
		t.Fatalf("canceled waiter provider calls = %d, want 0", waiterCalls.Load())
	}
}

func TestEnsureLAChannelProviderFailuresRetrySerially(t *testing.T) {
	for _, providerErr := range []error{errors.New("provider unavailable"), context.DeadlineExceeded} {
		t.Run(providerErr.Error(), func(t *testing.T) {
			h := newLAChannelDBHarness(t)
			topicID, activityID := h.topic(), h.activity()
			const callers = 8
			var calls, inFlight, maxInFlight, maxConnections atomic.Int32
			errs := make([]error, callers)
			sampleConnections := func() {
				var inUse int32
				for _, store := range h.stores {
					inUse += int32(store.db.Stats().InUse)
				}
				raiseLAChannelAtomicMax(&maxConnections, inUse)
			}
			before := combinedLAChannelDBStats(h.stores)
			started := time.Now()
			runLAChannelConcurrent(callers, func(i int) {
				_, _, errs[i] = h.stores[i%2].EnsureLAChannel(
					context.Background(), activityID, topicID,
					func(context.Context) (string, error) {
						calls.Add(1)
						raiseLAChannelAtomicMax(&maxInFlight, inFlight.Add(1))
						sampleConnections()
						time.Sleep(10 * time.Millisecond)
						sampleConnections()
						inFlight.Add(-1)
						return "", providerErr
					},
				)
			})
			elapsed := time.Since(started)
			after := combinedLAChannelDBStats(h.stores)
			for i, err := range errs {
				if !errors.Is(err, providerErr) {
					t.Errorf("caller %d error = %v, want %v", i, err, providerErr)
				}
			}
			if calls.Load() != callers || maxInFlight.Load() != 1 {
				t.Fatalf("provider calls = %d, max concurrency = %d; want %d, 1",
					calls.Load(), maxInFlight.Load(), callers)
			}
			if _, err := h.stores[0].GetLAChannelByActivityID(
				context.Background(), activityID,
			); !errors.Is(err, Errors.NotFound) {
				t.Fatalf("channel lookup error = %v, want not found", err)
			}
			t.Logf(
				"failure wave: callers=%d elapsed=%s provider_calls=%d max_provider_concurrency=%d max_db_connections_in_use=%d db_wait_count=%d db_wait_duration=%s",
				callers,
				elapsed,
				calls.Load(),
				maxInFlight.Load(),
				maxConnections.Load(),
				after.waitCount-before.waitCount,
				after.waitDuration-before.waitDuration,
			)
		})
	}
}

func BenchmarkEnsureLAChannel(b *testing.B) {
	h := newLAChannelDBHarness(b)
	topicID := h.topic()
	for _, concurrency := range []int{1, 8, 32, 64, 128} {
		for _, delay := range []time.Duration{0, 100 * time.Millisecond} {
			b.Run(fmt.Sprintf("%d_callers/%s_provider", concurrency, delay), func(b *testing.B) {
				var providerCalls, providerInFlight, maxProvider, errorCount atomic.Int64
				latencies := make([]time.Duration, 0, b.N*concurrency)
				var wallElapsed, poolWaitDuration time.Duration
				var poolWaitCount int64
				for wave := 0; wave < b.N; wave++ {
					activityID, channelID := "bench-"+uuid.NewString(), "channel-"+uuid.NewString()
					waveLatency := make([]time.Duration, concurrency)
					created := make([]bool, concurrency)
					beforeWave := combinedLAChannelDBStats(h.stores)
					waveStarted := time.Now()
					runLAChannelConcurrent(concurrency, func(i int) {
						requestStarted := time.Now()
						channel, wasCreated, err := h.stores[i%2].EnsureLAChannel(
							context.Background(), activityID, topicID,
							func(context.Context) (string, error) {
								providerCalls.Add(1)
								inFlight := providerInFlight.Add(1)
								raiseLAChannelAtomicMax64(&maxProvider, inFlight)
								time.Sleep(delay)
								providerInFlight.Add(-1)
								return channelID, nil
							},
						)
						waveLatency[i], created[i] = time.Since(requestStarted), wasCreated
						if err != nil || channel == nil || channel.ChannelID != channelID {
							errorCount.Add(1)
						}
					})
					wallElapsed += time.Since(waveStarted)
					afterWave := combinedLAChannelDBStats(h.stores)
					poolWaitCount += afterWave.waitCount - beforeWave.waitCount
					poolWaitDuration += afterWave.waitDuration - beforeWave.waitDuration
					if countTrue(created) != 1 {
						b.Fatalf("wave %d created results = %d, want 1", wave, countTrue(created))
					}
					latencies = append(latencies, waveLatency...)
					h.deleteMapping(activityID)
				}
				sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
				b.ReportMetric(float64(wallElapsed.Nanoseconds())/float64(b.N), "wall-ns/wave")
				b.ReportMetric(float64(durationAtPercentile(latencies, .50)), "p50-ns/request")
				b.ReportMetric(float64(durationAtPercentile(latencies, .95)), "p95-ns/request")
				b.ReportMetric(float64(latencies[len(latencies)-1]), "max-ns/request")
				b.ReportMetric(float64(providerCalls.Load())/float64(b.N), "provider-calls/wave")
				b.ReportMetric(float64(maxProvider.Load()), "max-provider-concurrency")
				b.ReportMetric(float64(errorCount.Load()), "errors")
				b.ReportMetric(float64(poolWaitCount)/float64(b.N), "db-waits/wave")
				b.ReportMetric(
					float64(poolWaitDuration.Nanoseconds())/float64(b.N),
					"db-wait-ns/wave",
				)
				if providerCalls.Load() != int64(b.N) || errorCount.Load() != 0 {
					b.Fatalf("provider calls = %d, errors = %d", providerCalls.Load(), errorCount.Load())
				}
			})
		}
	}
}

type laChannelDBHarness struct {
	t      testing.TB
	stores []*PostgresStore
}

func newLAChannelDBHarness(t testing.TB) *laChannelDBHarness {
	t.Helper()
	databaseURL := os.Getenv("PUSHBOY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PUSHBOY_TEST_DATABASE_URL is not set")
	}
	h := &laChannelDBHarness{t: t}
	for range 2 {
		store := newLAChannelTestStore(t, databaseURL)
		store.db.SetMaxOpenConns(laChannelTestMaxOpenConnections)
		store.db.SetMaxIdleConns(laChannelTestMaxOpenConnections / 2)
		h.stores = append(h.stores, store)
		t.Cleanup(func() {
			if err := store.Close(); err != nil {
				t.Errorf("store close: %v", err)
			}
		})
	}
	return h
}

func newLAChannelTestStore(t testing.TB, databaseURL string) *PostgresStore {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(filepath.Clean(filepath.Join(workingDirectory, "..", ".."))); err != nil {
		t.Fatalf("change to repository root: %v", err)
	}
	store, storeErr := NewPostgresStore(databaseURL)
	if err := os.Chdir(workingDirectory); err != nil {
		t.Fatalf("restore working directory: %v", err)
	}
	if storeErr != nil {
		t.Fatalf("create store: %v", storeErr)
	}
	return store
}

func (h *laChannelDBHarness) topic() string {
	h.t.Helper()
	topicID := "channel-lock-" + uuid.NewString()
	if err := h.stores[0].CreateTopic(context.Background(), &Topic{ID: topicID, Name: topicID}); err != nil {
		h.t.Fatalf("create topic: %v", err)
	}
	h.t.Cleanup(func() {
		if err := h.stores[0].DeleteTopic(context.Background(), topicID); err != nil &&
			!errors.Is(err, Errors.NotFound) {
			h.t.Errorf("delete topic: %v", err)
		}
	})
	return topicID
}

func (h *laChannelDBHarness) activity() string {
	h.t.Helper()
	activityID := "activity-" + uuid.NewString()
	h.t.Cleanup(func() { h.deleteMapping(activityID) })
	return activityID
}

func (h *laChannelDBHarness) deleteMapping(activityID string) {
	h.t.Helper()
	if _, err := h.stores[0].db.ExecContext(
		context.Background(),
		`DELETE FROM live_activity_channels WHERE activity_id = $1`,
		activityID,
	); err != nil {
		h.t.Errorf("delete channel mapping: %v", err)
	}
}

func (h *laChannelDBHarness) createJob(activityID, topicID string) {
	h.t.Helper()
	job, created, err := h.stores[0].CreateOrGetLAStartJob(
		context.Background(), testLAStartJob(activityID, topicID),
	)
	if err != nil || !created {
		h.t.Fatalf("create start job: created = %v, err = %v", created, err)
	}
	h.t.Cleanup(func() {
		if _, err := h.stores[0].db.ExecContext(
			context.Background(), `DELETE FROM live_activity_jobs WHERE id = $1`, job.ID,
		); err != nil {
			h.t.Errorf("delete start job: %v", err)
		}
	})
}

func (h *laChannelDBHarness) waitForLockWaiter(
	activityID string,
	timeout time.Duration,
) (bool, error) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var waiting bool
		err := h.stores[0].db.QueryRowContext(context.Background(), `SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE locktype = 'advisory' AND NOT granted
			  AND classid::bigint = (hashtext('live-activity-channel')::bigint & 4294967295)
			  AND objid::bigint = (hashtext($1)::bigint & 4294967295)
			)`, activityID).Scan(&waiting)
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

func testLAStartJob(activityID, topicID string) *LiveActivityJob {
	now := time.Now().UTC()
	return &LiveActivityJob{
		ID:            uuid.NewString(),
		ActivityID:    activityID,
		ActivityType:  "RaceAttributes",
		TopicID:       topicID,
		Status:        model.LiveActivityJobStatusActive,
		LatestPayload: json.RawMessage(`{"lap":1}`),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func runLAChannelConcurrent(count int, run func(int)) {
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := 0; i < count; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			run(i)
		}(i)
	}
	close(start)
	group.Wait()
}

func raiseLAChannelAtomicMax(maximum *atomic.Int32, candidate int32) {
	for {
		current := maximum.Load()
		if candidate <= current || maximum.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func raiseLAChannelAtomicMax64(maximum *atomic.Int64, candidate int64) {
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

func combinedLAChannelDBStats(stores []*PostgresStore) (result laChannelDBStats) {
	for _, store := range stores {
		stats := store.db.Stats()
		result.waitCount += stats.WaitCount
		result.waitDuration += stats.WaitDuration
	}
	return result
}

func durationAtPercentile(sorted []time.Duration, percentile float64) time.Duration {
	rank := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if rank < 0 {
		return 0
	}
	return sorted[rank]
}

func countTrue(values []bool) (count int) {
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}
