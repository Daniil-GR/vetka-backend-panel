package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"vetka-backend-panel/internal/security"
	"vetka-backend-panel/internal/users"
)

type Manager struct {
	repo     *Repository
	userRepo *users.Repository
	agent    *AgentClient
}

func NewManager(repo *Repository, userRepo *users.Repository, agent *AgentClient) *Manager {
	return &Manager{repo: repo, userRepo: userRepo, agent: agent}
}

func (m *Manager) CreateNode(ctx context.Context, in CreateNodeInput) (Node, error) {
	in.ProtocolType = strings.ToLower(strings.TrimSpace(in.ProtocolType))
	if in.ProtocolType == "" {
		in.ProtocolType = "naive"
	}
	if in.NodeID == "" {
		token, err := security.Token("node", 10)
		if err != nil {
			return Node{}, err
		}
		in.NodeID = token
	}
	if in.NodeSecret == "" {
		token, err := security.Token("ns", 32)
		if err != nil {
			return Node{}, err
		}
		in.NodeSecret = token
	}
	if in.APIURL == "" {
		in.APIURL = "http://" + in.Domain + ":2222"
	}
	return m.repo.Create(ctx, in)
}

func (m *Manager) UpdateNode(ctx context.Context, id string, in UpdateNodeInput) (Node, error) {
	if in.NodeSecret == "" {
		current, err := m.repo.Get(ctx, id)
		if err != nil {
			return Node{}, err
		}
		in.NodeSecret = current.NodeSecret
	}
	return m.repo.Update(ctx, id, in)
}

func (m *Manager) DeleteNode(ctx context.Context, id string) error {
	return m.repo.Delete(ctx, id)
}

func (m *Manager) CheckNodeHealth(ctx context.Context, id string) (AgentCallResult, error) {
	node, err := m.repo.Get(ctx, id)
	if err != nil {
		return AgentCallResult{}, err
	}
	result, err := m.agent.Health(ctx, node)
	return m.recordStatus(ctx, id, result, err)
}

func (m *Manager) FetchNodeStatus(ctx context.Context, id string) (AgentCallResult, error) {
	node, err := m.repo.Get(ctx, id)
	if err != nil {
		return AgentCallResult{}, err
	}
	result, err := m.agent.Status(ctx, node)
	return m.recordStatus(ctx, id, result, err)
}

func (m *Manager) SyncNode(ctx context.Context, id string) error {
	node, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	version, err := m.repo.BumpVersion(ctx, id)
	if err != nil {
		return err
	}
	assigned, err := m.userRepo.ActiveAccessForNode(ctx, id)
	if err != nil {
		return err
	}
	payload := SyncPayload{
		NodeID:        node.NodeID,
		ConfigVersion: version,
		ProtocolType:  node.ProtocolType,
		Users:         make([]AgentUser, 0, len(assigned)),
	}
	for _, access := range assigned {
		payload.Users = append(payload.Users, AgentUser{
			ID:        access.UserID,
			Username:  access.ProtocolUsername,
			Password:  access.ProtocolPassword,
			Enabled:   access.Enabled,
			ExpiresAt: access.UserExpiresAt,
			QuotaMB:   0,
			Protocols: []string{node.ProtocolType},
			Meta: map[string]string{
				"backend_user_id":  access.UserID,
				"backend_username": access.Username,
			},
		})
	}
	result, requestBody, callErr := m.agent.Sync(ctx, node, payload)
	if callErr != nil {
		status := "http_error"
		msg := callErr.Error()
		if err := m.repo.InsertSyncEvent(ctx, id, version, status, &result.StatusCode, requestBody, result.Body, strPtr(msg)); err != nil {
			return err
		}
		_ = m.repo.MarkSyncFailure(ctx, id, status, msg)
		return callErr
	}
	var agentResp AgentResponse
	if err := json.Unmarshal(result.Body, &agentResp); err != nil {
		if insertErr := m.repo.InsertSyncEvent(ctx, id, version, "decode_error", &result.StatusCode, requestBody, result.Body, strPtr(err.Error())); insertErr != nil {
			return insertErr
		}
		_ = m.repo.MarkSyncFailure(ctx, id, "decode_error", err.Error())
		return fmt.Errorf("decode node agent response: %w", err)
	}
	if !agentResp.OK {
		status := agentResp.Status
		if status == "" {
			status = "apply_failed"
		}
		message := agentResponseError(agentResp)
		if insertErr := m.repo.InsertSyncEvent(ctx, id, version, status, &result.StatusCode, requestBody, result.Body, strPtr(message)); insertErr != nil {
			return insertErr
		}
		_ = m.repo.MarkSyncFailure(ctx, id, status, message)
		return fmt.Errorf("node agent sync failed: %s", message)
	}
	appliedVersion := agentResp.AppliedVersion
	if appliedVersion <= 0 {
		appliedVersion = version
	}
	if err := m.repo.InsertSyncEvent(ctx, id, version, "ok", &result.StatusCode, requestBody, result.Body, nil); err != nil {
		return err
	}
	if err := m.repo.MarkSyncSuccess(ctx, id, appliedVersion); err != nil {
		return err
	}
	return nil
}

func (m *Manager) SyncAllNodes(ctx context.Context) error {
	list, err := m.repo.List(ctx)
	if err != nil {
		return err
	}
	var errs []string
	for _, node := range list {
		if !node.Enabled {
			continue
		}
		if err := m.SyncNode(ctx, node.ID); err != nil {
			errs = append(errs, node.NodeID+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) recordStatus(ctx context.Context, id string, result AgentCallResult, callErr error) (AgentCallResult, error) {
	status := "ok"
	var errText *string
	if callErr != nil {
		status = "error"
		msg := callErr.Error()
		errText = &msg
	}
	_ = m.repo.MarkStatus(ctx, id, status, errText)
	return result, callErr
}

func agentResponseError(resp AgentResponse) string {
	parts := make([]string, 0, 3)
	if resp.Status != "" && resp.Status != "ok" {
		parts = append(parts, resp.Status)
	}
	if resp.Error != "" {
		parts = append(parts, resp.Error)
	}
	if resp.Message != "" && resp.Message != resp.Error {
		parts = append(parts, resp.Message)
	}
	if len(parts) == 0 {
		return "node agent returned ok=false"
	}
	return strings.Join(parts, ": ")
}

func strPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
