package testsupport

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"vetka-backend-panel/internal/db"
)

const migrationLockKey int64 = 5_829_441_072_341
const integrationLockTimeout = 30 * time.Second

var forbiddenTestDatabases = map[string]struct{}{
	"postgres":      {},
	"template0":     {},
	"template1":     {},
	"vetka_backend": {},
}

func OpenIntegrationTestDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()

	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	expectedName := strings.TrimSpace(os.Getenv("TEST_DATABASE_NAME"))
	if expectedName == "" {
		t.Fatal("TEST_DATABASE_NAME must be set when TEST_DATABASE_URL is provided")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var currentDatabase string
	if err := pool.QueryRow(ctx, `select current_database()`).Scan(&currentDatabase); err != nil {
		t.Fatalf("read current_database(): %v", err)
	}
	if err := GuardDedicatedTestDatabase(currentDatabase, expectedName); err != nil {
		t.Fatalf("unsafe integration database: %v", err)
	}

	lockConn := acquireMigrationLock(t, pool)
	t.Cleanup(func() {
		releaseMigrationLock(t, lockConn)
	})

	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	return pool, currentDatabase
}

func acquireMigrationLock(t *testing.T, pool *pgxpool.Pool) *pgxpool.Conn {
	t.Helper()

	lockCtx, lockCancel := context.WithTimeout(context.Background(), integrationLockTimeout)
	defer lockCancel()

	conn, err := pool.Acquire(lockCtx)
	if err != nil {
		t.Fatalf("acquire migration lock connection: %v", err)
	}
	if _, err := execAdvisoryLock(lockCtx, conn, migrationLockKey); err != nil {
		conn.Release()
		t.Fatalf("acquire migration lock: %v", err)
	}
	return conn
}

func releaseMigrationLock(t *testing.T, conn *pgxpool.Conn) {
	t.Helper()
	if conn == nil {
		return
	}
	unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer unlockCancel()
	unlocked, err := execAdvisoryUnlock(unlockCtx, conn, migrationLockKey)
	conn.Release()
	if err != nil {
		t.Fatalf("release migration lock: %v", err)
	}
	if !unlocked {
		t.Fatal("release migration lock: PostgreSQL returned false")
	}
}

type advisoryLocker interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func execAdvisoryLock(ctx context.Context, conn advisoryLocker, key int64) (pgconn.CommandTag, error) {
	return conn.Exec(ctx, `select pg_advisory_lock($1)`, key)
}

func WithIntegrationLock(ctx context.Context, pool *pgxpool.Pool, key int64, fn func(*pgxpool.Conn) error) error {
	lockCtx, lockCancel := context.WithTimeout(ctx, integrationLockTimeout)
	defer lockCancel()

	conn, err := pool.Acquire(lockCtx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := execAdvisoryLock(lockCtx, conn, key); err != nil {
		return err
	}
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer unlockCancel()
		_, _ = execAdvisoryUnlock(unlockCtx, conn, key)
	}()

	return fn(conn)
}

func execAdvisoryUnlock(ctx context.Context, conn advisoryLocker, key int64) (bool, error) {
	var unlocked bool
	if err := conn.QueryRow(ctx, `select pg_advisory_unlock($1)`, key).Scan(&unlocked); err != nil {
		return false, err
	}
	return unlocked, nil
}

func GuardDedicatedTestDatabase(currentDatabase, expectedDatabase string) error {
	currentDatabase = strings.TrimSpace(currentDatabase)
	expectedDatabase = strings.TrimSpace(expectedDatabase)
	if expectedDatabase == "" {
		return fmt.Errorf("TEST_DATABASE_NAME is empty")
	}
	if currentDatabase != expectedDatabase {
		return fmt.Errorf("current database %q does not match TEST_DATABASE_NAME %q", currentDatabase, expectedDatabase)
	}
	if _, forbidden := forbiddenTestDatabases[strings.ToLower(currentDatabase)]; forbidden {
		return fmt.Errorf("database %q is forbidden for integration tests", currentDatabase)
	}
	if !strings.HasSuffix(currentDatabase, "_test") && !strings.Contains(currentDatabase, "_test_") {
		return fmt.Errorf("database %q must end with _test or contain _test_", currentDatabase)
	}
	return nil
}

func NewFixtureUUID(t *testing.T) string {
	t.Helper()

	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("generate fixture UUID: %v", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4],
		raw[4:6],
		raw[6:8],
		raw[8:10],
		raw[10:16],
	)
}

func NewFixtureSuffix(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(NewFixtureUUID(t), "-", "")[:12]
}

func UUIDArray(ids ...string) ([]pgtype.UUID, error) {
	result := make([]pgtype.UUID, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return nil, fmt.Errorf("fixture UUID must not be empty")
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		var parsed pgtype.UUID
		if err := parsed.Scan(trimmed); err != nil {
			return nil, fmt.Errorf("invalid fixture UUID %q: %w", trimmed, err)
		}
		result = append(result, parsed)
		seen[trimmed] = struct{}{}
	}
	return result, nil
}

func CleanupFixtureRows(ctx context.Context, pool *pgxpool.Pool, userIDs, nodeIDs []string) error {
	typedUsers, err := UUIDArray(userIDs...)
	if err != nil {
		return err
	}
	typedNodes, err := UUIDArray(nodeIDs...)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	switch {
	case len(typedUsers) > 0 && len(typedNodes) > 0:
		if _, err := tx.Exec(ctx, `delete from user_node_access where user_id = any($1) or node_id = any($2)`, typedUsers, typedNodes); err != nil {
			return err
		}
	case len(typedUsers) > 0:
		if _, err := tx.Exec(ctx, `delete from user_node_access where user_id = any($1)`, typedUsers); err != nil {
			return err
		}
	case len(typedNodes) > 0:
		if _, err := tx.Exec(ctx, `delete from user_node_access where node_id = any($1)`, typedNodes); err != nil {
			return err
		}
	}
	if len(typedUsers) > 0 {
		if _, err := tx.Exec(ctx, `delete from users where id = any($1)`, typedUsers); err != nil {
			return err
		}
	}
	if len(typedNodes) > 0 {
		if _, err := tx.Exec(ctx, `delete from nodes where id = any($1)`, typedNodes); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
