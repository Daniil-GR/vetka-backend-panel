package users

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeExpiryRepo struct {
	users        []User
	nodesByUser  map[string][]string
	markedSynced []string
	markErr      map[string]error
	nodeErr      map[string]error
}

func (f *fakeExpiryRepo) UsersPendingExpiryReconcile(ctx context.Context) ([]User, error) {
	return f.users, nil
}

func (f *fakeExpiryRepo) ActiveNodeIDsForExpiredUser(ctx context.Context, userID string) ([]string, error) {
	return append([]string(nil), f.nodesByUser[userID]...), nil
}

func (f *fakeExpiryRepo) MarkExpirySynced(ctx context.Context, userID string) error {
	if err := f.markErr[userID]; err != nil {
		return err
	}
	f.markedSynced = append(f.markedSynced, userID)
	return nil
}

func TestExpiryReconcilerSyncsAffectedNodesAndMarksUsers(t *testing.T) {
	now := time.Now()
	repo := &fakeExpiryRepo{
		users: []User{
			{ID: "u1", Username: "expired-1", Enabled: true, ExpiresAt: ptrTime(now.Add(-time.Hour))},
			{ID: "u2", Username: "expired-2", Enabled: true, ExpiresAt: ptrTime(now.Add(-2 * time.Hour))},
		},
		nodesByUser: map[string][]string{
			"u1": {"node-a", "node-b"},
			"u2": {"node-b"},
		},
		markErr: map[string]error{},
	}
	var synced []string
	reconciler := NewExpiryReconciler(repo, func(ctx context.Context, nodeID string) error {
		synced = append(synced, nodeID)
		return nil
	}, nil, time.Minute)

	result, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.UsersFound != 2 || result.NodesAffected != 2 || result.SyncSuccessCount != 2 || result.UsersSynced != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if strings.Join(synced, ",") != "node-a,node-b" {
		t.Fatalf("unexpected synced nodes: %v", synced)
	}
	if strings.Join(repo.markedSynced, ",") != "u1,u2" {
		t.Fatalf("unexpected marked users: %v", repo.markedSynced)
	}
}

func TestExpiryReconcilerDoesNotMarkUserWhenNodeSyncFails(t *testing.T) {
	now := time.Now()
	repo := &fakeExpiryRepo{
		users: []User{
			{ID: "u1", Username: "expired-1", Enabled: true, ExpiresAt: ptrTime(now.Add(-time.Hour))},
		},
		nodesByUser: map[string][]string{
			"u1": {"node-a"},
		},
		markErr: map[string]error{},
	}
	reconciler := NewExpiryReconciler(repo, func(ctx context.Context, nodeID string) error {
		return errors.New("sync failed")
	}, nil, time.Minute)

	result, err := reconciler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected reconcile error")
	}
	if result.SyncSuccessCount != 0 || result.UsersSynced != 0 {
		t.Fatalf("unexpected result after failure: %#v", result)
	}
	if len(repo.markedSynced) != 0 {
		t.Fatalf("user should not be marked synced: %v", repo.markedSynced)
	}
}

func TestExpiryReconcilerIgnoresUsersWithoutActiveNodesAndDisabledNodesDoNotSync(t *testing.T) {
	now := time.Now()
	repo := &fakeExpiryRepo{
		users: []User{
			{ID: "u1", Username: "expired-1", Enabled: true, ExpiresAt: ptrTime(now.Add(-time.Hour))},
		},
		nodesByUser: map[string][]string{
			"u1": nil,
		},
		markErr: map[string]error{},
	}
	var syncCalls int
	reconciler := NewExpiryReconciler(repo, func(ctx context.Context, nodeID string) error {
		syncCalls++
		return nil
	}, nil, time.Minute)

	result, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if syncCalls != 0 || result.NodesAffected != 0 || result.UsersSynced != 1 {
		t.Fatalf("unexpected result: %#v syncCalls=%d", result, syncCalls)
	}
}

func TestExpiryReconcilerReturnsAlreadyRunningForConcurrentRunOnce(t *testing.T) {
	now := time.Now()
	repo := &fakeExpiryRepo{
		users: []User{
			{ID: "u1", Username: "expired-1", Enabled: true, ExpiresAt: ptrTime(now.Add(-time.Hour))},
		},
		nodesByUser: map[string][]string{
			"u1": {"node-a"},
		},
		markErr: map[string]error{},
	}
	started := make(chan struct{})
	release := make(chan struct{})
	reconciler := NewExpiryReconciler(repo, func(ctx context.Context, nodeID string) error {
		close(started)
		<-release
		return nil
	}, nil, time.Minute)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = reconciler.RunOnce(context.Background())
	}()

	<-started
	result, err := reconciler.RunOnce(context.Background())
	if !errors.Is(err, ErrExpiryReconcileAlreadyRunning) {
		t.Fatalf("expected already running error, got result=%#v err=%v", result, err)
	}

	close(release)
	<-done
}

