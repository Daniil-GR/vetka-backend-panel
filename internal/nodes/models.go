package nodes

import "time"

type Node struct {
	ID                   string
	NodeID               string
	Name                 string
	Domain               string
	APIURL               string
	ProtocolType         string
	NodeSecret           string
	Enabled              bool
	DesiredConfigVersion int64
	LastAppliedVersion   int64
	LastSeenAt           *time.Time
	LastStatus           *string
	LastError            *string
	LastSyncAt           *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type CreateNodeInput struct {
	NodeID       string `json:"node_id"`
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	APIURL       string `json:"api_url"`
	ProtocolType string `json:"protocol_type"`
	NodeSecret   string `json:"node_secret"`
	Enabled      bool   `json:"enabled"`
}

type UpdateNodeInput = CreateNodeInput

type SyncEvent struct {
	ID            string
	NodeID        string
	ConfigVersion int64
	Status        string
	HTTPStatus    *int
	RequestJSON   []byte
	ResponseJSON  []byte
	Error         *string
	CreatedAt     time.Time
}

type AgentUser struct {
	ID        string            `json:"id"`
	Username  string            `json:"username"`
	Password  string            `json:"password"`
	Enabled   bool              `json:"enabled"`
	ExpiresAt *time.Time        `json:"expires_at,omitempty"`
	QuotaMB   int               `json:"quota_mb"`
	Protocols []string          `json:"protocols"`
	Meta      map[string]string `json:"meta"`
}

type SyncPayload struct {
	NodeID        string      `json:"node_id"`
	ConfigVersion int64       `json:"config_version"`
	ProtocolType  string      `json:"protocol_type"`
	Users         []AgentUser `json:"users"`
}

type AgentResponse struct {
	OK              bool   `json:"ok"`
	NodeID          string `json:"node_id,omitempty"`
	CurrentVersion  int64  `json:"current_version,omitempty"`
	AppliedVersion  int64  `json:"applied_version,omitempty"`
	ReceivedVersion int64  `json:"received_version,omitempty"`
	Status          string `json:"status,omitempty"`
	Changed         bool   `json:"changed,omitempty"`
	Message         string `json:"message,omitempty"`
	Error           string `json:"error,omitempty"`
}
