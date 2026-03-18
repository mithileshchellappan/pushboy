package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func validateLAPlatform(platform string) error {
	switch storage.LAPlatform(platform) {
	case storage.LAPlatformIOS, storage.LAPlatformAndroid:
		return nil
	default:
		return fmt.Errorf("invalid LA platform: must be 'ios' or 'android'")
	}
}

func (s *PushboyService) CreateLARegistration(ctx context.Context, userID string, platform string, startToken string, autoStartEnabled bool, enabled bool) (*storage.LARegistration, error) {
	if userID == "" {
		return nil, fmt.Errorf("userID is required")
	}
	if startToken == "" {
		return nil, fmt.Errorf("start token is required")
	}
	if err := validateLAPlatform(platform); err != nil {
		return nil, err
	}
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return nil, err
	}

	registration := &storage.LARegistration{
		ID:               uuid.New().String(),
		UserID:           userID,
		Platform:         storage.LAPlatform(platform),
		StartToken:       startToken,
		AutoStartEnabled: autoStartEnabled,
		Enabled:          enabled,
	}

	return s.store.CreateLARegistration(ctx, registration)
}

func (s *PushboyService) UpdateLARegistration(ctx context.Context, userID, laRegistrationID, platform, startToken string, autoStartEnabled bool, enabled bool) (*storage.LARegistration, error) {
	if userID == "" || laRegistrationID == "" {
		return nil, fmt.Errorf("userID and laRegistrationID are required")
	}
	if startToken == "" {
		return nil, fmt.Errorf("start token is required")
	}
	if err := validateLAPlatform(platform); err != nil {
		return nil, err
	}
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return nil, err
	}

	existing, err := s.store.GetLARegistration(ctx, laRegistrationID)
	if err != nil {
		return nil, err
	}
	if existing.UserID != userID {
		return nil, storage.Errors.NotFound
	}

	registration := &storage.LARegistration{
		ID:               laRegistrationID,
		UserID:           userID,
		Platform:         storage.LAPlatform(platform),
		StartToken:       startToken,
		AutoStartEnabled: autoStartEnabled,
		Enabled:          enabled,
		CreatedAt:        existing.CreatedAt,
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
	}

	return s.store.UpdateLARegistration(ctx, registration)
}

func (s *PushboyService) DeleteLARegistration(ctx context.Context, userID, laRegistrationID string) error {
	if userID == "" || laRegistrationID == "" {
		return fmt.Errorf("userID and laRegistrationID are required")
	}
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return err
	}

	existing, err := s.store.GetLARegistration(ctx, laRegistrationID)
	if err != nil {
		return err
	}
	if existing.UserID != userID {
		return storage.Errors.NotFound
	}

	return s.store.DeleteLARegistration(ctx, laRegistrationID)
}

func (s *PushboyService) SubscribeUserToLATopic(ctx context.Context, userID, topicID string) (*storage.LAUserTopicPreference, error) {
	if userID == "" || topicID == "" {
		return nil, fmt.Errorf("userID and topicID are required")
	}
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	if _, err := s.store.GetTopicByID(ctx, topicID); err != nil {
		return nil, err
	}

	preference := &storage.LAUserTopicPreference{
		UserID:  userID,
		TopicID: topicID,
	}
	preference, err := s.store.UpsertLAUserTopicPreference(ctx, preference)
	if err != nil {
		return nil, err
	}

	registrations, err := s.store.GetLARegistrationsByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, registration := range registrations {
		if _, err := s.store.UpsertLATopicSubscription(ctx, &storage.LATopicSubscription{
			LARegistrationID: registration.ID,
			TopicID:          topicID,
		}); err != nil {
			return nil, err
		}
	}

	return preference, nil
}

func (s *PushboyService) UnsubscribeUserFromLATopic(ctx context.Context, userID, topicID string) error {
	if userID == "" || topicID == "" {
		return fmt.Errorf("userID and topicID are required")
	}
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return err
	}

	if err := s.store.DeleteLAUserTopicPreference(ctx, userID, topicID); err != nil {
		return err
	}

	registrations, err := s.store.GetLARegistrationsByUserID(ctx, userID)
	if err != nil {
		return err
	}
	for _, registration := range registrations {
		if err := s.store.DeleteLATopicSubscription(ctx, registration.ID, topicID); err != nil && err != storage.Errors.NotFound {
			return err
		}
	}

	return nil
}

