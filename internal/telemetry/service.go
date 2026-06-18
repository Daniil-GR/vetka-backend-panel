package telemetry

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"vetka-backend-panel/internal/nodes"
	"vetka-backend-panel/internal/security"
	"vetka-backend-panel/internal/users"
)

const (
	defaultConcurrency = 6
	pageTimeout        = 10 * time.Second
)

type nodeReader interface {
	List(context.Context) ([]nodes.Node, error)
	Get(context.Context, string) (nodes.Node, error)
}

type userReader interface {
	List(context.Context) ([]users.User, error)
	Get(context.Context, string) (users.User, error)
	SessionLookupForUser(context.Context, string) ([]users.SessionLookup, error)
	SessionLookupForNodes(context.Context, []string) ([]users.SessionLookup, error)
}

type agentTelemetryFetcher interface {
	TelemetrySessions(context.Context, nodes.Node, bool) (nodes.TelemetryResponse, nodes.AgentCallResult, error)
}

type Service struct {
	nodeRepo    nodeReader
	userRepo    userReader
	agent       agentTelemetryFetcher
	concurrency int
	pageTimeout time.Duration
}

func NewService(nodeRepo nodeReader, userRepo userReader, agent agentTelemetryFetcher) *Service {
	return &Service{
		nodeRepo:    nodeRepo,
		userRepo:    userRepo,
		agent:       agent,
		concurrency: defaultConcurrency,
		pageTimeout: pageTimeout,
	}
}

type sessionResolution int

const (
	sessionExcluded sessionResolution = iota
	sessionUnknown
	sessionMatched
)

type fallbackIndex map[string]map[string][]string

func (s *Service) NodeSessions(ctx context.Context, nodeID string, includeRecent bool) (NodeSessionsResult, error) {
	node, err := s.nodeRepo.Get(ctx, nodeID)
	if err != nil {
		return NodeSessionsResult{}, err
	}
	usersList, err := s.userRepo.List(ctx)
	if err != nil {
		return NodeSessionsResult{}, err
	}
	userMap := make(map[string]users.User, len(usersList))
	for _, user := range usersList {
		userMap[user.ID] = user
	}
	lookups, err := s.userRepo.SessionLookupForNodes(ctx, []string{node.ID})
	if err != nil {
		return NodeSessionsResult{}, err
	}
	view := s.fetchNodeView(ctx, node, includeRecent, userMap, buildFallbackIndex(lookups), false)
	sortSessions(view.Sessions)
	return NodeSessionsResult{
		Node:          view,
		IncludeRecent: includeRecent,
	}, nil
}

func (s *Service) UserSessions(ctx context.Context, userID string, includeRecent bool) (UserSessionsResult, error) {
	_, err := s.userRepo.Get(ctx, userID)
	if err != nil {
		return UserSessionsResult{}, err
	}
	lookups, err := s.userRepo.SessionLookupForUser(ctx, userID)
	if err != nil {
		return UserSessionsResult{}, err
	}

	nodesList, err := s.nodeRepo.List(ctx)
	if err != nil {
		return UserSessionsResult{}, err
	}
	nodeMap := make(map[string]nodes.Node, len(nodesList))
	for _, node := range nodesList {
		nodeMap[node.ID] = node
	}

	targetNodes := make([]nodes.Node, 0, len(lookups))
	seenNode := map[string]bool{}
	for _, item := range lookups {
		if seenNode[item.NodeID] {
			continue
		}
		node, ok := nodeMap[item.NodeID]
		if !ok {
			continue
		}
		targetNodes = append(targetNodes, node)
		seenNode[item.NodeID] = true
	}
	targetNodeIDs := collectNodeIDs(targetNodes)
	nodeLookups, err := s.userRepo.SessionLookupForNodes(ctx, targetNodeIDs)
	if err != nil {
		return UserSessionsResult{}, err
	}

	userRecord, err := s.userRepo.Get(ctx, userID)
	if err != nil {
		return UserSessionsResult{}, err
	}
	userMap := map[string]users.User{userRecord.ID: userRecord}
	nodeViews := s.fetchNodes(ctx, targetNodes, includeRecent, userMap, buildFallbackIndex(nodeLookups), true)

	rows := make([]SessionView, 0)
	for _, nodeView := range nodeViews {
		rows = append(rows, nodeView.Sessions...)
	}
	sortSessions(rows)

	return UserSessionsResult{
		Rows:          rows,
		Nodes:         nodeViews,
		Summary:       buildSummary(rows, nodeViews),
		IncludeRecent: includeRecent,
	}, nil
}

