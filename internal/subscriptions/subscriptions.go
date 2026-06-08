package subscriptions

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"

	"vetka-backend-panel/internal/users"
)

const (
	FormatDefault = ""
	FormatJSON    = "json"
	FormatKaring  = "karing"
	FormatSingBox = "sing-box"
	FormatRaw     = "raw"
	FormatMierus  = "mierus"
)

type Service struct {
	userRepo *users.Repository
	devMode  bool
}

func NewService(userRepo *users.Repository, devMode bool) *Service {
	return &Service{userRepo: userRepo, devMode: devMode}
}

func (s *Service) BuildByToken(ctx context.Context, token, format string) (string, string, error) {
	user, err := s.userRepo.GetByToken(ctx, token)
	if err != nil {
		return "", "", err
	}
	if !user.Enabled || users.IsExpired(user.ExpiresAt) {
		return "", "", ErrSubscriptionDisabled
	}
	assignments, err := s.userRepo.ActiveAccessForSubscription(ctx, user.ID)
	if err != nil {
		return "", "", err
	}
	return BuildSubscription(assignments, format, s.devMode)
}

var ErrSubscriptionDisabled = fmt.Errorf("subscription disabled or expired")

func BuildSubscription(assignments []users.AccessWithNode, format string, devMode bool) (string, string, error) {
	switch normalizeFormat(format) {
	case FormatJSON, FormatKaring, FormatSingBox:
		body, err := BuildSingboxJSON(assignments)
		return body, "application/json; charset=utf-8", err
	case FormatMierus:
		body := buildRawMieru(assignments, devMode)
		return body, "text/plain; charset=utf-8", nil
	case FormatRaw:
		body := buildRawAll(assignments, devMode)
		return body, "text/plain; charset=utf-8", nil
	default:
		body, err := BuildSingboxJSON(assignments)
		return body, "application/json; charset=utf-8", err
	}
}

func BuildNaiveURI(access users.AccessWithNode) string {
	settings := protocolSettingsForAccess(access)
	u := url.URL{
		Scheme: "naive+https",
		User:   url.UserPassword(access.ProtocolUsername, access.ProtocolPassword),
		Host:   nodeServer(access) + ":" + strconv.Itoa(settings.Naive.Port),
	}
	u.Fragment = access.NodeName
	return u.String()
}

func BuildMieruURI(access users.AccessWithNode, devMode bool) string {
	_ = devMode
	settings := protocolSettingsForAccess(access)
	profile := settings.Mieru.Profile
	if profile == "" {
		profile = access.NodeName
	}
	ports := settings.Mieru.Ports
	if len(ports) == 0 {
		ports = []string{"2012-2022"}
	}
	protocol := settings.Mieru.Protocol
	if protocol == "" {
		protocol = "TCP"
	}
	queryParts := []string{
		"profile=" + url.QueryEscape(profile),
		"mtu=" + url.QueryEscape(strconv.Itoa(settings.Mieru.MTU)),
		"multiplexing=" + url.QueryEscape(settings.Mieru.Multiplexing),
		"handshake-mode=" + url.QueryEscape(settings.Mieru.HandshakeMode),
	}
	if settings.Mieru.TrafficPattern != "" {
		queryParts = append(queryParts, "traffic-pattern="+url.QueryEscape(settings.Mieru.TrafficPattern))
	}
	for _, port := range ports {
		queryParts = append(queryParts,
			"port="+url.QueryEscape(port),
			"protocol="+url.QueryEscape(protocol),
		)
	}
	userInfo := url.UserPassword(access.ProtocolUsername, access.ProtocolPassword).String()
	host := nodeServer(access)
	return "mierus://" + userInfo + "@" + host + "?" + strings.Join(queryParts, "&") + "#" + url.QueryEscape(profile)
}

