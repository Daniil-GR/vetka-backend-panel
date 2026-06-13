package http

import (
	"bytes"
	"html/template"
	"io/fs"
	"strings"
	"testing"
	"time"

	"vetka-backend-panel/internal/http/handlers"
	"vetka-backend-panel/internal/nodes"
	"vetka-backend-panel/internal/users"
	"vetka-backend-panel/web"
)

func parseUITemplates(t *testing.T) *template.Template {
	t.Helper()
	loc := loadAppLocation("Europe/Moscow")
	return template.Must(template.New("").Funcs(template.FuncMap{
		"mask":                handlers.Mask,
		"formatTime":          func(v any) string { return formatTime(v, loc) },
		"formatDateTime":      func(v *time.Time) string { return formatDateTime(v, loc) },
		"formatDateTimeInput": func(v *time.Time) string { return formatDateTimeInput(v, loc) },
		"isUserExpired":       handlers.IsUserExpired,
		"isUserExpiringSoon":  handlers.IsUserExpiringSoon,
		"timeRemaining":       func(v *time.Time) string { return handlers.TimeRemaining(v) },
		"truncateText":        handlers.TruncateText,
		"safeJSONPreview":     handlers.SafeJSONPreview,
		"join":                strings.Join,
	}).ParseFS(web.FS, "templates/*.html", "templates/partials/*.html"))
}

func TestTemplatesParse(t *testing.T) {
	parseUITemplates(t)
}

func TestUsersTemplateCreateFormUsesDatetimeLocal(t *testing.T) {
	tmpl := parseUITemplates(t)
	data := map[string]any{
		"Title":       "Users",
		"NavItems":    []any{},
		"Breadcrumbs": []any{},
		"FlashItems":  []any{},
		"Environment": "DEV",
		"AppTimezone": "Europe/Moscow",
		"UserItems":   []any{},
		"Nodes":       []nodes.Node{{ID: "node-1", Name: "Node One", Domain: "example.com", ProtocolType: "mieru"}},
		"Filter":      "all",
		"Search":      "",
		"Sort":        "created_at",
		"UserStats":   users.DashboardStats{},
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "users.html", data); err != nil {
		t.Fatalf("render users template: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `type="datetime-local" name="expires_at"`) {
		t.Fatalf("create form must use datetime-local: %s", body)
	}
	if !strings.Contains(body, `type="hidden" name="enabled" value="false"`) {
		t.Fatalf("create form must include hidden false enabled field: %s", body)
	}
}

func TestUserDetailTemplateUsesDatetimeLocalValue(t *testing.T) {
	tmpl := parseUITemplates(t)
	expiresAt := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC)
	data := map[string]any{
		"Title":                  "User Detail",
		"NavItems":               []any{},
		"Breadcrumbs":            []any{},
		"FlashItems":             []any{},
		"Environment":            "DEV",
		"AppTimezone":            "Europe/Moscow",
		"UserStatusTone":         "success",
		"UserStatusLabel":        "Active",
		"MaskedToken":            handlers.Mask("sub-secret-demo"),
		"AssignedNodeCount":      1,
		"SubscriptionExpiryText": "Expires at: 2026-07-11 14:00 MSK",
		"SubscriptionGroups":     []any{},
		"Access":                 []users.UserNodeAccessDetail{},
		"Nodes":                  []nodes.Node{},
		"User": users.User{
			ID:                "user-1",
			Username:          "demo",
			SubscriptionToken: "sub-secret-demo",
			Enabled:           true,
			ExpiresAt:         &expiresAt,
			QuotaMB:           1024,
		},
	}

	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "user_detail.html", data); err != nil {
		t.Fatalf("render user detail: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `type="datetime-local" name="expires_at"`) {
		t.Fatalf("edit form must use datetime-local: %s", body)
	}
	if !strings.Contains(body, `value="2026-07-11T14:00"`) {
		t.Fatalf("expected exact datetime-local value, got: %s", body)
	}
	if !strings.Contains(body, `type="hidden" name="enabled" value="false"`) {
		t.Fatalf("edit form must include hidden false enabled field: %s", body)
	}
}

