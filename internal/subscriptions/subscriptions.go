package subscriptions

import (
	"context"
	"fmt"
	"net/url"
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
	// TODO: confirm the final Naive share URI format against production clients.
	u := url.URL{
		Scheme: "naive+https",
		User:   url.UserPassword(access.ProtocolUsername, access.ProtocolPassword),
		Host:   access.NodeDomain + ":443",
	}
	u.Fragment = access.NodeName
	return u.String()
}

func BuildMieruURI(access users.AccessWithNode, devMode bool) string {
	// TODO: replace this placeholder once the exact Mieru share link format is confirmed.
	value := fmt.Sprintf("mieru://%s:%s@%s#%s", url.QueryEscape(access.ProtocolUsername), url.QueryEscape(access.ProtocolPassword), access.NodeDomain, url.QueryEscape(access.NodeName))
	if devMode {
		return "# TODO mieru share format: " + value
	}
	return value
}
