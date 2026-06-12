package users

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

var ErrExpiryReconcileAlreadyRunning = errors.New("expiry reconcile already running")

type expiryReconcileRepository interface {
	UsersPendingExpiryReconcile(ctx context.Context) ([]User, error)
	ActiveNodeIDsForExpiredUser(ctx context.Context, userID string) ([]string, error)
	MarkExpirySynced(ctx context.Context, userID string) error
}

type ExpiryReconcileResult struct {
	UsersFound       int      `json:"users_found"`
	NodesAffected    int      `json:"nodes_affected"`
	SyncSuccessCount int      `json:"sync_success_count"`
	UsersSynced      int      `json:"users_synced"`
	Errors           []string `json:"errors,omitempty"`
}

type ExpiryReconciler struct {
	repo     expiryReconcileRepository
	syncNode func(context.Context, string) error
	logger   *slog.Logger
	interval time.Duration
	running  atomic.Bool
}

func NewExpiryReconciler(repo expiryReconcileRepository, syncNode func(context.Context, string) error, logger *slog.Logger, interval time.Duration) *ExpiryReconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ExpiryReconciler{
		repo:     repo,
		syncNode: syncNode,
		logger:   logger,
		interval: interval,
	}
}

func (r *ExpiryReconciler) Run(ctx context.Context) {
	r.logger.Info("expiry reconciler started", "interval", r.interval.String())
	defer r.logger.Info("expiry reconciler stopped")

	r.runIteration(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runIteration(ctx)
		}
	}
}

func (r *ExpiryReconciler) RunOnce(ctx context.Context) (ExpiryReconcileResult, error) {
	if !r.tryAcquire() {
		return ExpiryReconcileResult{}, ErrExpiryReconcileAlreadyRunning
	}
	defer r.release()
	return r.reconcile(ctx)
}

func (r *ExpiryReconciler) runIteration(ctx context.Context) {
	if !r.tryAcquire() {
		r.logger.Warn("expiry reconciler iteration skipped because another run is already active")
		return
	}
	defer r.release()

	result, err := r.reconcile(ctx)
	if err != nil {
		r.logger.Error("expiry reconciler iteration failed", "error", err, "users_found", result.UsersFound, "nodes_affected", result.NodesAffected, "sync_success_count", result.SyncSuccessCount, "users_synced", result.UsersSynced, "errors", strings.Join(result.Errors, "; "))
		return
	}
	r.logger.Info("expiry reconciler iteration finished", "users_found", result.UsersFound, "nodes_affected", result.NodesAffected, "sync_success_count", result.SyncSuccessCount, "users_synced", result.UsersSynced)
}

func (r *ExpiryReconciler) reconcile(ctx context.Context) (ExpiryReconcileResult, error) {
	result := ExpiryReconcileResult{}
	candidates, err := r.repo.UsersPendingExpiryReconcile(ctx)
	if err != nil {
		return result, err
	}

	now := time.Now()
	usersToNodes := make(map[string][]string, len(candidates))
	userIDs := make([]string, 0, len(candidates))
	uniqueNodes := map[string]bool{}

	for _, user := range candidates {
		if !needsExpirySync(user, now) {
			continue
		}
		result.UsersFound++
		userIDs = append(userIDs, user.ID)

		nodeIDs, err := r.repo.ActiveNodeIDsForExpiredUser(ctx, user.ID)
		if err != nil {
			result.Errors = append(result.Errors, user.Username+": "+err.Error())
			continue
		}
		usersToNodes[user.ID] = dedupeStrings(nodeIDs)
		for _, nodeID := range usersToNodes[user.ID] {
			uniqueNodes[nodeID] = true
		}
	}

	nodes := mapKeys(uniqueNodes)
	result.NodesAffected = len(nodes)

	nodeErrors := make(map[string]string, len(nodes))
	for _, nodeID := range nodes {
		if err := r.syncNode(ctx, nodeID); err != nil {
			nodeErrors[nodeID] = err.Error()
			result.Errors = append(result.Errors, nodeID+": "+err.Error())
			continue
		}
		result.SyncSuccessCount++
	}

	for _, userID := range userIDs {
		nodeIDs := usersToNodes[userID]
		if len(nodeIDs) == 0 {
			if err := r.repo.MarkExpirySynced(ctx, userID); err != nil {
				result.Errors = append(result.Errors, userID+": "+err.Error())
				continue
			}
			result.UsersSynced++
			continue
		}

		allSynced := true
		for _, nodeID := range nodeIDs {
			if _, failed := nodeErrors[nodeID]; failed {
				allSynced = false
				break
			}
		}
		if !allSynced {
			continue
		}
		if err := r.repo.MarkExpirySynced(ctx, userID); err != nil {
			result.Errors = append(result.Errors, userID+": "+err.Error())
			continue
		}
		result.UsersSynced++
	}

	if len(result.Errors) > 0 {
		return result, fmt.Errorf("expiry reconcile completed with errors")
	}
	return result, nil
}

func needsExpirySync(user User, now time.Time) bool {
	if !user.Enabled || user.ExpiresAt == nil {
		return false
	}
	if user.ExpiresAt.After(now) {
		return false
	}
	if user.ExpirySyncedAt == nil {
		return true
	}
	return user.ExpirySyncedAt.Before(*user.ExpiresAt)
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func mapKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func (r *ExpiryReconciler) tryAcquire() bool {
	return r.running.CompareAndSwap(false, true)
}

func (r *ExpiryReconciler) release() {
	r.running.Store(false)
}
