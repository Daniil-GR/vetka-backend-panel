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
	"vetka-backend-panel/internal/users"
)

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
}

func New(cfg config.Config, logger *slog.Logger, tmpl *template.Template, nodeRepo *nodes.Repository, nodeManager *nodes.Manager, expiryReconciler *users.ExpiryReconciler, userRepo *users.Repository, userSvc *users.Service, subSvc *subscriptions.Service) *Handler {
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
	}
}

func Mask(secret string) string {
	return security.MaskSecret(secret)
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login.html", h.loginData(r.URL, ""))
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if middleware.Login(h.cfg, w, r) {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
	h.render(w, "login.html", h.loginData(r.URL, "Invalid username or password"))
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
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
		label := strings.ReplaceAll(formatStatusLabel(strings.ReplaceAll(event.Status, "_", " ")), "Http", "HTTP")
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
		statusTone, statusLabel := userStatus(user)
		upcomingItems = append(upcomingItems, userListItem{
			User:              user,
			StatusTone:        statusTone,
			StatusLabel:       statusLabel,
			AssignedNodeCount: 0,
		})
	}

	data := h.pageData(r.URL, "Dashboard", "dashboard")
	data["Breadcrumbs"] = []breadcrumb{{Label: "Dashboard", URL: "/"}}
	data["NodeStats"] = nodeStats
	data["UserStats"] = userStats
	data["NodeItems"] = makeNodeListItems(nodesList, counts)
	data["UpcomingUsers"] = upcomingItems
	data["RecentEvents"] = eventItems
	h.render(w, "dashboard.html", data)
}

func (h *Handler) Nodes(w http.ResponseWriter, r *http.Request) {
	list, err := h.nodeRepo.List(r.Context())
	if h.handleErr(w, err) {
		return
	}
	counts, _ := h.nodeRepo.AssignedUserCounts(r.Context())
	nodeStats, _ := h.nodeRepo.DashboardStats(r.Context(), time.Now().Add(-24*time.Hour))
	data := h.pageData(r.URL, "Nodes", "nodes")
	data["Breadcrumbs"] = []breadcrumb{{Label: "Nodes", URL: "/nodes"}}
	data["NodeItems"] = makeNodeListItems(list, counts)
	data["NodeStats"] = nodeStats
	data["BackendIP"] = h.cfg.BackendPublicIP
	data["DefaultPort"] = h.cfg.NodeAgentDefaultPort
	h.render(w, "nodes.html", data)
}

func (h *Handler) CreateNode(w http.ResponseWriter, r *http.Request) {
	in := nodeInputFromForm(r)
	node, err := h.nodeManager.CreateNode(r.Context(), in)
	if err != nil {
		h.redirectWithErrorFlash(w, r, "/nodes", "", err)
		return
	}
	if in.Mode == nodes.NodeModeAdopt {
		h.redirectWithFlash(w, r, "/nodes", "Existing node adopted and connected", "success")
		return
	}
	data := h.pageData(r.URL, "Node Created", "nodes")
	data["Breadcrumbs"] = []breadcrumb{
		{Label: "Nodes", URL: "/nodes"},
		{Label: "Node Created", URL: ""},
	}
	data["Node"] = node
	data["BackendIP"] = h.cfg.BackendPublicIP
	data["DefaultPort"] = h.cfg.NodeAgentDefaultPort
	h.render(w, "node_created.html", data)
}

