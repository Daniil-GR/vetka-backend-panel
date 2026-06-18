package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"vetka-backend-panel/internal/config"
	"vetka-backend-panel/internal/http/middleware"
	"vetka-backend-panel/internal/nodes"
	"vetka-backend-panel/internal/security"
	"vetka-backend-panel/internal/subscriptions"
	"vetka-backend-panel/internal/telemetry"
	"vetka-backend-panel/internal/users"
)

type telemetryReader interface {
	AllSessions(context.Context, telemetry.Query) (telemetry.AllSessionsResult, error)
	NodeSessions(context.Context, string, bool) (telemetry.NodeSessionsResult, error)
	UserSessions(context.Context, string, bool) (telemetry.UserSessionsResult, error)
}

type Handler struct {
	cfg              config.Config
	appLocation      *time.Location
	logger           *slog.Logger
	tmpl             *template.Template
	nodeRepo         *nodes.Repository
	nodeManager      *nodes.Manager
	expiryReconciler *users.ExpiryReconciler
	userRepo         *users.Repository
	userSvc          *users.Service
	subSvc           *subscriptions.Service
	telemetrySvc     telemetryReader
}

func New(cfg config.Config, logger *slog.Logger, tmpl *template.Template, nodeRepo *nodes.Repository, nodeManager *nodes.Manager, expiryReconciler *users.ExpiryReconciler, userRepo *users.Repository, userSvc *users.Service, subSvc *subscriptions.Service, telemetrySvc telemetryReader) *Handler {
	return &Handler{
		cfg:              cfg,
		appLocation:      loadAppLocation(cfg.AppTimezone),
		logger:           logger,
		tmpl:             tmpl,
		nodeRepo:         nodeRepo,
		nodeManager:      nodeManager,
		expiryReconciler: expiryReconciler,
		userRepo:         userRepo,
		userSvc:          userSvc,
		subSvc:           subSvc,
		telemetrySvc:     telemetrySvc,
	}
}

func Mask(secret string) string {
	return security.MaskSecret(secret)
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, "login.html", h.loginData(r, ""))
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if middleware.Login(h.cfg, w, r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
	h.render(w, r, "login.html", h.loginData(r, "flash.login_invalid"))
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	locale := ResolveLocale(r)
	nodeStats, _ := h.nodeRepo.DashboardStats(r.Context(), now.Add(-24*time.Hour))
	userStats, _ := h.userRepo.DashboardStats(r.Context(), now.Add(72*time.Hour))
	nodesList, _ := h.nodeRepo.List(r.Context())
	counts, _ := h.nodeRepo.AssignedUserCounts(r.Context())
	events, _ := h.nodeRepo.RecentEvents(r.Context(), 10)
	upcoming, _ := h.userRepo.UpcomingExpirations(r.Context(), 6)

	nodeNames := map[string]string{}
	for _, node := range nodesList {
		nodeNames[node.ID] = node.Name
	}
	eventItems := make([]syncEventView, 0, len(events))
	for _, event := range events {
		tone := "success"
		label := strings.ReplaceAll(localizedStatusLabel(locale, strings.ReplaceAll(event.Status, "_", " ")), "Http", "HTTP")
		if event.Status != "ok" {
			tone = "danger"
		}
		safeErrText := ""
		if event.Error != nil {
			safeErrText = SafeOperationalError(*event.Error)
		}
		eventItems = append(eventItems, syncEventView{
			Event:           event,
			NodeName:        includesText(nodeNames[event.NodeID], event.NodeID),
			StatusTone:      tone,
			StatusLabel:     label,
			ErrorPreview:    TruncateText(safeErrText, 84),
			SafeError:       safeErrText,
			ResponsePreview: TruncateText(SafeJSONPreview(event.ResponseJSON), 160),
		})
	}

	upcomingItems := make([]userListItem, 0, len(upcoming))
	for _, user := range upcoming {
		statusTone, statusLabel := userStatus(locale, user)
		upcomingItems = append(upcomingItems, userListItem{
			User:              user,
			StatusTone:        statusTone,
			StatusLabel:       statusLabel,
			AssignedNodeCount: 0,
		})
	}

	data := h.pageData(r, "page.dashboard", "dashboard")
	data["Breadcrumbs"] = []breadcrumb{{Label: Translate(locale, "nav.dashboard"), URL: "/"}}
	data["NodeStats"] = nodeStats
	data["UserStats"] = userStats
	data["NodeItems"] = makeNodeListItems(locale, nodesList, counts)
	data["UpcomingUsers"] = upcomingItems
	data["RecentEvents"] = eventItems
	h.render(w, r, "dashboard.html", data)
}

func (h *Handler) Sessions(w http.ResponseWriter, r *http.Request) {
	locale := ResolveLocale(r)
	status := normalizeTelemetryStatus(r.URL.Query().Get("status"))
	requestedIncludeRecent := strings.EqualFold(r.URL.Query().Get("include_recent"), "true")
	includeRecent := requestedIncludeRecent || status == "recent" || status == "all"
	query := telemetry.Query{
		IncludeRecent: includeRecent,
		Search:        strings.TrimSpace(r.URL.Query().Get("q")),
		Protocol:      strings.TrimSpace(r.URL.Query().Get("protocol")),
		Status:        status,
	}
	if query.Protocol == "" {
		query.Protocol = "all"
	}

	result, err := h.telemetrySvc.AllSessions(r.Context(), query)
	if h.handleErr(w, r, err) {
		return
	}

	nodeViews := buildTelemetryNodeViews(locale, result.Nodes)
	issueNodes := buildTelemetryIssueNodes(nodeViews)
	data := h.pageData(r, "page.sessions", "sessions")
	data["Breadcrumbs"] = []breadcrumb{{Label: Translate(locale, "nav.sessions"), URL: "/sessions"}}
	data["TelemetryRows"] = buildTelemetrySessionRows(result.Rows)
	data["TelemetryNodes"] = nodeViews
	data["TelemetryIssueNodes"] = issueNodes
	data["HasTelemetryIssues"] = len(issueNodes) > 0
	data["IncludeRecent"] = includeRecent
	data["SessionQuery"] = query.Search
	data["SessionProtocol"] = query.Protocol
	data["SessionStatus"] = query.Status
	data["Summary"] = result.Summary
	h.render(w, r, "sessions.html", data)
}

func (h *Handler) Nodes(w http.ResponseWriter, r *http.Request) {
	locale := ResolveLocale(r)
	list, err := h.nodeRepo.List(r.Context())
	if h.handleErr(w, r, err) {
		return
	}
	counts, _ := h.nodeRepo.AssignedUserCounts(r.Context())
	nodeStats, _ := h.nodeRepo.DashboardStats(r.Context(), time.Now().Add(-24*time.Hour))
	data := h.pageData(r, "page.nodes", "nodes")
	data["Breadcrumbs"] = []breadcrumb{{Label: Translate(locale, "nav.nodes"), URL: "/nodes"}}
	data["NodeItems"] = makeNodeListItems(locale, list, counts)
	data["NodeStats"] = nodeStats
	data["BackendIP"] = h.cfg.BackendPublicIP
	data["DefaultPort"] = h.cfg.NodeAgentDefaultPort
	h.render(w, r, "nodes.html", data)
}

