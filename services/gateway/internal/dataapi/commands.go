package dataapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strings"
)

// InboxLease is supplied only for a command arising from a durable inbox event.
// The token is opaque and must never be logged.
type InboxLease struct {
	EventID string
	Token   string
}

type CommandResult struct {
	Operation         string          `json:"operation"`
	Aggregate         json.RawMessage `json:"aggregate"`
	Correct           *bool           `json:"correct"`
	AttemptsRemaining *int            `json:"attempts_remaining"`
	ChallengeProofID  *string         `json:"challenge_proof_id"`
}

// DecodeAggregate decodes the operation's confirmed aggregate into its DTO.
func DecodeAggregate[T any](result CommandResult) (T, error) {
	var value T
	if len(result.Aggregate) == 0 || string(result.Aggregate) == "null" {
		return value, errors.New("data-api: missing command aggregate")
	}
	if err := decodeStrict(result.Aggregate, &value); err != nil {
		return value, err
	}
	return value, nil
}

type commandEnvelope[P any] struct {
	Operation       string `json:"operation"`
	TargetID        string `json:"target_id"`
	ExpectedVersion int64  `json:"expected_version"`
	Payload         P      `json:"payload"`
}

type emptyPayload struct{}

type LocationInput struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Source    string  `json:"source"`
	Landmark  *string `json:"landmark,omitempty"`
	Confirmed bool    `json:"confirmed"`
}

type attestationPayload struct {
	Attestation bool `json:"attestation"`
}

type challengeIntent struct {
	Operation       string `json:"operation"`
	TargetID        string `json:"target_id"`
	ExpectedVersion int64  `json:"expected_version"`
}

type challengeCreatePayload struct {
	Purpose       string          `json:"purpose"`
	IntentPayload challengeIntent `json:"intent_payload"`
}

type challengeAnswerPayload struct {
	SelectedOption int `json:"selected_option"`
}

type acceptRulesPayload struct {
	RulesVersionID string `json:"rules_version_id"`
}

type InspectionUpdateInput struct {
	FuelLevel      *int   `json:"fuel_level,omitempty"`
	OdometerKM     *int64 `json:"odometer_km,omitempty"`
	NewDamage      *bool  `json:"new_damage,omitempty"`
	CabinClean     *bool  `json:"cabin_clean,omitempty"`
	ParkingAllowed *bool  `json:"parking_allowed,omitempty"`
	KeysReturned   *bool  `json:"keys_returned,omitempty"`
	CarLocked      *bool  `json:"car_locked,omitempty"`
}

type IssueCreateInput struct {
	Category     string   `json:"category"`
	Description  string   `json:"description"`
	TripID       *string  `json:"trip_id,omitempty"`
	InspectionID *string  `json:"inspection_id,omitempty"`
	AssetIDs     []string `json:"asset_ids"`
}

func validFuel(level int) bool {
	return level == 0 || level == 25 || level == 50 || level == 75 || level == 100
}

// CheckoutCreate creates a 15-minute hold. Python/mock owns the availability transaction.
func (c *Client) CheckoutCreate(ctx context.Context, actorMaxID, vehicleID string, vehicleVersion int64, key string, inbox *InboxLease) (CommandResult, error) {
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[emptyPayload]{"checkout.create", vehicleID, vehicleVersion, emptyPayload{}})
}

func (c *Client) CheckoutCancel(ctx context.Context, actorMaxID, checkoutID string, version int64, key string, inbox *InboxLease) (CommandResult, error) {
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[emptyPayload]{"checkout.cancel", checkoutID, version, emptyPayload{}})
}

// ChallengeCreateTake binds a math question to the current hold and vehicle intent.
func (c *Client) ChallengeCreateTake(ctx context.Context, actorMaxID, checkoutID string, checkoutVersion int64, vehicleID string, vehicleVersion int64, key string, inbox *InboxLease) (CommandResult, error) {
	if !validUUID(vehicleID) || vehicleVersion < 1 {
		return CommandResult{}, errors.New("data-api: invalid vehicle intent")
	}
	payload := challengeCreatePayload{Purpose: "take", IntentPayload: challengeIntent{Operation: "checkout.create", TargetID: vehicleID, ExpectedVersion: vehicleVersion}}
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[challengeCreatePayload]{"challenge.create", checkoutID, checkoutVersion, payload})
}

