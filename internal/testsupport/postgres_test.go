package testsupport

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		switch target := dest[i].(type) {
		case *bool:
			*target = r.values[i].(bool)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}

type fakeLocker struct {
	lastQuery string
	lastArg   any
	row       fakeRow
	execErr   error
}

func (f *fakeLocker) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	f.lastQuery = query
	if len(args) > 0 {
		f.lastArg = args[0]
	}
	return f.row
}

func (f *fakeLocker) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	f.lastQuery = query
	if len(args) > 0 {
		f.lastArg = args[0]
	}
	return pgconn.CommandTag{}, f.execErr
}

func TestGuardDedicatedTestDatabase(t *testing.T) {
	if err := GuardDedicatedTestDatabase("vetka_backend_telemetry_test", "vetka_backend_telemetry_test"); err != nil {
		t.Fatalf("expected safe dedicated database, got %v", err)
	}
	for _, tc := range []struct {
		current  string
		expected string
	}{
		{current: "postgres", expected: "postgres"},
		{current: "vetka_backend", expected: "vetka_backend"},
		{current: "vetka_backend_dev", expected: "vetka_backend_dev"},
		{current: "other_test", expected: "vetka_backend_telemetry_test"},
	} {
		if err := GuardDedicatedTestDatabase(tc.current, tc.expected); err == nil {
			t.Fatalf("expected guard failure for current=%q expected=%q", tc.current, tc.expected)
		}
	}
}

func TestExecAdvisoryLockAndUnlock(t *testing.T) {
	locker := &fakeLocker{row: fakeRow{values: []any{true}}}
	if _, err := execAdvisoryLock(context.Background(), locker, migrationLockKey); err != nil {
		t.Fatalf("execAdvisoryLock returned error: %v", err)
	}
	if locker.lastQuery != `select pg_advisory_lock($1)` {
		t.Fatalf("unexpected lock query: %s", locker.lastQuery)
	}

	locker.row = fakeRow{values: []any{true}}
	unlocked, err := execAdvisoryUnlock(context.Background(), locker, migrationLockKey)
	if err != nil {
		t.Fatalf("execAdvisoryUnlock returned error: %v", err)
	}
	if !unlocked {
		t.Fatal("expected advisory unlock to succeed")
	}
	if locker.lastQuery != `select pg_advisory_unlock($1)` {
		t.Fatalf("unexpected unlock query: %s", locker.lastQuery)
	}
}

func TestNewFixtureSuffix(t *testing.T) {
	a := NewFixtureSuffix(t)
	b := NewFixtureSuffix(t)
	if a == b {
		t.Fatalf("fixture suffixes must be unique enough, got %q twice", a)
	}
	if len(a) != 12 || len(b) != 12 {
		t.Fatalf("expected 12-char suffixes, got %q and %q", a, b)
	}
}
