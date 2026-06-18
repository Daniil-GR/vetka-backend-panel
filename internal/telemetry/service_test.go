package telemetry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vetka-backend-panel/internal/nodes"
	"vetka-backend-panel/internal/testsupport"
	"vetka-backend-panel/internal/users"
)

type fakeNodeRepo struct {
	list []nodes.Node
	byID map[string]nodes.Node
}

func (f fakeNodeRepo) List(context.Context) ([]nodes.Node, error) {
	return append([]nodes.Node(nil), f.list...), nil
}

func (f fakeNodeRepo) Get(_ context.Context, id string) (nodes.Node, error) {
	node, ok := f.byID[id]
	if !ok {
		return nodes.Node{}, errors.New("not found")
	}
	return node, nil
}

type fakeUserRepo struct {
	list          []users.User
	byID          map[string]users.User
	lookupsByUser map[string][]users.SessionLookup
	lookupsByNode map[string][]users.SessionLookup
}

func (f fakeUserRepo) List(context.Context) ([]users.User, error) {
	return append([]users.User(nil), f.list...), nil
}

func (f fakeUserRepo) Get(_ context.Context, id string) (users.User, error) {
	user, ok := f.byID[id]
	if !ok {
		return users.User{}, errors.New("not found")
	}
	return user, nil
}

func (f fakeUserRepo) SessionLookupForUser(_ context.Context, userID string) ([]users.SessionLookup, error) {
	return append([]users.SessionLookup(nil), f.lookupsByUser[userID]...), nil
}

func (f fakeUserRepo) SessionLookupForNodes(_ context.Context, nodeIDs []string) ([]users.SessionLookup, error) {
	result := make([]users.SessionLookup, 0)
	for _, nodeID := range nodeIDs {
		result = append(result, f.lookupsByNode[nodeID]...)
	}
	return result, nil
}

type fakeAgent struct {
	mu         sync.Mutex
	responses  map[string]nodes.TelemetryResponse
	errors     map[string]error
	calls      []string
	maxRunning int32
	running    int32
	sleep      time.Duration
}

func (f *fakeAgent) TelemetrySessions(ctx context.Context, node nodes.Node, includeRecent bool) (nodes.TelemetryResponse, nodes.AgentCallResult, error) {
	current := atomic.AddInt32(&f.running, 1)
	defer atomic.AddInt32(&f.running, -1)
	for {
		max := atomic.LoadInt32(&f.maxRunning)
		if current <= max || atomic.CompareAndSwapInt32(&f.maxRunning, max, current) {
			break
		}
	}

	f.mu.Lock()
	f.calls = append(f.calls, node.ID)
	f.mu.Unlock()

	if f.sleep > 0 {
		select {
		case <-ctx.Done():
			return nodes.TelemetryResponse{}, nodes.AgentCallResult{}, ctx.Err()
		case <-time.After(f.sleep):
		}
	}

	if err := f.errors[node.ID]; err != nil {
		return nodes.TelemetryResponse{}, nodes.AgentCallResult{}, err
	}
	response := f.responses[node.ID]
	return response, nodes.AgentCallResult{StatusCode: 200}, nil
}

func TestNodeSessionsMatchesExactUserAndFallback(t *testing.T) {
	now := time.Now()
	node := testNode("node-1", true, nodes.SetupStateConnected)
	user := users.User{ID: "user-1", Username: "demo"}
	service := NewService(
		fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
		fakeUserRepo{
			list: []users.User{user},
			byID: map[string]users.User{user.ID: user},
			lookupsByNode: map[string][]users.SessionLookup{
				node.ID: {
					{UserID: user.ID, NodeID: node.ID, ProtocolUsername: "u_demo"},
				},
			},
		},
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			node.ID: {
				OK:              true,
				CollectorStatus: "ok",
				Sessions: []nodes.TelemetrySession{
					{UserID: user.ID, ProtocolUsername: "u_demo", Active: true, LastSeenAt: timePtr(now)},
					{ProtocolUsername: "u_demo", Active: false, LastSeenAt: timePtr(now.Add(-time.Minute))},
				},
			},
		}},
	)

	result, err := service.NodeSessions(context.Background(), node.ID, true)
	if err != nil {
		t.Fatalf("NodeSessions returned error: %v", err)
	}
	if len(result.Node.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(result.Node.Sessions))
	}
	for _, row := range result.Node.Sessions {
		if !row.UserKnown || row.BackendUserID != user.ID {
			t.Fatalf("expected node telemetry rows to resolve to known user, got %+v", row)
		}
	}
}

