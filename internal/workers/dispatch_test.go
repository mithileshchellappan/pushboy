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

func TestDispatchLAStartSelectsBroadcastInput(t *testing.T) {
	tests := []struct {
		name            string
		capable         bool
		channelID       string
		wantChannel     string
		wantUpdateToken bool
	}{
		{"channel", true, "channel-id", "channel-id", false},
		{"token fallback", true, "", "", true},
		{"legacy", false, "channel-id", "", false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &capturingLADispatcher{}
			err := DispatchLATask(context.Background(), model.LASendTask{
				Target: model.SendTarget{
					TokenID:  "start-token-id",
					Token:    "push-to-start-token",
					Platform: model.APNS,
				},
				SupportsBroadcastChannels: test.capable,
				LAJob: &model.LAJobItem{
					DispatchID: "dispatch-start",
					Action:     model.LiveActivityActionStart,
					ActivityID: "activity-1",
					Activity:   "RaceAttributes",
					ChannelID:  test.channelID,
				},
			}, map[model.Platform]dispatch.Dispatcher{model.APNS: provider},
				pipeline.NewMemoryPipeline[model.LASendOutcome](1))
			if err != nil {
				t.Fatalf("DispatchLATask error = %v", err)
			}
			if provider.request == nil ||
				provider.request.InputPushChannel != test.wantChannel ||
				provider.request.RequestUpdateToken != test.wantUpdateToken {
				t.Fatalf("request = %+v, want channel %q update-token %v", provider.request, test.wantChannel, test.wantUpdateToken)
			}
		})
	}
}

func TestDispatchLAChannelUsesExistingOutcomePipeline(t *testing.T) {
	provider := &capturingLADispatcher{}
	dispatchers := map[model.Platform]dispatch.Dispatcher{model.APNS: provider}
	outcomes := pipeline.NewMemoryPipeline[model.LASendOutcome](1)
	task := model.LASendTask{
		Target:    model.SendTarget{Platform: model.APNS},
		ChannelID: "apple-channel-id",
		LAJob: &model.LAJobItem{
			DispatchID: "dispatch-1",
			Action:     model.LiveActivityActionUpdate,
			ActivityID: "activity-1",
			Activity:   "RaceAttributes",
		},
	}

	if err := DispatchLATask(context.Background(), task, dispatchers, outcomes); err != nil {
		t.Fatalf("DispatchLATask error = %v", err)
	}
	delivery, err := outcomes.Receive(context.Background())
	if err != nil {
		t.Fatalf("receive outcome: %v", err)
	}
	outcome := delivery.Get()
	if outcome.Receipt.Status != model.DeliveryStatusSuccess || outcome.Task.ChannelID != "apple-channel-id" {
		t.Fatalf("outcome = %+v, want normal successful LA outcome", outcome)
	}
	if provider.channelID != "apple-channel-id" {
		t.Fatalf("channelID = %q, want apple-channel-id", provider.channelID)
	}
}

func TestDispatchLAChannelPreservesExactProviderReason(t *testing.T) {
	provider := &capturingLADispatcher{
		channelErr: providerReasonError{reason: "ChannelNotRegistered"},
	}
	dispatchers := map[model.Platform]dispatch.Dispatcher{model.APNS: provider}
	outcomes := pipeline.NewMemoryPipeline[model.LASendOutcome](1)

	err := DispatchLATask(context.Background(), model.LASendTask{
		Target:    model.SendTarget{Platform: model.APNS},
		ChannelID: "apple-channel-id",
		LAJob: &model.LAJobItem{
			DispatchID: "dispatch-1",
			Action:     model.LiveActivityActionUpdate,
			ActivityID: "activity-1",
		},
	}, dispatchers, outcomes)
	if err == nil {
		t.Fatal("DispatchLATask error = nil, want provider failure")
	}

	delivery, receiveErr := outcomes.Receive(context.Background())
	if receiveErr != nil {
		t.Fatalf("receive outcome: %v", receiveErr)
	}
	outcome := delivery.Get()
	if outcome.ProviderReason != "ChannelNotRegistered" {
		t.Fatalf("ProviderReason = %q, want ChannelNotRegistered", outcome.ProviderReason)
	}
	if outcome.Receipt.Status != model.DeliveryStatusFailed {
		t.Fatalf("status = %q, want failed", outcome.Receipt.Status)
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

type capturingLADispatcher struct {
	channelID  string
	request    *model.LiveActivityRequest
	channelErr error
}

func (d *capturingLADispatcher) SendLiveActivityBroadcast(
	_ context.Context,
	channelID string,
	_ *model.LiveActivityRequest,
) error {
	d.channelID = channelID
	return d.channelErr
}

func (d *capturingLADispatcher) Send(context.Context, string, *model.NotificationPayload) error {
	return nil
}

func (d *capturingLADispatcher) SendLiveActivity(_ context.Context, _ string, request *model.LiveActivityRequest) error {
	d.request = request
	return nil
}

type providerReasonError struct {
	reason string
}

func (e providerReasonError) Error() string {
	return "provider error: " + e.reason
}

func (e providerReasonError) ProviderReason() string {
	return e.reason
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
