package nodes

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type DashboardStats struct {
	Total            int
	Connected        int
	Planned          int
	Unreachable      int
	Disabled         int
	Mieru            int
	Naive            int
	RecentSyncErrors int
}

const dashboardStatsQuery = `select
	count(*) as total,
	count(*) filter (where enabled and setup_state = 'connected') as connected,
	count(*) filter (where enabled and setup_state = 'planned') as planned,
	count(*) filter (where enabled and setup_state = 'unreachable') as unreachable,
	count(*) filter (where enabled = false or setup_state = 'disabled') as disabled,
	count(*) filter (where protocol_type = 'mieru') as mieru,
	count(*) filter (where protocol_type = 'naive') as naive,
	(
		select count(*)
		from node_sync_events
		where created_at >= $1
		  and status <> 'ok'
	) as recent_sync_errors
from nodes`

func (r *Repository) DashboardStats(ctx context.Context, since time.Time) (DashboardStats, error) {
	var stats DashboardStats
	err := r.pool.QueryRow(ctx, dashboardStatsQuery, since).Scan(
		&stats.Total,
		&stats.Connected,
		&stats.Planned,
		&stats.Unreachable,
		&stats.Disabled,
		&stats.Mieru,
		&stats.Naive,
		&stats.RecentSyncErrors,
	)
	return stats, err
}

func (r *Repository) AssignedUserCounts(ctx context.Context) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `select node_id, count(*)::int from user_node_access group by node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string]int{}
	for rows.Next() {
		var nodeID string
		var count int
		if err := rows.Scan(&nodeID, &count); err != nil {
			return nil, err
		}
		result[nodeID] = count
	}
	return result, rows.Err()
}

func (r *Repository) RecentEventsByNode(ctx context.Context, nodeID string, limit int) ([]SyncEvent, error) {
	rows, err := r.pool.Query(ctx, `select id, node_id, config_version, status, http_status, request_json, response_json, error, created_at
from node_sync_events
where node_id = $1
order by created_at desc
limit $2`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (SyncEvent, error) {
		var e SyncEvent
		err := row.Scan(&e.ID, &e.NodeID, &e.ConfigVersion, &e.Status, &e.HTTPStatus, &e.RequestJSON, &e.ResponseJSON, &e.Error, &e.CreatedAt)
		return e, err
	})
}
