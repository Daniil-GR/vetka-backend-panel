package handlers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"vetka-backend-panel/internal/nodes"
	"vetka-backend-panel/internal/telemetry"
	"vetka-backend-panel/internal/users"
)

type breadcrumb struct {
	Label string
	URL   string
}

type navItem struct {
	Label  string
	URL    string
	Key    string
	Active bool
}

type toastMessage struct {
	Level string
	Text  string
}

type viewData = map[string]any

func newViewData(title, nav string) viewData {
	return viewData{
		"Title":       title,
		"ActiveNav":   nav,
		"NavItems":    navItems(nav),
		"Environment": "",
		"Breadcrumbs": []breadcrumb{},
		"FlashItems":  []toastMessage{},
	}
}

func navItems(active string) []navItem {
	items := []navItem{
		{URL: "/", Key: "dashboard"},
		{URL: "/users", Key: "users"},
		{URL: "/nodes", Key: "nodes"},
		{URL: "/sessions", Key: "sessions"},
	}
	for i := range items {
		items[i].Active = items[i].Key == active
	}
	return items
}

func flashFromQuery(values url.Values) []toastMessage {
	text := strings.TrimSpace(values.Get("flash"))
	if text == "" {
		return nil
	}
	level := strings.TrimSpace(values.Get("level"))
	if level == "" {
		level = "info"
	}
	return []toastMessage{{Level: level, Text: text}}
}

func IsUserExpired(expiresAt *time.Time) bool {
	return expiresAt != nil && !expiresAt.After(time.Now())
}

func IsUserExpiringSoon(expiresAt *time.Time) bool {
	if expiresAt == nil {
		return false
	}
	now := time.Now()
	return expiresAt.After(now) && expiresAt.Before(now.Add(72*time.Hour))
}

func userStatus(locale Locale, user users.User) (string, string) {
	switch {
	case !user.Enabled:
		return "disabled", Translate(locale, "status.disabled")
	case IsUserExpired(user.ExpiresAt):
		return "expired", Translate(locale, "status.expired")
	case IsUserExpiringSoon(user.ExpiresAt):
		return "warning", Translate(locale, "status.expires_soon")
	default:
		return "success", Translate(locale, "status.active")
	}
}

func nodeStatusTone(locale Locale, node nodes.Node) (string, string) {
	if !node.Enabled || node.SetupState == nodes.SetupStateDisabled {
		return "disabled", Translate(locale, "status.disabled")
	}
	switch node.SetupState {
	case nodes.SetupStateConnected:
		return "success", Translate(locale, "status.connected")
	case nodes.SetupStateUnreachable:
		return "danger", Translate(locale, "status.unreachable")
	case nodes.SetupStatePlanned:
		return "warning", Translate(locale, "status.planned")
	default:
		return "muted", formatStatusLabel(node.SetupState)
	}
}

func protocolTone(protocol string) string {
	switch strings.ToLower(protocol) {
	case "mieru":
		return "protocol-mieru"
	case "naive":
		return "protocol-naive"
	default:
		return "muted"
	}
}

func collectorStatusTone(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ok":
		return "success"
	case "partial":
		return "warning"
	case "disabled":
		return "disabled"
	default:
		return "danger"
	}
}

func collectorStatusLabel(locale Locale, status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ok":
		return Translate(locale, "sessions.collector_ok")
	case "partial":
		return Translate(locale, "sessions.collector_partial")
	case "disabled":
		return Translate(locale, "sessions.collector_disabled")
	default:
		return Translate(locale, "sessions.collector_unavailable")
	}
}

func TimeRemaining(locale Locale, expiresAt *time.Time) string {
	return timeRemainingAt(locale, expiresAt, time.Now())
}

