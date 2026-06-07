package nodes

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Count(ctx context.Context) (total, online int, err error) {
	err = r.pool.QueryRow(ctx, `select count(*), count(*) filter (where last_status='ok') from nodes`).Scan(&total, &online)
	return total, online, err
}

func (r *Repository) List(ctx context.Context) ([]Node, error) {
	rows, err := r.pool.Query(ctx, `select id, node_id, name, domain, api_url, protocol_type, node_secret, enabled, desired_config_version, last_applied_version, last_seen_at, last_status, last_error, last_sync_at, created_at, updated_at from nodes order by created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, scanNode)
}

func (r *Repository) Get(ctx context.Context, id string) (Node, error) {
	rows, err := r.pool.Query(ctx, `select id, node_id, name, domain, api_url, protocol_type, node_secret, enabled, desired_config_version, last_applied_version, last_seen_at, last_status, last_error, last_sync_at, created_at, updated_at from nodes where id=$1`, id)
	if err != nil {
		return Node{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, scanNode)
}

func (r *Repository) Create(ctx context.Context, in CreateNodeInput) (Node, error) {
	rows, err := r.pool.Query(ctx, `insert into nodes(node_id, name, domain, api_url, protocol_type, node_secret, enabled) values($1,$2,$3,$4,$5,$6,$7) returning id, node_id, name, domain, api_url, protocol_type, node_secret, enabled, desired_config_version, last_applied_version, last_seen_at, last_status, last_error, last_sync_at, created_at, updated_at`,
		in.NodeID, in.Name, in.Domain, in.APIURL, strings.ToLower(in.ProtocolType), in.NodeSecret, in.Enabled)
	if err != nil {
		return Node{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, scanNode)
}

func (r *Repository) Update(ctx context.Context, id string, in UpdateNodeInput) (Node, error) {
	rows, err := r.pool.Query(ctx, `update nodes set node_id=$2, name=$3, domain=$4, api_url=$5, protocol_type=$6, node_secret=$7, enabled=$8 where id=$1 returning id, node_id, name, domain, api_url, protocol_type, node_secret, enabled, desired_config_version, last_applied_version, last_seen_at, last_status, last_error, last_sync_at, created_at, updated_at`,
		id, in.NodeID, in.Name, in.Domain, in.APIURL, strings.ToLower(in.ProtocolType), in.NodeSecret, in.Enabled)
	if err != nil {
		return Node{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, scanNode)
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `delete from nodes where id=$1`, id)
	return err
}

func (r *Repository) BumpVersion(ctx context.Context, id string) (int64, error) {
	var version int64
	err := r.pool.QueryRow(ctx, `update nodes set desired_config_version = desired_config_version + 1 where id=$1 returning desired_config_version`, id).Scan(&version)
	return version, err
}

func (r *Repository) MarkStatus(ctx context.Context, id, status string, errText *string) error {
	_, err := r.pool.Exec(ctx, `update nodes set last_status=$2, last_error=$3, last_seen_at=now() where id=$1`, id, status, errText)
	return err
}

func (r *Repository) MarkSyncSuccess(ctx context.Context, id string, version int64) error {
	_, err := r.pool.Exec(ctx, `update nodes set last_applied_version=$2, last_sync_at=now(), last_error=null, last_status='ok', last_seen_at=now() where id=$1`, id, version)
	return err
}

func (r *Repository) MarkSyncFailure(ctx context.Context, id, status, message string) error {
	_, err := r.pool.Exec(ctx, `update nodes set last_error=$3, last_status=$2, last_seen_at=now() where id=$1`, id, status, message)
	return err
}

func (r *Repository) InsertSyncEvent(ctx context.Context, nodeID string, version int64, status string, httpStatus *int, requestJSON, responseJSON []byte, errText *string) error {
	_, err := r.pool.Exec(ctx, `insert into node_sync_events(node_id, config_version, status, http_status, request_json, response_json, error) values($1,$2,$3,$4,$5,$6,$7)`, nodeID, version, status, httpStatus, requestJSON, responseJSON, errText)
	return err
}

func (r *Repository) RecentEvents(ctx context.Context, limit int) ([]SyncEvent, error) {
	rows, err := r.pool.Query(ctx, `select id, node_id, config_version, status, http_status, request_json, response_json, error, created_at from node_sync_events order by created_at desc limit $1`, limit)
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

func scanNode(row pgx.CollectableRow) (Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.NodeID, &n.Name, &n.Domain, &n.APIURL, &n.ProtocolType, &n.NodeSecret, &n.Enabled, &n.DesiredConfigVersion, &n.LastAppliedVersion, &n.LastSeenAt, &n.LastStatus, &n.LastError, &n.LastSyncAt, &n.CreatedAt, &n.UpdatedAt)
	return n, err
}
