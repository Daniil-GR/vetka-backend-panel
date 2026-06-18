package users

import (
	"context"
	"reflect"
	"testing"
	"time"

	"vetka-backend-panel/internal/testsupport"
)

func TestNormalizeNodeLookupIDs(t *testing.T) {
	ids, err := normalizeNodeLookupIDs([]string{
		"11111111-1111-1111-1111-111111111111",
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
	})
	if err != nil {
		t.Fatalf("normalizeNodeLookupIDs returned error: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected deduplicated IDs, got %d", len(ids))
	}
}

func TestNormalizeNodeLookupIDsRejectsInvalidValues(t *testing.T) {
	for _, input := range [][]string{{""}, {"not-a-uuid"}} {
		if _, err := normalizeNodeLookupIDs(input); err == nil {
			t.Fatalf("expected invalid input %v to fail", input)
		}
	}
}

func TestSessionLookupForNodesIntegration(t *testing.T) {
	pool, _ := testsupport.OpenIntegrationTestDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo := NewRepository(pool)

	node1ID := testsupport.NewFixtureUUID(t)
	node2ID := testsupport.NewFixtureUUID(t)
	user1ID := testsupport.NewFixtureUUID(t)
	user2ID := testsupport.NewFixtureUUID(t)
	suffix := testsupport.NewFixtureSuffix(t)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if err := testsupport.CleanupFixtureRows(cleanupCtx, pool, []string{user1ID, user2ID}, []string{node1ID, node2ID}); err != nil {
			t.Fatalf("cleanup fixture rows: %v", err)
		}
	})

	if _, err := pool.Exec(ctx, `insert into nodes (id, node_id, name, domain, api_url, protocol_type, node_secret, enabled, setup_state, protocol_settings)
values
($1, $3, 'Node One', $4, $5, 'naive', 'secret-1', true, 'connected', '{}'::jsonb),
($2, $6, 'Node Two', $7, $8, 'naive', 'secret-2', true, 'connected', '{}'::jsonb)`,
		node1ID,
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
($1, $3, true, $4, 0),
($2, $5, true, $6, 0)`,
		user1ID,
		user2ID,
		"user-"+suffix+"-one",
		"token-"+suffix+"-one",
		"user-"+suffix+"-two",
		"token-"+suffix+"-two",
	); err != nil {
		t.Fatalf("insert users: %v", err)
	}
	if _, err := pool.Exec(ctx, `insert into user_node_access (user_id, node_id, protocol_type, protocol_username, protocol_password, enabled)
values
($1, $2, 'naive', 'u_one', 'p_one', true),
($3, $2, 'naive', 'u_two', 'p_two', true),
($1, $4, 'naive', 'u_other', 'p_other', true)`, user1ID, node1ID, user2ID, node2ID); err != nil {
		t.Fatalf("insert access: %v", err)
	}

	rows, err := repo.SessionLookupForNodes(ctx, []string{node1ID, node2ID, node1ID})
	if err != nil {
		t.Fatalf("SessionLookupForNodes returned error: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 session lookups, got %d", len(rows))
	}
	if _, ok := reflect.TypeOf(SessionLookup{}).FieldByName("ProtocolPassword"); ok {
		t.Fatal("SessionLookup must stay read-only and must not expose protocol password")
	}
	for _, row := range rows {
		if row.ProtocolUsername == "" {
			t.Fatalf("protocol username must be present: %+v", row)
		}
	}

	if rows, err := repo.SessionLookupForNodes(ctx, nil); err != nil || rows != nil {
		t.Fatalf("expected nil,nil for empty list, got rows=%v err=%v", rows, err)
	}
	if _, err := repo.SessionLookupForNodes(ctx, []string{"not-a-uuid"}); err == nil {
		t.Fatal("expected invalid UUID to return controlled error")
	}
	if _, err := repo.SessionLookupForNodes(ctx, []string{""}); err == nil {
		t.Fatal("expected empty UUID to return controlled error")
	}
}
