package handlers

import (
	"bytes"
	"context"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vetka-backend-panel/internal/config"
	"vetka-backend-panel/internal/nodes"
	"vetka-backend-panel/internal/telemetry"
	"vetka-backend-panel/internal/users"
	"vetka-backend-panel/web"
)

type fakeTelemetryService struct {
	lastAllQuery          telemetry.Query
	lastNodeIncludeRecent bool
	lastUserIncludeRecent bool
	allResult             telemetry.AllSessionsResult
	allErr                error
	nodeResult            telemetry.NodeSessionsResult
	nodeErr               error
	userResult            telemetry.UserSessionsResult
	userErr               error
}

func (f *fakeTelemetryService) AllSessions(_ context.Context, query telemetry.Query) (telemetry.AllSessionsResult, error) {
	f.lastAllQuery = query
	return f.allResult, f.allErr
}

func (f *fakeTelemetryService) NodeSessions(_ context.Context, _ string, includeRecent bool) (telemetry.NodeSessionsResult, error) {
	f.lastNodeIncludeRecent = includeRecent
	return f.nodeResult, f.nodeErr
}

func (f *fakeTelemetryService) UserSessions(_ context.Context, _ string, includeRecent bool) (telemetry.UserSessionsResult, error) {
	f.lastUserIncludeRecent = includeRecent
	return f.userResult, f.userErr
}

func parseTelemetryTemplates(t *testing.T) *template.Template {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return template.Must(template.New("").Funcs(template.FuncMap{
		"mask":                 Mask,
		"t":                    Translate,
		"formatTime":           func(locale Locale, v any) string { return FormatTimeForLocale(locale, v, loc) },
		"formatDateTime":       func(locale Locale, v *time.Time) string { return FormatDateTimeForLocale(locale, v, loc) },
		"formatDateTimeInput":  func(v *time.Time) string { return v.In(loc).Format("2006-01-02T15:04") },
		"isUserExpired":        IsUserExpired,
		"isUserExpiringSoon":   IsUserExpiringSoon,
		"timeRemaining":        TimeRemaining,
		"truncateText":         TruncateText,
		"safeJSONPreview":      SafeJSONPreview,
		"maskSecretCompact":    MaskSecretCompact,
		"localizedStatusLabel": LocalizedStatusLabel,
		"formatBytes":          FormatBytesIEC,
		"join":                 strings.Join,
	}).ParseFS(web.FS, "templates/*.html", "templates/partials/*.html"))
}

func newTelemetryHandler(t *testing.T, svc *fakeTelemetryService) *Handler {
	t.Helper()
	return &Handler{
		cfg:          config.Config{AppEnv: "dev", AppTimezone: "Europe/Moscow"},
		appLocation:  loadAppLocation("Europe/Moscow"),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		tmpl:         parseTelemetryTemplates(t),
		telemetrySvc: svc,
	}
}

func TestSessionsHandlerDefaultsToActiveOnly(t *testing.T) {
	svc := &fakeTelemetryService{
		allResult: telemetry.AllSessionsResult{
			Rows:    []telemetry.SessionView{{NodeDBID: "node-1", NodeName: "Node One", NodeProtocol: "naive", Active: true, LastSeenAt: timePtrTest(time.Now())}},
			Nodes:   []telemetry.NodeCollectorView{{NodeDBID: "node-1", NodeName: "Node One", NodeProtocol: "naive", CollectorStatus: "ok"}},
			Summary: telemetry.Summary{ActiveSessions: 1, CollectorsOK: 1},
		},
	}
	h := newTelemetryHandler(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	rec := httptest.NewRecorder()
	h.Sessions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if svc.lastAllQuery.IncludeRecent {
		t.Fatal("expected active-only default fetch")
	}
	if svc.lastAllQuery.Status != "active" {
		t.Fatalf("expected default status active, got %q", svc.lastAllQuery.Status)
	}
}

func TestSessionsHandlerEffectiveIncludeRecent(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "explicit include_recent", url: "/sessions?include_recent=true", want: true},
		{name: "recent status", url: "/sessions?status=recent", want: true},
		{name: "all status", url: "/sessions?status=all", want: true},
		{name: "active status", url: "/sessions?status=active", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeTelemetryService{allResult: telemetry.AllSessionsResult{}}
			h := newTelemetryHandler(t, svc)
			rec := httptest.NewRecorder()
			h.Sessions(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
			if svc.lastAllQuery.IncludeRecent != tc.want {
				t.Fatalf("includeRecent = %v, want %v", svc.lastAllQuery.IncludeRecent, tc.want)
			}
		})
	}
}

