package workers

import (
	"context"
	"errors"
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

// fanoutLAStoreStub embeds storage.Store so only the methods FanoutLATokens
// touches need implementations; anything else panics loudly.
type fanoutLAStoreStub struct {
	storage.Store

	supersedeResults []supersedeResult
	supersedeCalls   []int

	statusUpdates []string
	failedLAJobs  []string

	tokenBatches     []*storage.LiveActivityTokenBatch
	tokenBatchErrors []error
	tokenBatchCall   int
	onTokenBatch     func(call int)

	completedEnqueues []int
	failedEnqueues    []int
	appliedOutcomes   [][]model.LASendOutcome
}

type supersedeResult struct {
	superseded bool
	err        error
}

func (s *fanoutLAStoreStub) SupersedeLADispatchIfStale(ctx context.Context, dispatchID string, emittedCount int) (bool, error) {
	s.supersedeCalls = append(s.supersedeCalls, emittedCount)
	result := supersedeResult{}
	if len(s.supersedeResults) > 0 {
		result = s.supersedeResults[0]
		s.supersedeResults = s.supersedeResults[1:]
	}
	return result.superseded, result.err
}

func (s *fanoutLAStoreStub) UpdateLADispatchStatus(ctx context.Context, dispatchID string, status string) error {
	s.statusUpdates = append(s.statusUpdates, status)
	return nil
}

func (s *fanoutLAStoreStub) FailLAJobIfActive(ctx context.Context, jobID string) error {
	s.failedLAJobs = append(s.failedLAJobs, jobID)
	return nil
}

func (s *fanoutLAStoreStub) GetLATokenBatchForDispatch(ctx context.Context, dispatchID string, cursor string, batchSize int) (*storage.LiveActivityTokenBatch, error) {
	call := s.tokenBatchCall
	if s.onTokenBatch != nil {
		s.onTokenBatch(call)
	}
	if call < len(s.tokenBatchErrors) && s.tokenBatchErrors[call] != nil {
		return nil, s.tokenBatchErrors[call]
	}
	if s.tokenBatchCall >= len(s.tokenBatches) {
		return &storage.LiveActivityTokenBatch{}, nil
	}
	batch := s.tokenBatches[s.tokenBatchCall]
	s.tokenBatchCall++
	return batch, nil
}

func (s *fanoutLAStoreStub) CompleteLADispatchEnqueue(ctx context.Context, dispatchID string, totalCount int) error {
	s.completedEnqueues = append(s.completedEnqueues, totalCount)
	return nil
}

func (s *fanoutLAStoreStub) FailLADispatchEnqueue(ctx context.Context, dispatchID string, totalCount int) error {
	s.failedEnqueues = append(s.failedEnqueues, totalCount)
	return nil
}

func (s *fanoutLAStoreStub) ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.LASendOutcome) error {
	s.appliedOutcomes = append(s.appliedOutcomes, outcomes)
	return nil
}

func laFanoutJob() model.LAJobItem {
	return model.LAJobItem{
		ID:         "job-item-1",
		Action:     model.LiveActivityActionUpdate,
		JobID:      "la-job-1",
		DispatchID: "dispatch-1",
	}
}

func laTokenBatch(hasMore bool, ids ...string) *storage.LiveActivityTokenBatch {
	batch := &storage.LiveActivityTokenBatch{HasMore: hasMore}
	for _, id := range ids {
		batch.Tokens = append(batch.Tokens, storage.LiveActivityToken{
			ID:       id,
			Token:    "tok-" + id,
			Platform: model.APNS,
		})
	}
	if hasMore && len(batch.Tokens) > 0 {
		batch.NextCursor = batch.Tokens[len(batch.Tokens)-1].ID
	}
	return batch
}

