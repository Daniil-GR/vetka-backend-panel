package handlers

import (
	"net/http"
	"strings"

	"vetka-backend-panel/internal/nodes"
)

func (h *Handler) pageData(r *http.Request, title, nav string) viewData {
	locale := ResolveLocale(r)
	data := newViewData(title, nav)
	for i := range data["NavItems"].([]navItem) {
		item := data["NavItems"].([]navItem)[i]
		item.Label = Translate(locale, "nav."+item.Key)
		data["NavItems"].([]navItem)[i] = item
	}
	data["Environment"] = strings.ToUpper(h.cfg.AppEnv)
	data["AppTimezone"] = h.cfg.AppTimezone
	data["PanelPublicBaseURL"] = h.cfg.PanelPublicBaseURL
	data["FlashItems"] = flashFromQuery(r.URL.Query())
	data["Locale"] = locale
	data["CurrentPath"] = h.safeReturnTo(r.URL.RequestURI())
	return data
}

func (h *Handler) loginData(r *http.Request, message string) viewData {
	locale := ResolveLocale(r)
	data := newViewData("page.login", "")
	data["FlashItems"] = nil
	if strings.TrimSpace(message) != "" {
		data["FlashItems"] = []toastMessage{{Level: "error", Text: Translate(locale, message)}}
	}
	data["LoginPage"] = true
	data["Locale"] = locale
	data["CurrentPath"] = "/login"
	return data
}

func makeNodeListItems(locale Locale, list []nodes.Node, counts map[string]int) []nodeListItem {
	items := make([]nodeListItem, 0, len(list))
	for _, node := range list {
		statusTone, statusLabel := nodeStatusTone(locale, node)
		lastError := ""
		if node.LastError != nil {
			lastError = TruncateText(SafeOperationalError(*node.LastError), 140)
		}
		items = append(items, nodeListItem{
			Node:              node,
			StatusTone:        statusTone,
			StatusLabel:       statusLabel,
			ProtocolTone:      protocolTone(node.ProtocolType),
			AssignedUserCount: counts[node.ID],
			LastErrorPreview:  lastError,
		})
	}
	return items
}
