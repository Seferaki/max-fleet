package dataapi

import "time"

// Integration exposes polling state without provider credentials.
type Integration struct {
	Key            string     `json:"key"`
	Mode           string     `json:"mode"`
	Marker         *string    `json:"marker"`
	LeaseExpiresAt *time.Time `json:"lease_expires_at"`
	Version        int64      `json:"version"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type IntegrationLease struct {
	LeaseToken     string      `json:"lease_token"`
	LeaseExpiresAt time.Time   `json:"lease_expires_at"`
	Integration    Integration `json:"integration"`
}