func (c *Client) ChallengeCreateReturn(ctx context.Context, actorMaxID, returnID string, returnVersion int64, tripID string, tripVersion int64, key string, inbox *InboxLease) (CommandResult, error) {
	if !validUUID(tripID) || tripVersion < 1 {
		return CommandResult{}, errors.New("data-api: invalid trip intent")
	}
	payload := challengeCreatePayload{Purpose: "return", IntentPayload: challengeIntent{Operation: "trip.begin_return", TargetID: tripID, ExpectedVersion: tripVersion}}
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[challengeCreatePayload]{"challenge.create", returnID, returnVersion, payload})
}

func (c *Client) ChallengeAnswer(ctx context.Context, actorMaxID, challengeID string, version int64, selectedOption int, key string, inbox *InboxLease) (CommandResult, error) {
	if selectedOption < 0 || selectedOption > 3 {
		return CommandResult{}, errors.New("data-api: invalid challenge option")
	}
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[challengeAnswerPayload]{"challenge.answer", challengeID, version, challengeAnswerPayload{selectedOption}})
}

func (c *Client) CheckoutAcceptRules(ctx context.Context, actorMaxID, checkoutID string, version int64, rulesVersionID string, key string, inbox *InboxLease) (CommandResult, error) {
	if !validUUID(rulesVersionID) {
		return CommandResult{}, errors.New("data-api: invalid rules version")
	}
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[acceptRulesPayload]{"checkout.accept_rules", checkoutID, version, acceptRulesPayload{rulesVersionID}})
}

func (c *Client) InspectionUpdate(ctx context.Context, actorMaxID, inspectionID string, version int64, input InspectionUpdateInput, key string, inbox *InboxLease) (CommandResult, error) {
	if input.FuelLevel == nil && input.OdometerKM == nil && input.NewDamage == nil && input.CabinClean == nil && input.ParkingAllowed == nil && input.KeysReturned == nil && input.CarLocked == nil ||
		input.FuelLevel != nil && !validFuel(*input.FuelLevel) || input.OdometerKM != nil && *input.OdometerKM < 0 {
		return CommandResult{}, errors.New("data-api: invalid inspection input")
	}
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[InspectionUpdateInput]{"inspection.update", inspectionID, version, input})
}

func (c *Client) CheckoutSetNoNewIssues(ctx context.Context, actorMaxID, checkoutID string, version int64, key string, inbox *InboxLease) (CommandResult, error) {
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[struct {
		Value bool `json:"value"`
	}]{"checkout.set_no_new_issues", checkoutID, version, struct {
		Value bool `json:"value"`
	}{true}})
}

func (c *Client) CheckoutStart(ctx context.Context, actorMaxID, checkoutID string, version int64, key string, inbox *InboxLease) (CommandResult, error) {
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[attestationPayload]{"checkout.start", checkoutID, version, attestationPayload{true}})
}

func (c *Client) TripBeginReturn(ctx context.Context, actorMaxID, tripID string, version int64, key string, inbox *InboxLease) (CommandResult, error) {
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[emptyPayload]{"trip.begin_return", tripID, version, emptyPayload{}})
}

func (c *Client) ReturnCancel(ctx context.Context, actorMaxID, returnID string, version int64, key string, inbox *InboxLease) (CommandResult, error) {
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[emptyPayload]{"return.cancel", returnID, version, emptyPayload{}})
}

func (c *Client) IssueCreate(ctx context.Context, actorMaxID, vehicleID string, vehicleVersion int64, input IssueCreateInput, key string, inbox *InboxLease) (CommandResult, error) {
	if (input.TripID == nil) == (input.InspectionID == nil) || strings.TrimSpace(input.Description) == "" || len(input.Description) > 1000 || len(input.AssetIDs) > 3 ||
		input.Category != "body_damage" && input.Category != "mechanical" && input.Category != "cleanliness" && input.Category != "keys" && input.Category != "other" {
		return CommandResult{}, errors.New("data-api: invalid issue input")
	}
	if input.TripID != nil && !validUUID(*input.TripID) || input.InspectionID != nil && !validUUID(*input.InspectionID) {
		return CommandResult{}, errors.New("data-api: invalid issue context")
	}
	seen := make(map[string]bool)
	for _, id := range input.AssetIDs {
		if !validUUID(id) || seen[id] {
			return CommandResult{}, errors.New("data-api: invalid issue asset")
		}
		seen[id] = true
	}
	if input.AssetIDs == nil {
		input.AssetIDs = []string{}
	}
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[IssueCreateInput]{"issue.create", vehicleID, vehicleVersion, input})
}