func BuildSingboxJSON(assignments []users.AccessWithNode) (string, error) {
	type dnsServer struct {
		Tag     string `json:"tag"`
		Address string `json:"address"`
		Detour  string `json:"detour"`
	}
	type dnsRule struct {
		Outbound string `json:"outbound"`
		Server   string `json:"server"`
	}
	type routeRule struct {
		Protocol string `json:"protocol"`
		Outbound string `json:"outbound"`
	}
	type tlsOptions struct {
		Enabled    bool   `json:"enabled"`
		ServerName string `json:"server_name"`
	}
	type outbound struct {
		Type         string      `json:"type"`
		Tag          string      `json:"tag"`
		Outbounds    []string    `json:"outbounds,omitempty"`
		Server       string      `json:"server,omitempty"`
		ServerPort   int         `json:"server_port,omitempty"`
		Transport    string      `json:"transport,omitempty"`
		Username     string      `json:"username,omitempty"`
		Password     string      `json:"password,omitempty"`
		Multiplexing string      `json:"multiplexing,omitempty"`
		QUIC         *bool       `json:"quic,omitempty"`
		TLS          *tlsOptions `json:"tls,omitempty"`
	}
	type config struct {
		Log struct {
			Level     string `json:"level"`
			Timestamp bool   `json:"timestamp"`
		} `json:"log"`
		DNS struct {
			Servers []dnsServer `json:"servers"`
			Rules   []dnsRule   `json:"rules"`
			Final   string      `json:"final"`
		} `json:"dns"`
		Outbounds []outbound `json:"outbounds"`
		Route     struct {
			Rules               []routeRule `json:"rules"`
			Final               string      `json:"final"`
			AutoDetectInterface bool        `json:"auto_detect_interface"`
		} `json:"route"`
	}

	proxyTags := make([]string, 0, len(assignments))
	outbounds := make([]outbound, 0, len(assignments)+3)
	for _, assignment := range assignments {
		tag := outboundTag(assignment)
		proxyTags = append(proxyTags, tag)
		switch assignment.NodeProtocolType {
		case "naive":
			settings := protocolSettingsForAccess(assignment)
			server := nodeServer(assignment)
			quic := false
			outbounds = append(outbounds, outbound{
				Type:       "naive",
				Tag:        tag,
				Server:     server,
				ServerPort: settings.Naive.Port,
				Username:   assignment.ProtocolUsername,
				Password:   assignment.ProtocolPassword,
				QUIC:       &quic,
				TLS: &tlsOptions{
					Enabled:    true,
					ServerName: server,
				},
			})
		case "mieru":
			settings := protocolSettingsForAccess(assignment)
			outbounds = append(outbounds, outbound{
				Type:         "mieru",
				Tag:          tag,
				Server:       nodeServer(assignment),
				ServerPort:   firstMieruPort(settings.Mieru.Ports),
				Transport:    defaultString(settings.Mieru.Protocol, "TCP"),
				Username:     assignment.ProtocolUsername,
				Password:     assignment.ProtocolPassword,
				Multiplexing: defaultString(settings.Mieru.Multiplexing, "MULTIPLEXING_HIGH"),
			})
		}
	}
	if len(proxyTags) == 0 {
		proxyTags = []string{"direct"}
	}
	cfg := config{}
	cfg.Log.Level = "info"
	cfg.Log.Timestamp = true
	cfg.DNS.Servers = []dnsServer{
		{Tag: "remote", Address: "tls://8.8.8.8", Detour: "proxy"},
		{Tag: "local", Address: "https://223.5.5.5/dns-query", Detour: "direct"},
	}
	cfg.DNS.Rules = []dnsRule{{Outbound: "any", Server: "local"}}
	cfg.DNS.Final = "remote"
	cfg.Outbounds = append(cfg.Outbounds, outbound{
		Type:      "selector",
		Tag:       "proxy",
		Outbounds: proxyTags,
	})
	cfg.Outbounds = append(cfg.Outbounds, outbounds...)
	cfg.Outbounds = append(cfg.Outbounds,
		outbound{Type: "direct", Tag: "direct"},
		outbound{Type: "dns", Tag: "dns-out"},
	)
	cfg.Route.Rules = []routeRule{{Protocol: "dns", Outbound: "dns-out"}}
	cfg.Route.Final = "proxy"
	cfg.Route.AutoDetectInterface = true

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func buildRawAll(assignments []users.AccessWithNode, devMode bool) string {
	lines := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		switch assignment.NodeProtocolType {
		case "naive":
			lines = append(lines, BuildNaiveURI(assignment))
		case "mieru":
			lines = append(lines, BuildMieruURI(assignment, devMode))
		}
	}
	return strings.Join(lines, "\n")
}