func TestNodeSessionsLeavesAmbiguousFallbackUnknown(t *testing.T) {
	now := time.Now()
	node := testNode("node-1", true, nodes.SetupStateConnected)
	service := NewService(
		fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
		fakeUserRepo{
			list: []users.User{{ID: "user-1", Username: "one"}, {ID: "user-2", Username: "two"}},
			byID: map[string]users.User{"user-1": {ID: "user-1", Username: "one"}, "user-2": {ID: "user-2", Username: "two"}},
			lookupsByNode: map[string][]users.SessionLookup{
				node.ID: {
					{UserID: "user-1", NodeID: node.ID, ProtocolUsername: "shared"},
					{UserID: "user-2", NodeID: node.ID, ProtocolUsername: "shared"},
				},
			},
		},
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			node.ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{{ProtocolUsername: "shared", Active: true, LastSeenAt: timePtr(now)}}},
		}},
	)

	result, err := service.NodeSessions(context.Background(), node.ID, false)
	if err != nil {
		t.Fatalf("NodeSessions returned error: %v", err)
	}
	if len(result.Node.Sessions) != 1 {
		t.Fatalf("expected one session, got %d", len(result.Node.Sessions))
	}
	if result.Node.Sessions[0].UserKnown {
		t.Fatalf("ambiguous fallback must remain unknown: %+v", result.Node.Sessions[0])
	}
}

func TestAllSessionsCollectsAcrossNodesAndUsesFallback(t *testing.T) {
	now := time.Now()
	node1 := testNode("node-1", true, nodes.SetupStateConnected)
	node2 := testNode("node-2", false, nodes.SetupStateConnected)
	node3 := testNode("node-3", true, nodes.SetupStatePlanned)
	user1 := users.User{ID: "user-1", Username: "demo"}
	user2 := users.User{ID: "user-2", Username: "other"}

	service := NewService(
		fakeNodeRepo{
			list: []nodes.Node{node1, node2, node3},
			byID: map[string]nodes.Node{node1.ID: node1, node2.ID: node2, node3.ID: node3},
		},
		fakeUserRepo{
			list: []users.User{user1, user2},
			byID: map[string]users.User{user1.ID: user1, user2.ID: user2},
			lookupsByNode: map[string][]users.SessionLookup{
				node1.ID: []users.SessionLookup{{UserID: user1.ID, NodeID: node1.ID, ProtocolUsername: "u_demo"}},
				node2.ID: []users.SessionLookup{{UserID: user2.ID, NodeID: node2.ID, ProtocolUsername: "u_other"}},
			},
		},
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			node1.ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{
				{UserID: user1.ID, ProtocolUsername: "u_demo", ClientIP: strPtr("198.51.100.10"), Active: true, LastSeenAt: timePtr(now)},
			}},
			node2.ID: {OK: true, CollectorStatus: "partial", Sessions: []nodes.TelemetrySession{
				{ProtocolUsername: "u_other", ClientIP: strPtr("198.51.100.11"), Active: false, LastSeenAt: timePtr(now.Add(-time.Minute))},
			}},
		}},
	)

	result, err := service.AllSessions(context.Background(), Query{Protocol: "all", Status: "all", IncludeRecent: true})
	if err != nil {
		t.Fatalf("AllSessions returned error: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	if !result.Rows[0].UserKnown || result.Rows[0].BackendUserID != user1.ID {
		t.Fatalf("expected exact match on first row, got %+v", result.Rows[0])
	}
	if !result.Rows[1].UserKnown || result.Rows[1].BackendUserID != user2.ID {
		t.Fatalf("expected empty user_id fallback on second row, got %+v", result.Rows[1])
	}
	if len(result.Nodes) != 3 {
		t.Fatalf("expected 3 node collector views, got %d", len(result.Nodes))
	}
	if result.Nodes[2].SkippedReason != "planned" {
		t.Fatalf("expected planned node to be skipped, got %+v", result.Nodes[2])
	}
	if result.Summary.UniqueActiveIPs != 1 {
		t.Fatalf("expected one unique active IP, got %d", result.Summary.UniqueActiveIPs)
	}
}

