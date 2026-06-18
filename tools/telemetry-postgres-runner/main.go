package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

const (
	testDatabaseName = "vetka_backend_telemetry_test"
	testDatabaseUser = "postgres"
	testDatabasePass = "postgres"
	testTimeout      = 3 * time.Minute
	removeTimeout    = 5 * time.Second
)

type database interface {
	Start() error
	Stop() error
}

type commandRunner interface {
	Run(timeout time.Duration, name string, args []string, env []string, dir string) error
}

type osCommandRunner struct{}

func (osCommandRunner) Run(timeout time.Duration, name string, args []string, env []string, dir string) error {
	if timeout <= 0 {
		timeout = testTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("command timed out after %s", timeout)
	}
	return err
}

type deps struct {
	makeTempDir func() (string, error)
	removeAll   func(string) error
	freePort    func() (int, error)
	newDatabase func(runtimeDir string, port int) database
	runner      commandRunner
	repoRoot    string
	testTimeout time.Duration
}

func main() {
	if err := runMain(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runMain() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	return runWithDeps(deps{
		makeTempDir: func() (string, error) {
			return os.MkdirTemp("", "vetka-embedded-postgres-*")
		},
		removeAll: removeAllWithRetry,
		freePort:  freePort,
		newDatabase: func(runtimeDir string, port int) database {
			cfg := embeddedpostgres.DefaultConfig().
				Version(embeddedpostgres.V16).
				Port(uint32(port)).
				Database(testDatabaseName).
				Username(testDatabaseUser).
				Password(testDatabasePass).
				RuntimePath(filepath.Join(runtimeDir, "runtime")).
				DataPath(filepath.Join(runtimeDir, "data")).
				BinariesPath(filepath.Join(runtimeDir, "binaries"))
			return embeddedpostgres.NewDatabase(cfg)
		},
		runner:      osCommandRunner{},
		repoRoot:    filepath.Clean(filepath.Join(cwd, "..", "..")),
		testTimeout: testTimeout,
	})
}

func runWithDeps(d deps) (err error) {
	if d.testTimeout <= 0 {
		d.testTimeout = testTimeout
	}

	port, err := d.freePort()
	if err != nil {
		return fmt.Errorf("select free port: %w", err)
	}

	runtimeDir, err := d.makeTempDir()
	if err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	defer func() {
		if removeErr := d.removeAll(runtimeDir); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove runtime dir: %w", removeErr))
		}
	}()

	db := d.newDatabase(runtimeDir, port)
	if err := db.Start(); err != nil {
		return fmt.Errorf("start embedded postgres: %w", err)
	}
	defer func() {
		if stopErr := db.Stop(); stopErr != nil {
			err = errors.Join(err, fmt.Errorf("stop embedded postgres: %w", stopErr))
		}
	}()

	databaseURL := fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable", testDatabaseUser, testDatabasePass, port, testDatabaseName)
	fmt.Fprintf(os.Stdout, "TEST_DATABASE_NAME=%s\n", testDatabaseName)
	fmt.Fprintf(os.Stdout, "TEST_DATABASE_URL=postgres://%s:***@127.0.0.1:%d/%s?sslmode=disable\n", testDatabaseUser, port, testDatabaseName)

	env := append(os.Environ(),
		"TEST_DATABASE_URL="+databaseURL,
		"TEST_DATABASE_NAME="+testDatabaseName,
	)
	for _, spec := range []struct {
		pkg  string
		test string
	}{
		{pkg: "./internal/testsupport", test: "TestAdvisoryLockSerializesConcurrentIntegrationTests"},
		{pkg: "./internal/users", test: "TestSessionLookupForNodesIntegration"},
		{pkg: "./internal/telemetry", test: "TestUserSessionsIntegrationWithRealRepository"},
	} {
		if runErr := d.runner.Run(d.testTimeout, "go", []string{"test", spec.pkg, "-run", spec.test, "-count=1", "-v"}, env, d.repoRoot); runErr != nil {
			return fmt.Errorf("go test %s (%s) failed: %w", spec.pkg, spec.test, runErr)
		}
	}
	return nil
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func removeAllWithRetry(path string) error {
	deadline := time.Now().Add(removeTimeout)
	var lastErr error
	for {
		if err := os.RemoveAll(path); err == nil || os.IsNotExist(err) {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(200 * time.Millisecond)
	}
}
