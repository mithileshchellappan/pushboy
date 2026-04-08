package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type LAPipeline struct {
	store       storage.Store
	dispatchers map[string]dispatch.LADispatcher
	numSenders  int
}

func NewLAPipeline(store storage.Store, dispatchers map[string]dispatch.LADispatcher, numSenders int) *LAPipeline {
	if numSenders < 1 {
		numSenders = 1
	}
	return &LAPipeline{
		store:       store,
		dispatchers: dispatchers,
		numSenders:  numSenders,
	}
}

func (p *LAPipeline) ProcessJob(ctx context.Context, snapshot *storage.LASnapshot, claimedVersion int64, claimToken string) error {
	if snapshot == nil {
		return fmt.Errorf("la snapshot is required")
	}

	payload := &dispatch.LAPayload{
		LAID:            snapshot.ID,
		Kind:            snapshot.Kind,
		ExternalRef:     snapshot.ExternalRef,
		Event:           snapshot.PendingEvent,
		StartAttributes: snapshot.StartAttributes,
		CurrentState:    snapshot.CurrentState,
		Alert:           snapshot.Alert,
	}

	var dispatchErrors []string

	if snapshot.PendingEvent == storage.LAEventStart {
		registrations, err := p.fetchRegistrations(ctx, snapshot)
		if err != nil {
			dispatchErrors = append(dispatchErrors, err.Error())
		} else {
			dispatchErrors = append(dispatchErrors, p.dispatchStarts(ctx, registrations, payload)...)
		}
	}

	updateTokens, err := p.fetchUpdateTokens(ctx, snapshot)
	if err != nil {
		dispatchErrors = append(dispatchErrors, err.Error())
	} else {
		dispatchErrors = append(dispatchErrors, p.dispatchUpdates(ctx, updateTokens, payload)...)
	}

	lastError := strings.Join(dispatchErrors, "; ")
	if _, err := p.store.CompleteLAActivityDispatch(ctx, snapshot.ID, claimToken, claimedVersion, finalLAStatus(snapshot.PendingEvent), lastError); err != nil {
		if lastError != "" {
			return fmt.Errorf("%s; complete dispatch: %w", lastError, err)
		}
		return err
	}

	if lastError != "" {
		return fmt.Errorf("%s", lastError)
	}

	return nil
}

func (p *LAPipeline) fetchRegistrations(ctx context.Context, snapshot *storage.LASnapshot) ([]storage.LARegistration, error) {
	switch snapshot.AudienceKind {
	case storage.LAAudienceKindUser:
		return p.store.GetEnabledLARegistrationsByUserID(ctx, snapshot.UserID)
	case storage.LAAudienceKindTopic:
		return p.store.GetEnabledLARegistrationsByTopicID(ctx, snapshot.TopicID)
	default:
		return nil, fmt.Errorf("unsupported LA audience kind: %s", snapshot.AudienceKind)
	}
}

func (p *LAPipeline) fetchUpdateTokens(ctx context.Context, snapshot *storage.LASnapshot) ([]storage.LAUpdateToken, error) {
	switch snapshot.AudienceKind {
	case storage.LAAudienceKindUser:
		return p.store.GetLAUpdateTokensByUser(ctx, snapshot.UserID)
	case storage.LAAudienceKindTopic:
		return p.store.GetLAUpdateTokensByTopic(ctx, snapshot.TopicID)
	default:
		return nil, fmt.Errorf("unsupported LA audience kind: %s", snapshot.AudienceKind)
	}
}

func (p *LAPipeline) dispatchStarts(ctx context.Context, registrations []storage.LARegistration, payload *dispatch.LAPayload) []string {
	if len(registrations) == 0 {
		return nil
	}

	jobs := make(chan storage.LARegistration, len(registrations))
	errs := make(chan string, len(registrations))
	var wg sync.WaitGroup

	for i := 0; i < p.numSenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for registration := range jobs {
				if !registration.AutoStartEnabled {
					continue
				}
				dispatcher, ok := p.dispatchers[string(registration.Platform)]
				if !ok {
					errs <- fmt.Sprintf("missing LA dispatcher for platform %s", registration.Platform)
					continue
				}
				if err := dispatcher.SendStart(ctx, &registration, payload); err != nil {
					errs <- err.Error()
				}
			}
		}()
	}

	for _, registration := range registrations {
		jobs <- registration
	}
	close(jobs)
	wg.Wait()
	close(errs)

	return collectLAErrors(errs)
}

func (p *LAPipeline) dispatchUpdates(ctx context.Context, updateTokens []storage.LAUpdateToken, payload *dispatch.LAPayload) []string {
	if len(updateTokens) == 0 {
		return nil
	}

	jobs := make(chan storage.LAUpdateToken, len(updateTokens))
	errs := make(chan string, len(updateTokens))
	var wg sync.WaitGroup

	for i := 0; i < p.numSenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for updateToken := range jobs {
				dispatcher, ok := p.dispatchers[string(updateToken.Platform)]
				if !ok {
					errs <- fmt.Sprintf("missing LA dispatcher for platform %s", updateToken.Platform)
					continue
				}
				if err := dispatcher.SendUpdate(ctx, &updateToken, payload); err != nil {
					errs <- err.Error()
				}
			}
		}()
	}

	for _, updateToken := range updateTokens {
		jobs <- updateToken
	}
	close(jobs)
	wg.Wait()
	close(errs)

	return collectLAErrors(errs)
}

func collectLAErrors(errs <-chan string) []string {
	var collected []string
	for err := range errs {
		log.Printf("WORKER: -> LA dispatch error: %s", err)
		collected = append(collected, err)
	}
	return collected
}

func finalLAStatus(event storage.LAEvent) storage.LAActivityStatus {
	switch event {
	case storage.LAEventEnd:
		return storage.LAActivityStatusEnded
	default:
		return storage.LAActivityStatusActive
	}
}
