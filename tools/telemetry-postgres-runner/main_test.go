package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeDatabase struct {
	startErr error
	stopErr  error
	started  bool
	stopped  bool
}

func (f *fakeDatabase) Start() error {
	f.started = true
	return f.startErr
}

func (f *fakeDatabase) Stop() error {
	f.stopped = true
	return f.stopErr
}

type fakeRunner struct {
	calls []string
	errAt int
}

func (f *fakeRunner) Run(timeout time.Duration, name string, args []string, env []string, dir string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " ")+" @ "+dir)
	if timeout <= 0 {
		return errors.New("missing timeout")
	}
	if f.errAt > 0 && len(f.calls) == f.errAt {
		return errors.New("child failed")
	}
	if !containsEnv(env, "TEST_DATABASE_NAME="+testDatabaseName) {
		return errors.New("missing TEST_DATABASE_NAME")
	}
	if !containsEnvPrefix(env, "TEST_DATABASE_URL=postgres://postgres:postgres@127.0.0.1:") {
		return errors.New("missing TEST_DATABASE_URL")
	}
	return nil
}

func TestRunWithDepsSuccessfulCleanup(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "runner")
	db := &fakeDatabase{}
	runner := &fakeRunner{}
	removed := ""
	err := runWithDeps(deps{
		makeTempDir: func() (string, error) { return tmp, os.MkdirAll(tmp, 0o755) },
		removeAll: func(path string) error {
			removed = path
			return os.RemoveAll(path)
		},
		freePort:    func() (int, error) { return 55432, nil },
		newDatabase: func(string, int) database { return db },
		runner:      runner,
		repoRoot:    "C:/repo",
	})
	if err != nil {
		t.Fatalf("runWithDeps returned error: %v", err)
	}
	if !db.started || !db.stopped {
		t.Fatalf("expected database start/stop, got started=%v stopped=%v", db.started, db.stopped)
	}
	if removed != tmp {
		t.Fatalf("expected temp dir cleanup for %q, got %q", tmp, removed)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("expected three child test invocations, got %v", runner.calls)
	}
	if !strings.Contains(runner.calls[0], "TestAdvisoryLockSerializesConcurrentIntegrationTests") {
		t.Fatalf("expected concurrency integration test first, got %v", runner.calls)
	}
}

func TestRunWithDepsCleansUpAfterFirstChildFailure(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "runner")
	db := &fakeDatabase{}
	runner := &fakeRunner{errAt: 1}
	removed := false
	err := runWithDeps(deps{
		makeTempDir: func() (string, error) { return tmp, os.MkdirAll(tmp, 0o755) },
		removeAll: func(path string) error {
			removed = true
			return os.RemoveAll(path)
		},
		freePort:    func() (int, error) { return 55432, nil },
		newDatabase: func(string, int) database { return db },
		runner:      runner,
		repoRoot:    "C:/repo",
	})
	if err == nil {
		t.Fatal("expected child failure to be returned")
	}
	if !db.stopped || !removed {
		t.Fatalf("expected cleanup after child failure, got stopped=%v removed=%v", db.stopped, removed)
	}
}

func TestRunWithDepsCleansUpAfterSecondChildFailure(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "runner")
	db := &fakeDatabase{}
	runner := &fakeRunner{errAt: 2}
	err := runWithDeps(deps{
		makeTempDir: func() (string, error) { return tmp, os.MkdirAll(tmp, 0o755) },
		removeAll:   os.RemoveAll,
		freePort:    func() (int, error) { return 55432, nil },
		newDatabase: func(string, int) database { return db },
		runner:      runner,
		repoRoot:    "C:/repo",
	})
	if err == nil {
		t.Fatal("expected second child failure to be returned")
	}
	if !db.stopped {
		t.Fatal("expected database stop after second child failure")
	}
}

func TestRunWithDepsReturnsStartFailureWithoutStop(t *testing.T) {
	db := &fakeDatabase{startErr: errors.New("boom")}
	err := runWithDeps(deps{
		makeTempDir: func() (string, error) { return t.TempDir(), nil },
		removeAll:   func(string) error { return nil },
		freePort:    func() (int, error) { return 55432, nil },
		newDatabase: func(string, int) database { return db },
		runner:      &fakeRunner{},
		repoRoot:    "C:/repo",
	})
	if err == nil {
		t.Fatal("expected start failure")
	}
	if db.stopped {
		t.Fatal("database stop should not run when start fails")
	}
}

func TestRunWithDepsIncludesStopFailure(t *testing.T) {
	db := &fakeDatabase{stopErr: errors.New("stop failed")}
	err := runWithDeps(deps{
		makeTempDir: func() (string, error) { return t.TempDir(), nil },
		removeAll:   func(string) error { return nil },
		freePort:    func() (int, error) { return 55432, nil },
		newDatabase: func(string, int) database { return db },
		runner:      &fakeRunner{},
		repoRoot:    "C:/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "stop embedded postgres") {
		t.Fatalf("expected stop failure to be reported, got %v", err)
	}
}

type timeoutRunner struct{}

func (timeoutRunner) Run(timeout time.Duration, name string, args []string, env []string, dir string) error {
	return errors.New("command timed out after " + timeout.String())
}

func TestRunWithDepsCleansUpAfterChildTimeout(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "runner")
	db := &fakeDatabase{}
	removed := false
	err := runWithDeps(deps{
		makeTempDir: func() (string, error) { return tmp, os.MkdirAll(tmp, 0o755) },
		removeAll: func(path string) error {
			removed = true
			return os.RemoveAll(path)
		},
		freePort:    func() (int, error) { return 55432, nil },
		newDatabase: func(string, int) database { return db },
		runner:      timeoutRunner{},
		repoRoot:    "C:/repo",
		testTimeout: 10 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if !db.stopped || !removed {
		t.Fatalf("expected cleanup after child timeout, got stopped=%v removed=%v", db.stopped, removed)
	}
}

func containsEnv(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

func containsEnvPrefix(env []string, prefix string) bool {
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}