func (s *Service) AllSessions(ctx context.Context, query Query) (AllSessionsResult, error) {
	nodesList, err := s.nodeRepo.List(ctx)
	if err != nil {
		return AllSessionsResult{}, err
	}
	usersList, err := s.userRepo.List(ctx)
	if err != nil {
		return AllSessionsResult{}, err
	}
	userMap := make(map[string]users.User, len(usersList))
	for _, user := range usersList {
		userMap[user.ID] = user
	}
	nodeIDs := collectNodeIDs(nodesList)
	lookups, err := s.userRepo.SessionLookupForNodes(ctx, nodeIDs)
	if err != nil {
		return AllSessionsResult{}, err
	}

	nodeViews := s.fetchNodes(ctx, nodesList, query.IncludeRecent, userMap, buildFallbackIndex(lookups), false)
	rows := make([]SessionView, 0)
	for _, nodeView := range nodeViews {
		rows = append(rows, nodeView.Sessions...)
	}
	rows = filterSessions(rows, query)
	sortSessions(rows)

	return AllSessionsResult{
		Rows:          rows,
		Nodes:         nodeViews,
		Summary:       buildSummary(rows, nodeViews),
		IncludeRecent: query.IncludeRecent,
	}, nil
}

func (s *Service) fetchNodes(ctx context.Context, nodeList []nodes.Node, includeRecent bool, userMap map[string]users.User, assignments fallbackIndex, strictUserScope bool) []NodeCollectorView {
	timeout := s.pageTimeout
	if timeout <= 0 {
		timeout = pageTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	results := make([]NodeCollectorView, len(nodeList))
	if len(nodeList) == 0 {
		return results[:0]
	}

	type job struct {
		index int
		node  nodes.Node
	}

	semSize := s.concurrency
	if semSize <= 0 {
		semSize = defaultConcurrency
	}
	if semSize > len(nodeList) {
		semSize = len(nodeList)
	}

	jobs := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < semSize; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				results[item.index] = s.fetchNodeView(ctx, item.node, includeRecent, userMap, assignments, strictUserScope)
			}
		}()
	}

	for i, node := range nodeList {
		select {
		case <-ctx.Done():
			results[i] = NodeCollectorView{
				NodeDBID:        node.ID,
				NodeID:          node.NodeID,
				NodeName:        node.Name,
				NodeProtocol:    node.ProtocolType,
				NodeEnabled:     node.Enabled,
				CollectorStatus: "unavailable",
				Error:           "telemetry request canceled",
			}
		case jobs <- job{index: i, node: node}:
		}
	}
	close(jobs)
	wg.Wait()

	return results
}

func (s *Service) fetchNodeView(ctx context.Context, node nodes.Node, includeRecent bool, userMap map[string]users.User, assignments fallbackIndex, strictUserScope bool) NodeCollectorView {
	base := NodeCollectorView{
		NodeDBID:     node.ID,
		NodeID:       node.NodeID,
		NodeName:     node.Name,
		NodeProtocol: node.ProtocolType,
		NodeEnabled:  node.Enabled,
	}

	if skip, reason := skipNode(node); skip {
		base.CollectorStatus = "disabled"
		base.SkippedReason = reason
		base.ConfigurationIssue = configurationIssueForReason(reason)
		return base
	}

	response, _, err := s.agent.TelemetrySessions(ctx, node, includeRecent)
	if err != nil {
		base.CollectorStatus = "unavailable"
		base.Error = err.Error()
		return base
	}

	base.CollectorStatus = normalizedCollectorStatus(response.CollectorStatus)
	base.LastSuccessfulCollectionAt = response.LastSuccessfulCollectionAt
	base.Capabilities = response.Capabilities
	base.Warnings = append([]string(nil), response.Warnings...)
	base.Sessions = make([]SessionView, 0, len(response.Sessions))
	for _, session := range response.Sessions {
		matched, resolution := resolveSessionUser(node, session, userMap, assignments, strictUserScope)
		switch resolution {
		case sessionMatched:
			base.Sessions = append(base.Sessions, buildSessionView(node, session, matched))
		case sessionUnknown:
			base.Sessions = append(base.Sessions, buildSessionView(node, session, nil))
		}
	}
	return base
}