func TestFanoutLATokensFailsClosedWhenInitialSupersedeCheckErrors(t *testing.T) {
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{{err: errors.New("db down")}},
	}

	var emitted []model.LASendTask
	err := FanoutLATokens(context.Background(), store, laFanoutJob(), 10, func(ctx context.Context, task model.LASendTask) error {
		emitted = append(emitted, task)
		return nil
	})

	if err == nil {
		t.Fatal("expected error when supersede check fails, got nil")
	}
	if len(emitted) != 0 {
		t.Fatalf("emitted %d tasks, want 0", len(emitted))
	}
	if len(store.failedEnqueues) != 1 || store.failedEnqueues[0] != 0 {
		t.Fatalf("failed enqueues = %v, want [0]", store.failedEnqueues)
	}
}

func TestFanoutLATokensDoesNotRunSupersedeChecksForStart(t *testing.T) {
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{{err: errors.New("db down")}},
		tokenBatches: []*storage.LiveActivityTokenBatch{
			laTokenBatch(false, "t1"),
		},
	}
	job := laFanoutJob()
	job.Action = model.LiveActivityActionStart

	err := FanoutLATokens(context.Background(), store, job, 10, func(ctx context.Context, task model.LASendTask) error {
		return nil
	})

	if err != nil {
		t.Fatalf("FanoutLATokens error = %v", err)
	}
	if len(store.supersedeCalls) != 0 {
		t.Fatalf("supersede calls = %v, want none for start", store.supersedeCalls)
	}
	if len(store.completedEnqueues) != 1 || store.completedEnqueues[0] != 1 {
		t.Fatalf("completed enqueues = %v, want [1]", store.completedEnqueues)
	}
}

func TestFanoutLAStartCarriesBroadcastChannelCapabilityToSendTask(t *testing.T) {
	store := &fanoutLAStoreStub{
		tokenBatches: []*storage.LiveActivityTokenBatch{{
			Tokens: []storage.LiveActivityToken{{
				ID:                        "start-token-1",
				Token:                     "push-to-start-token",
				Platform:                  model.APNS,
				TokenType:                 model.LiveActivityTokenTypeStart,
				SupportsBroadcastChannels: true,
			}},
		}},
	}
	job := laFanoutJob()
	job.Action = model.LiveActivityActionStart

	var emitted []model.LASendTask
	err := FanoutLATokens(context.Background(), store, job, 10, func(ctx context.Context, task model.LASendTask) error {
		emitted = append(emitted, task)
		return nil
	})
	if err != nil {
		t.Fatalf("FanoutLATokens error = %v", err)
	}
	if len(emitted) != 1 {
		t.Fatalf("emitted tasks = %d, want 1", len(emitted))
	}
	if !emitted[0].SupportsBroadcastChannels {
		t.Fatal("SupportsBroadcastChannels = false, want true")
	}
}

func TestFanoutLATokensSkipsWhenSupersededBeforeFanout(t *testing.T) {
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{{superseded: true}},
	}

	var emitted []model.LASendTask
	err := FanoutLATokens(context.Background(), store, laFanoutJob(), 10, func(ctx context.Context, task model.LASendTask) error {
		emitted = append(emitted, task)
		return nil
	})

	if err != nil {
		t.Fatalf("FanoutLATokens error = %v", err)
	}
	if len(emitted) != 0 {
		t.Fatalf("emitted %d tasks, want 0", len(emitted))
	}
	if len(store.statusUpdates) != 0 {
		t.Fatalf("status updates = %v, want none", store.statusUpdates)
	}
	if len(store.supersedeCalls) != 1 || store.supersedeCalls[0] != 0 {
		t.Fatalf("supersede calls = %v, want [0]", store.supersedeCalls)
	}
}

