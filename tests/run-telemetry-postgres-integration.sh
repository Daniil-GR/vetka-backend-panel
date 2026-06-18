#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ -n "${TEST_DATABASE_URL:-}" ]]; then
  if [[ -z "${TEST_DATABASE_NAME:-}" ]]; then
    echo "TEST_DATABASE_NAME is required when TEST_DATABASE_URL is provided" >&2
    exit 1
  fi

  if [[ "${TEST_DATABASE_NAME}" != *_test && "${TEST_DATABASE_NAME}" != *_test_* ]]; then
    echo "TEST_DATABASE_NAME must end with _test or contain _test_" >&2
    exit 1
  fi

  cd "${ROOT_DIR}"
  go test ./internal/testsupport -run TestAdvisoryLockSerializesConcurrentIntegrationTests -count=1 -v
  go test ./internal/users -run TestSessionLookupForNodesIntegration -count=1 -v
  go test ./internal/telemetry -run TestUserSessionsIntegrationWithRealRepository -count=1 -v
  exit 0
fi

(
  cd "${ROOT_DIR}/tools/telemetry-postgres-runner"
  go run .
)