func buildRawMieru(assignments []users.AccessWithNode, devMode bool) string {
	lines := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.NodeProtocolType == "mieru" {
			lines = append(lines, BuildMieruURI(assignment, devMode))
		}
	}
	return strings.Join(lines, "\n")
}

func normalizeFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatDefault, FormatJSON, FormatKaring, FormatSingBox:
		return FormatJSON
	case FormatRaw:
		return FormatRaw
	case FormatMierus:
		return FormatMierus
	default:
		return FormatJSON
	}
}

func outboundTag(access users.AccessWithNode) string {
	source := access.AgentNodeID
	if source == "" {
		source = access.NodeID
	}
	if source == "" {
		source = access.NodeName
	}
	source = strings.ToLower(source)
	var b strings.Builder
	prevDash := false
	for _, r := range source {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	value := strings.Trim(b.String(), "-")
	if value == "" {
		value = "node"
	}
	return "node-" + access.NodeProtocolType + "-" + value
}

func nodeServer(access users.AccessWithNode) string {
	if host := sanitizeHost(access.NodeDomain); host != "" {
		return host
	}
	if host := hostFromURL(access.NodeAPIURL); host != "" {
		return host
	}
	return "localhost"
}

func hostFromURL(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return sanitizeHost(value)
	}
	host := parsed.Hostname()
	if host == "" {
		host = parsed.Host
	}
	return sanitizeHost(host)
}

func sanitizeHost(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "http://")
	trimmed = strings.TrimPrefix(trimmed, "https://")
	if idx := strings.Index(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil && host != "" {
		trimmed = host
	}
	return path.Clean("/" + trimmed)[1:]
}

func firstMieruPort(ports []string) int {
	if len(ports) == 0 {
		return 2012
	}
	value := strings.TrimSpace(ports[0])
	if value == "" {
		return 2012
	}
	if strings.Contains(value, "-") {
		value = strings.SplitN(value, "-", 2)[0]
	}
	port, err := strconv.Atoi(value)
	if err != nil || port <= 0 {
		return 2012
	}
	return port
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

type protocolSettings struct {
	Mieru struct {
		Ports          []string `json:"ports"`
		Protocol       string   `json:"protocol"`
		MTU            int      `json:"mtu"`
		Multiplexing   string   `json:"multiplexing"`
		HandshakeMode  string   `json:"handshake_mode"`
		TrafficPattern string   `json:"traffic_pattern"`
		Profile        string   `json:"profile"`
	} `json:"mieru"`
	Naive struct {
		Port int `json:"port"`
	} `json:"naive"`
}

func protocolSettingsForAccess(access users.AccessWithNode) protocolSettings {
	settings := protocolSettings{}
	_ = json.Unmarshal(access.NodeProtocolSettingsJSON, &settings)
	if len(settings.Mieru.Ports) == 0 {
		settings.Mieru.Ports = []string{"2012-2022"}
	}
	if settings.Mieru.Protocol == "" {
		settings.Mieru.Protocol = "TCP"
	}
	if settings.Mieru.MTU == 0 {
		settings.Mieru.MTU = 1400
	}
	if settings.Mieru.Multiplexing == "" {
		settings.Mieru.Multiplexing = "MULTIPLEXING_HIGH"
	}
	if settings.Mieru.HandshakeMode == "" {
		settings.Mieru.HandshakeMode = "HANDSHAKE_NO_WAIT"
	}
	if settings.Mieru.Profile == "" {
		settings.Mieru.Profile = access.NodeName
	}
	if settings.Naive.Port == 0 {
		settings.Naive.Port = 443
	}
	return settings
}
