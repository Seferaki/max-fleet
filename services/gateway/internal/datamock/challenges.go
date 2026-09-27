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
	if !hasFields(command.Payload, "purpose", "intent_payload") || !hasFields(fields["intent_payload"], "operation", "target_id", "expected_version") || !strictPayload(command.Payload, &payload) || payload.Purpose != "take" || payload.IntentPayload.Operation != "checkout.create" || !validUUID(payload.IntentPayload.TargetID) || payload.IntentPayload.ExpectedVersion < 1 {
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
	expires := now.Add(5 * time.Minute)
	if checkout.ExpiresAt.Before(expires) {
		expires = checkout.ExpiresAt
	}
	challenge := dataapi.Challenge{ID: newRequestID(), Purpose: "take", Question: fmt.Sprintf("%d + %d = ?", a, b), Options: options, ExpiresAt: expires, AttemptsRemaining: 3, Version: 1, UpdatedAt: now}
	for id, old := range s.challenges {
		if old.CheckoutID == checkout.ID && !old.Solved && !old.Invalidated {
			old.Invalidated = true
			s.challenges[id] = old
		}
	}
	s.challenges[challenge.ID] = mockChallenge{Public: challenge, Actor: actor, CheckoutID: checkout.ID, CheckoutVersion: checkout.Version, CorrectOption: correctOption}
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
		s.fail(w, requestID, http.StatusConflict, "CHALLENGE_EXPIRED")
		return dataapi.CommandResult{}, false
	}
	checkout := s.checkouts[challenge.CheckoutID]
	if challenge.Invalidated || challenge.Solved || challenge.Public.AttemptsRemaining == 0 || checkout.Status != "holding" || checkout.Version != challenge.CheckoutVersion {
		s.fail(w, requestID, http.StatusConflict, "INVALID_STATE")
		return dataapi.CommandResult{}, false
	}
	now := s.now().UTC()
	correct := payload.SelectedOption == challenge.CorrectOption
	if !correct {
		challenge.Public.AttemptsRemaining--
	} else {
		challenge.Solved = true
		checkout.IntentConfirmedAt = &now
		checkout.Step = "rules"
		checkout.Version++
		checkout.UpdatedAt = now
		s.checkouts[checkout.ID] = checkout
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
