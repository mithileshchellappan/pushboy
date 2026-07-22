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

	tokenBatches   []*storage.LiveActivityTokenBatch
	tokenBatchCall int

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
