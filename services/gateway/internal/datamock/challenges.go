package datamock

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type mockChallenge struct {
	Public            dataapi.Challenge `json:"public"`
	Actor             string            `json:"actor"`
	CheckoutID        string            `json:"checkout_id"`
	CheckoutVersion   int64             `json:"checkout_version"`
	ReturnID          string            `json:"return_id,omitempty"`
	ReturnVersion     int64             `json:"return_version,omitempty"`
	AdminIntentSHA256 string            `json:"admin_intent_sha256,omitempty"`
	AdminTargetID     string            `json:"admin_target_id,omitempty"`
	CorrectOption     int               `json:"correct_option"`
	Solved            bool              `json:"solved"`
	Invalidated       bool              `json:"invalidated"`
	ProofConsumed     bool              `json:"proof_consumed,omitempty"`
}

type challengeCreatePayload struct {
	Purpose       string          `json:"purpose"`
	IntentPayload json.RawMessage `json:"intent_payload"`
}

type takeChallengeIntent struct {
	Operation       string `json:"operation"`
	TargetID        string `json:"target_id"`
	ExpectedVersion int64  `json:"expected_version"`
}

type adminChallengeIntent struct {
	Operation       string                  `json:"operation"`
	TargetID        *string                 `json:"target_id"`
	ExpectedVersion *int64                  `json:"expected_version"`
	Reason          *string                 `json:"reason,omitempty"`
	ReviewCompleted *bool                   `json:"review_completed,omitempty"`
	MaxUserID       *string                 `json:"max_user_id,omitempty"`
	DisplayName     *string                 `json:"display_name,omitempty"`
	CanStartTrip    *bool                   `json:"can_start_trip,omitempty"`
	AvailableData   *dataapi.AdminCloseData `json:"available_data,omitempty"`
}

type answerPayload struct {
	SelectedOption int `json:"selected_option"`
}

type rulesPayload struct {
	RulesVersionID string `json:"rules_version_id"`
}

func strictPayload(raw json.RawMessage, value any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value) == nil && decoder.Decode(new(any)) == io.EOF
}

func hasFields(raw json.RawMessage, names ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

func hasExactFields(raw json.RawMessage, names ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}

var adminChallengeOperations = map[string]string{
	"vehicle_block":   "vehicle.block",
	"vehicle_unblock": "vehicle.unblock",
	"employee_grant":  "employee.grant",
	"employee_access": "employee.access",
	"admin_close":     "trip.admin_close",
}

func adminIntentFields(purpose string) []string {
	switch purpose {
	case "vehicle_block":
		return []string{"operation", "target_id", "expected_version", "reason"}
	case "vehicle_unblock":
		return []string{"operation", "target_id", "expected_version", "reason", "review_completed"}
	case "employee_grant":
		return []string{"operation", "target_id", "expected_version", "max_user_id", "display_name"}
	case "employee_access":
		return []string{"operation", "target_id", "expected_version", "can_start_trip", "reason"}
	case "admin_close":
		return []string{"operation", "target_id", "expected_version", "reason"}
	default:
		return nil
	}
}

func (intent adminChallengeIntent) asMap() map[string]any {
	fields := map[string]any{
		"operation":        intent.Operation,
		"target_id":        intent.TargetID,
		"expected_version": intent.ExpectedVersion,
	}
	if intent.Reason != nil {
		fields["reason"] = *intent.Reason
	}
	if intent.ReviewCompleted != nil {
		fields["review_completed"] = *intent.ReviewCompleted
	}
	if intent.MaxUserID != nil {
		fields["max_user_id"] = *intent.MaxUserID
	}
	if intent.DisplayName != nil {
		fields["display_name"] = *intent.DisplayName
	}
	if intent.CanStartTrip != nil {
		fields["can_start_trip"] = *intent.CanStartTrip
	}
	if intent.AvailableData != nil {
		data := make(map[string]any)
		if intent.AvailableData.FuelLevel != nil {
			data["fuel_level"] = *intent.AvailableData.FuelLevel
		}
		if intent.AvailableData.OdometerKM != nil {
			data["odometer_km"] = *intent.AvailableData.OdometerKM
		}
		if intent.AvailableData.Latitude != nil {
			data["latitude"] = *intent.AvailableData.Latitude
		}
		if intent.AvailableData.Longitude != nil {
			data["longitude"] = *intent.AvailableData.Longitude
		}
		if intent.AvailableData.Landmark != nil {
			data["landmark"] = *intent.AvailableData.Landmark
		}
		if intent.AvailableData.KeysReturned != nil {
			data["keys_returned"] = *intent.AvailableData.KeysReturned
		}
		if intent.AvailableData.CarLocked != nil {
			data["car_locked"] = *intent.AvailableData.CarLocked
		}
		fields["available_data"] = data
	}
	return fields
}

func adminIntentHash(intent adminChallengeIntent) (string, error) {
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(intent.asMap()); err != nil {
		return "", err
	}
	encoded := strings.TrimSuffix(canonical.String(), "\n")
	encoded = strings.ReplaceAll(encoded, `\u2028`, "\u2028")
	encoded = strings.ReplaceAll(encoded, `\u2029`, "\u2029")
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:]), nil
}