func timeRemainingAt(locale Locale, expiresAt *time.Time, now time.Time) string {
	if expiresAt == nil {
		return Translate(locale, "status.unlimited")
	}
	if !expiresAt.After(now) {
		return Translate(locale, "status.expired")
	}

	duration := expiresAt.Sub(now).Round(time.Minute)
	if duration < time.Minute {
		duration = time.Minute
	}

	if duration < 24*time.Hour {
		totalMinutes := int(duration / time.Minute)
		hours := totalMinutes / 60
		minutes := totalMinutes % 60
		if NormalizeLocale(string(locale)) == LocaleRU {
			switch {
			case hours > 0 && minutes > 0:
				return fmt.Sprintf("Осталось %d ч %d мин", hours, minutes)
			case hours > 0:
				return fmt.Sprintf("Осталось %d ч", hours)
			default:
				return fmt.Sprintf("Осталось %d мин", minutes)
			}
		}
		switch {
		case hours > 0 && minutes > 0:
			return fmt.Sprintf("%dh %dm remaining", hours, minutes)
		case hours > 0:
			return fmt.Sprintf("%dh remaining", hours)
		default:
			return fmt.Sprintf("%dm remaining", minutes)
		}
	}

	days := int(duration.Hours()) / 24
	hours := int(duration.Hours()) % 24
	if NormalizeLocale(string(locale)) == LocaleRU {
		return fmt.Sprintf("Осталось %d д %d ч", days, hours)
	}
	return fmt.Sprintf("%dd %dh remaining", days, hours)
}

func MaskSecretCompact(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 4 {
		return strings.Repeat("•", len(runes))
	}
	if len(runes) <= 8 {
		return string(runes[:2]) + strings.Repeat("•", 4) + string(runes[len(runes)-2:])
	}
	return string(runes[:4]) + strings.Repeat("•", 8) + string(runes[len(runes)-4:])
}

func TruncateText(value string, size int) string {
	runes := []rune(value)
	if len(runes) <= size || size <= 3 {
		return value
	}
	return string(runes[:size-1]) + "..."
}

func SafeJSONPreview(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}

	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "[invalid response JSON]"
	}
	safeJSON, err := json.Marshal(redactSecrets(decoded))
	if err != nil {
		return "[invalid response JSON]"
	}
	return string(safeJSON)
}

