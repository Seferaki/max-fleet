package datamock

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type mockChallenge struct {
	Public          dataapi.Challenge `json:"public"`
	Actor           string            `json:"actor"`
	CheckoutID      string            `json:"checkout_id"`
	CheckoutVersion int64             `json:"checkout_version"`
	ReturnID        string            `json:"return_id,omitempty"`
	ReturnVersion   int64             `json:"return_version,omitempty"`
	CorrectOption   int               `json:"correct_option"`
	Solved          bool              `json:"solved"`
	Invalidated     bool              `json:"invalidated"`
}

type takeChallengePayload struct {
	Purpose       string `json:"purpose"`
	IntentPayload struct {
		Operation       string `json:"operation"`
		TargetID        string `json:"target_id"`
		ExpectedVersion int64  `json:"expected_version"`
	} `json:"intent_payload"`
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

func (s *Server) createChallenge(w http.ResponseWriter, requestID, actor string, command mockCommand) (dataapi.CommandResult, bool) {
	var payload takeChallengePayload
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(command.Payload, &fields)
	if !hasFields(command.Payload, "purpose", "intent_payload") || !hasFields(fields["intent_payload"], "operation", "target_id", "expected_version") || !strictPayload(command.Payload, &payload) || (payload.Purpose != "take" && payload.Purpose != "return") || !validUUID(payload.IntentPayload.TargetID) || payload.IntentPayload.ExpectedVersion < 1 {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return dataapi.CommandResult{}, false
	}
	record := mockChallenge{Actor: actor}
	expires := s.now().UTC().Add(5 * time.Minute)
	if payload.Purpose == "take" {
		if payload.IntentPayload.Operation != "checkout.create" {
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
		if checkout.Status != "holding" || checkout.IntentConfirmedAt != nil || checkout.VehicleID != payload.IntentPayload.TargetID {
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
		if payload.IntentPayload.ExpectedVersion != vehicleVersion {
			s.fail(w, requestID, http.StatusConflict, "STALE_VERSION")
			return dataapi.CommandResult{}, false
		}
		if checkout.ExpiresAt.Before(expires) {
			expires = checkout.ExpiresAt
		}
		record.CheckoutID, record.CheckoutVersion = checkout.ID, checkout.Version
	} else {
		if payload.IntentPayload.Operation != "trip.begin_return" {
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
		if draft.Status != "draft" || draft.IntentConfirmedAt != nil || trip.Status != "returning" || trip.ReturnID == nil || *trip.ReturnID != draft.ID || trip.ID != payload.IntentPayload.TargetID {
			s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
			return dataapi.CommandResult{}, false
		}
		if payload.IntentPayload.ExpectedVersion != trip.Version-1 {
			s.fail(w, requestID, http.StatusConflict, "STALE_VERSION")
			return dataapi.CommandResult{}, false
		}
		record.ReturnID, record.ReturnVersion = draft.ID, draft.Version
	}
	first, err := rand.Int(rand.Reader, big.NewInt(9))
	if err != nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "TEMPORARY_FAILURE")
		return dataapi.CommandResult{}, false
	}
	second, err := rand.Int(rand.Reader, big.NewInt(9))
	if err != nil {
		s.fail(w, requestID, http.StatusServiceUnavailable, "TEMPORARY_FAILURE")
		return dataapi.CommandResult{}, false
	}
	a, b := int(first.Int64())+1, int(second.Int64())+1
	sum := a + b
	start := max(0, min(sum-2, 15))
	options := []int{start, start + 1, start + 2, start + 3}
	for i := len(options) - 1; i > 0; i-- {
		pick, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			s.fail(w, requestID, http.StatusServiceUnavailable, "TEMPORARY_FAILURE")
			return dataapi.CommandResult{}, false
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
	now := s.now().UTC()
	challenge := dataapi.Challenge{ID: newRequestID(), Purpose: payload.Purpose, Question: fmt.Sprintf("%d + %d = ?", a, b), Options: options, ExpiresAt: expires, AttemptsRemaining: 3, Version: 1, UpdatedAt: now}
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
	if !correct {
		challenge.Public.AttemptsRemaining--
	} else {
		challenge.Solved = true
		if challenge.Public.Purpose == "take" {
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