func TestAllSessionsIgnoresFallbackForForeignUserID(t *testing.T) {
	now := time.Now()
	node := testNode("node-1", true, nodes.SetupStateConnected)
	user := users.User{ID: "user-1", Username: "demo"}
	service := NewService(
		fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
		fakeUserRepo{
			list: []users.User{user},
			byID: map[string]users.User{user.ID: user},
			lookupsByNode: map[string][]users.SessionLookup{
				node.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node.ID, ProtocolUsername: "u_demo"}},
			},
		},
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			node.ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{
				{UserID: "ghost-user", ProtocolUsername: "u_demo", Active: true, LastSeenAt: timePtr(now)},
			}},
		}},
	)

	result, err := service.AllSessions(context.Background(), Query{Protocol: "all", Status: "all"})
	if err != nil {
		t.Fatalf("AllSessions returned error: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected one row, got %d", len(result.Rows))
	}
	if result.Rows[0].UserKnown {
		t.Fatalf("foreign non-empty user_id must remain unknown globally, got %+v", result.Rows[0])
	}
}

func TestAllSessionsBoundedConcurrencyAndPartialFailure(t *testing.T) {
	list := make([]nodes.Node, 0, 10)
	responses := map[string]nodes.TelemetryResponse{}
	errorsByNode := map[string]error{"node-4": errors.New("unavailable")}
	for i := 0; i < 10; i++ {
		id := "node-" + string(rune('0'+i))
		node := testNode(id, true, nodes.SetupStateConnected)
		list = append(list, node)
		responses[id] = nodes.TelemetryResponse{OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{{ProtocolUsername: "u", Active: true, LastSeenAt: timePtr(time.Now())}}}
	}
	agent := &fakeAgent{responses: responses, errors: errorsByNode, sleep: 20 * time.Millisecond}
	service := NewService(
		fakeNodeRepo{list: list},
		fakeUserRepo{byID: map[string]users.User{}},
		agent,
	)

	result, err := service.AllSessions(context.Background(), Query{Protocol: "all", Status: "all"})
	if err != nil {
		t.Fatalf("AllSessions returned error: %v", err)
	}
	if len(result.Nodes) != 10 {
		t.Fatalf("expected 10 node views, got %d", len(result.Nodes))
	}
	if agent.maxRunning > defaultConcurrency {
		t.Fatalf("expected bounded concurrency <= %d, got %d", defaultConcurrency, agent.maxRunning)
	}
	foundUnavailable := false
	for _, node := range result.Nodes {
		if node.NodeDBID == "node-4" && node.Error != "" {
			foundUnavailable = true
		}
	}
	if !foundUnavailable {
		t.Fatal("expected one unavailable node error without breaking whole result")
	}
}

func TestAllSessionsAttemptsUnreachableAndSkipsMissingAgentParams(t *testing.T) {
	unreachable := testNode("node-1", true, nodes.SetupStateUnreachable)
	missingAPI := testNode("node-2", true, nodes.SetupStateConnected)
	missingAPI.APIURL = ""
	disabled := testNode("node-3", false, nodes.SetupStateConnected)
	planned := testNode("node-4", true, nodes.SetupStatePlanned)

	agent := &fakeAgent{responses: map[string]nodes.TelemetryResponse{
		unreachable.ID: {OK: true, CollectorStatus: "ok"},
		disabled.ID:    {OK: true, CollectorStatus: "disabled"},
	}}
	service := NewService(
		fakeNodeRepo{
			list: []nodes.Node{unreachable, missingAPI, disabled, planned},
			byID: map[string]nodes.Node{unreachable.ID: unreachable, missingAPI.ID: missingAPI, disabled.ID: disabled, planned.ID: planned},
		},
		fakeUserRepo{byID: map[string]users.User{}},
		agent,
	)

	result, err := service.AllSessions(context.Background(), Query{Protocol: "all", Status: "all"})
	if err != nil {
		t.Fatalf("AllSessions returned error: %v", err)
	}
	if len(agent.calls) != 2 {
		t.Fatalf("expected only reachable agent-param nodes to be queried, got calls %v", agent.calls)
	}
	issueNodes := 0
	for _, node := range result.Nodes {
		if node.NodeDBID == missingAPI.ID && node.SkippedReason != "missing_api_url" {
			t.Fatalf("expected missing API URL skip reason, got %+v", node)
		}
		if NodeHasIssue(node) {
			issueNodes++
		}
	}
	if result.Summary.CollectorsIssues != issueNodes {
		t.Fatalf("expected collector issue count %d to match issue nodes, got %d", issueNodes, result.Summary.CollectorsIssues)
	}
}

