package handlers

import (
	"net/url"
	"strings"

	"vetka-backend-panel/internal/nodes"
)

func (h *Handler) pageData(r *url.URL, title, nav string) viewData {
	data := newViewData(title, nav)
	data["Environment"] = strings.ToUpper(h.cfg.AppEnv)
	data["AppTimezone"] = h.cfg.AppTimezone
	data["PanelPublicBaseURL"] = h.cfg.PanelPublicBaseURL
	data["FlashItems"] = flashFromQuery(r.Query())
	return data
}

func (h *Handler) loginData(r *url.URL, message string) viewData {
	data := newViewData("Login", "")
	data["FlashItems"] = nil
	if strings.TrimSpace(message) != "" {
		data["FlashItems"] = []toastMessage{{Level: "error", Text: message}}
	}
	data["LoginPage"] = true
	return data
}

func makeNodeListItems(list []nodes.Node, counts map[string]int) []nodeListItem {
	items := make([]nodeListItem, 0, len(list))
	for _, node := range list {
		statusTone, statusLabel := nodeStatusTone(node)
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
