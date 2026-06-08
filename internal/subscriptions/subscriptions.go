package subscriptions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"vetka-backend-panel/internal/users"
)

type Service struct {
	userRepo *users.Repository
	devMode  bool
}

func NewService(userRepo *users.Repository, devMode bool) *Service {
	return &Service{userRepo: userRepo, devMode: devMode}
}

func (s *Service) BuildByToken(ctx context.Context, token string) (string, error) {
	user, err := s.userRepo.GetByToken(ctx, token)
	if err != nil {
		return "", err
	}
	if !user.Enabled || users.IsExpired(user.ExpiresAt) {
		return "", ErrSubscriptionDisabled
	}
	assignments, err := s.userRepo.ActiveAccessForSubscription(ctx, user.ID)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		switch assignment.NodeProtocolType {
		case "naive":
			lines = append(lines, BuildNaiveURI(assignment))
		case "mieru":
			lines = append(lines, BuildMieruURI(assignment, s.devMode))
		}
	}
	return strings.Join(lines, "\n"), nil
}

var ErrSubscriptionDisabled = fmt.Errorf("subscription disabled or expired")

func BuildNaiveURI(access users.AccessWithNode) string {
	settings := protocolSettingsForAccess(access)
	u := url.URL{
		Scheme: "naive+https",
		User:   url.UserPassword(access.ProtocolUsername, access.ProtocolPassword),
		Host:   access.NodeDomain + ":" + strconv.Itoa(settings.Naive.Port),
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
	host := sanitizeHost(access.NodeDomain)
	return "mierus://" + userInfo + "@" + host + "?" + strings.Join(queryParts, "&") + "#" + url.QueryEscape(profile)
}

func sanitizeHost(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "http://")
	trimmed = strings.TrimPrefix(trimmed, "https://")
	if idx := strings.Index(trimmed, "/"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return path.Clean("/" + trimmed)[1:]
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