func TestAllSessionsBlankWarningsAreNotIssues(t *testing.T) {
	node := testNode("node-1", true, nodes.SetupStateConnected)
	service := NewService(
		fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
		fakeUserRepo{byID: map[string]users.User{}},
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			node.ID: {OK: true, CollectorStatus: "ok", Warnings: []string{"", "   "}},
		}},
	)

	result, err := service.AllSessions(context.Background(), Query{Protocol: "all", Status: "all"})
	if err != nil {
		t.Fatalf("AllSessions returned error: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("expected one node result, got %d", len(result.Nodes))
	}
	if NodeHasIssue(result.Nodes[0]) {
		t.Fatalf("blank warnings must not count as issue: %+v", result.Nodes[0])
	}
	if result.Summary.CollectorsIssues != 0 {
		t.Fatalf("blank warnings must not increase collector issue count, got %d", result.Summary.CollectorsIssues)
	}
}

func TestAllSessionsContextCancellationDoesNotFail(t *testing.T) {
	node := testNode("node-1", true, nodes.SetupStateConnected)
	service := NewService(
		fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
		fakeUserRepo{byID: map[string]users.User{}},
		&fakeAgent{sleep: 200 * time.Millisecond},
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := service.AllSessions(ctx, Query{Protocol: "all", Status: "all"})
	if err != nil {
		t.Fatalf("AllSessions should degrade, not fail, on canceled context: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("expected one node result, got %d", len(result.Nodes))
	}
	if !NodeHasIssue(result.Nodes[0]) {
		t.Fatalf("canceled node must still be classified as issue: %+v", result.Nodes[0])
	}
	if result.Summary.CollectorsIssues != 1 {
		t.Fatalf("expected one collector issue after cancellation, got %d", result.Summary.CollectorsIssues)
	}
	if code := NodeIssueCode(result.Nodes[0]); code != "collector_unavailable" {
		t.Fatalf("expected collector_unavailable on cancellation, got %q", code)
	}
}

func TestAllSessionsTimeoutProducesConsistentIssue(t *testing.T) {
	node := testNode("node-1", true, nodes.SetupStateConnected)
	service := NewService(
		fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
		fakeUserRepo{byID: map[string]users.User{}},
		&fakeAgent{sleep: 120 * time.Millisecond},
	)
	service.pageTimeout = 20 * time.Millisecond

	result, err := service.AllSessions(context.Background(), Query{Protocol: "all", Status: "all"})
	if err != nil {
		t.Fatalf("AllSessions should degrade, not fail, on timeout: %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("expected one node result, got %d", len(result.Nodes))
	}
	if !NodeHasIssue(result.Nodes[0]) {
		t.Fatalf("timed out node must still be classified as issue: %+v", result.Nodes[0])
	}
	if result.Summary.CollectorsIssues != 1 {
		t.Fatalf("expected one collector issue after timeout, got %d", result.Summary.CollectorsIssues)
	}
	if code := NodeIssueCode(result.Nodes[0]); code != "collector_unavailable" {
		t.Fatalf("expected collector_unavailable on timeout, got %q", code)
	}
}

func TestUserSessionsProtectsAgainstForeignLeakage(t *testing.T) {
	now := time.Now()
	node := testNode("node-1", true, nodes.SetupStateConnected)
	user := users.User{ID: "user-1", Username: "demo"}
	other := users.User{ID: "user-2", Username: "other"}
	lookups := []users.SessionLookup{{UserID: user.ID, NodeID: node.ID, ProtocolUsername: "u_demo"}}

	service := NewService(
		fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
		fakeUserRepo{
			list:          []users.User{user, other},
			byID:          map[string]users.User{user.ID: user, other.ID: other},
			lookupsByUser: map[string][]users.SessionLookup{user.ID: lookups},
			lookupsByNode: map[string][]users.SessionLookup{
				node.ID: {
					{UserID: user.ID, NodeID: node.ID, ProtocolUsername: "u_demo"},
					{UserID: other.ID, NodeID: node.ID, ProtocolUsername: "u_demo"},
				},
			},
		},
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			node.ID: {
				OK:              true,
				CollectorStatus: "ok",
				Sessions: []nodes.TelemetrySession{
					{UserID: user.ID, ProtocolUsername: "u_demo", ClientIP: strPtr("198.51.100.10"), UploadBytes: 100, Active: true, LastSeenAt: timePtr(now)},
					{ProtocolUsername: "u_demo", ClientIP: strPtr("198.51.100.11"), UploadBytes: 200, Active: false, LastSeenAt: timePtr(now.Add(-time.Minute))},
					{UserID: other.ID, ProtocolUsername: "u_demo", ClientIP: strPtr("198.51.100.12"), UploadBytes: 300, Active: false, LastSeenAt: timePtr(now.Add(-2 * time.Minute))},
				},
			},
		}},
	)

	result, err := service.UserSessions(context.Background(), user.ID, true)
	if err != nil {
		t.Fatalf("UserSessions returned error: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected only exact selected user session and no ambiguous fallback, got %d", len(result.Rows))
	}
	for _, row := range result.Rows {
		if !row.UserKnown || row.BackendUserID != user.ID {
			t.Fatalf("expected only selected user rows, got %+v", row)
		}
		if row.ClientIP == "198.51.100.12" || row.ClientIP == "198.51.100.11" || row.UploadBytes == 300 || row.UploadBytes == 200 {
			t.Fatalf("foreign session leaked into user detail: %+v", row)
		}
	}
}

func TestUserSessionsAllowsUniqueFallbackForSelectedUser(t *testing.T) {
	now := time.Now()
	node := testNode("node-1", true, nodes.SetupStateConnected)
	user := users.User{ID: "user-1", Username: "demo"}
	service := NewService(
		fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
		fakeUserRepo{
			list:          []users.User{user},
			byID:          map[string]users.User{user.ID: user},
			lookupsByUser: map[string][]users.SessionLookup{user.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node.ID, ProtocolUsername: "u_demo"}}},
			lookupsByNode: map[string][]users.SessionLookup{node.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node.ID, ProtocolUsername: "u_demo"}}},
		},
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			node.ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{{ProtocolUsername: "u_demo", ClientIP: strPtr("198.51.100.11"), UploadBytes: 200, Active: false, LastSeenAt: timePtr(now)}}},
		}},
	)

	result, err := service.UserSessions(context.Background(), user.ID, true)
	if err != nil {
		t.Fatalf("UserSessions returned error: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected one unique fallback session, got %d", len(result.Rows))
	}
	if !result.Rows[0].UserKnown || result.Rows[0].BackendUserID != user.ID {
		t.Fatalf("expected fallback to resolve selected user, got %+v", result.Rows[0])
	}
}