func skipNode(node nodes.Node) (bool, string) {
	if node.SetupState == nodes.SetupStatePlanned {
		return true, "planned"
	}
	if strings.TrimSpace(node.APIURL) == "" {
		return true, "missing_api_url"
	}
	if strings.TrimSpace(node.NodeID) == "" {
		return true, "missing_node_id"
	}
	if strings.TrimSpace(node.NodeSecret) == "" {
		return true, "missing_node_secret"
	}
	return false, ""
}

func normalizedCollectorStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ok":
		return "ok"
	case "partial":
		return "partial"
	case "disabled":
		return "disabled"
	default:
		return "unavailable"
	}
}

func buildSessionView(node nodes.Node, session nodes.TelemetrySession, matchedUser *users.User) SessionView {
	view := SessionView{
		NodeDBID:               node.ID,
		NodeID:                 node.NodeID,
		NodeName:               node.Name,
		NodeProtocol:           node.ProtocolType,
		MaskedProtocolUsername: security.MaskSecret(session.ProtocolUsername),
		ClientIP:               strings.TrimSpace(ptrString(session.ClientIP)),
		Active:                 session.Active,
		FirstSeenAt:            session.FirstSeenAt,
		LastSeenAt:             session.LastSeenAt,
		UploadBytes:            session.UploadBytes,
		DownloadBytes:          session.DownloadBytes,
		Source:                 session.Source,
		TrafficScope:           session.TrafficScope,
		UserPresentInCache:     session.UserPresentInCache,
		IPObserved:             session.IPObserved,
		TrafficObserved:        session.TrafficObserved,
		searchProtocolUsername: strings.ToLower(strings.TrimSpace(session.ProtocolUsername)),
	}
	if matchedUser != nil {
		view.BackendUserID = matchedUser.ID
		view.BackendUsername = matchedUser.Username
		view.BackendDisplayName = matchedUser.DisplayName
		view.UserKnown = true
		if matchedUser.DisplayName != nil {
			view.searchBackendDisplay = strings.ToLower(strings.TrimSpace(*matchedUser.DisplayName))
		}
	}
	return view
}

func resolveSessionUser(node nodes.Node, session nodes.TelemetrySession, userMap map[string]users.User, assignments fallbackIndex, strictUserScope bool) (*users.User, sessionResolution) {
	if session.UserID != "" {
		user, ok := userMap[session.UserID]
		if !ok {
			if strictUserScope {
				return nil, sessionExcluded
			}
			return nil, sessionUnknown
		}
		return &user, sessionMatched
	}

	nodeAssignments := assignments[node.ID]
	if len(nodeAssignments) == 0 {
		if strictUserScope {
			return nil, sessionExcluded
		}
		return nil, sessionUnknown
	}
	userIDs := nodeAssignments[session.ProtocolUsername]
	if len(userIDs) != 1 {
		if strictUserScope {
			return nil, sessionExcluded
		}
		return nil, sessionUnknown
	}
	user, ok := userMap[userIDs[0]]
	if !ok {
		if strictUserScope {
			return nil, sessionExcluded
		}
		return nil, sessionUnknown
	}
	return &user, sessionMatched
}