func TestExpiryReconcilerRunOnceStillWorksNormally(t *testing.T) {
	now := time.Now()
	repo := &fakeExpiryRepo{
		users: []User{
			{ID: "u1", Username: "expired-1", Enabled: true, ExpiresAt: ptrTime(now.Add(-time.Hour))},
		},
		nodesByUser: map[string][]string{
			"u1": {"node-a"},
		},
		markErr: map[string]error{},
	}
	reconciler := NewExpiryReconciler(repo, func(ctx context.Context, nodeID string) error {
		return nil
	}, nil, time.Minute)

	result, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.SyncSuccessCount != 1 || result.UsersSynced != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExpiryReconcilerPreventsOverlappingRunOnceCalls(t *testing.T) {
	now := time.Now()
	repo := &fakeExpiryRepo{
		users: []User{
			{ID: "u1", Username: "expired-1", Enabled: true, ExpiresAt: ptrTime(now.Add(-time.Hour))},
		},
		nodesByUser: map[string][]string{
			"u1": {"node-a"},
		},
		markErr: map[string]error{},
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	reconciler := NewExpiryReconciler(repo, func(ctx context.Context, nodeID string) error {
		current := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if current <= prev || maxInFlight.CompareAndSwap(prev, current) {
				break
			}
		}
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		inFlight.Add(-1)
		return nil
	}, nil, time.Minute)

	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := reconciler.RunOnce(context.Background())
		results <- err
	}()
	<-started
	go func() {
		defer wg.Done()
		_, err := reconciler.RunOnce(context.Background())
		results <- err
	}()

	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)

	var alreadyRunningCount int
	for err := range results {
		if errors.Is(err, ErrExpiryReconcileAlreadyRunning) {
			alreadyRunningCount++
			continue
		}
		if err != nil {
			t.Fatalf("unexpected reconcile error: %v", err)
		}
	}
	if maxInFlight.Load() != 1 {
		t.Fatalf("expected no overlapping runs, maxInFlight=%d", maxInFlight.Load())
	}
	if alreadyRunningCount != 1 {
		t.Fatalf("expected one already running result, got %d", alreadyRunningCount)
	}
}

func TestExpiryReconcilerSyncsDisabledAssignedNodeForExpiredUser(t *testing.T) {
	now := time.Now()
	repo := &fakeExpiryRepo{
		users: []User{
			{ID: "u1", Username: "expired-disabled-node", Enabled: true, ExpiresAt: ptrTime(now.Add(-time.Hour))},
		},
		nodesByUser: map[string][]string{
			"u1": {"disabled-node"},
		},
		markErr: map[string]error{},
	}
	var synced []string
	reconciler := NewExpiryReconciler(repo, func(ctx context.Context, nodeID string) error {
		synced = append(synced, nodeID)
		return nil
	}, nil, time.Minute)

	result, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if result.NodesAffected != 1 || result.SyncSuccessCount != 1 || strings.Join(synced, ",") != "disabled-node" {
		t.Fatalf("unexpected result for disabled assigned node: %#v synced=%v", result, synced)
	}
}

func TestNeedsExpirySync(t *testing.T) {
	now := time.Now()
	expiredAt := now.Add(-time.Hour)
	futureAt := now.Add(time.Hour)

	cases := []struct {
		name string
		user User
		want bool
	}{
		{name: "expired and never synced", user: User{Enabled: true, ExpiresAt: &expiredAt}, want: true},
		{name: "future expiry ignored", user: User{Enabled: true, ExpiresAt: &futureAt}, want: false},
		{name: "nil expiry ignored", user: User{Enabled: true}, want: false},
		{name: "disabled user ignored", user: User{Enabled: false, ExpiresAt: &expiredAt}, want: false},
		{name: "already synced for this expiry ignored", user: User{Enabled: true, ExpiresAt: &expiredAt, ExpirySyncedAt: &now}, want: false},
		{name: "synced before expiry should retry", user: User{Enabled: true, ExpiresAt: &expiredAt, ExpirySyncedAt: ptrTime(expiredAt.Add(-time.Minute))}, want: true},
	}

	for _, tc := range cases {
		if got := needsExpirySync(tc.user, now); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