func TestUserSessionsExcludesAmbiguousFallbackForBothUsers(t *testing.T) {
	now := time.Now()
	node := testNode("node-1", true, nodes.SetupStateConnected)
	user1 := users.User{ID: "user-1", Username: "one"}
	user2 := users.User{ID: "user-2", Username: "two"}
	lookupsByNode := map[string][]users.SessionLookup{
		node.ID: {
			{UserID: user1.ID, NodeID: node.ID, ProtocolUsername: "shared"},
			{UserID: user2.ID, NodeID: node.ID, ProtocolUsername: "shared"},
		},
	}
	agent := &fakeAgent{responses: map[string]nodes.TelemetryResponse{
		node.ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{{ProtocolUsername: "shared", ClientIP: strPtr("198.51.100.50"), UploadBytes: 500, Active: true, LastSeenAt: timePtr(now)}}},
	}}

	for _, selected := range []users.User{user1, user2} {
		service := NewService(
			fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
			fakeUserRepo{
				list:          []users.User{user1, user2},
				byID:          map[string]users.User{user1.ID: user1, user2.ID: user2},
				lookupsByUser: map[string][]users.SessionLookup{selected.ID: []users.SessionLookup{{UserID: selected.ID, NodeID: node.ID, ProtocolUsername: "shared"}}},
				lookupsByNode: lookupsByNode,
			},
			agent,
		)
		result, err := service.UserSessions(context.Background(), selected.ID, true)
		if err != nil {
			t.Fatalf("UserSessions returned error: %v", err)
		}
		if len(result.Rows) != 0 {
			t.Fatalf("ambiguous fallback must not appear for %s, got %+v", selected.ID, result.Rows)
		}
	}
}