func validAdminIntent(purpose string, intent adminChallengeIntent) bool {
	if intent.Operation != adminChallengeOperations[purpose] || !hasExactAdminIntentPointers(purpose, intent) {
		return false
	}
	if purpose == "employee_grant" {
		return intent.TargetID == nil && intent.ExpectedVersion == nil && intent.AvailableData == nil && intent.MaxUserID != nil && validMaxID(*intent.MaxUserID) && intent.DisplayName != nil && len([]rune(*intent.DisplayName)) >= 1 && len([]rune(*intent.DisplayName)) <= 200
	}
	if intent.TargetID == nil || !validUUID(*intent.TargetID) || intent.ExpectedVersion == nil || *intent.ExpectedVersion < 1 {
		return false
	}
	switch purpose {
	case "vehicle_block":
		return intent.Reason != nil && len([]rune(*intent.Reason)) >= 1 && len([]rune(*intent.Reason)) <= 1000 && intent.AvailableData == nil
	case "admin_close":
		return intent.Reason != nil && len([]rune(*intent.Reason)) >= 1 && len([]rune(*intent.Reason)) <= 1000 && validMockAdminCloseData(intent.AvailableData)
	case "vehicle_unblock":
		return intent.Reason != nil && len([]rune(*intent.Reason)) >= 1 && len([]rune(*intent.Reason)) <= 1000 && intent.ReviewCompleted != nil && *intent.ReviewCompleted && intent.AvailableData == nil
	case "employee_access":
		return intent.CanStartTrip != nil && intent.Reason != nil && len([]rune(*intent.Reason)) >= 1 && len([]rune(*intent.Reason)) <= 1000 && intent.AvailableData == nil
	default:
		return false
	}
}

func hasExactAdminIntentPointers(purpose string, intent adminChallengeIntent) bool {
	fields := adminIntentFields(purpose)
	if len(fields) == 0 {
		return false
	}
	expected := make(map[string]bool, len(fields))
	for _, field := range fields {
		expected[field] = true
	}
	present := map[string]bool{
		"target_id":        intent.TargetID != nil,
		"expected_version": intent.ExpectedVersion != nil,
		"reason":           intent.Reason != nil,
		"review_completed": intent.ReviewCompleted != nil,
		"max_user_id":      intent.MaxUserID != nil,
		"display_name":     intent.DisplayName != nil,
		"can_start_trip":   intent.CanStartTrip != nil,
		"available_data":   intent.AvailableData != nil,
	}
	if purpose == "admin_close" && intent.AvailableData != nil {
		expected["available_data"] = true
	}
	for field, value := range present {
		if field == "target_id" || field == "expected_version" {
			if purpose == "employee_grant" {
				if value {
					return false
				}
				continue
			}
		}
		if expected[field] != value {
			return false
		}
	}
	return true
}

