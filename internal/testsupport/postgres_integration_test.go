package testsupport

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"vetka-backend-panel/internal/db"
)

func TestAdvisoryLockSerializesConcurrentIntegrationTests(t *testing.T) {
	databaseURL := testDatabaseURL(t)
	expectedName := testDatabaseName(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool1, err := db.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect pool1: %v", err)
	}
	defer pool1.Close()
	pool2, err := db.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect pool2: %v", err)
	}
	defer pool2.Close()

	assertDedicatedDatabase(t, pool1, expectedName)
	assertDedicatedDatabase(t, pool2, expectedName)

	conn1, err := pool1.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn1: %v", err)
	}
	defer conn1.Release()
	conn2, err := pool2.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn2: %v", err)
	}
	defer conn2.Release()

	lockKey := migrationLockKey + 99
	if _, err := execAdvisoryLock(ctx, conn1, lockKey); err != nil {
		t.Fatalf("lock conn1: %v", err)
	}

	started := make(chan struct{})
	acquired := make(chan error, 1)
	go func() {
		close(started)
		lockCtx, lockCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer lockCancel()
		_, err := execAdvisoryLock(lockCtx, conn2, lockKey)
		acquired <- err
	}()

	<-started
	select {
	case err := <-acquired:
		t.Fatalf("second lock attempt must wait, returned early with %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer unlockCancel()
	unlocked, err := execAdvisoryUnlock(unlockCtx, conn1, lockKey)
	if err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	if !unlocked {
		t.Fatal("release first lock returned false")
	}

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("second lock attempt failed after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second lock attempt did not complete after release")
	}

	secondUnlockCtx, secondUnlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer secondUnlockCancel()
	secondUnlocked, err := execAdvisoryUnlock(secondUnlockCtx, conn2, lockKey)
	if err != nil {
		t.Fatalf("unlock conn2: %v", err)
	}
	if !secondUnlocked {
		t.Fatal("unlock conn2 returned false")
	}
}

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if strings.TrimSpace(databaseURL) == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	return databaseURL
}

func testDatabaseName(t *testing.T) string {
	t.Helper()
	expectedName := os.Getenv("TEST_DATABASE_NAME")
	if strings.TrimSpace(expectedName) == "" {
		t.Fatal("TEST_DATABASE_NAME must be set when TEST_DATABASE_URL is provided")
	}
	return expectedName
}

func assertDedicatedDatabase(t *testing.T, pool *pgxpool.Pool, expectedName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var currentDatabase string
	if err := pool.QueryRow(ctx, `select current_database()`).Scan(&currentDatabase); err != nil {
		t.Fatalf("read current_database(): %v", err)
	}
	if err := GuardDedicatedTestDatabase(currentDatabase, expectedName); err != nil {
		t.Fatalf("unsafe integration database: %v", err)
	}
}
