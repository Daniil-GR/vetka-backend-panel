package telemetry

import (
	"time"

	"vetka-backend-panel/internal/nodes"
)

type Query struct {
	IncludeRecent bool
	Search        string
	Protocol      string
	Status        string
}

type Summary struct {
	ActiveSessions   int
	UniqueActiveIPs  int
	CollectorsOK     int
	CollectorsIssues int
}

type SessionView struct {
	NodeDBID               string
	NodeID                 string
	NodeName               string
	NodeProtocol           string
	BackendUserID          string
	BackendUsername        string
	BackendDisplayName     *string
	UserKnown              bool
	MaskedProtocolUsername string
	ClientIP               string
	Active                 bool
	FirstSeenAt            *time.Time
	LastSeenAt             *time.Time
	UploadBytes            int64
	DownloadBytes          int64
	Source                 string
	TrafficScope           string
	UserPresentInCache     bool
	IPObserved             bool
	TrafficObserved        bool
	searchProtocolUsername string
	searchBackendDisplay   string
}

type NodeCollectorView struct {
	NodeDBID                   string
	NodeID                     string
	NodeName                   string
	NodeProtocol               string
	NodeEnabled                bool
	CollectorStatus            string
	LastSuccessfulCollectionAt *time.Time
	Capabilities               nodes.TelemetryCapabilities
	Warnings                   []string
	Error                      string
	ConfigurationIssue         string
	Sessions                   []SessionView
	SkippedReason              string
}

type AllSessionsResult struct {
	Rows          []SessionView
	Nodes         []NodeCollectorView
	Summary       Summary
	IncludeRecent bool
}

type NodeSessionsResult struct {
	Node          NodeCollectorView
	IncludeRecent bool
}

type UserSessionsResult struct {
	Rows          []SessionView
	Nodes         []NodeCollectorView
	Summary       Summary
	IncludeRecent bool
}