func TestSessionsHandlerRendersRecentRowsForRecentStatus(t *testing.T) {
	now := time.Now()
	svc := &fakeTelemetryService{
		allResult: telemetry.AllSessionsResult{
			Rows:  []telemetry.SessionView{{NodeDBID: "node-1", NodeName: "Node One", NodeProtocol: "naive", Active: false, LastSeenAt: timePtrTest(now)}},
			Nodes: []telemetry.NodeCollectorView{{NodeDBID: "node-1", NodeName: "Node One", NodeProtocol: "naive", CollectorStatus: "ok"}},
		},
	}
	h := newTelemetryHandler(t, svc)
	rec := httptest.NewRecorder()
	h.Sessions(rec, httptest.NewRequest(http.MethodGet, "/sessions?status=recent", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, Translate(LocaleRU, "sessions.recent_only")) {
		t.Fatalf("expected recent row in output: %s", body)
	}
}

func TestSessionsHandlerWarningPanelOnlyForIssueNodes(t *testing.T) {
	baseRows := []telemetry.SessionView{{NodeDBID: "node-1", NodeName: "Node One", NodeProtocol: "mieru", Active: true, LastSeenAt: timePtrTest(time.Now())}}

	t.Run("all ok no panel", func(t *testing.T) {
		svc := &fakeTelemetryService{
			allResult: telemetry.AllSessionsResult{
				Rows:  baseRows,
				Nodes: []telemetry.NodeCollectorView{{NodeDBID: "node-1", NodeName: "Node One", NodeProtocol: "mieru", CollectorStatus: "ok", Capabilities: nodes.TelemetryCapabilities{ClientIP: false}}},
			},
		}
		h := newTelemetryHandler(t, svc)
		rec := httptest.NewRecorder()
		h.Sessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
		body := rec.Body.String()
		if strings.Contains(body, Translate(LocaleRU, "sessions.partial_warning")) {
			t.Fatalf("all-ok collectors must not render warning panel: %s", body)
		}
	})

	t.Run("partial renders panel and escapes warning", func(t *testing.T) {
		svc := &fakeTelemetryService{
			allResult: telemetry.AllSessionsResult{
				Rows: baseRows,
				Nodes: []telemetry.NodeCollectorView{{
					NodeDBID:        "node-1",
					NodeName:        "Node One",
					NodeProtocol:    "naive",
					CollectorStatus: "partial",
					Warnings:        []string{`{"password":"secret"}`},
					Capabilities:    nodes.TelemetryCapabilities{ClientIP: true},
				}},
			},
		}
		h := newTelemetryHandler(t, svc)
		rec := httptest.NewRecorder()
		h.Sessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
		body := rec.Body.String()
		if !strings.Contains(body, Translate(LocaleRU, "sessions.partial_warning")) {
			t.Fatalf("expected issue panel: %s", body)
		}
		if strings.Contains(body, "secret") {
			t.Fatalf("warning text must stay sanitized: %s", body)
		}
	})

	t.Run("planned node does not render issue panel", func(t *testing.T) {
		svc := &fakeTelemetryService{
			allResult: telemetry.AllSessionsResult{
				Rows: baseRows,
				Nodes: []telemetry.NodeCollectorView{{
					NodeDBID:        "node-2",
					NodeName:        "Planned Node",
					NodeProtocol:    "naive",
					CollectorStatus: "disabled",
					SkippedReason:   "planned",
				}},
				Summary: telemetry.Summary{CollectorsIssues: 0},
			},
		}
		h := newTelemetryHandler(t, svc)
		rec := httptest.NewRecorder()
		h.Sessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
		body := rec.Body.String()
		if strings.Contains(body, Translate(LocaleRU, "sessions.partial_warning")) {
			t.Fatalf("planned node must not trigger issue panel: %s", body)
		}
	})

	t.Run("missing API URL renders controlled issue", func(t *testing.T) {
		svc := &fakeTelemetryService{
			allResult: telemetry.AllSessionsResult{
				Rows: baseRows,
				Nodes: []telemetry.NodeCollectorView{{
					NodeDBID:           "node-3",
					NodeName:           "Broken Node",
					NodeProtocol:       "naive",
					CollectorStatus:    "disabled",
					SkippedReason:      "missing_api_url",
					ConfigurationIssue: "missing_api_url",
				}},
				Summary: telemetry.Summary{CollectorsIssues: 1},
			},
		}
		h := newTelemetryHandler(t, svc)
		rec := httptest.NewRecorder()
		h.Sessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
		body := rec.Body.String()
		if !strings.Contains(body, Translate(LocaleRU, "sessions.issue_missing_api_url")) {
			t.Fatalf("expected controlled missing-api issue text: %s", body)
		}
	})
}

func TestSessionsTemplateSidebarContainsSessions(t *testing.T) {
	tmpl := parseTelemetryTemplates(t)
	data := map[string]any{
		"Locale":              LocaleEN,
		"Title":               "page.sessions",
		"NavItems":            []navItem{{Label: Translate(LocaleEN, "nav.sessions"), URL: "/sessions", Active: true}},
		"Breadcrumbs":         []breadcrumb{{Label: Translate(LocaleEN, "nav.sessions"), URL: "/sessions"}},
		"FlashItems":          []toastMessage{},
		"Environment":         "DEV",
		"CurrentPath":         "/sessions",
		"Summary":             telemetry.Summary{},
		"TelemetryRows":       []telemetrySessionRowView{},
		"TelemetryNodes":      []telemetryNodeView{},
		"TelemetryIssueNodes": []telemetryNodeView{},
		"HasTelemetryIssues":  false,
		"IncludeRecent":       false,
		"SessionQuery":        "",
		"SessionProtocol":     "all",
		"SessionStatus":       "active",
	}

	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "sessions.html", data); err != nil {
		t.Fatalf("render sessions template: %v", err)
	}
	if !strings.Contains(out.String(), "Sessions") {
		t.Fatalf("expected sessions navigation label in sidebar output: %s", out.String())
	}
}

