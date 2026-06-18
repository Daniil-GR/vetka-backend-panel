package users

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type DashboardStats struct {
	Total       int
	Active      int
	Expired     int
	Disabled    int
	ExpiresSoon int
}

type UserNodeAccessDetail struct {
	Access
	NodeName         string
	NodeEnabled      bool
	NodeSetupState   string
	NodeProtocolType string
}

type NodeUserAccessDetail struct {
	Access
	Username      string
	DisplayName   *string
	UserEnabled   bool
	UserExpiresAt *time.Time
	UserQuotaMB   int
}

type SessionLookup struct {
	UserID           string
	NodeID           string
	ProtocolUsername string
}

func (r *Repository) DashboardStats(ctx context.Context, soonBefore time.Time) (DashboardStats, error) {
	var stats DashboardStats
	err := r.pool.QueryRow(ctx, `select
	count(*) as total,
	count(*) filter (
		where enabled
		  and (expires_at is null or expires_at > now())
	) as active,
	count(*) filter (
		where enabled
		  and expires_at is not null
		  and expires_at <= now()
	) as expired,
	count(*) filter (where enabled = false) as disabled,
	count(*) filter (
		where enabled
		  and expires_at is not null
		  and expires_at > now()
		  and expires_at <= $1
	) as expires_soon
from users`, soonBefore).Scan(
		&stats.Total,
		&stats.Active,
		&stats.Expired,
		&stats.Disabled,
		&stats.ExpiresSoon,
	)
	return stats, err
}

func (r *Repository) UpcomingExpirations(ctx context.Context, limit int) ([]User, error) {
	rows, err := r.pool.Query(ctx, `select id, username, display_name, enabled, expires_at, expiry_synced_at, quota_mb, subscription_token, notes, created_at, updated_at
from users
where enabled
  and expires_at is not null
  and expires_at > now()
order by expires_at asc
limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, scanUser)
}

func (r *Repository) AssignmentCounts(ctx context.Context) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `select user_id, count(*)::int from user_node_access group by user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string]int{}
	for rows.Next() {
		var userID string
		var count int
		if err := rows.Scan(&userID, &count); err != nil {
			return nil, err
		}
		result[userID] = count
	}
	return result, rows.Err()
}

func (r *Repository) AccessDetailForUser(ctx context.Context, userID string) ([]UserNodeAccessDetail, error) {
	rows, err := r.pool.Query(ctx, `select
	a.id, a.user_id, a.node_id, a.protocol_type, a.protocol_username, a.protocol_password, a.enabled, a.created_at, a.updated_at,
	n.name, n.enabled, n.setup_state, n.protocol_type
from user_node_access a
join nodes n on n.id = a.node_id
where a.user_id = $1
order by n.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (UserNodeAccessDetail, error) {
		var item UserNodeAccessDetail
		err := row.Scan(
			&item.ID,
			&item.UserID,
			&item.NodeID,
			&item.ProtocolType,
			&item.ProtocolUsername,
			&item.ProtocolPassword,
			&item.Enabled,
			&item.CreatedAt,
			&item.UpdatedAt,
			&item.NodeName,
			&item.NodeEnabled,
			&item.NodeSetupState,
			&item.NodeProtocolType,
		)
		return item, err
	})
}

func (r *Repository) AccessDetailForNode(ctx context.Context, nodeID string) ([]NodeUserAccessDetail, error) {
	rows, err := r.pool.Query(ctx, `select
	a.id, a.user_id, a.node_id, a.protocol_type, a.protocol_username, a.protocol_password, a.enabled, a.created_at, a.updated_at,
	u.username, u.display_name, u.enabled, u.expires_at, u.quota_mb
from user_node_access a
join users u on u.id = a.user_id
where a.node_id = $1
order by u.username`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (NodeUserAccessDetail, error) {
		var item NodeUserAccessDetail
		err := row.Scan(
			&item.ID,
			&item.UserID,
			&item.NodeID,
			&item.ProtocolType,
			&item.ProtocolUsername,
			&item.ProtocolPassword,
			&item.Enabled,
			&item.CreatedAt,
			&item.UpdatedAt,
			&item.Username,
			&item.DisplayName,
			&item.UserEnabled,
			&item.UserExpiresAt,
			&item.UserQuotaMB,
		)
		return item, err
	})
}

func (r *Repository) SessionLookupForUser(ctx context.Context, userID string) ([]SessionLookup, error) {
	rows, err := r.pool.Query(ctx, `select user_id, node_id, protocol_username
from user_node_access
where user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, scanSessionLookup)
}

func (r *Repository) SessionLookupForNodes(ctx context.Context, nodeIDs []string) ([]SessionLookup, error) {
	typedIDs, err := normalizeNodeLookupIDs(nodeIDs)
	if err != nil {
		return nil, err
	}
	if len(typedIDs) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `select user_id, node_id, protocol_username
from user_node_access
where node_id = any($1)`, typedIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, scanSessionLookup)
}

func normalizeNodeLookupIDs(nodeIDs []string) ([]pgtype.UUID, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	result := make([]pgtype.UUID, 0, len(nodeIDs))
	seen := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		trimmed := nodeID
		if trimmed == "" {
			return nil, fmt.Errorf("invalid node id for session lookup: empty value")
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		var parsed pgtype.UUID
		if err := parsed.Scan(trimmed); err != nil {
			return nil, fmt.Errorf("invalid node id for session lookup %q: %w", trimmed, err)
		}
		seen[trimmed] = struct{}{}
		result = append(result, parsed)
	}
	return result, nil
}

func scanSessionLookup(row pgx.CollectableRow) (SessionLookup, error) {
	var item SessionLookup
	err := row.Scan(&item.UserID, &item.NodeID, &item.ProtocolUsername)
	return item, err
}