func (h *Handler) NodeDetail(w http.ResponseWriter, r *http.Request) {
	node, err := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	assignments, _ := h.userRepo.AccessDetailForNode(r.Context(), node.ID)
	events, _ := h.nodeRepo.RecentEventsByNode(r.Context(), node.ID, 12)

	statusTone, statusLabel := nodeStatusTone(node)
	eventItems := make([]syncEventView, 0, len(events))
	for _, event := range events {
		tone := "success"
		label := formatStatusLabel(strings.ReplaceAll(event.Status, "_", " "))
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

	data := h.pageData(r.URL, node.Name, "nodes")
	data["Breadcrumbs"] = []breadcrumb{
		{Label: "Nodes", URL: "/nodes"},
		{Label: node.Name, URL: ""},
	}
	data["Node"] = node
	data["NodeStatusTone"] = statusTone
	data["NodeStatusLabel"] = statusLabel
	data["ProtocolTone"] = protocolTone(node.ProtocolType)
	data["MaskedSecret"] = Mask(node.NodeSecret)
	data["SafeLastError"] = ""
	if node.LastError != nil {
		data["SafeLastError"] = SafeOperationalError(*node.LastError)
	}
	data["Assignments"] = assignmentViews
	data["Events"] = eventItems
	h.render(w, "node_detail.html", data)
}

func (h *Handler) EditNodePage(w http.ResponseWriter, r *http.Request) {
	node, err := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	data := h.pageData(r.URL, "Edit Node", "nodes")
	data["Breadcrumbs"] = []breadcrumb{
		{Label: "Nodes", URL: "/nodes"},
		{Label: node.Name, URL: "/nodes/" + node.ID},
		{Label: "Edit", URL: ""},
	}
	data["Node"] = node
	h.render(w, "node_edit.html", data)
}

func (h *Handler) ValidateNodeStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.nodeManager.ValidateNodeStatus(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id")+"/edit", "Validation failed: ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id")+"/edit", formatNodeStatusFlash(status), "success")
}

func (h *Handler) UpdateNode(w http.ResponseWriter, r *http.Request) {
	_, err := h.nodeManager.UpdateNode(r.Context(), chi.URLParam(r, "id"), nodeInputFromForm(r))
	if err != nil {
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id")+"/edit", "", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "Node updated", "success")
}

func (h *Handler) DeleteNode(w http.ResponseWriter, r *http.Request) {
	if h.handleErr(w, h.nodeManager.DeleteNode(r.Context(), chi.URLParam(r, "id"))) {
		return
	}
	h.redirectWithFlash(w, r, "/nodes", "Node deleted", "success")
}

func (h *Handler) NodeHealth(w http.ResponseWriter, r *http.Request) {
	node, getErr := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if getErr != nil {
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "", getErr)
		return
	}
	if _, err := h.nodeManager.CheckNodeHealth(r.Context(), chi.URLParam(r, "id")); err != nil {
		if node.SetupState == nodes.SetupStatePlanned {
			h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "Node is not reachable yet", "error")
			return
		}
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "Health failed: ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "Health OK", "success")
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
			h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "Node is not reachable yet", "error")
			return
		}
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "Status failed: ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), formatNodeStatusFlash(status), "success")
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
			h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "Node is not reachable yet", "error")
			return
		}
		h.redirectWithErrorFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), "Sync failed: ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id"), formatSyncFlash(resp), "success")
}

func (h *Handler) SyncAllNodes(w http.ResponseWriter, r *http.Request) {
	if err := h.nodeManager.SyncAllNodes(r.Context()); err != nil {
		h.redirectWithErrorFlash(w, r, "/nodes", "Sync all failed: ", err)
		return
	}
	h.redirectWithFlash(w, r, "/nodes", "Sync all OK", "success")
}