func (s *PushboyService) StartLAForUser(ctx context.Context, userID, kind, externalRef string, startAttributes, state map[string]any, alert *storage.LAAlert) (*storage.LAActivity, error) {
	if userID == "" || kind == "" {
		return nil, fmt.Errorf("userID and kind are required")
	}
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return nil, err
	}

	activity := &storage.LAActivity{
		ID:              uuid.New().String(),
		Kind:            kind,
		AudienceKind:    storage.LAAudienceKindUser,
		UserID:          userID,
		ExternalRef:     externalRef,
		Status:          storage.LAActivityStatusStarting,
		PendingEvent:    storage.LAEventStart,
		StartAttributes: startAttributes,
		CurrentState:    state,
		Alert:           alert,
		StateVersion:    1,
		DispatchNeeded:  true,
		DispatchAfter:   time.Now().UTC().Format(time.RFC3339),
	}
	activity, err := s.store.CreateLAActivity(ctx, activity)
	if err != nil {
		return nil, err
	}

	registrations, err := s.store.GetEnabledLARegistrationsByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	instances := make([]storage.LAInstance, 0, len(registrations))
	for _, registration := range registrations {
		instances = append(instances, storage.LAInstance{
			ID:               uuid.New().String(),
			LAID:             activity.ID,
			LARegistrationID: registration.ID,
			Platform:         registration.Platform,
			Status:           storage.LAInstanceStatusPendingStart,
		})
	}

	if err := s.store.CreateMissingLAInstances(ctx, instances); err != nil {
		return nil, err
	}

	return activity, nil
}

func (s *PushboyService) StartLAForTopic(ctx context.Context, topicID, kind, externalRef string, startAttributes, state map[string]any, alert *storage.LAAlert) (*storage.LAActivity, error) {
	if topicID == "" || kind == "" {
		return nil, fmt.Errorf("topicID and kind are required")
	}
	if _, err := s.store.GetTopicByID(ctx, topicID); err != nil {
		return nil, err
	}

	activity := &storage.LAActivity{
		ID:              uuid.New().String(),
		Kind:            kind,
		AudienceKind:    storage.LAAudienceKindTopic,
		TopicID:         topicID,
		ExternalRef:     externalRef,
		Status:          storage.LAActivityStatusStarting,
		PendingEvent:    storage.LAEventStart,
		StartAttributes: startAttributes,
		CurrentState:    state,
		Alert:           alert,
		StateVersion:    1,
		DispatchNeeded:  true,
		DispatchAfter:   time.Now().UTC().Format(time.RFC3339),
	}
	activity, err := s.store.CreateLAActivity(ctx, activity)
	if err != nil {
		return nil, err
	}

	registrations, err := s.store.GetLARegistrationsByTopicID(ctx, topicID)
	if err != nil {
		return nil, err
	}

	instances := make([]storage.LAInstance, 0, len(registrations))
	for _, registration := range registrations {
		instances = append(instances, storage.LAInstance{
			ID:               uuid.New().String(),
			LAID:             activity.ID,
			LARegistrationID: registration.ID,
			Platform:         registration.Platform,
			Status:           storage.LAInstanceStatusPendingStart,
		})
	}

	if err := s.store.CreateMissingLAInstances(ctx, instances); err != nil {
		return nil, err
	}

	return activity, nil
}

func (s *PushboyService) UpdateLA(ctx context.Context, laID string, state map[string]any, alert *storage.LAAlert) (*storage.LAActivity, error) {
	if laID == "" {
		return nil, fmt.Errorf("laID is required")
	}

	activity, err := s.store.GetLAActivity(ctx, laID)
	if err != nil {
		return nil, err
	}

	activity.PendingEvent = storage.LAEventUpdate
	activity.Status = storage.LAActivityStatusActive
	activity.CurrentState = state
	activity.Alert = alert
	activity.StateVersion++
	activity.DispatchNeeded = true
	activity.DispatchAfter = time.Now().UTC().Format(time.RFC3339)

	return s.store.UpdateLAActivity(ctx, activity)
}

func (s *PushboyService) EndLA(ctx context.Context, laID string, state map[string]any, alert *storage.LAAlert) (*storage.LAActivity, error) {
	if laID == "" {
		return nil, fmt.Errorf("laID is required")
	}

	activity, err := s.store.GetLAActivity(ctx, laID)
	if err != nil {
		return nil, err
	}

	activity.PendingEvent = storage.LAEventEnd
	activity.Status = storage.LAActivityStatusEnding
	activity.CurrentState = state
	activity.Alert = alert
	activity.StateVersion++
	activity.DispatchNeeded = true
	activity.DispatchAfter = time.Now().UTC().Format(time.RFC3339)

	return s.store.UpdateLAActivity(ctx, activity)
}

func (s *PushboyService) ActivateLAInstance(ctx context.Context, laInstanceID, deliveryToken string) (*storage.LAInstance, error) {
	if laInstanceID == "" || deliveryToken == "" {
		return nil, fmt.Errorf("laInstanceID and deliveryToken are required")
	}

	if err := s.store.ActivateLAInstance(ctx, laInstanceID, deliveryToken); err != nil {
		return nil, err
	}

	return s.store.GetLAInstance(ctx, laInstanceID)
}
