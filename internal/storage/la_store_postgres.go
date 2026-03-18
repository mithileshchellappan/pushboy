package storage

import (
	"context"
	"time"
)

func (s *PostgresStore) CreateLARegistration(ctx context.Context, registration *LARegistration) (*LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) UpdateLARegistration(ctx context.Context, registration *LARegistration) (*LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) GetLARegistration(ctx context.Context, laRegistrationID string) (*LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) GetLARegistrationsByUserID(ctx context.Context, userID string) ([]LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) GetEnabledLARegistrationsByUserID(ctx context.Context, userID string) ([]LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) DeleteLARegistration(ctx context.Context, laRegistrationID string) error {
	return Errors.NotImplemented
}

func (s *PostgresStore) UpsertLAUserTopicPreference(ctx context.Context, preference *LAUserTopicPreference) (*LAUserTopicPreference, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) DeleteLAUserTopicPreference(ctx context.Context, userID, topicID string) error {
	return Errors.NotImplemented
}

func (s *PostgresStore) GetLAUserTopicPreferences(ctx context.Context, userID string) ([]LAUserTopicPreference, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) UpsertLATopicSubscription(ctx context.Context, subscription *LATopicSubscription) (*LATopicSubscription, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) DeleteLATopicSubscription(ctx context.Context, laRegistrationID, topicID string) error {
	return Errors.NotImplemented
}

func (s *PostgresStore) GetLATopicSubscriptionsByRegistrationID(ctx context.Context, laRegistrationID string) ([]LATopicSubscription, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) GetLARegistrationsByTopicID(ctx context.Context, topicID string) ([]LARegistration, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) CreateLAActivity(ctx context.Context, activity *LAActivity) (*LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) GetLAActivity(ctx context.Context, laID string) (*LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) GetActiveLAActivitiesByTopicID(ctx context.Context, topicID string) ([]LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) UpdateLAActivity(ctx context.Context, activity *LAActivity) (*LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) ClaimReadyLAActivities(ctx context.Context, claimToken string, claimUntil time.Time, limit int) ([]LAActivity, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) CreateLAInstance(ctx context.Context, instance *LAInstance) (*LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) CreateMissingLAInstances(ctx context.Context, instances []LAInstance) error {
	return Errors.NotImplemented
}

func (s *PostgresStore) GetLAInstance(ctx context.Context, instanceID string) (*LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) GetLAInstancesByActivityID(ctx context.Context, laID string) ([]LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) GetRetryableLAInstancesByActivityID(ctx context.Context, laID string, asOf time.Time) ([]LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) ActivateLAInstance(ctx context.Context, instanceID string, deliveryToken string) error {
	return Errors.NotImplemented
}

func (s *PostgresStore) UpdateLAInstance(ctx context.Context, instance *LAInstance) (*LAInstance, error) {
	return nil, Errors.NotImplemented
}

func (s *PostgresStore) UpdateActiveLAInstancesDeliveryTokenByRegistrationID(ctx context.Context, laRegistrationID, deliveryToken string) error {
	return Errors.NotImplemented
}
