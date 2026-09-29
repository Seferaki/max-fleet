package datamock

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

const adminActorID = "8000000000000000003"

func postRawCommand(t *testing.T, mock *Server, actor, key, operation string, target any, version any, payload any) (int, *dataapi.CommandResult, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"operation": operation, "target_id": target, "expected_version": version, "payload": payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/commands", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer test-service-token")
	request.Header.Set("X-Contract-Version", dataapi.ContractVersion)
	request.Header.Set("X-Request-ID", "11111111-1111-4111-8111-111111111111")
	request.Header.Set("X-Actor-Max-ID", actor)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	mock.Handler().ServeHTTP(response, request)
	var envelope struct {
		Data  *dataapi.CommandResult `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode command response: %v; body=%s", err, response.Body.String())
	}
	return response.Code, envelope.Data, envelope.Error.Code
}

func TestAdminChallengeAnswerReturnsPersistentProof(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	path := t.TempDir() + "/state.json"
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	intent := map[string]any{
		"operation": "vehicle.block", "target_id": firstVehicleID,
		"expected_version": 1, "reason": "Проверка тормозов",
	}
	createPayload := map[string]any{"purpose": "vehicle_block", "intent_payload": intent}
	status, created, code := postRawCommand(t, mock, driverID, "nonadmin-create-1", "challenge.create", firstVehicleID, 1, createPayload)
	if status != http.StatusForbidden || code != "ADMIN_REQUIRED" || created != nil {
		t.Fatalf("non-admin challenge create: status=%d code=%s data=%+v", status, code, created)
	}
	status, created, code = postRawCommand(t, mock, adminActorID, "admin-create-1", "challenge.create", firstVehicleID, 1, createPayload)
	if status != http.StatusOK || code != "" || created == nil {
		t.Fatalf("admin challenge create: status=%d code=%s data=%+v", status, code, created)
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](*created)
	if err != nil || challenge.Purpose != "vehicle_block" || challenge.AttemptsRemaining != 3 {
		t.Fatalf("admin challenge: %+v %v", challenge, err)
	}
	_, wrongActorAnswer, wrongActorCode := postRawCommand(t, mock, driverID, "foreign-answer-1", "challenge.answer", challenge.ID, challenge.Version, map[string]any{"selected_option": 0})
	if wrongActorAnswer != nil || wrongActorCode != "NOT_FOUND" {
		t.Fatalf("foreign answer leaked challenge: %+v %s", wrongActorAnswer, wrongActorCode)
	}
	correct, _ := challengeChoices(t, challenge)

	// A solved admin proof must survive restart before the final domain command consumes it.
	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	status, answered, code := postRawCommand(t, restarted, adminActorID, "admin-answer-1", "challenge.answer", challenge.ID, challenge.Version, map[string]any{"selected_option": correct})
	if status != http.StatusOK || code != "" || answered == nil || answered.Correct == nil || !*answered.Correct || answered.ChallengeProofID == nil || *answered.ChallengeProofID != challenge.ID {
		t.Fatalf("admin challenge answer: status=%d code=%s data=%+v", status, code, answered)
	}
	status, repeated, code := postRawCommand(t, restarted, adminActorID, "admin-answer-1", "challenge.answer", challenge.ID, challenge.Version, map[string]any{"selected_option": correct})
	if status != http.StatusOK || code != "" || repeated == nil || repeated.ChallengeProofID == nil || *repeated.ChallengeProofID != challenge.ID {
		t.Fatalf("idempotent proof answer: status=%d code=%s data=%+v", status, code, repeated)
	}
	stored := restarted.challenges[challenge.ID]
	if !stored.Solved || stored.ProofConsumed || stored.AdminIntentSHA256 == "" || stored.Actor != adminActorID {
		t.Fatalf("persisted admin proof state: %+v", stored)
	}
}

func TestAdminChallengeIntentRequiresExactFieldsAndMatchingTarget(t *testing.T) {
	mock, err := NewWithClock("test-service-token", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"operation": "vehicle.block", "target_id": firstVehicleID,
		"expected_version": 1, "reason": "Проверка тормозов",
	}
	cases := []struct {
		name    string
		purpose string
		actor   string
		target  any
		version any
		intent  map[string]any
		status  int
		code    string
	}{
		{name: "missing required reason", actor: adminActorID, target: firstVehicleID, version: 1, intent: map[string]any{"operation": "vehicle.block", "target_id": firstVehicleID, "expected_version": 1}, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "reject extra key", actor: adminActorID, target: firstVehicleID, version: 1, intent: map[string]any{"operation": "vehicle.block", "target_id": firstVehicleID, "expected_version": 1, "reason": "Проверка", "unexpected": true}, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "purpose operation mismatch", purpose: "vehicle_unblock", actor: adminActorID, target: firstVehicleID, version: 1, intent: map[string]any{"operation": "vehicle.block", "target_id": firstVehicleID, "expected_version": 1, "reason": "Проверка", "review_completed": true}, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "admin close rejects null available data", purpose: "admin_close", actor: adminActorID, target: "50000000-0000-4000-8000-000000000001", version: 1, intent: map[string]any{"operation": "trip.admin_close", "target_id": "50000000-0000-4000-8000-000000000001", "expected_version": 1, "reason": "Проверка", "available_data": nil}, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "outer target mismatch", actor: adminActorID, target: "10000000-0000-4000-8000-000000000002", version: 1, intent: base, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "outer version mismatch", actor: adminActorID, target: firstVehicleID, version: 2, intent: base, status: http.StatusBadRequest, code: "INVALID_REQUEST"},
		{name: "stale object version", actor: adminActorID, target: firstVehicleID, version: 2, intent: map[string]any{"operation": "vehicle.block", "target_id": firstVehicleID, "expected_version": 2, "reason": "Проверка"}, status: http.StatusConflict, code: "STALE_VERSION"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			purpose := tc.purpose
			if purpose == "" {
				purpose = "vehicle_block"
			}
			payload := map[string]any{"purpose": purpose, "intent_payload": tc.intent}
			status, result, code := postRawCommand(t, mock, tc.actor, "invalid-intent-"+string(rune('a'+index)), "challenge.create", tc.target, tc.version, payload)
			if status != tc.status || code != tc.code || result != nil {
				t.Fatalf("got status=%d code=%s result=%+v; want %d %s", status, code, result, tc.status, tc.code)
			}
		})
	}
}

func TestEmployeeGrantChallengeUsesNullAggregateTarget(t *testing.T) {
	mock, err := NewWithClock("test-service-token", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	intent := map[string]any{
		"operation": "employee.grant", "target_id": nil, "expected_version": nil,
		"max_user_id": "8000000000000000099", "display_name": "Сотрудник примера",
	}
	payload := map[string]any{"purpose": "employee_grant", "intent_payload": intent}
	status, result, code := postRawCommand(t, mock, adminActorID, "grant-challenge-1", "challenge.create", nil, nil, payload)
	if status != http.StatusOK || code != "" || result == nil {
		t.Fatalf("grant challenge: status=%d code=%s data=%+v", status, code, result)
	}
	challenge, err := dataapi.DecodeAggregate[dataapi.Challenge](*result)
	if err != nil || challenge.Purpose != "employee_grant" {
		t.Fatalf("grant challenge aggregate: %+v %v", challenge, err)
	}
}

func TestAdminIntentHashMatchesPythonCanonicalJSON(t *testing.T) {
	reason := "Проверка тормозов <>&"
	target := firstVehicleID
	version := int64(7)
	intent := adminChallengeIntent{Operation: "vehicle.block", TargetID: &target, ExpectedVersion: &version, Reason: &reason}
	got, err := adminIntentHash(intent)
	if err != nil {
		t.Fatal(err)
	}
	const want = "31e423b9ca2878be6b48263dd2dcf1d68049464d84bde64a547c5783163222d6"
	if got != want {
		t.Fatalf("canonical intent hash = %s, want %s", got, want)
	}
	tripID, tripVersion := "50000000-0000-4000-8000-000000000001", int64(7)
	closeReason := "Водитель недоступен"
	fuel, odometer := 75, int64(42149)
	keysReturned, carLocked := false, false
	latitude, longitude, landmark := 55.75, 37.61, "У ворот"
	closeIntent := adminChallengeIntent{
		Operation: "trip.admin_close", TargetID: &tripID, ExpectedVersion: &tripVersion, Reason: &closeReason,
		AvailableData: &dataapi.AdminCloseData{FuelLevel: &fuel, OdometerKM: &odometer, KeysReturned: &keysReturned, CarLocked: &carLocked, Latitude: &latitude, Longitude: &longitude, Landmark: &landmark},
	}
	got, err = adminIntentHash(closeIntent)
	if err != nil {
		t.Fatal(err)
	}
	const wantClose = "17e43ca8d7820e2d23415ed25e0de5974d9d9e40c1a46ebc70890defe59b9c47"
	if got != wantClose {
		t.Fatalf("canonical admin-close intent hash = %s, want %s", got, wantClose)
	}
}
