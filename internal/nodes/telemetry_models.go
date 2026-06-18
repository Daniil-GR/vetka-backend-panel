package nodes

import "time"

type TelemetryResponse struct {
	OK                         bool                  `json:"ok"`
	NodeID                     string                `json:"node_id"`
	ProtocolType               string                `json:"protocol_type"`
	GeneratedAt                *time.Time            `json:"generated_at"`
	CollectorStatus            string                `json:"collector_status"`
	SessionTTLSeconds          int64                 `json:"session_ttl_seconds"`
	HistoryTTLSeconds          int64                 `json:"history_ttl_seconds"`
	LastSuccessfulCollectionAt *time.Time            `json:"last_successful_collection_at"`
	Capabilities               TelemetryCapabilities `json:"capabilities"`
	Warnings                   []string              `json:"warnings"`
	Sessions                   []TelemetrySession    `json:"sessions"`
}

type TelemetryCapabilities struct {
	ClientIP        bool `json:"client_ip"`
	PerUserActivity bool `json:"per_user_activity"`
	TrafficCounters bool `json:"traffic_counters"`
	FirstSeen       bool `json:"first_seen"`
	LastSeen        bool `json:"last_seen"`
}

type TelemetrySession struct {
	UserID             string     `json:"user_id"`
	ProtocolUsername   string     `json:"protocol_username"`
	UserPresentInCache bool       `json:"user_present_in_cache"`
	Protocol           string     `json:"protocol"`
	ClientIP           *string    `json:"client_ip"`
	FirstSeenAt        *time.Time `json:"first_seen_at"`
	LastSeenAt         *time.Time `json:"last_seen_at"`
	UploadBytes        int64      `json:"upload_bytes"`
	DownloadBytes      int64      `json:"download_bytes"`
	Active             bool       `json:"active"`
	IPObserved         bool       `json:"ip_observed"`
	TrafficObserved    bool       `json:"traffic_observed"`
	Source             string     `json:"source"`
	TrafficScope       string     `json:"traffic_scope"`
}
