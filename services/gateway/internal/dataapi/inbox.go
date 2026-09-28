package dataapi

import "time"

// NormalizedEvent is the durable, private-chat event stored by POST /inbox.
// Photo bytes and provider URLs are deliberately absent.
type NormalizedEvent struct {
	IntegrationKey string            `json:"integration_key"`
	EventKey       string            `json:"event_key"`
	EventType      string            `json:"event_type"`
	ActorMaxUserID string            `json:"actor_max_user_id"`
	ChatID         string            `json:"chat_id"`
	MessageID      *string           `json:"message_id"`
	CallbackID     *string           `json:"callback_id"`
	OccurredAt     time.Time         `json:"occurred_at"`
	Payload        NormalizedPayload `json:"payload"`
}

type NormalizedPayload struct {
	Kind            string   `json:"kind"`
	Text            *string  `json:"text"`
	CallbackData    *string  `json:"callback_data"`
	PhotoSourceKey  *string  `json:"photo_source_key"`
	Latitude        *float64 `json:"latitude"`
	Longitude       *float64 `json:"longitude"`
	AttachmentCount int      `json:"attachment_count"`
}

type InboxStored struct {
	ID        string    `json:"id"`
	Duplicate bool      `json:"duplicate"`
	StoredAt  time.Time `json:"stored_at"`
}

type InboxClaimItem struct {
	ID             string          `json:"id"`
	Event          NormalizedEvent `json:"event"`
	LeaseToken     string          `json:"lease_token"`
	LeaseExpiresAt time.Time       `json:"lease_expires_at"`
	Attempt        int             `json:"attempt"`
}

type InboxClaim struct {
	Items []InboxClaimItem `json:"items"`
}

type QueueTransition struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}
