package users

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) CreateWithAssignments(ctx context.Context, in CreateUserInput, token string, nodeProtocols map[string]string, creds []AssignmentCredential) (User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	user, err := createUserWithQuerier(ctx, tx, in, token)
	if err != nil {
		return User{}, err
	}
	for _, cred := range creds {
		protocol, ok := nodeProtocols[cred.NodeID]
		if !ok || protocol == "" {
			return User{}, pgx.ErrNoRows
		}
		if err := assignNodeWithExecutor(ctx, tx, user.ID, cred.NodeID, protocol, cred.ProtocolUsername, cred.ProtocolPassword); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return user, nil
}

func (r *Repository) Count(ctx context.Context) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `select count(*) from users`).Scan(&count)
	return count, err
}

func (r *Repository) List(ctx context.Context) ([]User, error) {
	rows, err := r.pool.Query(ctx, `select id, username, display_name, enabled, expires_at, subscription_token, notes, created_at, updated_at from users order by created_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, scanUser)
}

func (r *Repository) Get(ctx context.Context, id string) (User, error) {
	rows, err := r.pool.Query(ctx, `select id, username, display_name, enabled, expires_at, subscription_token, notes, created_at, updated_at from users where id=$1`, id)
	if err != nil {
		return User{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, scanUser)
}

func (r *Repository) GetByToken(ctx context.Context, token string) (User, error) {
	rows, err := r.pool.Query(ctx, `select id, username, display_name, enabled, expires_at, subscription_token, notes, created_at, updated_at from users where subscription_token=$1`, token)
	if err != nil {
		return User{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, scanUser)
}

func (r *Repository) Create(ctx context.Context, in CreateUserInput, token string) (User, error) {
	return createUserWithQuerier(ctx, r.pool, in, token)
}

func (r *Repository) Update(ctx context.Context, id string, in UpdateUserInput) (User, error) {
	rows, err := r.pool.Query(ctx, `update users set username=$2, display_name=$3, enabled=$4, expires_at=$5, notes=$6 where id=$1 returning id, username, display_name, enabled, expires_at, subscription_token, notes, created_at, updated_at`,
		id, in.Username, in.DisplayName, in.Enabled, in.ExpiresAt, in.Notes)
	if err != nil {
		return User{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, scanUser)
}

func (r *Repository) SetEnabled(ctx context.Context, id string, enabled bool) error {
	_, err := r.pool.Exec(ctx, `update users set enabled=$2 where id=$1`, id, enabled)
	return err
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx, `delete from users where id=$1`, id)
	return err
}

func (r *Repository) AssignNode(ctx context.Context, userID, nodeID, protocolType, username, password string) error {
	return assignNodeWithExecutor(ctx, r.pool, userID, nodeID, protocolType, username, password)
}

func (r *Repository) UnassignNode(ctx context.Context, userID, nodeID string) error {
	_, err := r.pool.Exec(ctx, `delete from user_node_access where user_id=$1 and node_id=$2`, userID, nodeID)
	return err
}

func (r *Repository) AccessForUser(ctx context.Context, userID string) ([]Access, error) {
	rows, err := r.pool.Query(ctx, `select id, user_id, node_id, protocol_type, protocol_username, protocol_password, enabled, created_at, updated_at from user_node_access where user_id=$1 order by created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, scanAccess)
}

func (r *Repository) ActiveAccessForNode(ctx context.Context, nodeID string) ([]AccessWithUser, error) {
	rows, err := r.pool.Query(ctx, `select a.id, a.user_id, a.node_id, a.protocol_type, a.protocol_username, a.protocol_password, a.enabled, a.created_at, a.updated_at, u.username, u.enabled, u.expires_at
from user_node_access a
join users u on u.id = a.user_id
where a.node_id=$1 and a.enabled and u.enabled and (u.expires_at is null or u.expires_at > now())
order by u.username`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccessWithUser, error) {
		var a AccessWithUser
		err := row.Scan(&a.ID, &a.UserID, &a.NodeID, &a.ProtocolType, &a.ProtocolUsername, &a.ProtocolPassword, &a.Enabled, &a.CreatedAt, &a.UpdatedAt, &a.Username, &a.UserEnabled, &a.UserExpiresAt)
		return a, err
	})
}

func (r *Repository) ActiveAccessForSubscription(ctx context.Context, userID string) ([]AccessWithNode, error) {
	rows, err := r.pool.Query(ctx, `select a.id, a.user_id, a.node_id, a.protocol_type, a.protocol_username, a.protocol_password, a.enabled, a.created_at, a.updated_at, n.node_id, n.name, n.domain, n.api_url, n.protocol_type, n.protocol_settings
from user_node_access a
join nodes n on n.id = a.node_id
where a.user_id=$1 and a.enabled and n.enabled
order by n.name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccessWithNode, error) {
		var a AccessWithNode
		err := row.Scan(&a.ID, &a.UserID, &a.NodeID, &a.ProtocolType, &a.ProtocolUsername, &a.ProtocolPassword, &a.Enabled, &a.CreatedAt, &a.UpdatedAt, &a.AgentNodeID, &a.NodeName, &a.NodeDomain, &a.NodeAPIURL, &a.NodeProtocolType, &a.NodeProtocolSettingsJSON)
		return a, err
	})
}

type AccessWithNode struct {
	Access
	AgentNodeID              string
	NodeName                 string
	NodeDomain               string
	NodeAPIURL               string
	NodeProtocolType         string
	NodeProtocolSettingsJSON []byte
}

func IsExpired(expiresAt *time.Time) bool {
	return expiresAt != nil && expiresAt.Before(time.Now())
}

func scanUser(row pgx.CollectableRow) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Enabled, &u.ExpiresAt, &u.SubscriptionToken, &u.Notes, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

func scanAccess(row pgx.CollectableRow) (Access, error) {
	var a Access
	err := row.Scan(&a.ID, &a.UserID, &a.NodeID, &a.ProtocolType, &a.ProtocolUsername, &a.ProtocolPassword, &a.Enabled, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func createUserWithQuerier(ctx context.Context, db queryer, in CreateUserInput, token string) (User, error) {
	rows, err := db.Query(ctx, `insert into users(username, display_name, enabled, expires_at, subscription_token, notes) values($1,$2,$3,$4,$5,$6) returning id, username, display_name, enabled, expires_at, subscription_token, notes, created_at, updated_at`,
		in.Username, in.DisplayName, in.Enabled, in.ExpiresAt, token, in.Notes)
	if err != nil {
		return User{}, err
	}
	defer rows.Close()
	return pgx.CollectOneRow(rows, scanUser)
}

func assignNodeWithExecutor(ctx context.Context, db executor, userID, nodeID, protocolType, username, password string) error {
	_, err := db.Exec(ctx, `insert into user_node_access(user_id, node_id, protocol_type, protocol_username, protocol_password, enabled) values($1,$2,$3,$4,$5,true) on conflict(user_id, node_id) do update set protocol_type=excluded.protocol_type, protocol_username=excluded.protocol_username, protocol_password=excluded.protocol_password, enabled=true`,
		userID, nodeID, protocolType, username, password)
	return err
}