func TestUserSessionsExcludesZeroMatchFallback(t *testing.T) {
	now := time.Now()
	node := testNode("node-1", true, nodes.SetupStateConnected)
	user := users.User{ID: "user-1", Username: "demo"}
	service := NewService(
		fakeNodeRepo{list: []nodes.Node{node}, byID: map[string]nodes.Node{node.ID: node}},
		fakeUserRepo{
			list:          []users.User{user},
			byID:          map[string]users.User{user.ID: user},
			lookupsByUser: map[string][]users.SessionLookup{user.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node.ID, ProtocolUsername: "u_demo"}}},
			lookupsByNode: map[string][]users.SessionLookup{node.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node.ID, ProtocolUsername: "u_demo"}}},
		},
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			node.ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{{ProtocolUsername: "u_missing", ClientIP: strPtr("198.51.100.60"), Active: false, LastSeenAt: timePtr(now)}}},
		}},
	)

	result, err := service.UserSessions(context.Background(), user.ID, true)
	if err != nil {
		t.Fatalf("UserSessions returned error: %v", err)
	}
	if len(result.Rows) != 0 {
		t.Fatalf("zero-match fallback must be excluded, got %+v", result.Rows)
	}
}

func TestUserSessionsFallbackStaysOnSameNodeOnly(t *testing.T) {
	now := time.Now()
	node1 := testNode("node-1", true, nodes.SetupStateConnected)
	node2 := testNode("node-2", true, nodes.SetupStateConnected)
	user := users.User{ID: "user-1", Username: "demo"}
	service := NewService(
		fakeNodeRepo{
			list: []nodes.Node{node1, node2},
			byID: map[string]nodes.Node{node1.ID: node1, node2.ID: node2},
		},
		fakeUserRepo{
			list: []users.User{user},
			byID: map[string]users.User{user.ID: user},
			lookupsByUser: map[string][]users.SessionLookup{
				user.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node1.ID, ProtocolUsername: "shared"}},
			},
			lookupsByNode: map[string][]users.SessionLookup{
				node1.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node1.ID, ProtocolUsername: "shared"}},
			},
		},
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			node1.ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{{ProtocolUsername: "shared", Active: true, LastSeenAt: timePtr(now)}}},
			node2.ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{{ProtocolUsername: "shared", Active: true, LastSeenAt: timePtr(now)}}},
		}},
	)

	result, err := service.UserSessions(context.Background(), user.ID, true)
	if err != nil {
		t.Fatalf("UserSessions returned error: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected only one same-node fallback row, got %d", len(result.Rows))
	}
	if result.Rows[0].NodeDBID != node1.ID {
		t.Fatalf("fallback must stay on assigned node, got %+v", result.Rows[0])
	}
}