func (h *Handler) Users(w http.ResponseWriter, r *http.Request) {
	list, err := h.userRepo.List(r.Context())
	if h.handleErr(w, err) {
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
		statusTone, statusLabel := userStatus(user)
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

	data := h.pageData(r.URL, "Users", "users")
	data["Breadcrumbs"] = []breadcrumb{{Label: "Users", URL: "/users"}}
	data["UserItems"] = items
	data["Nodes"] = nodesList
	data["Filter"] = filter
	data["Search"] = search
	data["Sort"] = sortMode
	data["UserStats"], _ = h.userRepo.DashboardStats(r.Context(), time.Now().Add(72*time.Hour))
	h.render(w, "users.html", data)
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	nodesList, err := h.nodeRepo.List(r.Context())
	if h.handleErr(w, err) {
		return
	}
	protocols := map[string]string{}
	for _, node := range nodesList {
		protocols[node.ID] = node.ProtocolType
	}
	in, inputErr := h.userInputFromForm(r)
	if inputErr != nil {
		h.redirectWithFlash(w, r, "/users", "Invalid expiration date and time", "error")
		return
	}
	user, err := h.userSvc.CreateUser(r.Context(), in, protocols)
	if h.handleErr(w, err) {
		return
	}
	syncErrors := h.syncNodesAfterChangeForUI(r.Context(), in.NodeIDs)
	if len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+user.ID, "User saved, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+user.ID, "User saved and synced", "success")
}

func (h *Handler) UserDetail(w http.ResponseWriter, r *http.Request) {
	user, err := h.userRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	access, _ := h.userRepo.AccessDetailForUser(r.Context(), user.ID)
	nodesList, _ := h.nodeRepo.List(r.Context())
	base := h.cfg.SubscriptionPublicBaseURL + "/sub/" + user.SubscriptionToken
	statusTone, statusLabel := userStatus(user)
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
	data := h.pageData(r.URL, "User Detail", "users")
	data["Breadcrumbs"] = []breadcrumb{
		{Label: "Users", URL: "/users"},
		{Label: user.Username, URL: ""},
	}
	data["User"] = user
	data["Access"] = accessViews
	data["Nodes"] = nodesList
	data["SubscriptionExpiryText"] = subscriptionExpiryText(user.ExpiresAt, h.appLocation)
	data["UserStatusTone"] = statusTone
	data["UserStatusLabel"] = statusLabel
	data["MaskedToken"] = Mask(user.SubscriptionToken)
	data["AssignedNodeCount"] = len(access)
	hiddifyLinks := []subscriptionLink{
		{Label: "Hiddify Subscription", URL: base + "?format=hiddify", QR: true},
		{Label: "Hiddify JSON Experimental", URL: base + "?format=hiddify-json"},
	}
	if hasDetailedProtocolAccess(access, "mieru") {
		hiddifyLinks = append(hiddifyLinks, subscriptionLink{Label: "Hiddify Mieru Only", URL: base + "?format=mierus"})
	}
	if hasDetailedProtocolAccess(access, "naive") {
		hiddifyLinks = append(hiddifyLinks, subscriptionLink{Label: "Hiddify Naive Only", URL: base + "?format=naive"})
	}
	data["SubscriptionGroups"] = []subscriptionLinkGroup{
		{
			Title: "Karing",
			Links: []subscriptionLink{
				{Label: "Karing Subscription", URL: base, QR: true},
				{Label: "Karing JSON", URL: base + "?format=json"},
			},
		},
		{
			Title: "Hiddify",
			Links: hiddifyLinks,
		},
		{
			Title: "Debug",
			Links: []subscriptionLink{
				{Label: "Raw Links", URL: base + "?format=raw"},
			},
		},
	}
	h.render(w, "user_detail.html", data)
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	nodeIDs, err := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	in, inputErr := h.userInputFromForm(r)
	if inputErr != nil {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Invalid expiration date and time", "error")
		return
	}
	_, err = h.userRepo.Update(r.Context(), chi.URLParam(r, "id"), in)
	if h.handleErr(w, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), nodeIDs); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "User saved, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "User saved and synced", "success")
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	nodeIDs, err := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	if h.handleErr(w, h.userRepo.Delete(r.Context(), chi.URLParam(r, "id"))) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), nodeIDs); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users", "User deleted, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users", "User deleted and synced", "success")
}

func (h *Handler) EnableUser(w http.ResponseWriter, r *http.Request) {
	if err := h.userRepo.SetEnabled(r.Context(), chi.URLParam(r, "id"), true); h.handleErr(w, err) {
		return
	}
	nodeIDs, err := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), nodeIDs); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "User enabled, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "User enabled and synced", "success")
}

func (h *Handler) DisableUser(w http.ResponseWriter, r *http.Request) {
	if err := h.userRepo.SetEnabled(r.Context(), chi.URLParam(r, "id"), false); h.handleErr(w, err) {
		return
	}
	nodeIDs, err := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), nodeIDs); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "User disabled, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "User disabled and synced", "success")
}

func (h *Handler) AssignNode(w http.ResponseWriter, r *http.Request) {
	node, err := h.nodeRepo.Get(r.Context(), r.FormValue("node_id"))
	if h.handleErr(w, err) {
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
	if h.handleErr(w, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), []string{node.ID}); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Assignment saved, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Assignment saved and synced", "success")
}

func (h *Handler) UnassignNode(w http.ResponseWriter, r *http.Request) {
	nodeID := r.FormValue("node_id")
	if err := h.userRepo.UnassignNode(r.Context(), chi.URLParam(r, "id"), nodeID); h.handleErr(w, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), []string{nodeID}); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Unassigned, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Node unassigned and synced", "success")
}

func (h *Handler) EnableUserNodeAccess(w http.ResponseWriter, r *http.Request) {
	access, err := h.userRepo.AccessByID(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "accessID"))
	if h.handleErr(w, err) {
		return
	}
	if err := h.userRepo.SetAccessEnabled(r.Context(), chi.URLParam(r, "id"), access.ID, true); h.handleErr(w, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), []string{access.NodeID}); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Node access enabled, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Node access enabled and synced", "success")
}

func (h *Handler) DisableUserNodeAccess(w http.ResponseWriter, r *http.Request) {
	access, err := h.userRepo.AccessByID(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "accessID"))
	if h.handleErr(w, err) {
		return
	}
	if err := h.userRepo.SetAccessEnabled(r.Context(), chi.URLParam(r, "id"), access.ID, false); h.handleErr(w, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChangeForUI(r.Context(), []string{access.NodeID}); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Node access disabled, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Node access disabled and synced", "success")
}