func TestFanoutLATokensRecordsEmittedCountWhenSupersededMidFanout(t *testing.T) {
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{
			{},                 // initial guard: not stale
			{},                 // before batch 1: not stale
			{superseded: true}, // before batch 2: superseded
		},
		tokenBatches: []*storage.LiveActivityTokenBatch{
			laTokenBatch(true, "t1", "t2"),
			laTokenBatch(false, "t3"),
		},
	}

	var emitted []model.LASendTask
	err := FanoutLATokens(context.Background(), store, laFanoutJob(), 2, func(ctx context.Context, task model.LASendTask) error {
		emitted = append(emitted, task)
		return nil
	})

	if err != nil {
		t.Fatalf("FanoutLATokens error = %v", err)
	}
	if len(emitted) != 2 {
		t.Fatalf("emitted %d tasks, want 2 (first batch only)", len(emitted))
	}
	want := []int{0, 0, 2}
	if len(store.supersedeCalls) != len(want) {
		t.Fatalf("supersede calls = %v, want %v", store.supersedeCalls, want)
	}
	for i, count := range want {
		if store.supersedeCalls[i] != count {
			t.Fatalf("supersede call %d emitted count = %d, want %d", i, store.supersedeCalls[i], count)
		}
	}
	if len(store.completedEnqueues) != 0 {
		t.Fatalf("completed enqueues = %v, want none for a superseded dispatch", store.completedEnqueues)
	}
}

func TestFanoutLATokensCompletesWhenNotSuperseded(t *testing.T) {
	store := &fanoutLAStoreStub{
		tokenBatches: []*storage.LiveActivityTokenBatch{
			laTokenBatch(false, "t1", "t2"),
		},
	}

	var emitted []model.LASendTask
	err := FanoutLATokens(context.Background(), store, laFanoutJob(), 10, func(ctx context.Context, task model.LASendTask) error {
		emitted = append(emitted, task)
		return nil
	})

	if err != nil {
		t.Fatalf("FanoutLATokens error = %v", err)
	}
	if len(emitted) != 2 {
		t.Fatalf("emitted %d tasks, want 2", len(emitted))
	}
	if len(store.completedEnqueues) != 1 || store.completedEnqueues[0] != 2 {
		t.Fatalf("completed enqueues = %v, want [2]", store.completedEnqueues)
	}
	if len(store.statusUpdates) != 1 || store.statusUpdates[0] != "IN_PROGRESS" {
		t.Fatalf("status updates = %v, want [IN_PROGRESS]", store.statusUpdates)
	}
}

func TestFanoutLATokensEnqueuesOneCountedChannelBeforeFirstTokenQuery(t *testing.T) {
	var tasks []model.LASendTask
	channelObservedBeforeTokenQuery := false
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{{superseded: false}, {superseded: false}},
		tokenBatches:     []*storage.LiveActivityTokenBatch{laTokenBatch(false, "token-1")},
		onTokenBatch: func(call int) {
			if call == 0 {
				channelObservedBeforeTokenQuery = len(tasks) == 1 &&
					tasks[0].ChannelID == "apple-channel-id"
			}
		},
	}
	job := laFanoutJob()
	job.ChannelID = "apple-channel-id"

	err := FanoutLATokens(context.Background(), store, job, 10, func(_ context.Context, task model.LASendTask) error {
		tasks = append(tasks, task)
		return nil
	})
	if err != nil {
		t.Fatalf("FanoutLATokens error = %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want one channel and one token", len(tasks))
	}
	if tasks[0].ChannelID != "apple-channel-id" || tasks[0].Target.Platform != model.APNS {
		t.Fatalf("first task = %+v, want channel task", tasks[0])
	}
	if tasks[1].Target.TokenID != "token-1" {
		t.Fatalf("second task = %+v, want token task", tasks[1])
	}
	if !channelObservedBeforeTokenQuery {
		t.Fatal("channel task must be enqueued before the first token query")
	}
	if len(store.completedEnqueues) != 1 || store.completedEnqueues[0] != 2 {
		t.Fatalf("completed totals = %v, want [2]", store.completedEnqueues)
	}
}