func (h *Handler) CreateNode(w http.ResponseWriter, r *http.Request) {
	in := nodeInputFromForm(r)
	node, err := h.nodeManager.CreateNode(r.Context(), in)
	if err != nil {
		h.redirectWithErrorFlash(w, r, "/nodes", "", err)
		return
	}
	if in.Mode == nodes.NodeModeAdopt {
		h.redirectWithFlash(w, r, "/nodes", flashText(ResolveLocale(r), "node_adopted_connected"), "success")
		return
	}
	data := h.pageData(r, "page.node_created", "nodes")
	locale := ResolveLocale(r)
	data["Breadcrumbs"] = []breadcrumb{
		{Label: Translate(locale, "nav.nodes"), URL: "/nodes"},
		{Label: Translate(locale, "page.node_created"), URL: ""},
	}
	data["Node"] = node
	data["BackendIP"] = h.cfg.BackendPublicIP
	data["DefaultPort"] = h.cfg.NodeAgentDefaultPort
	h.render(w, r, "node_created.html", data)
}

func (h *Handler) NodeDetail(w http.ResponseWriter, r *http.Request) {
	node, err := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, r, err) {
		return
	}
	assignments, _ := h.userRepo.AccessDetailForNode(r.Context(), node.ID)
	events, _ := h.nodeRepo.RecentEventsByNode(r.Context(), node.ID, 12)
	locale := ResolveLocale(r)
	statusTone, statusLabel := nodeStatusTone(locale, node)
	eventItems := make([]syncEventView, 0, len(events))
	for _, event := range events {
		tone := "success"
		label := localizedStatusLabel(locale, strings.ReplaceAll(event.Status, "_", " "))
		label = strings.ReplaceAll(label, "Http", "HTTP")
		if event.Status != "ok" {
			tone = "danger"
		}
		safeErrText := ""
		if event.Error != nil {
			safeErrText = SafeOperationalError(*event.Error)
		}
		eventItems = append(eventItems, syncEventView{
			Event:           event,
			NodeName:        node.Name,
			StatusTone:      tone,
			StatusLabel:     label,
			ErrorPreview:    TruncateText(safeErrText, 84),
			SafeError:       safeErrText,
			ResponsePreview: TruncateText(SafeJSONPreview(event.ResponseJSON), 160),
		})
	}
	assignmentViews := make([]nodeAssignmentView, 0, len(assignments))
	for _, item := range assignments {
		assignmentViews = append(assignmentViews, nodeAssignmentView{
			UserID:                 item.UserID,
			Username:               item.Username,
			DisplayName:            item.DisplayName,
			UserEnabled:            item.UserEnabled,
			UserExpiresAt:          item.UserExpiresAt,
			Enabled:                item.Enabled,
			MaskedProtocolUsername: Mask(item.ProtocolUsername),
			MaskedProtocolPassword: Mask(item.ProtocolPassword),
		})
	}

	data := h.pageData(r, "page.node_detail", "nodes")
	data["Breadcrumbs"] = []breadcrumb{
		{Label: Translate(locale, "nav.nodes"), URL: "/nodes"},
		{Label: node.Name, URL: ""},
	}
	data["Node"] = node
	data["NodeStatusTone"] = statusTone
	data["NodeStatusLabel"] = statusLabel
	data["ProtocolTone"] = protocolTone(node.ProtocolType)
	data["MaskedSecret"] = MaskSecretCompact(node.NodeSecret)
	data["SafeLastError"] = ""
	if node.LastError != nil {
		data["SafeLastError"] = SafeOperationalError(*node.LastError)
	}
	data["Assignments"] = assignmentViews
	data["Events"] = eventItems
	includeRecent := strings.EqualFold(r.URL.Query().Get("include_recent"), "true")
	if telemetryResult, telemetryErr := h.telemetrySvc.NodeSessions(r.Context(), node.ID, includeRecent); telemetryErr == nil {
		data["TelemetryNode"] = buildTelemetryNodeViews(locale, []telemetry.NodeCollectorView{telemetryResult.Node})[0]
		data["TelemetryIncludeRecent"] = includeRecent
	} else {
		data["TelemetryNode"] = buildTelemetryNodeViews(locale, []telemetry.NodeCollectorView{{
			NodeDBID:        node.ID,
			NodeID:          node.NodeID,
			NodeName:        node.Name,
			NodeProtocol:    node.ProtocolType,
			NodeEnabled:     node.Enabled,
			CollectorStatus: "unavailable",
			Error:           telemetryErr.Error(),
		}})[0]
		data["TelemetryIncludeRecent"] = includeRecent
	}
	h.render(w, r, "node_detail.html", data)
}

func (h *Handler) EditNodePage(w http.ResponseWriter, r *http.Request) {
	node, err := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, r, err) {
		return
	}
	locale := ResolveLocale(r)
	data := h.pageData(r, "page.node_edit", "nodes")
	data["Breadcrumbs"] = []breadcrumb{
		{Label: Translate(locale, "nav.nodes"), URL: "/nodes"},
		{Label: node.Name, URL: "/nodes/" + node.ID},
		{Label: Translate(locale, "page.node_edit"), URL: ""},
	}
	data["Node"] = node
	h.render(w, r, "node_edit.html", data)
}

func (h *Handler) ValidateNodeStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.nodeManager.ValidateNodeStatus(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id")+"/edit", flashText(ResolveLocale(r), "validation_failed")+": ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id")+"/edit", formatNodeStatusFlash(ResolveLocale(r), status), "success")
}

func (h *Handler) UpdateNode(w http.ResponseWriter, r *http.Request) {
	_, err := h.nodeManager.UpdateNode(r.Context(), chi.URLParam(r, "id"), nodeInputFromForm(r))
	if err != nil {
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id")+"/edit", "", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "node_updated"), "success")
}

func (h *Handler) DeleteNode(w http.ResponseWriter, r *http.Request) {
	if h.handleErr(w, r, h.nodeManager.DeleteNode(r.Context(), chi.URLParam(r, "id"))) {
		return
	}
	h.redirectWithFlash(w, r, "/nodes", flashText(ResolveLocale(r), "node_deleted"), "success")
}

func (h *Handler) NodeHealth(w http.ResponseWriter, r *http.Request) {
	node, getErr := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if getErr != nil {
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "", getErr)
		return
	}
	if _, err := h.nodeManager.CheckNodeHealth(r.Context(), chi.URLParam(r, "id")); err != nil {
		if node.SetupState == nodes.SetupStatePlanned {
			h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "node_not_reachable_yet"), "error")
			return
		}
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "health_failed")+": ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "health_ok"), "success")
}