func validAdminIntentPayloadShape(purpose string, raw json.RawMessage, fields []string) bool {
	if purpose != "admin_close" || hasExactFields(raw, fields...) {
		return hasExactFields(raw, fields...)
	}
	withData := append(append([]string(nil), fields...), "available_data")
	return hasFields(raw, withData...)
}

func (s *Server) createChallenge(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload challengeCreatePayload
	if !hasFields(command.Payload, "purpose", "intent_payload") || !strictPayload(command.Payload, &payload) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if _, isAdmin := adminChallengeOperations[payload.Purpose]; isAdmin {
		return s.createAdminChallenge(w, requestID, actor, command, payload)
	}
	if payload.Purpose != "take" && payload.Purpose != "return" {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	var intent takeChallengeIntent
	if !hasFields(payload.IntentPayload, "operation", "target_id", "expected_version") || !strictPayload(payload.IntentPayload, &intent) || !validUUID(intent.TargetID) || intent.ExpectedVersion < 1 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	record := mockChallenge{Actor: actor}
	expires := s.now().UTC().Add(5 * time.Minute)
	if payload.Purpose == "take" {
		if intent.Operation != "checkout.create" {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return dataapi.CommandResult{}, false
		}
		checkout, found := s.checkouts[command.TargetID]
		if !found || checkout.EmployeeID != s.employees[actor].ID {
			s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
			return dataapi.CommandResult{}, false
		}
		if checkout.Version != command.Version {
			s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", checkout.Version)
			return dataapi.CommandResult{}, false
		}
		if checkout.Status != "holding" || checkout.IntentConfirmedAt != nil || checkout.VehicleID != intent.TargetID {
			s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
			return dataapi.CommandResult{}, false
		}
		vehicleVersion := int64(0)
		for _, vehicle := range s.vehicles {
			if vehicle.ID == checkout.VehicleID {
				vehicleVersion = vehicle.Version - 1 // checkout.create advanced the vehicle version.
				break
			}
		}
		if intent.ExpectedVersion != vehicleVersion {
			s.fail(w, requestID, http.StatusConflict, "STALE_VERSION")
			return dataapi.CommandResult{}, false
		}
		if checkout.ExpiresAt.Before(expires) {
			expires = checkout.ExpiresAt
		}
		record.CheckoutID, record.CheckoutVersion = checkout.ID, checkout.Version
	} else {
		if intent.Operation != "trip.begin_return" {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return dataapi.CommandResult{}, false
		}
		draft, found := s.returns[command.TargetID]
		trip := s.trips[draft.TripID]
		if !found || trip.EmployeeID != s.employees[actor].ID {
			s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
			return dataapi.CommandResult{}, false
		}
		if draft.Version != command.Version {
			s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", draft.Version)
			return dataapi.CommandResult{}, false
		}
		if draft.Status != "draft" || draft.IntentConfirmedAt != nil || trip.Status != "returning" || trip.ReturnID == nil || *trip.ReturnID != draft.ID || trip.ID != intent.TargetID {
			s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
			return dataapi.CommandResult{}, false
		}
		if intent.ExpectedVersion != trip.Version-1 {
			s.fail(w, requestID, http.StatusConflict, "STALE_VERSION")
			return dataapi.CommandResult{}, false
		}
		record.ReturnID, record.ReturnVersion = draft.ID, draft.Version
	}
	now := s.now().UTC()
	challenge, correctOption, err := newMathChallenge(payload.Purpose, expires, now)
	if err != nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "TEMPORARY_FAILURE")
		return dataapi.CommandResult{}, false
	}
	for id, old := range s.challenges {
		if (record.CheckoutID != "" && old.CheckoutID == record.CheckoutID || record.ReturnID != "" && old.ReturnID == record.ReturnID) && !old.Solved && !old.Invalidated {
			old.Invalidated = true
			s.challenges[id] = old
		}
	}
	record.Public = challenge
	record.CorrectOption = correctOption
	s.challenges[challenge.ID] = record
	return commandResult("challenge.create", challenge), true
}