func TestFanoutLAUpdateKeepsChannelAPNSAndFCMTokenTargets(t *testing.T) {
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{{}, {}},
		tokenBatches: []*storage.LiveActivityTokenBatch{{
			Tokens: []storage.LiveActivityToken{
				{
					ID:       "apns-token",
					Token:    "apns-token-value",
					Platform: model.APNS,
				},
				{
					ID:       "fcm-token",
					Token:    "fcm-token-value",
					Platform: model.FCM,
				},
			},
		}},
	}
	job := laFanoutJob()
	job.ChannelID = "apple-channel-id"

	var tasks []model.LASendTask
	err := FanoutLATokens(
		context.Background(),
		store,
		job,
		10,
		func(_ context.Context, task model.LASendTask) error {
			tasks = append(tasks, task)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("FanoutLATokens error = %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks = %d, want channel plus APNS and FCM tokens", len(tasks))
	}
	if tasks[0].ChannelID != "apple-channel-id" || tasks[0].Target.Platform != model.APNS {
		t.Fatalf("first task = %+v, want APNS channel", tasks[0])
	}
	if tasks[1].Target.TokenID != "apns-token" || tasks[1].Target.Platform != model.APNS {
		t.Fatalf("second task = %+v, want direct APNS token", tasks[1])
	}
	if tasks[2].Target.TokenID != "fcm-token" || tasks[2].Target.Platform != model.FCM {
		t.Fatalf("third task = %+v, want FCM token", tasks[2])
	}
	if len(store.completedEnqueues) != 1 || store.completedEnqueues[0] != 3 {
		t.Fatalf("completed totals = %v, want [3]", store.completedEnqueues)
	}
}

func TestFanoutLATokensKeepsChannelAttemptWhenLaterTokenQueryFails(t *testing.T) {
	tokenQueryErr := errors.New("token query failed")
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{{}, {}, {}},
		tokenBatches: []*storage.LiveActivityTokenBatch{
			laTokenBatch(true, "token-1"),
		},
		tokenBatchErrors: []error{nil, tokenQueryErr},
	}
	job := laFanoutJob()
	job.ChannelID = "apple-channel-id"

	var tasks []model.LASendTask
	err := FanoutLATokens(context.Background(), store, job, 1, func(_ context.Context, task model.LASendTask) error {
		tasks = append(tasks, task)
		return nil
	})

	if !errors.Is(err, tokenQueryErr) {
		t.Fatalf("FanoutLATokens error = %v, want token query failure", err)
	}
	if len(tasks) != 2 ||
		tasks[0].ChannelID != "apple-channel-id" ||
		tasks[1].Target.TokenID != "token-1" {
		t.Fatalf("tasks = %+v, want channel followed by first token batch", tasks)
	}
	if len(store.failedEnqueues) != 1 || store.failedEnqueues[0] != 2 {
		t.Fatalf("failed enqueues = %v, want channel and emitted token counted", store.failedEnqueues)
	}
}

func TestFanoutLATokensCountsChannelInSubsequentSupersessionCheck(t *testing.T) {
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{
			{},
			{},
			{superseded: true},
		},
		tokenBatches: []*storage.LiveActivityTokenBatch{
			laTokenBatch(true, "t1", "t2"),
		},
	}
	job := laFanoutJob()
	job.ChannelID = "apple-channel-id"

	var tasks []model.LASendTask
	err := FanoutLATokens(context.Background(), store, job, 2, func(_ context.Context, task model.LASendTask) error {
		tasks = append(tasks, task)
		return nil
	})
	if err != nil {
		t.Fatalf("FanoutLATokens error = %v", err)
	}
	if len(tasks) != 3 || tasks[0].ChannelID != "apple-channel-id" {
		t.Fatalf("tasks = %+v, want channel and first token batch", tasks)
	}
	wantSupersedeCalls := []int{0, 0, 3}
	if len(store.supersedeCalls) != len(wantSupersedeCalls) {
		t.Fatalf("supersede calls = %v, want %v", store.supersedeCalls, wantSupersedeCalls)
	}
	for i, want := range wantSupersedeCalls {
		if store.supersedeCalls[i] != want {
			t.Fatalf("supersede call %d emitted count = %d, want %d", i, store.supersedeCalls[i], want)
		}
	}
	if len(store.completedEnqueues) != 0 {
		t.Fatalf("completed enqueues = %v, want none for superseded dispatch", store.completedEnqueues)
	}
}

