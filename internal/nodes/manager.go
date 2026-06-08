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
	in.Mode = strings.ToLower(strings.TrimSpace(in.Mode))
	if in.Mode == "" {
		in.Mode = NodeModePlanned
	}
	in.ProtocolType = strings.ToLower(strings.TrimSpace(in.ProtocolType))
	if in.ProtocolType == "" {
		in.ProtocolType = "naive"
	}
	if in.Mode == NodeModeAdopt {
		if in.NodeID == "" || in.NodeSecret == "" || in.APIURL == "" || in.ProtocolType == "" {
			return Node{}, fmt.Errorf("adopt mode requires node_id, node_secret, api_url, and protocol_type")
		}
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
	if in.APIURL == "" && in.Mode != NodeModePlanned {
		in.APIURL = "http://" + in.Domain + ":2222"
	}
	if in.Mode == NodeModePlanned {
		return m.repo.Create(ctx, in)
	}
	remoteStatus, err := m.validateNodeAgentConfig(ctx, Node{
		NodeID:       in.NodeID,
		APIURL:       in.APIURL,
		ProtocolType: in.ProtocolType,
		NodeSecret:   in.NodeSecret,
	})
	if err != nil {
		return Node{}, err
	}
	in.Mode = NodeModeAdopt
	node, err := m.repo.Create(ctx, in)
	if err != nil {
		return Node{}, err
	}
	if err := m.repo.SyncRemoteState(ctx, node.ID, remoteStatus.CurrentVersion, remoteAppliedVersionFromStatus(remoteStatus), remoteStatus.LastError); err != nil {
		return Node{}, err
	}
	return m.repo.Get(ctx, node.ID)
}

func (m *Manager) UpdateNode(ctx context.Context, id string, in UpdateNodeInput) (Node, error) {
	current, err := m.repo.Get(ctx, id)
	if err != nil {
		return Node{}, err
	}
	if in.NodeSecret == "" {
		in.NodeSecret = current.NodeSecret
	}
	var remoteStatus AgentStatusResponse
	shouldValidate := current.SetupState == SetupStateConnected && in.APIURL != "" && in.NodeSecret != "" && in.NodeID != "" && in.ProtocolType != ""
	if shouldValidate {
		remoteStatus, err = m.validateNodeAgentConfig(ctx, Node{
			NodeID:       in.NodeID,
			APIURL:       in.APIURL,
			ProtocolType: in.ProtocolType,
			NodeSecret:   in.NodeSecret,
		})
		if err != nil {
			return Node{}, err
		}
	}
	node, err := m.repo.Update(ctx, id, in)
	if err != nil {
		return Node{}, err
	}
	if shouldValidate {
		if err := m.repo.SyncRemoteState(ctx, node.ID, remoteStatus.CurrentVersion, remoteAppliedVersionFromStatus(remoteStatus), remoteStatus.LastError); err != nil {
			return Node{}, err
		}
	}
	return m.repo.Get(ctx, node.ID)
}

func (m *Manager) ValidateNodeStatus(ctx context.Context, id string) (AgentStatusResponse, error) {
	node, err := m.repo.Get(ctx, id)
	if err != nil {
		return AgentStatusResponse{}, err
	}
	status, err := m.validateNodeAgentConfig(ctx, node)
	if err != nil {
		return AgentStatusResponse{}, err
	}
	if err := m.repo.SyncRemoteState(ctx, node.ID, status.CurrentVersion, remoteAppliedVersionFromStatus(status), status.LastError); err != nil {
		return AgentStatusResponse{}, err
	}
	return status, nil
}

func (m *Manager) DeleteNode(ctx context.Context, id string) error {
	return m.repo.Delete(ctx, id)
}

func (m *Manager) CheckNodeHealth(ctx context.Context, id string) (AgentStatusResponse, error) {
	node, err := m.repo.Get(ctx, id)
	if err != nil {
		return AgentStatusResponse{}, err
	}
	result, err := m.agent.Health(ctx, node)
	if _, err = m.recordStatus(ctx, id, result, err); err != nil {
		return AgentStatusResponse{}, err
	}
	return m.fetchAndReconcileStatus(ctx, node)
}