func (h *Handler) SyncUserNodes(w http.ResponseWriter, r *http.Request) {
	errs, err := h.syncUserAssignmentsForUI(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	if len(errs) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Sync failed for nodes: "+strings.Join(errs, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Affected nodes synced", "success")
}

func (h *Handler) ReconcileExpiredUsers(w http.ResponseWriter, r *http.Request) {
	result, err := h.expiryReconciler.RunOnce(r.Context())
	if err != nil {
		h.redirectWithFlash(w, r, "/users", "Expired users reconcile finished with errors: "+formatExpiryReconcileResult(result), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users", "Expired users reconcile OK: "+formatExpiryReconcileResult(result), "success")
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

func subscriptionExpiryText(expiresAt *time.Time, loc *time.Location) string {
	if expiresAt == nil {
		return "Subscription: unlimited"
	}
	formatted := formatDateTimeValue(expiresAt, loc)
	if expiresAt.Before(time.Now()) {
		return "Subscription: expired at " + formatted
	}
	if expiresAt.Before(time.Now().Add(72 * time.Hour)) {
		return "Expires at: " + formatted + " (expires soon)"
	}
	return "Expires at: " + formatted
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

func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("render template", "template", name, "error", SafeOperationalError(err.Error()))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (h *Handler) handleErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	h.logger.Error("request failed", "error", SafeOperationalError(err.Error()))
	http.Error(w, "Internal server error", http.StatusInternalServerError)
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

func formatDateTimeValue(t *time.Time, loc *time.Location) string {
	if t == nil {
		return "unlimited"
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

func (h *Handler) redirectWithFlash(w http.ResponseWriter, r *http.Request, path, message, level string) {
	values := url.Values{}
	values.Set("flash", message)
	values.Set("level", level)
	http.Redirect(w, r, path+"?"+values.Encode(), http.StatusFound)
}

func safeUIErrorText(value string) string {
	sanitized := SafeOperationalError(value)
	if sanitized == "" {
		return "Operation failed. Check backend logs."
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
	if strings.TrimSpace(sanitized) == "" || sanitized == "***" {
		return "Operation failed. Check backend logs."
	}
	return sanitized
}

func SafeUIError(err error) string {
	if err == nil {
		return "Operation failed. Check backend logs."
	}
	return safeUIErrorText(err.Error())
}

func (h *Handler) redirectWithErrorFlash(w http.ResponseWriter, r *http.Request, path, prefix string, err error) {
	safeErr := SafeUIError(err)
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

func (h *Handler) syncUserAssignmentsForUI(ctx context.Context, userID string) ([]string, error) {
	errs, err := h.syncUserAssignments(ctx, userID)
	if err != nil {
		return nil, err
	}
	return sanitizeNodeSyncErrors(errs), nil
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

func (h *Handler) syncNodesAfterChangeForUI(ctx context.Context, nodeIDs []string) []string {
	return sanitizeNodeSyncErrors(h.syncNodesAfterChange(ctx, nodeIDs))
}

func sanitizeNodeSyncErrors(errs []string) []string {
	safe := make([]string, 0, len(errs))
	for _, errText := range errs {
		nodeID, detail, found := strings.Cut(errText, ": ")
		if !found {
			safe = append(safe, safeUIErrorText(errText))
			continue
		}
		safe = append(safe, nodeID+": "+safeUIErrorText(detail))
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

func formatNodeStatusFlash(status nodes.AgentStatusResponse) string {
	return "Status OK: current_version=" + strconv.FormatInt(status.CurrentVersion, 10) +
		", applied_version=" + strconv.FormatInt(status.AppliedVersion, 10) +
		", users_cached=" + strconv.Itoa(status.UsersCached)
}

func formatSyncFlash(resp nodes.AgentResponse) string {
	return "Sync OK: applied_version=" + strconv.FormatInt(resp.AppliedVersion, 10)
}

func formatExpiryReconcileResult(result users.ExpiryReconcileResult) string {
	parts := []string{
		"users_found=" + strconv.Itoa(result.UsersFound),
		"nodes_affected=" + strconv.Itoa(result.NodesAffected),
		"sync_success_count=" + strconv.Itoa(result.SyncSuccessCount),
		"users_synced=" + strconv.Itoa(result.UsersSynced),
	}
	if len(result.Errors) > 0 {
		parts = append(parts, "errors="+strings.Join(result.Errors, "; "))
	}
	return strings.Join(parts, ", ")
}