func (h *Handler) NodeStatus(w http.ResponseWriter, r *http.Request) {
	node, getErr := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if getErr != nil {
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "", getErr)
		return
	}
	status, err := h.nodeManager.FetchNodeStatus(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if node.SetupState == nodes.SetupStatePlanned {
			h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "node_not_reachable_yet"), "error")
			return
		}
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "status_failed")+": ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), formatNodeStatusFlash(ResolveLocale(r), status), "success")
}

func (h *Handler) SyncNode(w http.ResponseWriter, r *http.Request) {
	node, getErr := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if getErr != nil {
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "", getErr)
		return
	}
	resp, err := h.nodeManager.SyncNode(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if node.SetupState == nodes.SetupStatePlanned {
			h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "node_not_reachable_yet"), "error")
			return
		}
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "sync_failed")+": ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), formatSyncFlash(ResolveLocale(r), resp), "success")
}

func (h *Handler) SyncAllNodes(w http.ResponseWriter, r *http.Request) {
	if err := h.nodeManager.SyncAllNodes(r.Context()); err != nil {
		h.redirectWithErrorFlash(w, r, "/nodes", flashText(ResolveLocale(r), "sync_all_failed")+": ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes", flashText(ResolveLocale(r), "sync_all_ok"), "success")
}

func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	locale := ResolveLocale(r)
	list, err := h.userRepo.List(r.Context())
	if h.handleErr(w, r, err) {
		return
	}
	nodesList, _ := h.nodeRepo.List(r.Context())
	counts, _ := h.userRepo.AssignmentCounts(r.Context())
	filter := strings.TrimSpace(r.URL.Query().Get("status"))
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	sortMode := strings.TrimSpace(r.URL.Query().Get("sort"))
	if sortMode == "" {
		sortMode = "created_at"
	}
	items := make([]userListItem, 0, len(list))
	for _, user := range list {
		statusTone, statusLabel := userStatus(locale, user)
		item := userListItem{
			User:              user,
			StatusTone:        statusTone,
			StatusLabel:       statusLabel,
			AssignedNodeCount: counts[user.ID],
		}
		if !matchesUserFilter(item, filter, search) {
			continue
		}
		items = append(items, item)
	}
	sortUserViews(items, sortMode)

	data := h.pageData(r, "page.users", "users")
	data["Breadcrumbs"] = []breadcrumb{{Label: Translate(locale, "nav.users"), URL: "/users"}}
	data["UserItems"] = items
	data["Nodes"] = nodesList
	data["Filter"] = filter
	data["Search"] = search
	data["Sort"] = sortMode
	data["UserStats"], _ = h.userRepo.DashboardStats(r.Context(), time.Now().Add(72*time.Hour))
	h.render(w, r, "users.html", data)
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	nodesList, err := h.nodeRepo.List(r.Context())
	if h.handleErr(w, r, err) {
		return
	}
	protocols := map[string]string{}
	for _, node := range nodesList {
		protocols[node.ID] = node.ProtocolType
	}
	in, inputErr := h.userInputFromForm(r)
	if inputErr != nil {
		h.redirectWithFlash(w, r, "/users", flashText(ResolveLocale(r), "invalid_expiration"), "error")
		return
	}
	user, err := h.userSvc.CreateUser(r.Context(), in, protocols)
	if h.handleErr(w, r, err) {
		return
	}
	syncErrors := h.syncNodesAfterChangeForUI(r.Context(), ResolveLocale(r), in.NodeIDs)
	if len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+user.ID, flashWithList(ResolveLocale(r), "user_saved_sync_failed", syncErrors), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+user.ID, flashText(ResolveLocale(r), "user_saved_synced"), "success")
}

func (h *Handler) UserDetail(w http.ResponseWriter, r *http.Request) {
	user, err := h.userRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, r, err) {
		return
	}
	access, _ := h.userRepo.AccessDetailForUser(r.Context(), user.ID)
	nodesList, _ := h.nodeRepo.List(r.Context())
	base := h.cfg.SubscriptionPublicBaseURL + "/sub/" + user.SubscriptionToken
	locale := ResolveLocale(r)
	statusTone, statusLabel := userStatus(locale, user)
	accessViews := make([]assignmentView, 0, len(access))
	for _, item := range access {
		accessViews = append(accessViews, assignmentView{
			ID:                     item.ID,
			UserID:                 item.UserID,
			NodeID:                 item.NodeID,
			NodeName:               item.NodeName,
			NodeSetupState:         item.NodeSetupState,
			NodeProtocolType:       item.NodeProtocolType,
			NodeEnabled:            item.NodeEnabled,
			Enabled:                item.Enabled,
			MaskedProtocolUsername: Mask(item.ProtocolUsername),
			MaskedProtocolPassword: Mask(item.ProtocolPassword),
		})
	}
	data := h.pageData(r, "page.user_detail", "users")
	data["Breadcrumbs"] = []breadcrumb{
		{Label: Translate(locale, "nav.users"), URL: "/users"},
		{Label: user.Username, URL: ""},
	}
	data["User"] = user
	data["Access"] = accessViews
	data["Nodes"] = nodesList
	data["SubscriptionExpiryText"] = subscriptionExpiryText(locale, user.ExpiresAt, h.appLocation)
	data["UserStatusTone"] = statusTone
	data["UserStatusLabel"] = statusLabel
	data["MaskedToken"] = Mask(user.SubscriptionToken)
	data["AssignedNodeCount"] = len(access)
	hiddifyLinks := []subscriptionLink{
		{Label: subscriptionText(locale, "hiddify_subscription"), URL: base + "?format=hiddify", QR: true},
		{Label: subscriptionText(locale, "hiddify_json"), URL: base + "?format=hiddify-json"},
	}
	if hasDetailedProtocolAccess(access, "mieru") {
		hiddifyLinks = append(hiddifyLinks, subscriptionLink{Label: subscriptionText(locale, "hiddify_mieru_only"), URL: base + "?format=mierus"})
	}
	if hasDetailedProtocolAccess(access, "naive") {
		hiddifyLinks = append(hiddifyLinks, subscriptionLink{Label: subscriptionText(locale, "hiddify_naive_only"), URL: base + "?format=naive"})
	}
	data["SubscriptionGroups"] = []subscriptionLinkGroup{
		{
			Title: subscriptionText(locale, "group_karing"),
			Links: []subscriptionLink{
				{Label: subscriptionText(locale, "karing_subscription"), URL: base, QR: true},
				{Label: subscriptionText(locale, "karing_json"), URL: base + "?format=json"},
			},
		},
		{
			Title: subscriptionText(locale, "group_hiddify"),
			Links: hiddifyLinks,
		},
		{
			Title: subscriptionText(locale, "group_debug"),
			Links: []subscriptionLink{
				{Label: subscriptionText(locale, "raw_links"), URL: base + "?format=raw"},
			},
		},
	}
	includeRecent := strings.EqualFold(r.URL.Query().Get("include_recent"), "true")
	if telemetryResult, telemetryErr := h.telemetrySvc.UserSessions(r.Context(), user.ID, includeRecent); telemetryErr == nil {
		data["UserTelemetryRows"] = buildTelemetrySessionRows(telemetryResult.Rows)
		userTelemetryNodes := buildTelemetryNodeViews(locale, telemetryResult.Nodes)
		data["UserTelemetryNodes"] = userTelemetryNodes
		data["UserTelemetryIssueNodes"] = buildTelemetryIssueNodes(userTelemetryNodes)
		data["UserTelemetryIncludeRecent"] = includeRecent
	} else {
		data["UserTelemetryRows"] = []telemetrySessionRowView{}
		userTelemetryNodes := buildTelemetryNodeViews(locale, []telemetry.NodeCollectorView{{
			CollectorStatus: "unavailable",
			Error:           telemetryErr.Error(),
		}})
		data["UserTelemetryNodes"] = userTelemetryNodes
		data["UserTelemetryIssueNodes"] = userTelemetryNodes
		data["UserTelemetryIncludeRecent"] = includeRecent
	}
	h.render(w, r, "user_detail.html", data)
}

func normalizeTelemetryStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "recent":
		return "recent"
	case "all":
		return "all"
	default:
		return "active"
	}
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	nodeIDs, err := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, r, err) {
		return
	}
	in, inputErr := h.userInputFromForm(r)
	if inputErr != nil {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "invalid_expiration"), "error")
		return
	}
	_, err = h.userRepo.Update(r.Context(), chi.URLParam(r, "id"), in)
	if h.handleErr(w, r, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), ResolveLocale(r), nodeIDs); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashWithList(ResolveLocale(r), "user_saved_sync_failed", syncErrors), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "user_saved_synced"), "success")
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	nodeIDs, err := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, r, err) {
		return
	}
	if h.handleErr(w, r, h.userRepo.Delete(r.Context(), chi.URLParam(r, "id"))) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), ResolveLocale(r), nodeIDs); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users", flashWithList(ResolveLocale(r), "user_deleted_sync_failed", syncErrors), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users", flashText(ResolveLocale(r), "user_deleted_synced"), "success")
}

func (h *Handler) EnableUser(w http.ResponseWriter, r *http.Request) {
	if err := h.userRepo.SetEnabled(r.Context(), chi.URLParam(r, "id"), true); h.handleErr(w, r, err) {
		return
	}
	nodeIDs, err := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, r, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), ResolveLocale(r), nodeIDs); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashWithList(ResolveLocale(r), "user_enabled_sync_failed", syncErrors), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "user_enabled_synced"), "success")
}

func (h *Handler) DisableUser(w http.ResponseWriter, r *http.Request) {
	if err := h.userRepo.SetEnabled(r.Context(), chi.URLParam(r, "id"), false); h.handleErr(w, r, err) {
		return
	}
	nodeIDs, err := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, r, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), ResolveLocale(r), nodeIDs); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashWithList(ResolveLocale(r), "user_disabled_sync_failed", syncErrors), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "user_disabled_synced"), "success")
}

func (h *Handler) AssignNode(w http.ResponseWriter, r *http.Request) {
	node, err := h.nodeRepo.Get(r.Context(), r.FormValue("node_id"))
	if h.handleErr(w, r, err) {
		return
	}
	username := r.FormValue("protocol_username")
	password := r.FormValue("protocol_password")
	if username == "" {
		username, _ = security.Token("u", 8)
	}
	if password == "" {
		password, _ = security.Token("p", 18)
	}
	err = h.userRepo.AssignNode(r.Context(), chi.URLParam(r, "id"), node.ID, node.ProtocolType, username, password)
	if h.handleErr(w, r, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), ResolveLocale(r), []string{node.ID}); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashWithList(ResolveLocale(r), "assignment_saved_sync_failed", syncErrors), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "assignment_saved_synced"), "success")
}

func (h *Handler) UnassignNode(w http.ResponseWriter, r *http.Request) {
	nodeID := r.FormValue("node_id")
	if err := h.userRepo.UnassignNode(r.Context(), chi.URLParam(r, "id"), nodeID); h.handleErr(w, r, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), ResolveLocale(r), []string{nodeID}); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashWithList(ResolveLocale(r), "unassigned_sync_failed", syncErrors), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "node_unassigned_synced"), "success")
}

func (h *Handler) EnableUserNodeAccess(w http.ResponseWriter, r *http.Request) {
	access, err := h.userRepo.AccessByID(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "accessID"))
	if h.handleErr(w, r, err) {
		return
	}
	if err := h.userRepo.SetAccessEnabled(r.Context(), chi.URLParam(r, "id"), access.ID, true); h.handleErr(w, r, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), ResolveLocale(r), []string{access.NodeID}); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashWithList(ResolveLocale(r), "node_access_enabled_sync_failed", syncErrors), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "node_access_enabled_synced"), "success")
}

func (h *Handler) DisableUserNodeAccess(w http.ResponseWriter, r *http.Request) {
	access, err := h.userRepo.AccessByID(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "accessID"))
	if h.handleErr(w, r, err) {
		return
	}
	if err := h.userRepo.SetAccessEnabled(r.Context(), chi.URLParam(r, "id"), access.ID, false); h.handleErr(w, r, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), ResolveLocale(r), []string{access.NodeID}); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashWithList(ResolveLocale(r), "node_access_disabled_sync_failed", syncErrors), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "node_access_disabled_synced"), "success")
}

func (h *Handler) SyncUserNodes(w http.ResponseWriter, r *http.Request) {
	errs, err := h.syncUserAssignmentsForUI(r.Context(), ResolveLocale(r), chi.URLParam(r, "id"))
	if h.handleErr(w, r, err) {
		return
	}
	if len(errs) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashWithList(ResolveLocale(r), "sync_failed_for_nodes", errs), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), flashText(ResolveLocale(r), "affected_nodes_synced"), "success")
}

