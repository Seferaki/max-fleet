package dataapi

import (
	"encoding/json"
	"time"
)

// DTOs mirror the read projections of contracts/data-api.openapi.yaml v1.0.
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

type CurrentState struct {
	Checkout            *Checkout       `json:"checkout"`
	Trip                json.RawMessage `json:"trip"`
	Return              json.RawMessage `json:"return"`
	NextStep            *string         `json:"next_step"`
	ConversationVersion int64           `json:"conversation_version"`
}

type PhotoUploadResult struct {
	AssetID    string     `json:"asset_id"`
	SHA256     string     `json:"sha256"`
	Inspection Inspection `json:"inspection"`
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