func TestNodeDetailRendersCollectorSummaryAndSurvivesTelemetryError(t *testing.T) {
	node := nodes.Node{ID: "node-1", NodeID: "agent-1", Name: "Node One", Domain: "node.example.com", ProtocolType: "mieru", Enabled: true, SetupState: "connected"}
	now := time.Now()

	t.Run("summary", func(t *testing.T) {
		svc := &fakeTelemetryService{
			nodeResult: telemetry.NodeSessionsResult{
				Node: telemetry.NodeCollectorView{
					NodeDBID:                   node.ID,
					NodeID:                     node.NodeID,
					NodeName:                   node.Name,
					NodeProtocol:               node.ProtocolType,
					CollectorStatus:            "ok",
					LastSuccessfulCollectionAt: timePtrTest(now),
					Capabilities: nodes.TelemetryCapabilities{
						ClientIP:        false,
						PerUserActivity: true,
						TrafficCounters: true,
						FirstSeen:       true,
						LastSeen:        true,
					},
					Warnings: []string{`<warn>`},
				},
			},
		}
		h := newTelemetryHandler(t, svc)
		h.nodeRepo = &nodes.Repository{}
		h.userRepo = &users.Repository{}

		req := httptest.NewRequest(http.MethodGet, "/nodes/node-1", nil)
		rec := httptest.NewRecorder()
		// direct render path with prepared data is easier than full repo wiring
		data := h.pageData(req, "page.node_detail", "nodes")
		data["Node"] = node
		data["NodeStatusTone"] = "success"
		data["NodeStatusLabel"] = "Connected"
		data["ProtocolTone"] = "protocol-mieru"
		data["MaskedSecret"] = MaskSecretCompact("raw-super-secret")
		data["Assignments"] = []nodeAssignmentView{}
		data["Events"] = []syncEventView{}
		data["TelemetryIncludeRecent"] = true
		data["TelemetryNode"] = buildTelemetryNodeViews(LocaleRU, []telemetry.NodeCollectorView{svc.nodeResult.Node})[0]

		if err := h.tmpl.ExecuteTemplate(rec, "node_detail.html", data); err != nil {
			t.Fatalf("render node detail: %v", err)
		}
		body := rec.Body.String()
		for _, want := range []string{
			Translate(LocaleRU, "sessions.last_successful_collection"),
			Translate(LocaleRU, "sessions.capability_client_ip"),
			Translate(LocaleRU, "sessions.ip_unavailable_mieru"),
		} {
			if !strings.Contains(body, want) {
				t.Fatalf("expected %q in node detail output: %s", want, body)
			}
		}
		if strings.Contains(body, "<warn>") {
			t.Fatalf("warnings must be escaped: %s", body)
		}
	})

	t.Run("telemetry error fallback", func(t *testing.T) {
		h := newTelemetryHandler(t, &fakeTelemetryService{})
		data := h.pageData(httptest.NewRequest(http.MethodGet, "/nodes/node-1", nil), "page.node_detail", "nodes")
		data["Node"] = node
		data["NodeStatusTone"] = "success"
		data["NodeStatusLabel"] = "Connected"
		data["ProtocolTone"] = "protocol-mieru"
		data["MaskedSecret"] = MaskSecretCompact("raw-super-secret")
		data["Assignments"] = []nodeAssignmentView{}
		data["Events"] = []syncEventView{}
		data["TelemetryIncludeRecent"] = false
		data["TelemetryNode"] = telemetryNodeView{
			NodeDBID:        node.ID,
			NodeName:        node.Name,
			CollectorStatus: "unavailable",
			CollectorTone:   "danger",
			CollectorLabel:  collectorStatusLabel(LocaleRU, "unavailable"),
			IssueText:       Translate(LocaleRU, "sessions.issue_collector_unavailable"),
			HasIssues:       true,
			Error:           SafeOperationalError(`authorization: Bearer abcdef`),
		}
		var out bytes.Buffer
		if err := h.tmpl.ExecuteTemplate(&out, "node_detail.html", data); err != nil {
			t.Fatalf("render node detail error state: %v", err)
		}
		body := out.String()
		if !strings.Contains(body, collectorStatusLabel(LocaleRU, "unavailable")) {
			t.Fatalf("expected unavailable telemetry state: %s", body)
		}
		if !strings.Contains(body, Translate(LocaleRU, "sessions.issue_collector_unavailable")) {
			t.Fatalf("expected non-empty unavailable issue text: %s", body)
		}
		if strings.Contains(body, "abcdef") {
			t.Fatalf("raw telemetry error leaked: %s", body)
		}
	})
}