func TestUserDetailTemplateRendersSubscriptionGroups(t *testing.T) {
	tmpl := parseUITemplates(t)
	expiresAt := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	data := map[string]any{
		"Title":                  "User Detail",
		"NavItems":               []any{},
		"Breadcrumbs":            []any{},
		"FlashItems":             []any{},
		"Environment":            "DEV",
		"AppTimezone":            "Europe/Moscow",
		"UserStatusTone":         "success",
		"UserStatusLabel":        "Active",
		"MaskedToken":            handlers.Mask("sub-secret-demo"),
		"AssignedNodeCount":      1,
		"SubscriptionExpiryText": "Expires at: 2026-06-15 15:00 MSK",
		"SubscriptionGroups": []any{
			map[string]any{"Title": "Karing", "Links": []any{map[string]any{"Label": "Karing Subscription", "URL": "https://panel/sub/token", "QR": true}}},
			map[string]any{"Title": "Hiddify", "Links": []any{map[string]any{"Label": "Hiddify Subscription", "URL": "https://panel/sub/token?format=hiddify", "QR": true}}},
			map[string]any{"Title": "Debug", "Links": []any{map[string]any{"Label": "Raw Links", "URL": "https://panel/sub/token?format=raw"}}},
		},
		"Access": []any{
			map[string]any{"ID": "a1", "UserID": "user-1", "NodeID": "node-1", "NodeName": "Mieru Node", "NodeEnabled": true, "NodeSetupState": "connected", "NodeProtocolType": "mieru", "Enabled": true, "MaskedProtocolUsername": handlers.Mask("u-demo"), "MaskedProtocolPassword": handlers.Mask("p-demo")},
		},
		"Nodes": []nodes.Node{{ID: "node-1", Name: "Mieru Node", ProtocolType: "mieru"}},
		"User": users.User{
			ID:                "user-1",
			Username:          "demo",
			SubscriptionToken: "sub-secret-demo",
			Enabled:           true,
			ExpiresAt:         &expiresAt,
			QuotaMB:           1024,
		},
	}

	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "user_detail.html", data); err != nil {
		t.Fatalf("render user detail: %v", err)
	}
	body := out.String()
	for _, needle := range []string{"Karing Subscription", "Hiddify Subscription", "Raw Links"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("expected %q in rendered template", needle)
		}
	}
}

func TestDashboardUpcomingExpirationsDoesNotForceExpiresSoonForDistantUser(t *testing.T) {
	tmpl := parseUITemplates(t)
	expiresAt := time.Now().Add(14 * 24 * time.Hour)
	data := map[string]any{
		"Title":        "Dashboard",
		"NavItems":     []any{},
		"Breadcrumbs":  []any{},
		"FlashItems":   []any{},
		"Environment":  "DEV",
		"AppTimezone":  "Europe/Moscow",
		"NodeStats":    nodes.DashboardStats{},
		"UserStats":    users.DashboardStats{},
		"NodeItems":    []any{},
		"RecentEvents": []any{},
		"UpcomingUsers": []any{
			map[string]any{
				"User":        users.User{ID: "user-1", Username: "future-user", ExpiresAt: &expiresAt},
				"StatusTone":  "success",
				"StatusLabel": "Active",
			},
		},
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "dashboard.html", data); err != nil {
		t.Fatalf("render dashboard: %v", err)
	}
	body := out.String()
	if strings.Contains(body, `<span class="badge badge-warning">Expires Soon</span>`) {
		t.Fatalf("distant upcoming user must not be forced into Expires Soon badge: %s", body)
	}
	if !strings.Contains(body, "Active") {
		t.Fatalf("expected active badge for distant upcoming user: %s", body)
	}
}