func (c *Client) InspectionConfirmPhotos(ctx context.Context, actorMaxID, inspectionID string, version int64, key string, inbox *InboxLease) (CommandResult, error) {
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[emptyPayload]{"inspection.confirm_photos", inspectionID, version, emptyPayload{}})
}

func (c *Client) ReturnSetLocation(ctx context.Context, actorMaxID, returnID string, version int64, key string, inbox *InboxLease, location LocationInput) (CommandResult, error) {
	if math.IsNaN(location.Latitude) || math.IsInf(location.Latitude, 0) || location.Latitude < -90 || location.Latitude > 90 || math.IsNaN(location.Longitude) || math.IsInf(location.Longitude, 0) || location.Longitude < -180 || location.Longitude > 180 || !location.Confirmed || (location.Source != "max_geo" && location.Source != "manual_map" && location.Source != "admin") || (location.Landmark != nil && len(*location.Landmark) > 500) {
		return CommandResult{}, errors.New("data-api: invalid confirmed location")
	}
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[LocationInput]{"return.set_location", returnID, version, location})
}

func (c *Client) ReturnComplete(ctx context.Context, actorMaxID, returnID string, version int64, key string, inbox *InboxLease) (CommandResult, error) {
	return executeCommand(ctx, c, actorMaxID, key, inbox, commandEnvelope[attestationPayload]{"return.complete", returnID, version, attestationPayload{true}})
}

func executeCommand[P any](ctx context.Context, c *Client, actorMaxID, key string, inbox *InboxLease, command commandEnvelope[P]) (CommandResult, error) {
	if !validMaxID(actorMaxID) || !validUUID(command.TargetID) || command.ExpectedVersion < 1 || !validKey(key) || !validInbox(inbox) {
		return CommandResult{}, errors.New("data-api: invalid command identity, version or lease")
	}
	body, err := json.Marshal(command)
	if err != nil {
		return CommandResult{}, errors.New("data-api: invalid command body")
	}
	result, err := request[CommandResult](ctx, c, "POST", "/commands", actorMaxID, body, "application/json", key, inbox, nil)
	if err != nil {
		return CommandResult{}, err
	}
	if result.Operation != command.Operation || len(result.Aggregate) == 0 || string(result.Aggregate) == "null" {
		return CommandResult{}, errors.New("data-api: invalid command result")
	}
	return result, nil
}

// OwnCommandResult resolves a timed-out command using the same actor and key.
func (c *Client) OwnCommandResult(ctx context.Context, actorMaxID, key, operation string) (CommandResult, error) {
	if !validMaxID(actorMaxID) || !validKey(key) || !knownOperation(operation) {
		return CommandResult{}, errors.New("data-api: invalid command lookup")
	}
	result, err := get[CommandResult](ctx, c, "/commands/"+url.PathEscape(key)+"?operation="+url.QueryEscape(operation), actorMaxID)
	if err != nil {
		return CommandResult{}, err
	}
	if result.Operation != operation || len(result.Aggregate) == 0 || string(result.Aggregate) == "null" {
		return CommandResult{}, errors.New("data-api: invalid command result")
	}
	return result, nil
}

func validKey(key string) bool {
	return len(key) >= 8 && len(key) <= 200 && !strings.ContainsAny(key, "\r\n/\\?#")
}

func validInbox(inbox *InboxLease) bool {
	return inbox == nil || (validUUID(inbox.EventID) && len(inbox.Token) > 0 && len(inbox.Token) <= 200 && !strings.ContainsAny(inbox.Token, "\r\n"))
}

func knownOperation(operation string) bool {
	switch operation {
	case "checkout.create", "checkout.cancel", "challenge.create", "challenge.answer", "checkout.accept_rules", "inspection.update", "inspection.confirm_photos", "checkout.set_no_new_issues", "checkout.start", "trip.begin_return", "return.cancel", "return.set_location", "return.complete", "issue.create", "vehicle.block", "vehicle.unblock", "vehicle.edit", "vehicle.correct_snapshot", "vehicle.annotate", "employee.grant", "employee.access", "issue.resolve", "trip.admin_close", "conversation.save":
		return true
	}
	return false
}
