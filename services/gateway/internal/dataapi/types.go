package dataapi

import (
	"time"
)

// DTOs mirror the read projections of contracts/data-api.openapi.yaml v1.4.
// Nullable fields use pointers so absent values remain distinct from zero values.
type Meta struct {
	ContractVersion string   `json:"contract_version"`
	BuildSHA        string   `json:"build_sha"`
	Mode            string   `json:"mode"`
	Capabilities    []string `json:"capabilities"`
}

type Employee struct {
	ID           string    `json:"id"`
	MaxUserID    string    `json:"max_user_id"`
	DisplayName  string    `json:"display_name"`
	Role         string    `json:"role"`
	CanStartTrip bool      `json:"can_start_trip"`
	ActiveTripID *string   `json:"active_trip_id"`
	Version      int64     `json:"version"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Me struct {
	Allowed   bool      `json:"allowed"`
	MaxUserID string    `json:"max_user_id"`
	Employee  *Employee `json:"employee"`
}

type Rules struct {
	ID           string `json:"id"`
	VersionLabel string `json:"version_label"`
	Body         string `json:"body"`
}

type Challenge struct {
	ID                string    `json:"id"`
	Purpose           string    `json:"purpose"`
	Question          string    `json:"question"`
	Options           []int     `json:"options"`
	ExpiresAt         time.Time `json:"expires_at"`
	AttemptsRemaining int       `json:"attempts_remaining"`
	Version           int64     `json:"version"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ParkingLocation struct {
	ID          string    `json:"id"`
	Latitude    float64   `json:"latitude"`
	Longitude   float64   `json:"longitude"`
	Source      string    `json:"source"`
	Landmark    *string   `json:"landmark"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

type Vehicle struct {
	ID                     string           `json:"id"`
	Plate                  string           `json:"plate"`
	Make                   string           `json:"make"`
	Model                  string           `json:"model"`
	Description            string           `json:"description"`
	KeyInstructions        string           `json:"key_instructions"`
	Status                 string           `json:"status"`
	ManualBlocked          bool             `json:"manual_blocked"`
	NeedsReview            bool             `json:"needs_review"`
	CurrentParking         *ParkingLocation `json:"current_parking"`
	CurrentFuel            *int             `json:"current_fuel"`
	CurrentOdometerKM      *int64           `json:"current_odometer_km"`
	FuelConfirmedAt        *time.Time       `json:"fuel_confirmed_at"`
	OdometerConfirmedAt    *time.Time       `json:"odometer_confirmed_at"`
	KnownNonblockingIssues []string         `json:"known_nonblocking_issues"`
	Version                int64            `json:"version"`
	UpdatedAt              time.Time        `json:"updated_at"`
}

type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type Inspection struct {
	ID                string     `json:"id"`
	Phase             string     `json:"phase"`
	Status            string     `json:"status"`
	FuelLevel         *int       `json:"fuel_level"`
	OdometerKM        *int64     `json:"odometer_km"`
	NewDamage         *bool      `json:"new_damage"`
	CabinClean        *bool      `json:"cabin_clean"`
	ParkingAllowed    *bool      `json:"parking_allowed"`
	KeysReturned      *bool      `json:"keys_returned"`
	CarLocked         *bool      `json:"car_locked"`
	OccupiedSlots     []int      `json:"occupied_slots"`
	MissingSlots      []int      `json:"missing_slots"`
	PhotosConfirmedAt *time.Time `json:"photos_confirmed_at"`
	Version           int64      `json:"version"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type Checkout struct {
	ID                string     `json:"id"`
	VehicleID         string     `json:"vehicle_id"`
	EmployeeID        string     `json:"employee_id"`
	Status            string     `json:"status"`
	Step              string     `json:"step"`
	ExpiresAt         time.Time  `json:"expires_at"`
	IntentConfirmedAt *time.Time `json:"intent_confirmed_at"`
	RulesVersionID    *string    `json:"rules_version_id"`
	RulesAcceptedAt   *time.Time `json:"rules_accepted_at"`
	NoNewIssues       *bool      `json:"no_new_issues"`
	Inspection        Inspection `json:"inspection"`
	Version           int64      `json:"version"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type Trip struct {
	ID               string           `json:"id"`
	VehicleID        string           `json:"vehicle_id"`
	EmployeeID       string           `json:"employee_id"`
	CheckoutID       string           `json:"checkout_id"`
	Status           string           `json:"status"`
	StartedAt        time.Time        `json:"started_at"`
	EndedAt          *time.Time       `json:"ended_at"`
	ReturnID         *string          `json:"return_id"`
	MissingData      []string         `json:"missing_data"`
	BeforeInspection Inspection       `json:"before_inspection"`
	AfterInspection  *Inspection      `json:"after_inspection"`
	ParkingLocation  *ParkingLocation `json:"parking_location"`
	Issues           []Issue          `json:"issues"`
	Version          int64            `json:"version"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

type Issue struct {
	ID             string    `json:"id"`
	VehicleID      string    `json:"vehicle_id"`
	AuthorID       string    `json:"author_id"`
	Stage          string    `json:"stage"`
	Category       string    `json:"category"`
	Description    string    `json:"description"`
	Status         string    `json:"status"`
	BlocksIssuance bool      `json:"blocks_issuance"`
	TripID         *string   `json:"trip_id"`
	InspectionID   *string   `json:"inspection_id"`
	AssetIDs       []string  `json:"asset_ids"`
	Version        int64     `json:"version"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Return struct {
	ID                string           `json:"id"`
	TripID            string           `json:"trip_id"`
	Status            string           `json:"status"`
	Step              string           `json:"step"`
	IntentConfirmedAt *time.Time       `json:"intent_confirmed_at"`
	ParkingLocation   *ParkingLocation `json:"parking_location"`
	Inspection        Inspection       `json:"inspection"`
	Version           int64            `json:"version"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

type CurrentState struct {
	Checkout            *Checkout     `json:"checkout"`
	Trip                *Trip         `json:"trip"`
	Return              *Return       `json:"return"`
	NextStep            *string       `json:"next_step"`
	Conversation        *Conversation `json:"conversation"`
	ConversationVersion int64         `json:"conversation_version"`
}

type ConversationContext struct {
	TargetID       *string  `json:"target_id,omitempty"`
	VehicleID      *string  `json:"vehicle_id,omitempty"`
	VehicleVersion *int64   `json:"vehicle_version,omitempty"`
	IssueCategory  *string  `json:"issue_category,omitempty"`
	DraftText      *string  `json:"draft_text,omitempty"`
	AssetIDs       []string `json:"asset_ids,omitempty"`
	IssueID        *string  `json:"issue_id,omitempty"`
	SelectedSlot   *int     `json:"selected_slot,omitempty"`
	ChallengeID    *string  `json:"challenge_id,omitempty"`
	TripID         *string  `json:"trip_id,omitempty"`
	ReturnID       *string  `json:"return_id,omitempty"`
	Cursor         *string  `json:"cursor,omitempty"`
}

type Conversation struct {
	Flow             string              `json:"flow"`
	Step             string              `json:"step"`
	Context          ConversationContext `json:"context"`
	PendingInputKind *string             `json:"pending_input_kind"`
	Version          int64               `json:"version"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

type PhotoUploadResult struct {
	AssetID    string     `json:"asset_id"`
	SHA256     string     `json:"sha256"`
	Inspection Inspection `json:"inspection"`
}

type StagedAsset struct {
	AssetID   string    `json:"asset_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type AdminSummary struct {
	Available   int `json:"available"`
	Holding     int `json:"holding"`
	ActiveTrips int `json:"active_trips"`
	Returning   int `json:"returning"`
	NeedsReview int `json:"needs_review"`
	OpenIssues  int `json:"open_issues"`
}

type ErrorDetails struct {
	CurrentVersion *int64 `json:"current_version,omitempty"`
	MissingSlots   []int  `json:"missing_slots,omitempty"`
}

type APIError struct {
	Status    int          `json:"-"`
	Code      string       `json:"code"`
	Message   string       `json:"message"`
	Retryable bool         `json:"retryable"`
	Details   ErrorDetails `json:"details"`
}

func (e *APIError) Error() string {
	return "data-api: " + e.Code
}