var (
	sensitiveTextPattern = regexp.MustCompile(`(?i)(["']?(?:password|protocol_password|protocol_username|node_secret|nodeSecret|secret|token|subscription_token|admin_api_token|authorization)["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
	authHeaderPattern    = regexp.MustCompile(`(?i)(["']?authorization["']?\s*[:=]\s*)bearer\s+[^\s,;]+`)
	bearerTokenPattern   = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;]+`)
)

func redactSecrets(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if isSensitiveJSONKey(key) {
				result[key] = "***"
				continue
			}
			result[key] = redactSecrets(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactSecrets(item)
		}
		return result
	default:
		return value
	}
}

func isSensitiveJSONKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "password", "protocol_password", "protocol_username", "node_secret", "nodesecret", "secret", "token", "subscription_token", "admin_api_token", "authorization":
		return true
	default:
		return false
	}
}

func SafeOperationalError(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	var decoded any
	if json.Unmarshal([]byte(trimmed), &decoded) == nil {
		safeJSON, err := json.Marshal(redactSecrets(decoded))
		if err != nil {
			return "[redacted operational error]"
		}
		return string(safeJSON)
	}

	sanitized := authHeaderPattern.ReplaceAllString(trimmed, `${1}***`)
	sanitized = bearerTokenPattern.ReplaceAllString(sanitized, "Bearer ***")
	sanitized = sensitiveTextPattern.ReplaceAllStringFunc(sanitized, func(match string) string {
		parts := sensitiveTextPattern.FindStringSubmatch(match)
		if len(parts) != 3 {
			return "[redacted operational error]"
		}
		redactedValue := "***"
		if strings.HasPrefix(parts[2], `"`) && strings.HasSuffix(parts[2], `"`) {
			redactedValue = `"***"`
		} else if strings.HasPrefix(parts[2], `'`) && strings.HasSuffix(parts[2], `'`) {
			redactedValue = `'***'`
		}
		return parts[1] + redactedValue
	})

	if sanitized == trimmed && containsSensitiveMarker(trimmed) {
		return "[redacted operational error]"
	}
	return sanitized
}

func containsSensitiveMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, key := range []string{
		"password",
		"protocol_password",
		"protocol_username",
		"node_secret",
		"nodesecret",
		"secret",
		"token",
		"subscription_token",
		"admin_api_token",
		"authorization",
		"bearer ",
	} {
		if strings.Contains(lower, key) {
			return true
		}
	}
	return false
}

func sortUserViews(items []userListItem, mode string) {
	switch mode {
	case "expires_at":
		sort.SliceStable(items, func(i, j int) bool {
			left := items[i].User.ExpiresAt
			right := items[j].User.ExpiresAt
			switch {
			case left == nil && right == nil:
				return items[i].User.Username < items[j].User.Username
			case left == nil:
				return false
			case right == nil:
				return true
			default:
				return left.Before(*right)
			}
		})
	case "created_at":
		sort.SliceStable(items, func(i, j int) bool {
			return items[i].User.CreatedAt.After(items[j].User.CreatedAt)
		})
	default:
		sort.SliceStable(items, func(i, j int) bool {
			return strings.ToLower(items[i].User.Username) < strings.ToLower(items[j].User.Username)
		})
	}
}

func includesText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func hasDetailedProtocolAccess(access []users.UserNodeAccessDetail, protocol string) bool {
	for _, item := range access {
		if item.NodeProtocolType == protocol && item.Enabled && item.NodeEnabled {
			return true
		}
	}
	return false
}

func formatStatusLabel(status string) string {
	if status == "" {
		return ""
	}
	parts := strings.Split(strings.ReplaceAll(status, "-", "_"), "_")
	for i, part := range parts {
		if part == "" {
			continue
		}
		runes := []rune(strings.ToLower(part))
		if len(runes) == 0 {
			continue
		}
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ")
}

func localizedStatusLabel(locale Locale, status string) string {
	key := "status." + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(status, "-", "_"), " ", "_"))
	translated := Translate(locale, key)
	if translated != key {
		return translated
	}
	return formatStatusLabel(status)
}

func LocalizedStatusLabel(locale Locale, status string) string {
	return localizedStatusLabel(locale, status)
}

func FormatTimeForLocale(locale Locale, t any, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	layout := "2006-01-02 15:04"
	if NormalizeLocale(string(locale)) == LocaleRU {
		layout = "02.01.2006 15:04"
	}
	switch value := t.(type) {
	case time.Time:
		return value.In(loc).Format(layout)
	case *time.Time:
		if value == nil {
			return ""
		}
		return value.In(loc).Format(layout)
	default:
		return ""
	}
}

func FormatDateTimeForLocale(locale Locale, t *time.Time, loc *time.Location) string {
	if t == nil {
		return Translate(locale, "status.unlimited")
	}
	if loc == nil {
		loc = time.UTC
	}
	layout := "2006-01-02 15:04"
	if NormalizeLocale(string(locale)) == LocaleRU {
		layout = "02.01.2006 15:04"
	}
	return t.In(loc).Format(layout)
}

func FormatDateTimeWithZoneForLocale(locale Locale, t *time.Time, loc *time.Location) string {
	if t == nil {
		return Translate(locale, "status.unlimited")
	}
	if loc == nil {
		loc = time.UTC
	}
	layout := "2006-01-02 15:04 MST"
	if NormalizeLocale(string(locale)) == LocaleRU {
		layout = "02.01.2006 15:04 MST"
	}
	return t.In(loc).Format(layout)
}

func FormatBytesIEC(value int64) string {
	if value < 0 {
		return "0 B"
	}
	const unit = 1024
	if value < unit {
		return strconv.FormatInt(value, 10) + " B"
	}
	divisor := float64(unit)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for i, label := range units {
		next := divisor * unit
		if float64(value) < next || i == len(units)-1 {
			return fmt.Sprintf("%.1f %s", float64(value)/divisor, label)
		}
		divisor = next
	}
	return strconv.FormatInt(value, 10) + " B"
}

func matchesUserFilter(item userListItem, filter, search string) bool {
	filter = strings.TrimSpace(strings.ToLower(filter))
	search = strings.TrimSpace(strings.ToLower(search))
	if search != "" {
		display := ""
		if item.User.DisplayName != nil {
			display = strings.ToLower(*item.User.DisplayName)
		}
		if !strings.Contains(strings.ToLower(item.User.Username), search) && !strings.Contains(display, search) {
			return false
		}
	}
	switch filter {
	case "", "all":
		return true
	case "active":
		return item.StatusTone == "success"
	case "expired":
		return item.StatusTone == "expired"
	case "disabled":
		return item.StatusTone == "disabled"
	case "expires-soon":
		return item.StatusTone == "warning"
	default:
		return true
	}
}

type userListItem struct {
	User              users.User
	StatusTone        string
	StatusLabel       string
	AssignedNodeCount int
}

type nodeListItem struct {
	Node              nodes.Node
	StatusTone        string
	StatusLabel       string
	ProtocolTone      string
	AssignedUserCount int
	LastErrorPreview  string
}

type syncEventView struct {
	Event           nodes.SyncEvent
	NodeName        string
	StatusTone      string
	StatusLabel     string
	ErrorPreview    string
	SafeError       string
	ResponsePreview string
}

type subscriptionLink struct {
	Label string
	URL   string
	QR    bool
}

type subscriptionLinkGroup struct {
	Title string
	Links []subscriptionLink
}

type assignmentView struct {
	ID                     string
	UserID                 string
	NodeID                 string
	NodeName               string
	NodeSetupState         string
	NodeProtocolType       string
	NodeEnabled            bool
	Enabled                bool
	MaskedProtocolUsername string
	MaskedProtocolPassword string
}

type nodeAssignmentView struct {
	UserID                 string
	Username               string
	DisplayName            *string
	UserEnabled            bool
	UserExpiresAt          *time.Time
	Enabled                bool
	MaskedProtocolUsername string
	MaskedProtocolPassword string
}

type telemetrySessionRowView struct {
	NodeDBID               string
	NodeName               string
	NodeProtocol           string
	BackendUserID          string
	BackendUsername        string
	BackendDisplayName     *string
	UserKnown              bool
	MaskedProtocolUsername string
	ClientIP               string
	Active                 bool
	FirstSeenAt            *time.Time
	LastSeenAt             *time.Time
	UploadText             string
	DownloadText           string
	TrafficText            string
	Source                 string
	TrafficScope           string
	UserPresentInCache     bool
	IPObserved             bool
	TrafficObserved        bool
}

type telemetryNodeView struct {
	NodeDBID                   string
	NodeID                     string
	NodeName                   string
	NodeProtocol               string
	NodeEnabled                bool
	CollectorStatus            string
	CollectorTone              string
	CollectorLabel             string
	LastSuccessfulCollectionAt *time.Time
	PerUserActivityCapable     bool
	TrafficCountersCapable     bool
	FirstSeenCapable           bool
	LastSeenCapable            bool
	ClientIPCapable            bool
	Warnings                   []string
	Error                      string
	IssueText                  string
	Rows                       []telemetrySessionRowView
	SkippedReason              string
	HasIssues                  bool
}

func buildTelemetrySessionRows(rows []telemetry.SessionView) []telemetrySessionRowView {
	result := make([]telemetrySessionRowView, 0, len(rows))
	for _, row := range rows {
		trafficText := FormatBytesIEC(row.UploadBytes + row.DownloadBytes)
		result = append(result, telemetrySessionRowView{
			NodeDBID:               row.NodeDBID,
			NodeName:               row.NodeName,
			NodeProtocol:           row.NodeProtocol,
			BackendUserID:          row.BackendUserID,
			BackendUsername:        row.BackendUsername,
			BackendDisplayName:     row.BackendDisplayName,
			UserKnown:              row.UserKnown,
			MaskedProtocolUsername: row.MaskedProtocolUsername,
			ClientIP:               row.ClientIP,
			Active:                 row.Active,
			FirstSeenAt:            row.FirstSeenAt,
			LastSeenAt:             row.LastSeenAt,
			UploadText:             FormatBytesIEC(row.UploadBytes),
			DownloadText:           FormatBytesIEC(row.DownloadBytes),
			TrafficText:            trafficText,
			Source:                 row.Source,
			TrafficScope:           row.TrafficScope,
			UserPresentInCache:     row.UserPresentInCache,
			IPObserved:             row.IPObserved,
			TrafficObserved:        row.TrafficObserved,
		})
	}
	return result
}

func buildTelemetryNodeViews(locale Locale, nodesList []telemetry.NodeCollectorView) []telemetryNodeView {
	result := make([]telemetryNodeView, 0, len(nodesList))
	for _, node := range nodesList {
		warnings := make([]string, 0, len(node.Warnings))
		for _, warning := range node.Warnings {
			safe := strings.TrimSpace(SafeOperationalError(warning))
			if safe == "" {
				continue
			}
			warnings = append(warnings, safe)
		}
		hasIssues := telemetry.NodeHasIssue(node)
		result = append(result, telemetryNodeView{
			NodeDBID:                   node.NodeDBID,
			NodeID:                     node.NodeID,
			NodeName:                   node.NodeName,
			NodeProtocol:               node.NodeProtocol,
			NodeEnabled:                node.NodeEnabled,
			CollectorStatus:            node.CollectorStatus,
			CollectorTone:              collectorStatusTone(node.CollectorStatus),
			CollectorLabel:             collectorStatusLabel(locale, node.CollectorStatus),
			LastSuccessfulCollectionAt: node.LastSuccessfulCollectionAt,
			PerUserActivityCapable:     node.Capabilities.PerUserActivity,
			TrafficCountersCapable:     node.Capabilities.TrafficCounters,
			FirstSeenCapable:           node.Capabilities.FirstSeen,
			LastSeenCapable:            node.Capabilities.LastSeen,
			ClientIPCapable:            node.Capabilities.ClientIP,
			Warnings:                   warnings,
			Error:                      SafeOperationalError(node.Error),
			IssueText:                  telemetryIssueText(locale, node),
			Rows:                       buildTelemetrySessionRows(node.Sessions),
			SkippedReason:              node.SkippedReason,
			HasIssues:                  hasIssues,
		})
	}
	return result
}

func buildTelemetryIssueNodes(nodesList []telemetryNodeView) []telemetryNodeView {
	result := make([]telemetryNodeView, 0, len(nodesList))
	for _, node := range nodesList {
		if node.HasIssues {
			result = append(result, node)
		}
	}
	return result
}

func telemetryIssueText(locale Locale, node telemetry.NodeCollectorView) string {
	if safe := strings.TrimSpace(SafeOperationalError(node.Error)); safe != "" {
		return safe
	}
	if len(node.Warnings) > 0 {
		items := make([]string, 0, len(node.Warnings))
		for _, warning := range node.Warnings {
			safe := strings.TrimSpace(SafeOperationalError(warning))
			if safe == "" {
				continue
			}
			items = append(items, safe)
		}
		if len(items) > 0 {
			return strings.Join(items, "; ")
		}
	}
	switch node.SkippedReason {
	case "missing_api_url":
		return Translate(locale, "sessions.issue_missing_api_url")
	case "missing_node_id":
		return Translate(locale, "sessions.issue_missing_node_id")
	case "missing_node_secret":
		return Translate(locale, "sessions.issue_missing_node_secret")
	}
	switch telemetry.NodeIssueCode(node) {
	case "collector_warning":
		return Translate(locale, "sessions.issue_collector_warning")
	case "collector_partial":
		return Translate(locale, "sessions.issue_collector_partial")
	case "collector_unavailable":
		return Translate(locale, "sessions.issue_collector_unavailable")
	case "collector_disabled":
		return Translate(locale, "sessions.issue_collector_disabled")
	default:
		return ""
	}
}
