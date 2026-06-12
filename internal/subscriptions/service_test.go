package subscriptions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"vetka-backend-panel/internal/users"
)

type fakeSubscriptionRepo struct {
	user        users.User
	assignments []users.AccessWithNode
	getErr      error
}

func (f *fakeSubscriptionRepo) GetByToken(ctx context.Context, token string) (users.User, error) {
	if f.getErr != nil {
		return users.User{}, f.getErr
	}
	return f.user, nil
}

func (f *fakeSubscriptionRepo) ActiveAccessForSubscription(ctx context.Context, userID string) ([]users.AccessWithNode, error) {
	return f.assignments, nil
}

func TestBuildByTokenRejectsExpiredUser(t *testing.T) {
	repo := &fakeSubscriptionRepo{
		user: users.User{
			ID:        "u1",
			Enabled:   true,
			ExpiresAt: ptrTime(time.Now().Add(-time.Hour)),
		},
	}
	svc := NewService(repo, false, DefaultProfileTitle, DefaultUpdateIntervalHours)

	_, _, err := svc.BuildByToken(context.Background(), "sub-token", "")
	if !errors.Is(err, ErrSubscriptionDisabled) {
		t.Fatalf("expected ErrSubscriptionDisabled, got %v", err)
	}
}

func TestBuildByTokenAllowsActiveUser(t *testing.T) {
	repo := &fakeSubscriptionRepo{
		user: users.User{
			ID:        "u1",
			Enabled:   true,
			ExpiresAt: ptrTime(time.Now().Add(time.Hour)),
		},
		assignments: []users.AccessWithNode{
			{
				Access:                   users.Access{Enabled: true, ProtocolUsername: "demo", ProtocolPassword: "secret"},
				NodeName:                 "Node One",
				NodeDomain:               "example.com",
				NodeProtocolType:         "naive",
				NodeProtocolSettingsJSON: []byte(`{"naive":{"port":443}}`),
			},
		},
	}
	svc := NewService(repo, false, DefaultProfileTitle, DefaultUpdateIntervalHours)

	body, _, err := svc.BuildByToken(context.Background(), "sub-token", "naive")
	if err != nil {
		t.Fatalf("BuildByToken returned error: %v", err)
	}
	if !strings.Contains(body, "naive://") {
		t.Fatalf("expected active subscription output, got %s", body)
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
