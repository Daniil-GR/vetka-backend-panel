package users

import "time"

type User struct {
	ID                string
	Username          string
	DisplayName       *string
	Enabled           bool
	ExpiresAt         *time.Time
	SubscriptionToken string
	Notes             *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type CreateUserInput struct {
	Username    string     `json:"username"`
	DisplayName *string    `json:"display_name"`
	Enabled     bool       `json:"enabled"`
	ExpiresAt   *time.Time `json:"expires_at"`
	Notes       *string    `json:"notes"`
	NodeIDs     []string   `json:"node_ids"`
}

type UpdateUserInput = CreateUserInput

type Access struct {
	ID               string
	UserID           string
	NodeID           string
	ProtocolType     string
	ProtocolUsername string
	ProtocolPassword string
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type AccessWithUser struct {
	Access
	Username      string
	UserEnabled   bool
	UserExpiresAt *time.Time
}

type UserWithAccess struct {
	User
	Access []Access
}
