package handlers

import (
	"context"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"vetka-backend-panel/internal/config"
	"vetka-backend-panel/internal/nodes"
	"vetka-backend-panel/internal/security"
	"vetka-backend-panel/internal/subscriptions"
	"vetka-backend-panel/internal/users"
)

type Handler struct {
	cfg         config.Config
	logger      *slog.Logger
	tmpl        *template.Template
	nodeRepo    *nodes.Repository
	nodeManager *nodes.Manager
	userRepo    *users.Repository
	userSvc     *users.Service
	subSvc      *subscriptions.Service
}

func New(cfg config.Config, logger *slog.Logger, tmpl *template.Template, nodeRepo *nodes.Repository, nodeManager *nodes.Manager, userRepo *users.Repository, userSvc *users.Service, subSvc *subscriptions.Service) *Handler {
	return &Handler{cfg: cfg, logger: logger, tmpl: tmpl, nodeRepo: nodeRepo, nodeManager: nodeManager, userRepo: userRepo, userSvc: userSvc, subSvc: subSvc}
}

func Mask(secret string) string {
	return security.MaskSecret(secret)
}

func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login.html", map[string]any{"Title": "Login"})
}

func (h *Handler) Dashboard(w http.ResponseWriter, r *http.Request) {
	totalNodes, onlineNodes, _ := h.nodeRepo.Count(r.Context())
	userCount, _ := h.userRepo.Count(r.Context())
	events, _ := h.nodeRepo.RecentEvents(r.Context(), 12)
	h.render(w, "dashboard.html", map[string]any{
		"Title": "Dashboard", "NodeCount": totalNodes, "UserCount": userCount,
		"OnlineNodes": onlineNodes, "OfflineNodes": totalNodes - onlineNodes, "Events": events,
	})
}

func (h *Handler) Nodes(w http.ResponseWriter, r *http.Request) {
	list, err := h.nodeRepo.List(r.Context())
	if h.handleErr(w, err) {
		return
	}
	h.render(w, "nodes.html", map[string]any{
		"Title":       "Nodes",
		"Nodes":       list,
		"BackendIP":   h.cfg.BackendPublicIP,
		"DefaultPort": h.cfg.NodeAgentDefaultPort,
		"Flash":       r.URL.Query().Get("flash"),
		"FlashLevel":  r.URL.Query().Get("level"),
	})
}

func (h *Handler) CreateNode(w http.ResponseWriter, r *http.Request) {
	in := nodeInputFromForm(r)
	node, err := h.nodeManager.CreateNode(r.Context(), in)
	if err != nil {
		h.redirectWithFlash(w, r, "/nodes", err.Error(), "error")
		return
	}
	if in.Mode == nodes.NodeModeAdopt {
		h.redirectWithFlash(w, r, "/nodes", "Existing node adopted and connected", "success")
		return
	}
	h.render(w, "node_created.html", map[string]any{"Title": "Node created", "Node": node, "BackendIP": h.cfg.BackendPublicIP, "DefaultPort": h.cfg.NodeAgentDefaultPort})
}

func (h *Handler) EditNodePage(w http.ResponseWriter, r *http.Request) {
	node, err := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	h.render(w, "node_edit.html", map[string]any{
		"Title":      "Edit Node",
		"Node":       node,
		"Flash":      r.URL.Query().Get("flash"),
		"FlashLevel": r.URL.Query().Get("level"),
	})
}

func (h *Handler) ValidateNodeStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.nodeManager.ValidateNodeStatus(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id")+"/edit", "Validation failed: "+err.Error(), "error")
		return
	}
	h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id")+"/edit", formatNodeStatusFlash(status), "success")
}

