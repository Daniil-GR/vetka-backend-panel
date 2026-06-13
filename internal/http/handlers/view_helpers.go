package handlers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"vetka-backend-panel/internal/nodes"
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
		{Label: "Dashboard", URL: "/", Key: "dashboard"},
		{Label: "Users", URL: "/users", Key: "users"},
		{Label: "Nodes", URL: "/nodes", Key: "nodes"},
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

func userStatus(user users.User) (string, string) {
	switch {
	case !user.Enabled:
		return "disabled", "Disabled"
	case IsUserExpired(user.ExpiresAt):
		return "expired", "Expired"
	case IsUserExpiringSoon(user.ExpiresAt):
		return "warning", "Expires Soon"
	default:
		return "success", "Active"
	}
}

func nodeStatusTone(node nodes.Node) (string, string) {
	if !node.Enabled || node.SetupState == nodes.SetupStateDisabled {
		return "disabled", "Disabled"
	}
	switch node.SetupState {
	case nodes.SetupStateConnected:
		return "success", "Connected"
	case nodes.SetupStateUnreachable:
		return "danger", "Unreachable"
	case nodes.SetupStatePlanned:
		return "warning", "Planned"
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

func TimeRemaining(expiresAt *time.Time) string {
	if expiresAt == nil {
		return "Unlimited"
	}
	now := time.Now()
	if !expiresAt.After(now) {
		return "Expired"
	}
	duration := expiresAt.Sub(now).Round(time.Minute)
	if duration < 24*time.Hour {
		return duration.String()
	}
	days := int(duration.Hours()) / 24
	hours := int(duration.Hours()) % 24
	return fmt.Sprintf("%dd %dh remaining", days, hours)
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
		return item.StatusLabel == "Active"
	case "expired":
		return item.StatusLabel == "Expired"
	case "disabled":
		return item.StatusLabel == "Disabled"
	case "expires-soon":
		return item.StatusLabel == "Expires Soon"
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