func (m *Manager) FetchNodeStatus(ctx context.Context, id string) (AgentStatusResponse, error) {
	node, err := m.repo.Get(ctx, id)
	if err != nil {
		return AgentStatusResponse{}, err
	}
	return m.fetchAndReconcileStatus(ctx, node)
}

func (m *Manager) SyncNode(ctx context.Context, id string) (AgentResponse, error) {
	node, err := m.repo.Get(ctx, id)
	if err != nil {
		return AgentResponse{}, err
	}
	if _, err := m.fetchAndReconcileStatus(ctx, node); err != nil {
		return AgentResponse{}, err
	}
	node, err = m.repo.Get(ctx, id)
	if err != nil {
		return AgentResponse{}, err
	}
	version, err := m.repo.BumpVersion(ctx, id)
	if err != nil {
		return AgentResponse{}, err
	}
	assigned, err := m.userRepo.ActiveAccessForNode(ctx, id)
	if err != nil {
		return AgentResponse{}, err
	}
	return m.syncNodeAttempt(ctx, node, assigned, version, true)
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
		if _, err := m.SyncNode(ctx, node.ID); err != nil {
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

func (m *Manager) validateNodeAgentConfig(ctx context.Context, node Node) (AgentStatusResponse, error) {
	status, _, err := m.agent.StatusInfo(ctx, node)
	if err != nil {
		return AgentStatusResponse{}, err
	}
	if !status.OK {
		return AgentStatusResponse{}, fmt.Errorf("node agent /status failed: %s", statusMessage(status))
	}
	if status.NodeID != "" && status.NodeID != node.NodeID {
		return AgentStatusResponse{}, fmt.Errorf("node_id mismatch: form=%s agent=%s", node.NodeID, status.NodeID)
	}
	if status.ProtocolType != "" && status.ProtocolType != node.ProtocolType {
		return AgentStatusResponse{}, fmt.Errorf("protocol_type mismatch: form=%s agent=%s", node.ProtocolType, status.ProtocolType)
	}
	return status, nil
}

func (m *Manager) fetchAndReconcileStatus(ctx context.Context, node Node) (AgentStatusResponse, error) {
	status, result, err := m.agent.StatusInfo(ctx, node)
	if _, recordErr := m.recordStatus(ctx, node.ID, result, err); recordErr != nil {
		return AgentStatusResponse{}, recordErr
	}
	if err != nil {
		return AgentStatusResponse{}, err
	}
	if !status.OK {
		message := statusMessage(status)
		_ = m.repo.MarkStatus(ctx, node.ID, "error", strPtr(message))
		return AgentStatusResponse{}, fmt.Errorf("node agent status failed: %s", message)
	}
	if err := m.repo.SyncRemoteState(ctx, node.ID, status.CurrentVersion, remoteAppliedVersionFromStatus(status), status.LastError); err != nil {
		return AgentStatusResponse{}, err
	}
	return status, nil
}

func (m *Manager) syncNodeAttempt(ctx context.Context, node Node, assigned []users.AccessWithUser, version int64, allowRetry bool) (AgentResponse, error) {
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
		if allowRetry {
			if retryResp, retried, retryErr := m.retryOnStaleVersion(ctx, node, assigned, version, result, requestBody); retried {
				return retryResp, retryErr
			}
		}
		status := "http_error"
		msg := callErr.Error()
		if err := m.repo.InsertSyncEvent(ctx, node.ID, version, status, &result.StatusCode, requestBody, result.Body, strPtr(msg)); err != nil {
			return AgentResponse{}, err
		}
		_ = m.repo.MarkSyncFailure(ctx, node.ID, status, msg)
		return AgentResponse{}, callErr
	}
	var agentResp AgentResponse
	if err := json.Unmarshal(result.Body, &agentResp); err != nil {
		if insertErr := m.repo.InsertSyncEvent(ctx, node.ID, version, "decode_error", &result.StatusCode, requestBody, result.Body, strPtr(err.Error())); insertErr != nil {
			return AgentResponse{}, insertErr
		}
		_ = m.repo.MarkSyncFailure(ctx, node.ID, "decode_error", err.Error())
		return AgentResponse{}, fmt.Errorf("decode node agent response: %w", err)
	}
	if !agentResp.OK {
		if allowRetry && agentResp.Status == "stale_version" {
			if retryResp, retried, retryErr := m.retryFromAgentResponse(ctx, node, assigned, version, requestBody, result, agentResp); retried {
				return retryResp, retryErr
			}
		}
		status := agentResp.Status
		if status == "" {
			status = "apply_failed"
		}
		message := agentResponseError(agentResp)
		if insertErr := m.repo.InsertSyncEvent(ctx, node.ID, version, status, &result.StatusCode, requestBody, result.Body, strPtr(message)); insertErr != nil {
			return AgentResponse{}, insertErr
		}
		_ = m.repo.MarkSyncFailure(ctx, node.ID, status, message)
		return AgentResponse{}, fmt.Errorf("node agent sync failed: %s", message)
	}
	appliedVersion := agentResp.AppliedVersion
	if appliedVersion <= 0 {
		appliedVersion = version
		agentResp.AppliedVersion = version
	}
	if err := m.repo.InsertSyncEvent(ctx, node.ID, version, "ok", &result.StatusCode, requestBody, result.Body, nil); err != nil {
		return AgentResponse{}, err
	}
	if err := m.repo.MarkSyncSuccess(ctx, node.ID, appliedVersion); err != nil {
		return AgentResponse{}, err
	}
	return agentResp, nil
}

func (m *Manager) retryOnStaleVersion(ctx context.Context, node Node, assigned []users.AccessWithUser, version int64, result AgentCallResult, requestBody []byte) (AgentResponse, bool, error) {
	var resp AgentResponse
	if err := json.Unmarshal(result.Body, &resp); err != nil {
		return AgentResponse{}, false, nil
	}
	if resp.Status != "stale_version" {
		return AgentResponse{}, false, nil
	}
	return m.retryFromAgentResponse(ctx, node, assigned, version, requestBody, result, resp)
}

func (m *Manager) retryFromAgentResponse(ctx context.Context, node Node, assigned []users.AccessWithUser, version int64, requestBody []byte, result AgentCallResult, resp AgentResponse) (AgentResponse, bool, error) {
	message := agentResponseError(resp)
	if insertErr := m.repo.InsertSyncEvent(ctx, node.ID, version, "stale_version", &result.StatusCode, requestBody, result.Body, strPtr(message)); insertErr != nil {
		return AgentResponse{}, true, insertErr
	}
	_ = m.repo.MarkSyncFailure(ctx, node.ID, "stale_version", message)
	if err := m.repo.SyncRemoteState(ctx, node.ID, resp.CurrentVersion, remoteAppliedVersionFromResponse(resp), nil); err != nil {
		return AgentResponse{}, true, err
	}
	retryNode, err := m.repo.Get(ctx, node.ID)
	if err != nil {
		return AgentResponse{}, true, err
	}
	retryVersion, err := m.repo.BumpVersion(ctx, node.ID)
	if err != nil {
		return AgentResponse{}, true, err
	}
	retryResp, retryErr := m.syncNodeAttempt(ctx, retryNode, assigned, retryVersion, false)
	return retryResp, true, retryErr
}

func remoteAppliedVersionFromStatus(status AgentStatusResponse) int64 {
	appliedVersion := status.AppliedVersion
	if appliedVersion <= 0 {
		return status.CurrentVersion
	}
	return appliedVersion
}

func remoteAppliedVersionFromResponse(resp AgentResponse) int64 {
	appliedVersion := resp.AppliedVersion
	if appliedVersion <= 0 {
		return resp.CurrentVersion
	}
	return appliedVersion
}

func statusMessage(status AgentStatusResponse) string {
	parts := make([]string, 0, 3)
	if status.Error != "" {
		parts = append(parts, status.Error)
	}
	if status.Message != "" && status.Message != status.Error {
		parts = append(parts, status.Message)
	}
	if status.LastError != nil && *status.LastError != "" {
		parts = append(parts, *status.LastError)
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
