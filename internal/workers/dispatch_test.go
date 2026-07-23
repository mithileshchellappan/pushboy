package workers

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
)

func TestDispatchFailuresReturnContextWithoutDirectLogging(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	dispatchers := map[model.Platform]dispatch.Dispatcher{
		model.APNS: failingDispatcher{err: providerErr},
	}
	pushOutcomes := pipeline.NewMemoryPipeline[model.SendOutcome](1)
	laOutcomes := pipeline.NewMemoryPipeline[model.LASendOutcome](1)

	output := captureStdout(t, func() {
		pushErr := DispatchPushTask(context.Background(), model.SendTask{
			Target: model.SendTarget{TokenID: "push-token-id", Token: "push-token", Platform: model.APNS},
			Job:    &model.JobItem{ID: "push-job", Payload: &model.NotificationPayload{}},
		}, dispatchers, pushOutcomes)
		if pushErr == nil || !strings.Contains(pushErr.Error(), "push-token-id") || !errors.Is(pushErr, providerErr) {
			t.Fatalf("DispatchPushTask error = %v, want contextual wrapped provider error", pushErr)
		}

		laErr := DispatchLATask(context.Background(), model.LASendTask{
			Target: model.SendTarget{TokenID: "la-token-id", Token: "la-token", Platform: model.APNS},
			LAJob: &model.LAJobItem{
				DispatchID: "la-dispatch",
				Action:     model.LiveActivityActionUpdate,
				ActivityID: "activity-1",
				Activity:   "RaceAttributes",
			},
		}, dispatchers, laOutcomes)
		if laErr == nil || !strings.Contains(laErr.Error(), "la-token-id") || !errors.Is(laErr, providerErr) {
			t.Fatalf("DispatchLATask error = %v, want contextual wrapped provider error", laErr)
		}
	})

	if output != "" {
		t.Fatalf("dispatch functions wrote directly to stdout: %q", output)
	}

	pushDelivery, err := pushOutcomes.Receive(context.Background())
	if err != nil {
		t.Fatalf("receive push outcome: %v", err)
	}
	if got := pushDelivery.Get().Receipt.StatusReason; got != providerErr.Error() {
		t.Fatalf("push receipt reason = %q, want %q", got, providerErr)
	}

	laDelivery, err := laOutcomes.Receive(context.Background())
	if err != nil {
		t.Fatalf("receive LA outcome: %v", err)
	}
	if got := laDelivery.Get().Receipt.StatusReason; got != providerErr.Error() {
		t.Fatalf("LA receipt reason = %q, want %q", got, providerErr)
	}
}

type failingDispatcher struct {
	err error
}

func (d failingDispatcher) Send(context.Context, string, *model.NotificationPayload) error {
	return d.err
}

func (d failingDispatcher) SendLiveActivity(context.Context, string, *model.LiveActivityRequest) error {
	return d.err
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = original
	}()

	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	os.Stdout = original

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stdout reader: %v", err)
	}
	return string(output)
}