func newMathChallenge(purpose string, expires, now time.Time) (dataapi.Challenge, int, error) {
	first, err := rand.Int(rand.Reader, big.NewInt(9))
	if err != nil {
		return dataapi.Challenge{}, 0, err
	}
	second, err := rand.Int(rand.Reader, big.NewInt(9))
	if err != nil {
		return dataapi.Challenge{}, 0, err
	}
	a, b := int(first.Int64())+1, int(second.Int64())+1
	sum := a + b
	start := max(0, min(sum-2, 15))
	options := []int{start, start + 1, start + 2, start + 3}
	for i := len(options) - 1; i > 0; i-- {
		pick, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return dataapi.Challenge{}, 0, err
		}
		j := int(pick.Int64())
		options[i], options[j] = options[j], options[i]
	}
	correctOption := 0
	for i, option := range options {
		if option == sum {
			correctOption = i
			break
		}
	}
	challenge := dataapi.Challenge{ID: newRequestID(), Purpose: purpose, Question: fmt.Sprintf("%d + %d = ?", a, b), Options: options, ExpiresAt: expires, AttemptsRemaining: 3, Version: 1, UpdatedAt: now}
	return challenge, correctOption, nil
}

func (s *Server) createAdminChallenge(w http.ResponseWriter, requestID, actor string, command mockCommand, payload challengeCreatePayload) (dataapi.CommandResult, bool) {
	fields := adminIntentFields(payload.Purpose)
	var intent adminChallengeIntent
	if !validAdminIntentPayloadShape(payload.Purpose, payload.IntentPayload, fields) || !strictPayload(payload.IntentPayload, &intent) || !validAdminIntent(payload.Purpose, intent) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if s.employees[actor].Role != "admin" {
		s.fail(w, requestID, http.StatusForbidden, "ADMIN_REQUIRED")
		return dataapi.CommandResult{}, false
	}
	if intent.Operation != adminChallengeOperations[payload.Purpose] || !commandMatchesIntentTarget(command, intent) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	if !s.checkAdminIntentTarget(w, requestID, intent) {
		return dataapi.CommandResult{}, false
	}
	intentHash, err := adminIntentHash(intent)
	if err != nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "TEMPORARY_FAILURE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	expires := now.Add(5 * time.Minute)
	challenge, correctOption, err := newMathChallenge(payload.Purpose, expires, now)
	if err != nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "TEMPORARY_FAILURE")
		return dataapi.CommandResult{}, false
	}
	targetID := ""
	if intent.TargetID != nil {
		targetID = *intent.TargetID
	}
	for id, old := range s.challenges {
		if old.Actor == actor && old.AdminIntentSHA256 != "" && old.Public.Purpose == payload.Purpose && old.AdminTargetID == targetID && !old.Solved && !old.Invalidated {
			old.Invalidated = true
			old.Public.Version++
			old.Public.UpdatedAt = now
			s.challenges[id] = old
		}
	}
	record := mockChallenge{Public: challenge, Actor: actor, AdminIntentSHA256: intentHash, AdminTargetID: targetID, CorrectOption: correctOption}
	s.challenges[challenge.ID] = record
	return commandResult("challenge.create", challenge), true
}

func commandMatchesIntentTarget(command mockCommand, intent adminChallengeIntent) bool {
	if intent.TargetID == nil || intent.ExpectedVersion == nil {
		return intent.TargetID == nil && intent.ExpectedVersion == nil && command.NullTarget && command.NullVersion
	}
	return !command.NullTarget && !command.NullVersion && command.TargetID == *intent.TargetID && command.Version == *intent.ExpectedVersion
}

