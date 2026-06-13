package nodes

import (
	"strings"
	"testing"
)

func TestDashboardStatsQueryExcludesDisabledNodesFromConnectedCounts(t *testing.T) {
	query := strings.ToLower(dashboardStatsQuery)
	if !strings.Contains(query, "count(*) filter (where enabled and setup_state = 'connected') as connected") {
		t.Fatalf("connected metric must require enabled nodes: %s", dashboardStatsQuery)
	}
	if !strings.Contains(query, "count(*) filter (where enabled = false or setup_state = 'disabled') as disabled") {
		t.Fatalf("disabled metric must include hidden/disabled nodes: %s", dashboardStatsQuery)
	}
}
