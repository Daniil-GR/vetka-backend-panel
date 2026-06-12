package users

import (
	"strings"
	"testing"
)

func TestActiveNodeIDsForExpiredUserQueryIncludesDisabledNodes(t *testing.T) {
	query := strings.ToLower(activeNodeIDsForExpiredUserQuery)
	if strings.Contains(query, "n.enabled") {
		t.Fatalf("expiry affected nodes query must not filter by n.enabled: %s", activeNodeIDsForExpiredUserQuery)
	}
	if !strings.Contains(query, "n.api_url <> ''") {
		t.Fatalf("expiry affected nodes query must avoid planned nodes without api_url: %s", activeNodeIDsForExpiredUserQuery)
	}
}