func TestFanoutLATokensRecordsChannelEnqueueFailureAsNormalOutcome(t *testing.T) {
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{{superseded: false}, {superseded: false}},
		tokenBatches:     []*storage.LiveActivityTokenBatch{{}},
	}
	job := laFanoutJob()
	job.ChannelID = "apple-channel-id"

	err := FanoutLATokens(context.Background(), store, job, 10, func(_ context.Context, task model.LASendTask) error {
		if task.ChannelID != "" {
			return errors.New("channel queue closed")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("FanoutLATokens error = %v, want counted failure", err)
	}
	if len(store.completedEnqueues) != 1 || store.completedEnqueues[0] != 1 {
		t.Fatalf("completed totals = %v, want [1]", store.completedEnqueues)
	}
	if len(store.appliedOutcomes) != 1 ||
		len(store.appliedOutcomes[0]) != 1 ||
		store.appliedOutcomes[0][0].Task.ChannelID != "apple-channel-id" ||
		store.appliedOutcomes[0][0].Receipt.Status != model.DeliveryStatusFailed {
		t.Fatalf("outcomes = %+v, want one normal failed LA outcome", store.appliedOutcomes)
	}
}

func TestFanoutLATokensFlushesEnqueueFailuresWhenSupersededMidFanout(t *testing.T) {
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{
			{},                 // initial guard: not stale
			{},                 // before batch 1: not stale
			{superseded: true}, // before batch 2: superseded
		},
		tokenBatches: []*storage.LiveActivityTokenBatch{
			laTokenBatch(true, "t1", "t2"),
			laTokenBatch(false, "t3"),
		},
	}

	err := FanoutLATokens(context.Background(), store, laFanoutJob(), 2, func(ctx context.Context, task model.LASendTask) error {
		if task.Target.TokenID == "t2" {
			return errors.New("queue full")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("FanoutLATokens error = %v", err)
	}
	if len(store.appliedOutcomes) != 1 {
		t.Fatalf("applied outcome batches = %d, want 1 (enqueue failures must flush before supersede return)", len(store.appliedOutcomes))
	}
	outcomes := store.appliedOutcomes[0]
	if len(outcomes) != 1 || outcomes[0].Task.Target.TokenID != "t2" {
		t.Fatalf("applied outcomes = %+v, want single failure for t2", outcomes)
	}
	if outcomes[0].Receipt.Status != model.DeliveryStatusFailed {
		t.Fatalf("receipt status = %s, want FAILED", outcomes[0].Receipt.Status)
	}
}

func TestFanoutLATokensFailsAndAccountsWhenMidFanoutSupersedeCheckErrors(t *testing.T) {
	guardErr := errors.New("db timeout")
	store := &fanoutLAStoreStub{
		supersedeResults: []supersedeResult{
			{},              // initial guard: not stale
			{},              // before batch 1: not stale
			{err: guardErr}, // before batch 2: unknown, stop fanout
		},
		tokenBatches: []*storage.LiveActivityTokenBatch{
			laTokenBatch(true, "t1", "t2"),
			laTokenBatch(false, "t3"),
		},
	}

	var emitted []model.LASendTask
	err := FanoutLATokens(context.Background(), store, laFanoutJob(), 2, func(ctx context.Context, task model.LASendTask) error {
		emitted = append(emitted, task)
		if task.Target.TokenID == "t2" {
			return errors.New("queue full")
		}
		return nil
	})

	if err == nil || !errors.Is(err, guardErr) {
		t.Fatalf("FanoutLATokens error = %v, want supersede-check failure", err)
	}
	if len(emitted) != 2 {
		t.Fatalf("emitted %d tasks, want first batch only", len(emitted))
	}
	if len(store.failedEnqueues) != 1 || store.failedEnqueues[0] != 2 {
		t.Fatalf("failed enqueues = %v, want [2]", store.failedEnqueues)
	}
	if len(store.completedEnqueues) != 0 {
		t.Fatalf("completed enqueues = %v, want none", store.completedEnqueues)
	}
	if len(store.appliedOutcomes) != 1 || len(store.appliedOutcomes[0]) != 1 || store.appliedOutcomes[0][0].Task.Target.TokenID != "t2" {
		t.Fatalf("applied outcomes = %+v, want the t2 enqueue failure", store.appliedOutcomes)
	}
}
