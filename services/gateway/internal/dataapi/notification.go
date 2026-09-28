package dataapi

import "time"

type NotificationEvent struct {
	Type       string    `json:"type"`
	ResourceID string    `json:"resource_id"`
	VehicleID  *string   `json:"vehicle_id"`
	Reason     *string   `json:"reason"`
	OccurredAt time.Time `json:"occurred_at"`
}

type NotificationLease struct {
	DeliveryID         string            `json:"delivery_id"`
	Event              NotificationEvent `json:"event"`
	RecipientMaxUserID string            `json:"recipient_max_user_id"`
	LeaseToken         string            `json:"lease_token"`
	LeaseExpiresAt     time.Time         `json:"lease_expires_at"`
	Attempt            int               `json:"attempt"`
}

type NotificationClaim struct {
	Items []NotificationLease `json:"items"`
}