func TestUserSessionsDoesNotBreakOnUnavailableAssignedNode(t *testing.T) {
	now := time.Now()
	node1 := testNode("node-1", true, nodes.SetupStateConnected)
	node2 := testNode("node-2", true, nodes.SetupStateConnected)
	user := users.User{ID: "user-1", Username: "demo"}

	service := NewService(
		fakeNodeRepo{
			list: []nodes.Node{node1, node2},
			byID: map[string]nodes.Node{node1.ID: node1, node2.ID: node2},
		},
		fakeUserRepo{
			list: []users.User{user},
			byID: map[string]users.User{user.ID: user},
			lookupsByUser: map[string][]users.SessionLookup{
				user.ID: {
					{UserID: user.ID, NodeID: node1.ID, ProtocolUsername: "u_one"},
					{UserID: user.ID, NodeID: node2.ID, ProtocolUsername: "u_two"},
				},
			},
			lookupsByNode: map[string][]users.SessionLookup{
				node1.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node1.ID, ProtocolUsername: "u_one"}},
				node2.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node2.ID, ProtocolUsername: "u_two"}},
			},
		},
		&fakeAgent{
			responses: map[string]nodes.TelemetryResponse{
				node1.ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{{UserID: user.ID, ProtocolUsername: "u_one", Active: true, LastSeenAt: timePtr(now)}}},
			},
			errors: map[string]error{node2.ID: errors.New("timeout")},
		},
	)

	result, err := service.UserSessions(context.Background(), user.ID, true)
	if err != nil {
		t.Fatalf("UserSessions returned error: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected one successful row, got %d", len(result.Rows))
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("expected both assigned nodes in result, got %d", len(result.Nodes))
	}
}

func TestUserSessionsQueriesOnlyAssignedNodes(t *testing.T) {
	node1 := testNode("node-1", true, nodes.SetupStateConnected)
	node2 := testNode("node-2", true, nodes.SetupStateConnected)
	node3 := testNode("node-3", true, nodes.SetupStateConnected)
	user := users.User{ID: "user-1", Username: "demo"}
	agent := &fakeAgent{responses: map[string]nodes.TelemetryResponse{
		node1.ID: {OK: true, CollectorStatus: "ok"},
		node2.ID: {OK: true, CollectorStatus: "ok"},
		node3.ID: {OK: true, CollectorStatus: "ok"},
	}}

	service := NewService(
		fakeNodeRepo{
			list: []nodes.Node{node1, node2, node3},
			byID: map[string]nodes.Node{node1.ID: node1, node2.ID: node2, node3.ID: node3},
		},
		fakeUserRepo{
			list: []users.User{user},
			byID: map[string]users.User{user.ID: user},
			lookupsByUser: map[string][]users.SessionLookup{
				user.ID: {
					{UserID: user.ID, NodeID: node1.ID, ProtocolUsername: "u_one"},
					{UserID: user.ID, NodeID: node2.ID, ProtocolUsername: "u_two"},
				},
			},
			lookupsByNode: map[string][]users.SessionLookup{
				node1.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node1.ID, ProtocolUsername: "u_one"}},
				node2.ID: []users.SessionLookup{{UserID: user.ID, NodeID: node2.ID, ProtocolUsername: "u_two"}},
			},
		},
		agent,
	)

	if _, err := service.UserSessions(context.Background(), user.ID, false); err != nil {
		t.Fatalf("UserSessions returned error: %v", err)
	}
	if len(agent.calls) != 2 {
		t.Fatalf("expected only assigned nodes to be queried, got %v", agent.calls)
	}
}

