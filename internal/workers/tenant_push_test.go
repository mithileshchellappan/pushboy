package workers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type fakeTenantPushStore struct {
	attempt      storage.TenantPushAttempt
	claim        uuid.UUID
	claimCalls   int
	writeCalls   int
	written      model.DeliveryReceipt
	ambiguous    bool
	failedWrites int
}

func (s *fakeTenantPushStore) ClaimTenantPushRecipient(_ context.Context, _ uuid.UUID, _, _ string, claimID uuid.UUID) (storage.TenantPushAttempt, error) {
	s.claimCalls++
	if s.claim == uuid.Nil {
		s.claim = claimID
		if s.ambiguous {
			return storage.TenantPushAttempt{}, errors.New("connection lost after commit")
		}
	}
	if claimID != s.claim {
		return storage.TenantPushAttempt{}, storage.ErrTenantPushRecipientDone
	}
	return s.attempt, nil
}

func (s *fakeTenantPushStore) CheckTenantPushRecipient(_ context.Context, _ uuid.UUID, _, _ string, claimID uuid.UUID) (storage.TenantPushAttempt, error) {
	if claimID != s.claim {
		return storage.TenantPushAttempt{}, storage.ErrTenantPushRecipientStale
	}
	return s.attempt, nil
}

func (s *fakeTenantPushStore) ApplyPushOutcomeBatch(_ context.Context, receipts []model.DeliveryReceipt) error {
	s.writeCalls++
	if s.writeCalls <= s.failedWrites {
		return errors.New("connection retry")
	}
	s.written = receipts[0]
	return nil
}

type fakeTenantPushRoutes struct {
	calls int
	err   error
}

func (r *fakeTenantPushRoutes) SendPush(ctx context.Context, ref model.ProviderRef, _ model.Platform, _ string, _ *model.NotificationPayload, admit dispatch.TenantAdmission) error {
	if err := admit(ctx, ref, 1); err != nil {
		return err
	}
	r.calls++
	return r.err
}

func newTenantPushWorkerFixture() (model.SendTask, *fakeTenantPushStore) {
	tenant := uuid.New()
	task := model.SendTask{
		Job:    &model.JobItem{TenantID: tenant, ID: uuid.NewString()},
		Target: model.SendTarget{ReceiptID: uuid.NewString()},
	}
	store := &fakeTenantPushStore{attempt: storage.TenantPushAttempt{
		ProviderRef: model.ProviderRef{TenantID: tenant, RouteID: uuid.New()}, Platform: model.APNS, CredentialVersion: 1,
	}}
	return task, store
}

func TestDispatchTenantPushRecoversAmbiguousClaimAndOutcomeWrite(t *testing.T) {
	task, store := newTenantPushWorkerFixture()
	store.ambiguous = true
	store.failedWrites = 1
	routes := &fakeTenantPushRoutes{}
	outcomes := pipeline.NewMemoryPipeline[model.SendOutcome](1)
	if err := outcomes.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := DispatchTenantPush(ctx, store, routes, task, outcomes); err != nil {
		t.Fatal(err)
	}
	if store.claimCalls != 2 || routes.calls != 1 || store.writeCalls != 2 ||
		store.written.Status != model.DeliveryStatusSuccess || store.written.ClaimID != store.claim {
		t.Fatalf("claim, provider send, and result writes = %d, %d, %d, %+v",
			store.claimCalls, routes.calls, store.writeCalls, store.written)
	}
	if err := DispatchTenantPush(ctx, store, routes, task, outcomes); err != nil {
		t.Fatal(err)
	}
	if routes.calls != 1 || store.writeCalls != 2 {
		t.Fatalf("duplicate task sent or wrote again: %d sends, %d writes", routes.calls, store.writeCalls)
	}
}

func TestDispatchTenantPushDistinguishesKnownPreflightFromUnknown(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status model.DeliveryStatus
		reason string
	}{
		{"preflight", model.ErrProviderUnavailable, model.DeliveryStatusFailed, model.TenantPushReasonProviderUnavailable},
		{"preflight canceled", context.Canceled, model.DeliveryStatusFailed, model.TenantPushReasonProviderUnavailable},
		{"provider outcome unknown", model.ErrProviderOutcomeUnknown, model.DeliveryStatusUnknown, model.TenantPushReasonDeliveryUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			task, store := newTenantPushWorkerFixture()
			routes := &fakeTenantPushRoutes{err: tt.err}
			outcomes := pipeline.NewMemoryPipeline[model.SendOutcome](1)
			if err := DispatchTenantPush(t.Context(), store, routes, task, outcomes); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			delivery, err := outcomes.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if receipt := delivery.Get().Receipt; receipt.Status != tt.status || receipt.StatusReason != tt.reason {
				t.Fatalf("receipt = %+v, want %q %q", receipt, tt.status, tt.reason)
			}
		})
	}
}

func TestPushOutcomeWorkerRetriesScopedResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	outcomes := pipeline.NewMemoryPipeline[model.SendOutcome](1)
	stored := make(chan model.DeliveryReceipt, 1)
	var attempts int
	store := tenantPushOutcomeWriterFunc(func(_ context.Context, receipts []model.DeliveryReceipt) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary insert failure")
		}
		stored <- receipts[0]
		return nil
	})
	worker := NewPushOutcomeWorker(store, outcomes, 1, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	receipt := model.DeliveryReceipt{ID: uuid.NewString(), TenantID: uuid.New(), ClaimID: uuid.New(), Status: model.DeliveryStatusSuccess}
	if err := outcomes.Submit(ctx, model.SendOutcome{Receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	select {
	case actual := <-stored:
		if actual != receipt {
			t.Fatalf("retry applied different outcome: %+v", actual)
		}
	case <-ctx.Done():
		t.Fatal("outcome worker did not retain the failed scoped batch")
	}
}

type tenantPushOutcomeWriterFunc func(context.Context, []model.DeliveryReceipt) error

func (f tenantPushOutcomeWriterFunc) ApplyPushOutcomeBatch(ctx context.Context, receipts []model.DeliveryReceipt) error {
	return f(ctx, receipts)
}