func (h *Handler) ReconcileExpiredUsers(w http.ResponseWriter, r *http.Request) {
	result, err := h.expiryReconciler.RunOnce(r.Context())
	if err != nil {
		h.redirectWithFlash(w, r, "/users", flashText(ResolveLocale(r), "expired_reconcile_failed")+": "+formatExpiryReconcileResult(ResolveLocale(r), result), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users", flashText(ResolveLocale(r), "expired_reconcile_ok")+": "+formatExpiryReconcileResult(ResolveLocale(r), result), "success")
}

func (h *Handler) Subscription(w http.ResponseWriter, r *http.Request) {
	user, userErr := h.userRepo.GetByToken(r.Context(), chi.URLParam(r, "token"))
	if userErr != nil {
		httpErrorRaw(w, userErr, http.StatusNotFound)
		return
	}
	format := r.URL.Query().Get("format")
	body, contentType, err := h.subSvc.BuildByToken(r.Context(), chi.URLParam(r, "token"), format)
	if err != nil {
		httpErrorRaw(w, err, http.StatusNotFound)
		return
	}
	h.applySubscriptionHeaders(w, format, contentType, user)
	_, _ = w.Write([]byte(body + "\n"))
}

func (h *Handler) APICreateUser(w http.ResponseWriter, r *http.Request) {
	var in users.CreateUserInput
	if decodeJSON(w, r, &in) {
		return
	}
	nodesList, _ := h.nodeRepo.List(r.Context())
	protocols := map[string]string{}
	for _, node := range nodesList {
		protocols[node.ID] = node.ProtocolType
	}
	user, err := h.userSvc.CreateUser(r.Context(), in, protocols)
	if err != nil {
		writeJSONOrError(w, http.StatusCreated, user, err)
		return
	}
	syncErrors := h.syncNodesAfterChange(r.Context(), in.NodeIDs)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "user": user, "sync_errors": syncErrors})
}

func (h *Handler) APIGetUser(w http.ResponseWriter, r *http.Request) {
	user, err := h.userRepo.Get(r.Context(), chi.URLParam(r, "id"))
	writeJSONOrError(w, http.StatusOK, user, err)
}

func (h *Handler) APIUpdateUser(w http.ResponseWriter, r *http.Request) {
	var in users.UpdateUserInput
	if decodeJSON(w, r, &in) {
		return
	}
	user, err := h.userRepo.Update(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		writeJSONOrError(w, http.StatusOK, user, err)
		return
	}
	nodeIDs, _ := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": user, "sync_errors": h.syncNodesAfterChange(r.Context(), nodeIDs)})
}

func (h *Handler) APIEnableUser(w http.ResponseWriter, r *http.Request) {
	err := h.userRepo.SetEnabled(r.Context(), chi.URLParam(r, "id"), true)
	if err != nil {
		writeJSONOrError(w, http.StatusOK, map[string]bool{"ok": true}, err)
		return
	}
	nodeIDs, _ := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sync_errors": h.syncNodesAfterChange(r.Context(), nodeIDs)})
}

func (h *Handler) APIDisableUser(w http.ResponseWriter, r *http.Request) {
	err := h.userRepo.SetEnabled(r.Context(), chi.URLParam(r, "id"), false)
	if err != nil {
		writeJSONOrError(w, http.StatusOK, map[string]bool{"ok": true}, err)
		return
	}
	nodeIDs, _ := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sync_errors": h.syncNodesAfterChange(r.Context(), nodeIDs)})
}

func (h *Handler) APIAssignNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		NodeID           string `json:"node_id"`
		ProtocolUsername string `json:"protocol_username"`
		ProtocolPassword string `json:"protocol_password"`
	}
	if decodeJSON(w, r, &in) {
		return
	}
	node, err := h.nodeRepo.Get(r.Context(), in.NodeID)
	if err != nil {
		writeJSONOrError(w, http.StatusBadRequest, nil, err)
		return
	}
	if in.ProtocolUsername == "" {
		in.ProtocolUsername, _ = security.Token("u", 8)
	}
	if in.ProtocolPassword == "" {
		in.ProtocolPassword, _ = security.Token("p", 18)
	}
	err = h.userRepo.AssignNode(r.Context(), chi.URLParam(r, "id"), node.ID, node.ProtocolType, in.ProtocolUsername, in.ProtocolPassword)
	if err != nil {
		writeJSONOrError(w, http.StatusOK, map[string]bool{"ok": true}, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sync_errors": h.syncNodesAfterChange(r.Context(), []string{node.ID})})
}

func (h *Handler) APIUnassignNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		NodeID string `json:"node_id"`
	}
	if decodeJSON(w, r, &in) {
		return
	}
	err := h.userRepo.UnassignNode(r.Context(), chi.URLParam(r, "id"), in.NodeID)
	if err != nil {
		writeJSONOrError(w, http.StatusOK, map[string]bool{"ok": true}, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sync_errors": h.syncNodesAfterChange(r.Context(), []string{in.NodeID})})
}

func (h *Handler) APISyncUser(w http.ResponseWriter, r *http.Request) {
	errs, err := h.syncUserAssignments(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeJSONOrError(w, http.StatusBadRequest, nil, err)
		return
	}
	if len(errs) > 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "errors": errs})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) APIUserSubscription(w http.ResponseWriter, r *http.Request) {
	user, err := h.userRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeJSONOrError(w, http.StatusNotFound, nil, err)
		return
	}
	base := h.cfg.SubscriptionPublicBaseURL + "/sub/" + user.SubscriptionToken
	writeJSONOrError(w, http.StatusOK, map[string]string{
		"url":           base,
		"json_url":      base + "?format=json",
		"karing_url":    base + "?format=karing",
		"hiddify_url":   base + "?format=hiddify",
		"hiddify_json":  base + "?format=hiddify-json",
		"raw_url":       base + "?format=raw",
		"mierus_url":    base + "?format=mierus",
		"naive_url":     base + "?format=naive",
		"singbox_url":   base + "?format=sing-box",
		"profile_title": h.cfg.SubscriptionProfileTitle,
	}, nil)
}

func (h *Handler) APIReconcileExpiredUsers(w http.ResponseWriter, r *http.Request) {
	result, err := h.expiryReconciler.RunOnce(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "result": result, "errors": result.Errors})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (h *Handler) applySubscriptionHeaders(w http.ResponseWriter, format, contentType string, user users.User) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(h.cfg.SubscriptionProfileTitle)))
	w.Header().Set("Profile-Update-Interval", strconv.Itoa(h.cfg.SubscriptionUpdateIntervalHours))
	w.Header().Set("Subscription-Userinfo", subscriptionUserinfo(user))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, subscriptions.ContentDispositionFilename(format)))
}

func subscriptionUserinfo(user users.User) string {
	expire := int64(0)
	if user.ExpiresAt != nil {
		expire = user.ExpiresAt.UTC().Unix()
	}
	total := int64(0)
	if user.QuotaMB > 0 {
		total = int64(user.QuotaMB) * 1024 * 1024
	}
	return fmt.Sprintf("upload=0; download=0; total=%d; expire=%d", total, expire)
}

func subscriptionExpiryText(locale Locale, expiresAt *time.Time, loc *time.Location) string {
	return subscriptionExpiryTextAt(locale, expiresAt, loc, time.Now())
}