func TestUserSessionsIntegrationWithRealRepository(t *testing.T) {
	pool, _ := testsupport.OpenIntegrationTestDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nodeID := testsupport.NewFixtureUUID(t)
	node2ID := testsupport.NewFixtureUUID(t)
	user1 := testsupport.NewFixtureUUID(t)
	user2 := testsupport.NewFixtureUUID(t)
	user3 := testsupport.NewFixtureUUID(t)
	suffix := testsupport.NewFixtureSuffix(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := testsupport.CleanupFixtureRows(cleanupCtx, pool, []string{user1, user2, user3}, []string{nodeID, node2ID}); err != nil {
			t.Fatalf("cleanup fixture rows: %v", err)
		}
	})

	if _, err := pool.Exec(ctx, `insert into nodes (id, node_id, name, domain, api_url, protocol_type, node_secret, enabled, setup_state, protocol_settings)
values
($1, $3, 'Node One', $4, $5, 'naive', 'secret-1', true, 'connected', '{}'::jsonb),
($2, $6, 'Node Two', $7, $8, 'naive', 'secret-2', true, 'connected', '{}'::jsonb)`,
		nodeID,
		node2ID,
		"agent-"+suffix+"-1",
		"one-"+suffix+".example.com",
		"http://one-"+suffix+":2222",
		"agent-"+suffix+"-2",
		"two-"+suffix+".example.com",
		"http://two-"+suffix+":2222",
	); err != nil {
		t.Fatalf("insert nodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `insert into users (id, username, enabled, subscription_token, quota_mb)
values
($1, $4, true, $5, 0),
($2, $6, true, $7, 0),
($3, $8, true, $9, 0)`,
		user1,
		user2,
		user3,
		"user-"+suffix+"-one",
		"token-"+suffix+"-one",
		"user-"+suffix+"-two",
		"token-"+suffix+"-two",
		"user-"+suffix+"-three",
		"token-"+suffix+"-three",
	); err != nil {
		t.Fatalf("insert users: %v", err)
	}
	if _, err := pool.Exec(ctx, `insert into user_node_access (user_id, node_id, protocol_type, protocol_username, protocol_password, enabled)
values
($1, $2, 'naive', 'shared', 'pw1', true),
($3, $2, 'naive', 'shared', 'pw2', true),
($1, $4, 'naive', 'solo', 'pw3', true),
($5, $4, 'naive', 'other', 'pw4', true)`, user1, nodeID, user2, node2ID, user3); err != nil {
		t.Fatalf("insert access: %v", err)
	}

	userRepo := users.NewRepository(pool)
	service := NewService(
		fakeNodeRepo{
			list: []nodes.Node{testNode(nodeID, true, nodes.SetupStateConnected), testNode(node2ID, true, nodes.SetupStateConnected)},
			byID: map[string]nodes.Node{
				nodeID:  testNode(nodeID, true, nodes.SetupStateConnected),
				node2ID: testNode(node2ID, true, nodes.SetupStateConnected),
			},
		},
		userRepo,
		&fakeAgent{responses: map[string]nodes.TelemetryResponse{
			nodeID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{
				{ProtocolUsername: "shared", ClientIP: strPtr("198.51.100.77"), UploadBytes: 777, Active: true, LastSeenAt: timePtr(time.Now())},
				{UserID: user2, ProtocolUsername: "shared", ClientIP: strPtr("198.51.100.88"), UploadBytes: 888, Active: true, LastSeenAt: timePtr(time.Now())},
			}},
			node2ID: {OK: true, CollectorStatus: "ok", Sessions: []nodes.TelemetrySession{
				{ProtocolUsername: "solo", ClientIP: strPtr("198.51.100.99"), UploadBytes: 999, Active: true, LastSeenAt: timePtr(time.Now())},
			}},
		}},
	)

	result, err := service.UserSessions(ctx, user1, true)
	if err != nil {
		t.Fatalf("UserSessions returned error: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("expected only unique fallback row with real repository lookups, got %+v", result.Rows)
	}
	if !result.Rows[0].UserKnown || result.Rows[0].BackendUserID != user1 {
		t.Fatalf("expected selected user unique fallback row, got %+v", result.Rows[0])
	}
	if result.Rows[0].NodeDBID != node2ID {
		t.Fatalf("expected only node-2 unique fallback row, got %+v", result.Rows[0])
	}
	if result.Rows[0].ClientIP != "198.51.100.99" || result.Rows[0].UploadBytes != 999 {
		t.Fatalf("unexpected unique fallback telemetry row: %+v", result.Rows[0])
	}
}

func testNode(id string, enabled bool, setup string) nodes.Node {
	return nodes.Node{
		ID:           id,
		NodeID:       id + "-agent",
		Name:         id,
		APIURL:       "http://" + id + ":2222",
		NodeSecret:   "secret-" + id,
		ProtocolType: "naive",
		Enabled:      enabled,
		SetupState:   setup,
	}
}

func strPtr(value string) *string { return &value }

func timePtr(value time.Time) *time.Time { return &value }