func (h *Handler) UpdateNode(w http.ResponseWriter, r *http.Request) {
	_, err := h.nodeManager.UpdateNode(r.Context(), chi.URLParam(r, "id"), nodeInputFromForm(r))
	if err != nil {
		h.redirectWithFlash(w, r, "/nodes/"+chi.URLParam(r, "id")+"/edit", err.Error(), "error")
		return
	}
	h.redirectWithFlash(w, r, "/nodes", "Node updated", "success")
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
		h.redirectWithFlash(w, r, "/nodes", getErr.Error(), "error")
		return
	}
	if _, err := h.nodeManager.CheckNodeHealth(r.Context(), chi.URLParam(r, "id")); err != nil {
		message := "Health failed: " + err.Error()
		if node.SetupState == nodes.SetupStatePlanned {
			message = "Node is not reachable yet"
		}
		h.redirectWithFlash(w, r, "/nodes", message, "error")
		return
	}
	h.redirectWithFlash(w, r, "/nodes", "Health OK", "success")
}

func (h *Handler) NodeStatus(w http.ResponseWriter, r *http.Request) {
	node, getErr := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if getErr != nil {
		h.redirectWithFlash(w, r, "/nodes", getErr.Error(), "error")
		return
	}
	status, err := h.nodeManager.FetchNodeStatus(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		message := "Status failed: " + err.Error()
		if node.SetupState == nodes.SetupStatePlanned {
			message = "Node is not reachable yet"
		}
		h.redirectWithFlash(w, r, "/nodes", message, "error")
		return
	}
	h.redirectWithFlash(w, r, "/nodes", formatNodeStatusFlash(status), "success")
}

func (h *Handler) SyncNode(w http.ResponseWriter, r *http.Request) {
	node, getErr := h.nodeRepo.Get(r.Context(), chi.URLParam(r, "id"))
	if getErr != nil {
		h.redirectWithFlash(w, r, "/nodes", getErr.Error(), "error")
		return
	}
	resp, err := h.nodeManager.SyncNode(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		message := "Sync failed: " + err.Error()
		if node.SetupState == nodes.SetupStatePlanned {
			message = "Node is not reachable yet"
		}
		h.redirectWithFlash(w, r, "/nodes", message, "error")
		return
	}
	h.redirectWithFlash(w, r, "/nodes", formatSyncFlash(resp), "success")
}

