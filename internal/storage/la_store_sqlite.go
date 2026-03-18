package storage

import (
	"context"
	"time"
)

func (s *SQLiteStore) CreateLARegistration(ctx context.Context, registration *LARegistration) (*LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) UpdateLARegistration(ctx context.Context, registration *LARegistration) (*LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) GetLARegistration(ctx context.Context, laRegistrationID string) (*LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) GetLARegistrationsByUserID(ctx context.Context, userID string) ([]LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) GetEnabledLARegistrationsByUserID(ctx context.Context, userID string) ([]LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) DeleteLARegistration(ctx context.Context, laRegistrationID string) error {
	return Errors.NotImplemented
}

func (s *SQLiteStore) UpsertLAUserTopicPreference(ctx context.Context, preference *LAUserTopicPreference) (*LAUserTopicPreference, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) DeleteLAUserTopicPreference(ctx context.Context, userID, topicID string) error {
	return Errors.NotImplemented
}

func (s *SQLiteStore) GetLAUserTopicPreferences(ctx context.Context, userID string) ([]LAUserTopicPreference, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) UpsertLATopicSubscription(ctx context.Context, subscription *LATopicSubscription) (*LATopicSubscription, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) DeleteLATopicSubscription(ctx context.Context, laRegistrationID, topicID string) error {
	return Errors.NotImplemented
}

func (s *SQLiteStore) GetLATopicSubscriptionsByRegistrationID(ctx context.Context, laRegistrationID string) ([]LATopicSubscription, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) GetLARegistrationsByTopicID(ctx context.Context, topicID string) ([]LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) CreateLAActivity(ctx context.Context, activity *LAActivity) (*LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) GetLAActivity(ctx context.Context, laID string) (*LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) GetActiveLAActivitiesByTopicID(ctx context.Context, topicID string) ([]LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) UpdateLAActivity(ctx context.Context, activity *LAActivity) (*LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) ClaimReadyLAActivities(ctx context.Context, claimToken string, claimUntil time.Time, limit int) ([]LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) CreateLAInstance(ctx context.Context, instance *LAInstance) (*LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) CreateMissingLAInstances(ctx context.Context, instances []LAInstance) error {
	return Errors.NotImplemented
}

func (s *SQLiteStore) GetLAInstance(ctx context.Context, instanceID string) (*LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) GetLAInstancesByActivityID(ctx context.Context, laID string) ([]LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) GetRetryableLAInstancesByActivityID(ctx context.Context, laID string, asOf time.Time) ([]LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) ActivateLAInstance(ctx context.Context, instanceID string, deliveryToken string) error {
	return Errors.NotImplemented
}

func (s *SQLiteStore) UpdateLAInstance(ctx context.Context, instance *LAInstance) (*LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *SQLiteStore) UpdateActiveLAInstancesDeliveryTokenByRegistrationID(ctx context.Context, laRegistrationID, deliveryToken string) error {
	return Errors.NotImplemented
}