func TestDashboardTemplateUsesSafeSyncErrors(t *testing.T) {
	tmpl := parseUITemplates(t)
	rawError := `authorization: Bearer abcdef protocol_username=u_demo protocol_password=p_demo`
	data := map[string]any{
		"Title":         "Dashboard",
		"NavItems":      []any{},
		"Breadcrumbs":   []any{},
		"FlashItems":    []any{},
		"Environment":   "DEV",
		"AppTimezone":   "Europe/Moscow",
		"NodeStats":     nodes.DashboardStats{},
		"UserStats":     users.DashboardStats{},
		"NodeItems":     []any{},
		"UpcomingUsers": []any{},
		"RecentEvents": []any{
			map[string]any{
				"Event":        nodes.SyncEvent{Error: &rawError},
				"NodeName":     "Node One",
				"StatusTone":   "danger",
				"StatusLabel":  "Apply Failed",
				"ErrorPreview": handlers.TruncateText(handlers.SafeOperationalError(rawError), 84),
				"SafeError":    handlers.SafeOperationalError(rawError),
			},
		},
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "dashboard.html", data); err != nil {
		t.Fatalf("render dashboard: %v", err)
	}
	body := out.String()
	for _, leak := range []string{"abcdef", "u_demo", "p_demo"} {
		if strings.Contains(body, leak) {
			t.Fatalf("raw dashboard error leaked %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, handlers.SafeOperationalError(rawError)) {
		t.Fatalf("expected sanitized dashboard error: %s", body)
	}
}

func TestNodeDetailTemplateMasksNodeSecretAndPasswords(t *testing.T) {
	tmpl := parseUITemplates(t)
	lastStatus := "ok"
	lastError := `authorization: Bearer abcdef protocol_password=p-demo`
	eventError := `node agent returned 500: {"password":"secret"}`
	expiresAt := time.Now().Add(24 * time.Hour)
	data := map[string]any{
		"Title":           "Node Detail",
		"NavItems":        []any{},
		"Breadcrumbs":     []any{},
		"FlashItems":      []any{},
		"Environment":     "DEV",
		"NodeStatusTone":  "success",
		"NodeStatusLabel": "Connected",
		"ProtocolTone":    "protocol-mieru",
		"MaskedSecret":    handlers.Mask("raw-super-secret"),
		"Node": nodes.Node{
			ID:                   "node-1",
			NodeID:               "test-mieru-1",
			Name:                 "Test Node",
			Domain:               "node.example.com",
			NodeSecret:           "raw-super-secret",
			ProtocolType:         "mieru",
			Enabled:              true,
			SetupState:           "connected",
			DesiredConfigVersion: 9,
			LastAppliedVersion:   9,
			LastStatus:           &lastStatus,
			LastError:            &lastError,
			ProtocolSettings: nodes.ProtocolSettings{
				Mieru: nodes.MieruProtocolSettings{Ports: []string{"2012-2022"}, Protocol: "TCP", MTU: 1400, Multiplexing: "MULTIPLEXING_HIGH", HandshakeMode: "HANDSHAKE_NO_WAIT", Profile: "Test Node"},
			},
		},
		"SafeLastError": handlers.SafeOperationalError(lastError),
		"Assignments": []any{
			map[string]any{"Username": "demo", "UserEnabled": true, "UserExpiresAt": &expiresAt, "MaskedProtocolUsername": handlers.Mask("proto-user"), "MaskedProtocolPassword": handlers.Mask("proto-password"), "Enabled": true},
		},
		"Events": []any{
			map[string]any{
				"Event":        nodes.SyncEvent{Error: &eventError},
				"StatusTone":   "danger",
				"StatusLabel":  "Apply Failed",
				"ErrorPreview": handlers.TruncateText(handlers.SafeOperationalError(eventError), 84),
				"SafeError":    handlers.SafeOperationalError(eventError),
			},
		},
	}

	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "node_detail.html", data); err != nil {
		t.Fatalf("render node detail: %v", err)
	}
	body := out.String()
	if strings.Contains(body, "raw-super-secret") {
		t.Fatalf("raw node secret leaked in template: %s", body)
	}
	if strings.Contains(body, "abcdef") || strings.Contains(body, `"password":"secret"`) || strings.Contains(body, `password":"secret`) {
		t.Fatalf("raw operational error leaked in template: %s", body)
	}
	if strings.Contains(body, "proto-password") {
		t.Fatalf("raw protocol password leaked in template: %s", body)
	}
	if strings.Contains(body, "proto-user") {
		t.Fatalf("raw protocol username leaked in template: %s", body)
	}
	if !strings.Contains(body, handlers.Mask("raw-super-secret")) {
		t.Fatal("expected masked node secret in template")
	}
	if !strings.Contains(body, handlers.SafeOperationalError(lastError)) {
		t.Fatalf("expected sanitized last error in template: %s", body)
	}
	if !strings.Contains(body, "node agent returned 500:") || !strings.Contains(body, "***") {
		t.Fatalf("expected sanitized operational errors in template: %s", body)
	}
	if !strings.Contains(body, handlers.Mask("proto-user")) || !strings.Contains(body, handlers.Mask("proto-password")) {
		t.Fatalf("expected masked assignment credentials in template: %s", body)
	}
}

func TestUserDetailTemplateMasksProtocolUsernameAndPassword(t *testing.T) {
	tmpl := parseUITemplates(t)
	expiresAt := time.Now().Add(24 * time.Hour)
	data := map[string]any{
		"Title":                  "User Detail",
		"NavItems":               []any{},
		"Breadcrumbs":            []any{},
		"FlashItems":             []any{},
		"Environment":            "DEV",
		"AppTimezone":            "Europe/Moscow",
		"UserStatusTone":         "success",
		"UserStatusLabel":        "Active",
		"MaskedToken":            handlers.Mask("sub-secret-demo"),
		"AssignedNodeCount":      1,
		"SubscriptionExpiryText": "Expires at: 2026-06-15 15:00 MSK",
		"SubscriptionGroups":     []any{},
		"Access": []any{
			map[string]any{"ID": "a1", "UserID": "user-1", "NodeID": "node-1", "NodeName": "Mieru Node", "NodeEnabled": true, "NodeSetupState": "connected", "NodeProtocolType": "mieru", "Enabled": true, "MaskedProtocolUsername": handlers.Mask("u-demo"), "MaskedProtocolPassword": handlers.Mask("p-demo")},
		},
		"Nodes": []nodes.Node{{ID: "node-1", Name: "Mieru Node", ProtocolType: "mieru"}},
		"User":  users.User{ID: "user-1", Username: "demo", Enabled: true, ExpiresAt: &expiresAt, SubscriptionToken: "sub-secret-demo"},
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "user_detail.html", data); err != nil {
		t.Fatalf("render user detail: %v", err)
	}
	body := out.String()
	if strings.Contains(body, "u-demo") || strings.Contains(body, "p-demo") {
		t.Fatalf("raw protocol credentials leaked: %s", body)
	}
	if !strings.Contains(body, handlers.Mask("u-demo")) || !strings.Contains(body, handlers.Mask("p-demo")) {
		t.Fatalf("expected masked protocol credentials in template: %s", body)
	}
}

func TestFlashMessagesRenderEscapedWithoutDuplicateFallback(t *testing.T) {
	tmpl := parseUITemplates(t)
	data := map[string]any{
		"Title":      "Login",
		"LoginPage":  true,
		"FlashItems": []any{map[string]any{"Level": "error", "Text": `<script>alert("x")</script>`}},
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "login.html", data); err != nil {
		t.Fatalf("render login: %v", err)
	}
	body := out.String()
	if strings.Contains(body, `<script>alert("x")</script>`) {
		t.Fatalf("flash message was not escaped: %s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;alert") {
		t.Fatalf("escaped flash text missing: %s", body)
	}
	if strings.Contains(body, "flash-fallback") {
		t.Fatalf("flash fallback must not be rendered alongside toasts: %s", body)
	}
}

func TestUsersTemplateRendersExpiredBadgeClass(t *testing.T) {
	tmpl := parseUITemplates(t)
	expiresAt := time.Now().Add(-time.Hour)
	data := map[string]any{
		"Title":       "Users",
		"NavItems":    []any{},
		"Breadcrumbs": []any{},
		"FlashItems":  []any{},
		"Environment": "DEV",
		"AppTimezone": "Europe/Moscow",
		"UserItems": []any{
			map[string]any{
				"User":        users.User{ID: "u1", Username: "expired", ExpiresAt: &expiresAt, SubscriptionToken: "tok"},
				"StatusTone":  "expired",
				"StatusLabel": "Expired",
			},
		},
		"Nodes":     []nodes.Node{},
		"Filter":    "all",
		"Search":    "",
		"Sort":      "created_at",
		"UserStats": users.DashboardStats{},
	}
	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "users.html", data); err != nil {
		t.Fatalf("render users template: %v", err)
	}
	if !strings.Contains(out.String(), `badge badge-expired`) {
		t.Fatalf("expected expired badge class in HTML: %s", out.String())
	}
}

func TestStyleCSSIncludesExpiredBadgeAndCheckboxSelectors(t *testing.T) {
	content, err := fs.ReadFile(web.FS, "static/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	css := string(content)
	if !strings.Contains(css, ".badge-expired") {
		t.Fatal("expected .badge-expired CSS class")
	}
	if strings.Contains(css, ".field input,") {
		t.Fatal("generic .field input selector must not style checkboxes/hidden inputs")
	}
	if !strings.Contains(css, `.field input:not([type="checkbox"]):not([type="hidden"])`) {
		t.Fatal("expected narrowed field input selector")
	}
	if !strings.Contains(css, `.field-checkbox input[type="checkbox"]`) {
		t.Fatal("expected dedicated checkbox styling")
	}
}

func TestRepresentativeTemplatesRender(t *testing.T) {
	tmpl := parseUITemplates(t)
	expiresAt := time.Date(2026, 7, 11, 11, 0, 0, 0, time.UTC)
	node := nodes.Node{
		ID: "node-1", NodeID: "agent-1", Name: "Node One", Domain: "example.com", APIURL: "http://node:2222",
		ProtocolType: "mieru", NodeSecret: "secret", Enabled: true, SetupState: "connected",
		DesiredConfigVersion: 4, LastAppliedVersion: 4,
		ProtocolSettings: nodes.ProtocolSettings{Mieru: nodes.MieruProtocolSettings{Ports: []string{"2012-2022"}, Protocol: "TCP", MTU: 1400, Multiplexing: "MULTIPLEXING_HIGH", HandshakeMode: "HANDSHAKE_NO_WAIT", Profile: "Node One"}},
	}
	user := users.User{ID: "user-1", Username: "demo", Enabled: true, ExpiresAt: &expiresAt, SubscriptionToken: "subtok"}
	cases := map[string]map[string]any{
		"dashboard.html": {
			"Title": "Dashboard", "NavItems": []any{}, "Breadcrumbs": []any{}, "FlashItems": []any{}, "Environment": "DEV", "AppTimezone": "Europe/Moscow",
			"NodeStats": nodes.DashboardStats{}, "UserStats": users.DashboardStats{}, "NodeItems": []any{}, "RecentEvents": []any{}, "UpcomingUsers": []any{},
		},
		"users.html": {
			"Title": "Users", "NavItems": []any{}, "Breadcrumbs": []any{}, "FlashItems": []any{}, "Environment": "DEV", "AppTimezone": "Europe/Moscow",
			"UserItems": []any{}, "Nodes": []nodes.Node{node}, "Filter": "all", "Search": "", "Sort": "created_at", "UserStats": users.DashboardStats{},
		},
		"user_detail.html": {
			"Title": "User Detail", "NavItems": []any{}, "Breadcrumbs": []any{}, "FlashItems": []any{}, "Environment": "DEV", "AppTimezone": "Europe/Moscow",
			"UserStatusTone": "success", "UserStatusLabel": "Active", "User": user, "MaskedToken": handlers.Mask(user.SubscriptionToken),
			"AssignedNodeCount": 1, "SubscriptionExpiryText": "Expires at: 2026-07-11 14:00 MSK", "SubscriptionGroups": []any{}, "Access": []users.UserNodeAccessDetail{}, "Nodes": []nodes.Node{node},
		},
		"nodes.html": {
			"Title": "Nodes", "NavItems": []any{}, "Breadcrumbs": []any{}, "FlashItems": []any{}, "Environment": "DEV",
			"NodeItems": []any{}, "NodeStats": nodes.DashboardStats{}, "BackendIP": "127.0.0.1", "DefaultPort": 2222,
		},
		"node_detail.html": {
			"Title": "Node Detail", "NavItems": []any{}, "Breadcrumbs": []any{}, "FlashItems": []any{}, "Environment": "DEV",
			"NodeStatusTone": "success", "NodeStatusLabel": "Connected", "ProtocolTone": "protocol-mieru", "MaskedSecret": handlers.Mask(node.NodeSecret), "Node": node, "Assignments": []users.NodeUserAccessDetail{}, "Events": []any{},
		},
		"node_edit.html": {
			"Title": "Edit Node", "NavItems": []any{}, "Breadcrumbs": []any{}, "FlashItems": []any{}, "Environment": "DEV", "Node": node,
		},
		"node_created.html": {
			"Title": "Node Created", "NavItems": []any{}, "Breadcrumbs": []any{}, "FlashItems": []any{}, "Environment": "DEV", "Node": node, "BackendIP": "127.0.0.1", "DefaultPort": 2222,
		},
		"login.html": {
			"Title": "Login", "LoginPage": true, "FlashItems": []any{},
		},
	}

	for name, data := range cases {
		var out bytes.Buffer
		if err := tmpl.ExecuteTemplate(&out, name, data); err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
	}
}