func subscriptionExpiryTextAt(locale Locale, expiresAt *time.Time, loc *time.Location, now time.Time) string {
	if expiresAt == nil {
		return Translate(locale, "subscription.unlimited")
	}
	formatted := FormatDateTimeWithZoneForLocale(locale, expiresAt, loc)
	if expiresAt.Before(now) {
		return Translate(locale, "subscription.expired_at") + " " + formatted
	}
	if expiresAt.Before(now.Add(72 * time.Hour)) {
		return Translate(locale, "subscription.expires_at") + " " + formatted + " (" + Translate(locale, "status.expires_soon") + ")"
	}
	return Translate(locale, "subscription.expires_at") + " " + formatted
}

func (h *Handler) APIListNodes(w http.ResponseWriter, r *http.Request) {
	list, err := h.nodeRepo.List(r.Context())
	if err != nil {
		writeJSONOrError(w, http.StatusBadRequest, nil, err)
		return
	}
	response := make([]nodeResponse, 0, len(list))
	for _, node := range list {
		response = append(response, newNodeResponse(node, false))
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) APICreateNode(w http.ResponseWriter, r *http.Request) {
	var in nodes.CreateNodeInput
	if decodeJSON(w, r, &in) {
		return
	}
	if in.Mode == "" {
		in.Mode = nodes.NodeModePlanned
	}
	node, err := h.nodeManager.CreateNode(r.Context(), in)
	if err != nil {
		writeJSONOrError(w, http.StatusBadRequest, nil, err)
		return
	}
	writeJSON(w, http.StatusCreated, newNodeResponse(node, true))
}

func (h *Handler) APIGetNode(w http.ResponseWriter, r *http.Request) {
	node, err := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeJSONOrError(w, http.StatusNotFound, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, newNodeResponse(node, false))
}

func (h *Handler) APIUpdateNode(w http.ResponseWriter, r *http.Request) {
	var in nodes.UpdateNodeInput
	if decodeJSON(w, r, &in) {
		return
	}
	node, err := h.nodeManager.UpdateNode(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		writeJSONOrError(w, http.StatusBadRequest, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, newNodeResponse(node, false))
}

func (h *Handler) APINodeHealth(w http.ResponseWriter, r *http.Request) {
	status, err := h.nodeManager.CheckNodeHealth(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *Handler) APINodeStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.nodeManager.FetchNodeStatus(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *Handler) APISyncNode(w http.ResponseWriter, r *http.Request) {
	resp, err := h.nodeManager.SyncNode(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) APISyncAllNodes(w http.ResponseWriter, r *http.Request) {
	if err := h.nodeManager.SyncAllNodes(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("render template", "template", name, "error", SafeOperationalError(err.Error()))
		http.Error(w, Translate(ResolveLocale(r), "flash.internal_server_error"), http.StatusInternalServerError)
	}
}

func (h *Handler) handleErr(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	h.logger.Error("request failed", "error", SafeOperationalError(err.Error()))
	http.Error(w, Translate(ResolveLocale(r), "flash.internal_server_error"), http.StatusInternalServerError)
	return true
}

func nodeInputFromForm(r *http.Request) nodes.CreateNodeInput {
	return nodes.CreateNodeInput{
		Mode:         r.FormValue("mode"),
		NodeID:       r.FormValue("node_id"),
		Name:         r.FormValue("name"),
		Domain:       r.FormValue("domain"),
		APIURL:       r.FormValue("api_url"),
		ProtocolType: r.FormValue("protocol_type"),
		NodeSecret:   r.FormValue("node_secret"),
		Enabled:      boolFromForm(r, "enabled", true),
		ProtocolSettings: nodes.ProtocolSettings{
			Mieru: nodes.MieruProtocolSettings{
				Ports:          splitCSV(r.FormValue("mieru_ports")),
				Protocol:       r.FormValue("mieru_protocol"),
				MTU:            intFromForm(r.FormValue("mieru_mtu")),
				Multiplexing:   r.FormValue("mieru_multiplexing"),
				HandshakeMode:  r.FormValue("mieru_handshake_mode"),
				TrafficPattern: r.FormValue("mieru_traffic_pattern"),
				Profile:        r.FormValue("mieru_profile"),
			},
			Naive: nodes.NaiveProtocolSettings{
				Port: intFromForm(r.FormValue("naive_port")),
			},
		},
	}
}

func (h *Handler) userInputFromForm(r *http.Request) (users.CreateUserInput, error) {
	expiresAt, err := parseOptionalDateTime(r.FormValue("expires_at"), h.appLocation)
	if err != nil {
		return users.CreateUserInput{}, err
	}
	return users.CreateUserInput{
		Username:    r.FormValue("username"),
		DisplayName: stringPtr(r.FormValue("display_name")),
		Enabled:     boolFromForm(r, "enabled", true),
		ExpiresAt:   expiresAt,
		QuotaMB:     intFromForm(r.FormValue("quota_mb")),
		Notes:       stringPtr(r.FormValue("notes")),
		NodeIDs:     r.Form["node_ids"],
	}, nil
}

func boolFromForm(r *http.Request, key string, fallback bool) bool {
	_ = r.ParseForm()
	values, ok := r.PostForm[key]
	if !ok || len(values) == 0 {
		return fallback
	}
	for _, value := range values {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "on", "1":
			return true
		}
	}
	return false
}

func parseOptionalDateTime(value string, loc *time.Location) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	if loc == nil {
		loc = time.UTC
	}
	t, err := time.ParseInLocation("2006-01-02T15:04", value, loc)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func intFromForm(value string) int {
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpErrorRaw(w, err, http.StatusBadRequest)
		return true
	}
	return false
}

func writeJSONOrError(w http.ResponseWriter, status int, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func loadAppLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (h *Handler) SetLanguage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	rawLanguage := strings.TrimSpace(strings.ToLower(r.FormValue("language")))
	locale := LocaleRU
	if rawLanguage == string(LocaleEN) {
		locale = LocaleEN
	}
	returnTo := h.safeReturnTo(r.FormValue("return_to"))
	http.SetCookie(w, &http.Cookie{
		Name:     languageCookieName,
		Value:    string(locale),
		Path:     "/",
		MaxAge:   31536000,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(strings.ToLower(h.cfg.PanelPublicBaseURL), "https://"),
	})
	http.Redirect(w, r, returnTo, http.StatusFound)
}

func (h *Handler) redirectWithFlash(w http.ResponseWriter, r *http.Request, path, message, level string) {
	values := url.Values{}
	values.Set("flash", message)
	values.Set("level", level)
	http.Redirect(w, r, path+"?"+values.Encode(), http.StatusFound)
}

func (h *Handler) safeReturnTo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\\\r\n\x00") {
		return "/"
	}

	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return "/"
	}

	if parsed.IsAbs() || parsed.Host != "" || parsed.Opaque != "" {
		return "/"
	}

	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return "/"
	}

	if !strings.HasPrefix(decodedPath, "/") || strings.HasPrefix(decodedPath, "//") || strings.Contains(decodedPath, "\\") {
		return "/"
	}

	return value
}