func (h *Handler) SyncAllNodes(w http.ResponseWriter, r *http.Request) {
	if err := h.nodeManager.SyncAllNodes(r.Context()); err != nil {
		h.redirectWithFlash(w, r, "/nodes", "Sync all failed: "+err.Error(), "error")
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
	h.render(w, "users.html", map[string]any{
		"Title":      "Users",
		"Users":      list,
		"Nodes":      nodesList,
		"Flash":      r.URL.Query().Get("flash"),
		"FlashLevel": r.URL.Query().Get("level"),
	})
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
	user, err := h.userSvc.CreateUser(r.Context(), userInputFromForm(r), protocols)
	if h.handleErr(w, err) {
		return
	}
	syncErrors := h.syncNodesAfterChange(r.Context(), userInputFromForm(r).NodeIDs)
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
	access, _ := h.userRepo.AccessForUser(r.Context(), user.ID)
	nodesList, _ := h.nodeRepo.List(r.Context())
	h.render(w, "user_detail.html", map[string]any{
		"Title":                 "User",
		"User":                  user,
		"Access":                access,
		"Nodes":                 nodesList,
		"SubscriptionURL":       h.cfg.PublicBaseURL + "/sub/" + user.SubscriptionToken,
		"SubscriptionJSONURL":   h.cfg.PublicBaseURL + "/sub/" + user.SubscriptionToken + "?format=json",
		"SubscriptionRawURL":    h.cfg.PublicBaseURL + "/sub/" + user.SubscriptionToken + "?format=raw",
		"SubscriptionMierusURL": h.cfg.PublicBaseURL + "/sub/" + user.SubscriptionToken + "?format=mierus",
		"Flash":                 r.URL.Query().Get("flash"),
		"FlashLevel":            r.URL.Query().Get("level"),
	})
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	nodeIDs, err := h.userNodeIDs(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	_, err = h.userRepo.Update(r.Context(), chi.URLParam(r, "id"), userInputFromForm(r))
	if h.handleErr(w, err) {
		return
	}
	if syncErrors := h.syncNodesAfterChange(r.Context(), nodeIDs); len(syncErrors) > 0 {
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
	if syncErrors := h.syncNodesAfterChange(r.Context(), nodeIDs); len(syncErrors) > 0 {
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
	if syncErrors := h.syncNodesAfterChange(r.Context(), nodeIDs); len(syncErrors) > 0 {
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
	if syncErrors := h.syncNodesAfterChange(r.Context(), nodeIDs); len(syncErrors) > 0 {
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
	if syncErrors := h.syncNodesAfterChange(r.Context(), []string{node.ID}); len(syncErrors) > 0 {
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
	if syncErrors := h.syncNodesAfterChange(r.Context(), []string{nodeID}); len(syncErrors) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Unassigned, but sync failed for nodes: "+strings.Join(syncErrors, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Node unassigned and synced", "success")
}

func (h *Handler) SyncUserNodes(w http.ResponseWriter, r *http.Request) {
	errs, err := h.syncUserAssignments(r.Context(), chi.URLParam(r, "id"))
	if h.handleErr(w, err) {
		return
	}
	if len(errs) > 0 {
		h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Sync failed for nodes: "+strings.Join(errs, "; "), "error")
		return
	}
	h.redirectWithFlash(w, r, "/users/"+chi.URLParam(r, "id"), "Affected nodes synced", "success")
}

func (h *Handler) Subscription(w http.ResponseWriter, r *http.Request) {
	body, contentType, err := h.subSvc.BuildByToken(r.Context(), chi.URLParam(r, "token"), r.URL.Query().Get("format"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
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
	base := h.cfg.PublicBaseURL + "/sub/" + user.SubscriptionToken
	writeJSONOrError(w, http.StatusOK, map[string]string{
		"url":         base,
		"json_url":    base + "?format=json",
		"karing_url":  base + "?format=karing",
		"raw_url":     base + "?format=raw",
		"mierus_url":  base + "?format=mierus",
		"singbox_url": base + "?format=sing-box",
	}, nil)
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

func (h *Handler) render(w http.ResponseWriter, name string, data map[string]any) {
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("render template", "template", name, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) handleErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	h.logger.Error("request failed", "error", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
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

func userInputFromForm(r *http.Request) users.CreateUserInput {
	return users.CreateUserInput{
		Username:    r.FormValue("username"),
		DisplayName: stringPtr(r.FormValue("display_name")),
		Enabled:     boolFromForm(r, "enabled", true),
		ExpiresAt:   parseDate(r.FormValue("expires_at")),
		Notes:       stringPtr(r.FormValue("notes")),
		NodeIDs:     r.Form["node_ids"],
	}
}

func boolFromForm(r *http.Request, key string, fallback bool) bool {
	value := r.FormValue(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return value == "on"
	}
	return parsed
}

func parseDate(value string) *time.Time {
	if value == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil
	}
	return &t
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
		http.Error(w, err.Error(), http.StatusBadRequest)
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

func (h *Handler) redirectWithFlash(w http.ResponseWriter, r *http.Request, path, message, level string) {
	values := url.Values{}
	values.Set("flash", message)
	values.Set("level", level)
	http.Redirect(w, r, path+"?"+values.Encode(), http.StatusFound)
}

func (h *Handler) syncUserAssignments(ctx context.Context, userID string) ([]string, error) {
	access, err := h.userRepo.AccessForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	errs := make([]string, 0)
	for _, a := range access {
		if _, err := h.nodeManager.SyncNode(ctx, a.NodeID); err != nil {
			errs = append(errs, a.NodeID+": "+err.Error())
		}
	}
	return errs, nil
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
			errs = append(errs, nodeID+": "+err.Error())
			continue
		}
		if !node.Enabled {
			continue
		}
		if _, err := h.nodeManager.SyncNode(ctx, nodeID); err != nil {
			errs = append(errs, nodeID+": "+err.Error())
		}
	}
	return errs
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
