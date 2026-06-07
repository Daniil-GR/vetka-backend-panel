package users

import (
	"context"
	"fmt"

	"vetka-backend-panel/internal/security"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateUser(ctx context.Context, in CreateUserInput, nodeProtocols map[string]string) (User, error) {
	token, err := security.Token("sub", 24)
	if err != nil {
		return User{}, err
	}
	creds := make([]AssignmentCredential, 0, len(in.NodeIDs))
	for _, nodeID := range in.NodeIDs {
		protocol := nodeProtocols[nodeID]
		if protocol == "" {
			return User{}, fmt.Errorf("unknown node protocol for node %s", nodeID)
		}
		protocolUser, err := security.Token("u", 8)
		if err != nil {
			return User{}, err
		}
		protocolPassword, err := security.Token("p", 18)
		if err != nil {
			return User{}, err
		}
		creds = append(creds, AssignmentCredential{
			NodeID:           nodeID,
			ProtocolUsername: protocolUser,
			ProtocolPassword: protocolPassword,
		})
	}
	user, err := s.repo.CreateWithAssignments(ctx, in, token, nodeProtocols, creds)
	if err != nil {
		return User{}, fmt.Errorf("create user with assignments: %w", err)
	}
	return user, nil
}