func safeUIErrorText(locale Locale, value string) string {
	sanitized := SafeOperationalError(value)
	if sanitized == "" {
		return Translate(locale, "flash.operation_failed")
	}
	sanitized = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, sanitized)
	sanitized = strings.Join(strings.Fields(sanitized), " ")
	sanitized = TruncateText(sanitized, 300)
	if strings.TrimSpace(sanitized) == "" || sanitized == "***" || sanitized == "[redacted operational error]" {
		return Translate(locale, "flash.operation_failed")
	}
	return sanitized
}

func SafeUIError(locale Locale, err error) string {
	if err == nil {
		return Translate(locale, "flash.operation_failed")
	}
	return safeUIErrorText(locale, err.Error())
}

func (h *Handler) redirectWithErrorFlash(w http.ResponseWriter, r *http.Request, path, prefix string, err error) {
	locale := ResolveLocale(r)
	safeErr := SafeUIError(locale, err)
	h.logger.Error("ui action failed", "path", r.URL.Path, "error", safeErr)
	message := safeErr
	if prefix != "" {
		message = prefix + safeErr
	}
	h.redirectWithFlash(w, r, path, message, "error")
}

func (h *Handler) syncUserAssignments(ctx context.Context, userID string) ([]string, error) {
	access, err := h.userRepo.AccessForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	errs := make([]string, 0)
	for _, a := range access {
		if _, err := h.nodeManager.SyncNode(ctx, a.NodeID); err != nil {
			errs = append(errs, formatRawNodeSyncError(a.NodeID, err))
		}
	}
	return errs, nil
}

func (h *Handler) syncUserAssignmentsForUI(ctx context.Context, locale Locale, userID string) ([]string, error) {
	errs, err := h.syncUserAssignments(ctx, userID)
	if err != nil {
		return nil, err
	}
	return sanitizeNodeSyncErrors(locale, errs), nil
}

func (h *Handler) syncNodesAfterChange(ctx context.Context, nodeIDs []string) []string {
	seen := map[string]bool{}
	errs := make([]string, 0)
	for _, nodeID := range nodeIDs {
		if nodeID == "" || seen[nodeID] {
			continue
		}
		seen[nodeID] = true
		node, err := h.nodeRepo.Get(ctx, nodeID)
		if err != nil {
			errs = append(errs, formatRawNodeSyncError(nodeID, err))
			continue
		}
		if !node.Enabled {
			continue
		}
		if _, err := h.nodeManager.SyncNode(ctx, nodeID); err != nil {
			errs = append(errs, formatRawNodeSyncError(nodeID, err))
		}
	}
	return errs
}

func (h *Handler) syncNodesAfterChangeForUI(ctx context.Context, locale Locale, nodeIDs []string) []string {
	return sanitizeNodeSyncErrors(locale, h.syncNodesAfterChange(ctx, nodeIDs))
}

func sanitizeNodeSyncErrors(locale Locale, errs []string) []string {
	safe := make([]string, 0, len(errs))
	for _, errText := range errs {
		nodeID, detail, found := strings.Cut(errText, ": ")
		if !found {
			safe = append(safe, safeUIErrorText(locale, errText))
			continue
		}
		safe = append(safe, nodeID+": "+safeUIErrorText(locale, detail))
	}
	return safe
}

func formatRawNodeSyncError(nodeID string, err error) string {
	return nodeID + ": " + rawErrorText(err)
}

func rawErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func httpErrorRaw(w http.ResponseWriter, err error, status int) {
	if err == nil {
		return
	}
	message := err.Error()
	http.Error(w, message, status)
}

func (h *Handler) userNodeIDs(ctx context.Context, userID string) ([]string, error) {
	access, err := h.userRepo.AccessForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	nodeIDs := make([]string, 0, len(access))
	for _, item := range access {
		nodeIDs = append(nodeIDs, item.NodeID)
	}
	return nodeIDs, nil
}

type nodeResponse struct {
	ID                   string                 `json:"id"`
	NodeID               string                 `json:"node_id"`
	Name                 string                 `json:"name"`
	Domain               string                 `json:"domain"`
	APIURL               string                 `json:"api_url"`
	ProtocolType         string                 `json:"protocol_type"`
	NodeSecret           string                 `json:"node_secret"`
	Enabled              bool                   `json:"enabled"`
	SetupState           string                 `json:"setup_state"`
	ProtocolSettings     nodes.ProtocolSettings `json:"protocol_settings"`
	DesiredConfigVersion int64                  `json:"desired_config_version"`
	LastAppliedVersion   int64                  `json:"last_applied_version"`
	LastSeenAt           *time.Time             `json:"last_seen_at,omitempty"`
	LastStatus           *string                `json:"last_status,omitempty"`
	LastError            *string                `json:"last_error,omitempty"`
	LastSyncAt           *time.Time             `json:"last_sync_at,omitempty"`
	CreatedAt            time.Time              `json:"created_at"`
	UpdatedAt            time.Time              `json:"updated_at"`
}

func newNodeResponse(node nodes.Node, exposeRawSecret bool) nodeResponse {
	secret := Mask(node.NodeSecret)
	if exposeRawSecret {
		secret = node.NodeSecret
	}
	return nodeResponse{
		ID:                   node.ID,
		NodeID:               node.NodeID,
		Name:                 node.Name,
		Domain:               node.Domain,
		APIURL:               node.APIURL,
		ProtocolType:         node.ProtocolType,
		NodeSecret:           secret,
		Enabled:              node.Enabled,
		SetupState:           node.SetupState,
		ProtocolSettings:     node.ProtocolSettings,
		DesiredConfigVersion: node.DesiredConfigVersion,
		LastAppliedVersion:   node.LastAppliedVersion,
		LastSeenAt:           node.LastSeenAt,
		LastStatus:           node.LastStatus,
		LastError:            node.LastError,
		LastSyncAt:           node.LastSyncAt,
		CreatedAt:            node.CreatedAt,
		UpdatedAt:            node.UpdatedAt,
	}
}

func localeText(locale Locale, ru, en string) string {
	if NormalizeLocale(string(locale)) == LocaleEN {
		return en
	}
	return ru
}