func filterSessions(rows []SessionView, query Query) []SessionView {
	search := strings.ToLower(strings.TrimSpace(query.Search))
	protocol := strings.ToLower(strings.TrimSpace(query.Protocol))
	status := strings.ToLower(strings.TrimSpace(query.Status))
	if status == "" {
		status = "active"
	}

	filtered := make([]SessionView, 0, len(rows))
	for _, row := range rows {
		if protocol != "" && protocol != "all" && strings.ToLower(row.NodeProtocol) != protocol {
			continue
		}
		switch status {
		case "active":
			if !row.Active {
				continue
			}
		case "recent":
			if row.Active {
				continue
			}
		case "all":
		default:
			if !row.Active {
				continue
			}
		}
		if search != "" && !matchesSearch(row, search) {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func matchesSearch(row SessionView, search string) bool {
	values := []string{
		strings.ToLower(row.BackendUsername),
		strings.ToLower(row.searchBackendDisplay),
		strings.ToLower(row.NodeName),
		strings.ToLower(row.ClientIP),
		strings.ToLower(row.searchProtocolUsername),
	}
	for _, value := range values {
		if strings.Contains(value, search) {
			return true
		}
	}
	return false
}

func buildSummary(rows []SessionView, nodeViews []NodeCollectorView) Summary {
	uniqueActiveIPs := map[string]struct{}{}
	summary := Summary{}
	for _, row := range rows {
		if row.Active {
			summary.ActiveSessions++
			if row.ClientIP != "" {
				uniqueActiveIPs[row.ClientIP] = struct{}{}
			}
		}
	}
	for _, nodeView := range nodeViews {
		if NodeHasIssue(nodeView) {
			summary.CollectorsIssues++
			continue
		}
		if nodeView.CollectorStatus == "ok" {
			summary.CollectorsOK++
		}
	}
	summary.UniqueActiveIPs = len(uniqueActiveIPs)
	return summary
}

func NodeHasIssue(node NodeCollectorView) bool {
	return NodeIssueCode(node) != ""
}

func NodeIssueCode(node NodeCollectorView) string {
	switch node.SkippedReason {
	case "planned":
		return ""
	case "missing_api_url", "missing_node_id", "missing_node_secret":
		return node.SkippedReason
	}
	if strings.TrimSpace(node.Error) != "" {
		return "collector_unavailable"
	}
	if len(normalizedWarnings(node.Warnings)) > 0 {
		return "collector_warning"
	}
	if strings.TrimSpace(node.ConfigurationIssue) != "" {
		return node.ConfigurationIssue
	}
	switch strings.ToLower(strings.TrimSpace(node.CollectorStatus)) {
	case "partial":
		return "collector_partial"
	case "unavailable":
		return "collector_unavailable"
	case "disabled":
		return "collector_disabled"
	default:
		return ""
	}
}

func normalizedWarnings(warnings []string) []string {
	result := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		trimmed := strings.TrimSpace(warning)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

func configurationIssueForReason(reason string) string {
	switch reason {
	case "missing_api_url":
		return "missing_api_url"
	case "missing_node_id":
		return "missing_node_id"
	case "missing_node_secret":
		return "missing_node_secret"
	default:
		return ""
	}
}

func sortSessions(rows []SessionView) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Active != rows[j].Active {
			return rows[i].Active
		}

		leftLast := rows[i].LastSeenAt
		rightLast := rows[j].LastSeenAt
		switch {
		case leftLast == nil && rightLast != nil:
			return false
		case leftLast != nil && rightLast == nil:
			return true
		case leftLast != nil && rightLast != nil && !leftLast.Equal(*rightLast):
			return leftLast.After(*rightLast)
		}

		if rows[i].NodeName != rows[j].NodeName {
			return rows[i].NodeName < rows[j].NodeName
		}

		leftUser := includesText(rows[i].BackendUsername, rows[i].MaskedProtocolUsername)
		rightUser := includesText(rows[j].BackendUsername, rows[j].MaskedProtocolUsername)
		return leftUser < rightUser
	})
}

func includesText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ptrString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func collectNodeIDs(nodeList []nodes.Node) []string {
	result := make([]string, 0, len(nodeList))
	for _, node := range nodeList {
		result = append(result, node.ID)
	}
	return result
}

func buildFallbackIndex(lookups []users.SessionLookup) fallbackIndex {
	index := make(fallbackIndex)
	for _, item := range lookups {
		nodeItems, ok := index[item.NodeID]
		if !ok {
			nodeItems = make(map[string][]string)
			index[item.NodeID] = nodeItems
		}
		existing := nodeItems[item.ProtocolUsername]
		for _, userID := range existing {
			if userID == item.UserID {
				goto next
			}
		}
		nodeItems[item.ProtocolUsername] = append(existing, item.UserID)
	next:
	}
	return index
}