func (s *Server) checkAdminIntentTarget(w http.ResponseWriter, requestID string, intent adminChallengeIntent) bool {
	if intent.Operation == "employee.grant" {
		return true
	}
	if intent.TargetID == nil || intent.ExpectedVersion == nil {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return false
	}
	targetID, expectedVersion := *intent.TargetID, *intent.ExpectedVersion
	switch intent.Operation {
	case "vehicle.block", "vehicle.unblock":
		for _, vehicle := range s.vehicles {
			if vehicle.ID == targetID {
				if vehicle.Version != expectedVersion {
					s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", vehicle.Version)
					return false
				}
				return true
			}
		}
	case "employee.access":
		for _, employee := range s.employees {
			if employee.ID == targetID {
				if employee.Version != expectedVersion {
					s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", employee.Version)
					return false
				}
				return true
			}
		}
	case "trip.admin_close":
		trip, found := s.trips[targetID]
		if found {
			if trip.Version != expectedVersion {
				s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", trip.Version)
				return false
			}
			return true
		}
	}
	s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
	return false
}

func (s *Server) answerChallenge(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload answerPayload
	if !hasFields(command.Payload, "selected_option") || !strictPayload(command.Payload, &payload) || payload.SelectedOption < 0 || payload.SelectedOption > 3 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	challenge, found := s.challenges[command.TargetID]
	if !found || challenge.Actor != actor {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if challenge.Public.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", challenge.Public.Version)
		return dataapi.CommandResult{}, false
	}
	if !s.now().UTC().Before(challenge.Public.ExpiresAt) {
		s.fail(w, requestID, http.StatusUnprocessableEntity, "CHALLENGE_EXPIRED")
		return dataapi.CommandResult{}, false
	}
	if challenge.Invalidated || challenge.Solved || challenge.Public.AttemptsRemaining == 0 {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	checkout := s.checkouts[challenge.CheckoutID]
	draft := s.returns[challenge.ReturnID]
	if challenge.Public.Purpose == "take" && (checkout.Status != "holding" || checkout.Version != challenge.CheckoutVersion) || challenge.Public.Purpose == "return" && (draft.Status != "draft" || draft.Version != challenge.ReturnVersion) {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	correct := payload.SelectedOption == challenge.CorrectOption
	var proofID *string
	if !correct {
		challenge.Public.AttemptsRemaining--
	} else {
		challenge.Solved = true
		if challenge.AdminIntentSHA256 != "" {
			id := challenge.Public.ID
			proofID = &id
		} else if challenge.Public.Purpose == "take" {
			checkout.IntentConfirmedAt = &now
			checkout.Step = "rules"
			checkout.Version++
			checkout.UpdatedAt = now
			s.checkouts[checkout.ID] = checkout
		} else {
			draft.IntentConfirmedAt = &now
			draft.Step = "checklist"
			draft.Version++
			draft.UpdatedAt = now
			s.returns[draft.ID] = draft
		}
	}
	challenge.Public.Version++
	challenge.Public.UpdatedAt = now
	s.challenges[challenge.Public.ID] = challenge
	result := commandResult("challenge.answer", challenge.Public)
	result.Correct = &correct
	result.ChallengeProofID = proofID
	remaining := challenge.Public.AttemptsRemaining
	result.AttemptsRemaining = &remaining
	return result, true
}

func (s *Server) acceptRules(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload rulesPayload
	if !hasFields(command.Payload, "rules_version_id") || !strictPayload(command.Payload, &payload) || !validUUID(payload.RulesVersionID) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	checkout, found := s.checkouts[command.TargetID]
	if !found || checkout.EmployeeID != s.employees[actor].ID {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return dataapi.CommandResult{}, false
	}
	if checkout.Version != command.Version {
		s.failVersion(w, requestID, http.StatusConflict, "STALE_VERSION", checkout.Version)
		return dataapi.CommandResult{}, false
	}
	if checkout.Status != "holding" || checkout.IntentConfirmedAt == nil || checkout.RulesAcceptedAt != nil {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	if payload.RulesVersionID != s.rules.ID {
		s.fail(w, requestID, http.StatusConflict, "STALE_VERSION")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	checkout.RulesVersionID = &payload.RulesVersionID
	checkout.RulesAcceptedAt = &now
	checkout.Step = "inspection"
	checkout.Version++
	checkout.UpdatedAt = now
	s.checkouts[checkout.ID] = checkout
	return commandResult("checkout.accept_rules", checkout), true
}