func flashText(locale Locale, key string) string {
	switch key {
	case "validation_failed":
		return localeText(locale, "Ошибка проверки", "Validation failed")
	case "node_adopted_connected":
		return localeText(locale, "Существующая нода подключена и синхронизирована", "Existing node adopted and connected")
	case "node_updated":
		return localeText(locale, "Нода обновлена", "Node updated")
	case "node_deleted":
		return localeText(locale, "Нода удалена", "Node deleted")
	case "node_not_reachable_yet":
		return localeText(locale, "Нода пока недоступна", "Node is not reachable yet")
	case "health_failed":
		return localeText(locale, "Проверка не удалась", "Health failed")
	case "health_ok":
		return localeText(locale, "Проверка прошла успешно", "Health OK")
	case "status_failed":
		return localeText(locale, "Получение статуса не удалось", "Status failed")
	case "sync_failed":
		return localeText(locale, "Синхронизация не удалась", "Sync failed")
	case "sync_all_failed":
		return localeText(locale, "Синхронизация всех нод не удалась", "Sync all failed")
	case "sync_all_ok":
		return localeText(locale, "Все ноды синхронизированы", "Sync all OK")
	case "invalid_expiration":
		return localeText(locale, "Некорректная дата и время окончания", "Invalid expiration date and time")
	case "user_saved_sync_failed":
		return localeText(locale, "Пользователь сохранён, но синхронизация нод завершилась ошибкой", "User saved, but node sync failed")
	case "user_saved_synced":
		return localeText(locale, "Пользователь сохранён и синхронизирован", "User saved and synced")
	case "user_deleted_sync_failed":
		return localeText(locale, "Пользователь удалён, но синхронизация нод завершилась ошибкой", "User deleted, but node sync failed")
	case "user_deleted_synced":
		return localeText(locale, "Пользователь удалён и синхронизирован", "User deleted and synced")
	case "user_enabled_sync_failed":
		return localeText(locale, "Пользователь включён, но синхронизация нод завершилась ошибкой", "User enabled, but node sync failed")
	case "user_enabled_synced":
		return localeText(locale, "Пользователь включён и синхронизирован", "User enabled and synced")
	case "user_disabled_sync_failed":
		return localeText(locale, "Пользователь отключён, но синхронизация нод завершилась ошибкой", "User disabled, but node sync failed")
	case "user_disabled_synced":
		return localeText(locale, "Пользователь отключён и синхронизирован", "User disabled and synced")
	case "assignment_saved_sync_failed":
		return localeText(locale, "Назначение сохранено, но синхронизация нод завершилась ошибкой", "Assignment saved, but node sync failed")
	case "assignment_saved_synced":
		return localeText(locale, "Назначение сохранено и синхронизировано", "Assignment saved and synced")
	case "unassigned_sync_failed":
		return localeText(locale, "Назначение удалено, но синхронизация нод завершилась ошибкой", "Unassigned, but node sync failed")
	case "node_unassigned_synced":
		return localeText(locale, "Нода отвязана и синхронизирована", "Node unassigned and synced")
	case "node_access_enabled_sync_failed":
		return localeText(locale, "Доступ к ноде включён, но синхронизация завершилась ошибкой", "Node access enabled, but node sync failed")
	case "node_access_enabled_synced":
		return localeText(locale, "Доступ к ноде включён и синхронизирован", "Node access enabled and synced")
	case "node_access_disabled_sync_failed":
		return localeText(locale, "Доступ к ноде отключён, но синхронизация завершилась ошибкой", "Node access disabled, but node sync failed")
	case "node_access_disabled_synced":
		return localeText(locale, "Доступ к ноде отключён и синхронизирован", "Node access disabled and synced")
	case "sync_failed_for_nodes":
		return localeText(locale, "Синхронизация нод завершилась ошибкой", "Sync failed for nodes")
	case "affected_nodes_synced":
		return localeText(locale, "Затронутые ноды синхронизированы", "Affected nodes synced")
	case "expired_reconcile_failed":
		return localeText(locale, "Сверка истёкших подписок завершилась с ошибками", "Expired users reconcile finished with errors")
	case "expired_reconcile_ok":
		return localeText(locale, "Сверка истёкших подписок завершена", "Expired users reconcile OK")
	case "subscription_unlimited":
		return localeText(locale, "Подписка: без ограничений", "Subscription: unlimited")
	case "subscription_expired_at":
		return localeText(locale, "Подписка истекла:", "Subscription: expired at")
	case "subscription_expires_at":
		return localeText(locale, "Срок действия до:", "Expires at:")
	default:
		return key
	}
}

func flashWithList(locale Locale, key string, items []string) string {
	if len(items) == 0 {
		return flashText(locale, key)
	}
	return flashText(locale, key) + ": " + strings.Join(items, "; ")
}

func subscriptionText(locale Locale, key string) string {
	switch key {
	case "group_karing":
		return "Karing"
	case "group_hiddify":
		return "Hiddify"
	case "group_debug":
		return localeText(locale, "Отладка", "Debug")
	case "karing_subscription":
		return localeText(locale, "Подписка Karing", "Karing Subscription")
	case "karing_json":
		return "Karing JSON"
	case "hiddify_subscription":
		return localeText(locale, "Подписка Hiddify", "Hiddify Subscription")
	case "hiddify_json":
		return localeText(locale, "Hiddify JSON (экспериментально)", "Hiddify JSON Experimental")
	case "hiddify_mieru_only":
		return localeText(locale, "Только Hiddify Mieru", "Hiddify Mieru Only")
	case "hiddify_naive_only":
		return localeText(locale, "Только Hiddify Naive", "Hiddify Naive Only")
	case "raw_links":
		return localeText(locale, "Сырые ссылки", "Raw Links")
	default:
		return key
	}
}

func formatNodeStatusFlash(locale Locale, status nodes.AgentStatusResponse) string {
	return Translate(locale, "flash.status_ok") +
		": " + Translate(locale, "status.current_version") + "=" + strconv.FormatInt(status.CurrentVersion, 10) +
		", " + Translate(locale, "status.applied_version") + "=" + strconv.FormatInt(status.AppliedVersion, 10) +
		", " + Translate(locale, "status.users_cached") + "=" + strconv.Itoa(status.UsersCached)
}

func formatSyncFlash(locale Locale, resp nodes.AgentResponse) string {
	return Translate(locale, "flash.sync_ok") +
		": " + Translate(locale, "status.applied_version") + "=" + strconv.FormatInt(resp.AppliedVersion, 10)
}

func formatExpiryReconcileResult(locale Locale, result users.ExpiryReconcileResult) string {
	parts := []string{
		Translate(locale, "reconcile.users_found") + ": " + strconv.Itoa(result.UsersFound),
		Translate(locale, "reconcile.nodes_affected") + ": " + strconv.Itoa(result.NodesAffected),
		Translate(locale, "reconcile.successful_syncs") + ": " + strconv.Itoa(result.SyncSuccessCount),
		Translate(locale, "reconcile.users_processed") + ": " + strconv.Itoa(result.UsersSynced),
	}
	if len(result.Errors) > 0 {
		parts = append(parts, Translate(locale, "reconcile.errors")+": "+strings.Join(sanitizeNodeSyncErrors(locale, result.Errors), "; "))
	}
	return strings.Join(parts, ", ")
}