func TestSessionsHandlerCancellationIssuePanelStaysConsistent(t *testing.T) {
	svc := &fakeTelemetryService{
		allResult: telemetry.AllSessionsResult{
			Nodes: []telemetry.NodeCollectorView{{
				NodeDBID:        "node-1",
				NodeName:        "Node One",
				NodeProtocol:    "naive",
				CollectorStatus: "unavailable",
				Error:           "telemetry request canceled",
			}},
			Summary: telemetry.Summary{CollectorsIssues: 1},
		},
	}
	h := newTelemetryHandler(t, svc)
	rec := httptest.NewRecorder()
	h.Sessions(rec, httptest.NewRequest(http.MethodGet, "/sessions", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if !strings.Contains(body, Translate(LocaleRU, "sessions.partial_warning")) {
		t.Fatalf("expected issue panel for canceled telemetry request: %s", body)
	}
	if strings.Contains(body, "<p></p>") {
		t.Fatalf("issue panel must not render empty issue text: %s", body)
	}
	if !strings.Contains(body, "telemetry request canceled") && !strings.Contains(body, Translate(LocaleRU, "sessions.issue_collector_unavailable")) {
		t.Fatalf("expected non-empty issue text for canceled telemetry request: %s", body)
	}
}

func TestUserDetailTemplateExcludesForeignSessionsAndMasksUnknowns(t *testing.T) {
	tmpl := parseTelemetryTemplates(t)
	expiresAt := time.Now().Add(24 * time.Hour)
	user := users.User{ID: "user-1", Username: "demo", Enabled: true, ExpiresAt: &expiresAt}
	maskedUnknown := Mask("u_unknown")
	data := map[string]any{
		"Locale":                     LocaleRU,
		"Title":                      "page.user_detail",
		"NavItems":                   []navItem{},
		"Breadcrumbs":                []breadcrumb{},
		"FlashItems":                 []toastMessage{},
		"Environment":                "DEV",
		"AppTimezone":                "Europe/Moscow",
		"User":                       user,
		"UserStatusTone":             "success",
		"UserStatusLabel":            "Активен",
		"MaskedToken":                Mask("sub-secret"),
		"AssignedNodeCount":          1,
		"SubscriptionExpiryText":     "Срок действия до: 11.07.2026 14:00 MSK",
		"SubscriptionGroups":         []subscriptionLinkGroup{},
		"Access":                     []assignmentView{},
		"Nodes":                      []nodes.Node{},
		"UserTelemetryIncludeRecent": true,
		"UserTelemetryNodes":         []telemetryNodeView{},
		"UserTelemetryIssueNodes":    []telemetryNodeView{},
		"UserTelemetryRows": []telemetrySessionRowView{
			{NodeDBID: "node-1", NodeName: "Node One", NodeProtocol: "mieru", UserKnown: false, MaskedProtocolUsername: Mask("u_unknown"), ClientIP: "", Active: true, LastSeenAt: timePtrTest(time.Now()), TrafficText: "1.0 KiB"},
		},
	}

	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "user_detail.html", data); err != nil {
		t.Fatalf("render user detail: %v", err)
	}
	body := out.String()
	if strings.Contains(body, "u_unknown") {
		t.Fatalf("raw unknown protocol username leaked: %s", body)
	}
	if strings.Contains(body, "/users/user-2") {
		t.Fatalf("foreign user link must not appear: %s", body)
	}
	if strings.Contains(body, maskedUnknown) {
		t.Fatalf("user detail must not render unresolved foreign/unknown username marker: %s", body)
	}
}

func TestUserDetailTopLevelTelemetryErrorShowsNonEmptyIssueText(t *testing.T) {
	tmpl := parseTelemetryTemplates(t)
	user := users.User{ID: "user-1", Username: "demo", Enabled: true, ExpiresAt: timePtrTest(time.Now().Add(24 * time.Hour))}
	data := map[string]any{
		"Locale":                     LocaleRU,
		"Title":                      "page.user_detail",
		"NavItems":                   []navItem{},
		"Breadcrumbs":                []breadcrumb{},
		"FlashItems":                 []toastMessage{},
		"Environment":                "DEV",
		"AppTimezone":                "Europe/Moscow",
		"User":                       user,
		"UserStatusTone":             "success",
		"UserStatusLabel":            "Активен",
		"MaskedToken":                Mask("sub-secret"),
		"AssignedNodeCount":          0,
		"SubscriptionExpiryText":     "Срок действия до: 11.07.2026 14:00 MSK",
		"SubscriptionGroups":         []subscriptionLinkGroup{},
		"Access":                     []assignmentView{},
		"Nodes":                      []nodes.Node{},
		"UserTelemetryIncludeRecent": false,
		"UserTelemetryRows":          []telemetrySessionRowView{},
		"UserTelemetryNodes": []telemetryNodeView{{
			CollectorStatus: "unavailable",
			CollectorTone:   "danger",
			CollectorLabel:  collectorStatusLabel(LocaleRU, "unavailable"),
			Error:           SafeOperationalError(`authorization: Bearer abcdef`),
			IssueText:       Translate(LocaleRU, "sessions.issue_collector_unavailable"),
			HasIssues:       true,
		}},
		"UserTelemetryIssueNodes": []telemetryNodeView{{
			CollectorStatus: "unavailable",
			CollectorTone:   "danger",
			CollectorLabel:  collectorStatusLabel(LocaleRU, "unavailable"),
			Error:           SafeOperationalError(`authorization: Bearer abcdef`),
			IssueText:       Translate(LocaleRU, "sessions.issue_collector_unavailable"),
			HasIssues:       true,
		}},
	}

	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, "user_detail.html", data); err != nil {
		t.Fatalf("render user detail error state: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, Translate(LocaleRU, "sessions.issue_collector_unavailable")) {
		t.Fatalf("expected non-empty user detail telemetry issue text: %s", body)
	}
	if strings.Contains(body, "abcdef") {
		t.Fatalf("raw telemetry error leaked in user detail: %s", body)
	}
}

func TestSessionsHandlerInvalidStatusDoesNotFail(t *testing.T) {
	svc := &fakeTelemetryService{allResult: telemetry.AllSessionsResult{}}
	h := newTelemetryHandler(t, svc)
	rec := httptest.NewRecorder()
	h.Sessions(rec, httptest.NewRequest(http.MethodGet, "/sessions?status=weird", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	if svc.lastAllQuery.Status != "active" {
		t.Fatalf("invalid status must normalize to active, got %q", svc.lastAllQuery.Status)
	}
}

func timePtrTest(value time.Time) *time.Time { return &value }
