package dataapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const commandVehicleID = "10000000-0000-4000-8000-000000000001"

func TestCommandRetryKeepsKeyBodyAndLease(t *testing.T) {
	var calls int
	var firstBody, firstRequestID string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/commands" || r.Header.Get("Idempotency-Key") != "stable-key-1" || r.Header.Get("X-Actor-Max-ID") != "900001" || r.Header.Get("X-Inbox-Event-ID") != testRequestID || r.Header.Get("X-Inbox-Lease") != "lease-test" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect command request")
		}
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if calls == 1 {
			firstBody = string(body)
			firstRequestID = r.Header.Get("X-Request-ID")
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"TEMPORARY_FAILURE","message":"later","retryable":true},"request_id":"` + testRequestID + `"}`))
			return
		}
		if string(body) != firstBody || r.Header.Get("X-Request-ID") != firstRequestID {
			t.Error("retry changed body or request ID")
		}
		_, _ = w.Write([]byte(`{"data":{"operation":"checkout.create","aggregate":{"id":"` + commandVehicleID + `"},"correct":null,"attempts_remaining":null,"challenge_proof_id":null},"request_id":"` + testRequestID + `"}`))
	})
	result, err := c.CheckoutCreate(context.Background(), "900001", commandVehicleID, 1, "stable-key-1", &InboxLease{testRequestID, "lease-test"})
	if err != nil || calls != 2 || result.Operation != "checkout.create" || !strings.Contains(firstBody, `"expected_version":1`) {
		t.Fatalf("command retry: calls=%d result=%+v err=%v", calls, result, err)
	}
}

func TestCommandConflictNotRetried(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"STALE_VERSION","message":"stale","retryable":false,"details":{"current_version":2}},"request_id":"` + testRequestID + `"}`))
	})
	_, err := c.CheckoutCreate(context.Background(), "900001", commandVehicleID, 1, "stable-key-2", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "STALE_VERSION" || calls != 1 {
		t.Fatalf("conflict: calls=%d err=%v", calls, err)
	}
}

func TestInvalidCommandNeverSent(t *testing.T) {
	c := newTestClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid command reached server") })
	if _, err := c.CheckoutCreate(context.Background(), "900001", commandVehicleID, 0, "stable-key-3", nil); err == nil {
		t.Fatal("zero version accepted")
	}
	if _, err := c.CheckoutCreate(context.Background(), "900001", commandVehicleID, 1, "short", nil); err == nil {
		t.Fatal("short idempotency key accepted")
	}
	if _, err := c.CheckoutCreate(context.Background(), "900001", commandVehicleID, 1, "stable-key-4", &InboxLease{testRequestID, ""}); err == nil {
		t.Fatal("empty lease accepted")
	}
	if _, err := c.ReturnSetLocation(context.Background(), "900001", commandVehicleID, 1, "stable-key-5", nil, LocationInput{Latitude: 55, Longitude: 37, Source: "manual_map", Confirmed: false}); err == nil {
		t.Fatal("unconfirmed map point accepted")
	}
}

func TestOwnCommandResultUsesActorAndOperation(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/commands/stable-key-6" || r.URL.Query().Get("operation") != "return.complete" || r.Header.Get("X-Actor-Max-ID") != "900001" {
			t.Error("incorrect lookup")
		}
		_, _ = w.Write([]byte(`{"data":{"operation":"return.complete","aggregate":{"id":"` + commandVehicleID + `"},"correct":null,"attempts_remaining":null,"challenge_proof_id":null},"request_id":"` + testRequestID + `"}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := c.OwnCommandResult(ctx, "900001", "stable-key-6", "return.complete")
	if err != nil || result.Operation != "return.complete" {
		t.Fatalf("lookup: %+v %v", result, err)
	}
}

func TestTakeChallengeAndRulesCommands(t *testing.T) {
	var operations []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, want := range []string{`"target_id":"` + commandVehicleID + `"`, `"expected_version":1`} {
			if !strings.Contains(text, want) {
				t.Errorf("command missing %s: %s", want, text)
			}
		}
		var operation string
		switch {
		case strings.Contains(text, `"operation":"challenge.create"`):
			operation = "challenge.create"
			if !strings.Contains(text, `"purpose":"take"`) || !strings.Contains(text, `"intent_payload"`) {
				t.Errorf("invalid take challenge: %s", text)
			}
		case strings.Contains(text, `"operation":"challenge.answer"`):
			operation = "challenge.answer"
			if !strings.Contains(text, `"selected_option":2`) {
				t.Errorf("invalid answer: %s", text)
			}
		case strings.Contains(text, `"operation":"checkout.accept_rules"`):
			operation = "checkout.accept_rules"
			if !strings.Contains(text, `"rules_version_id":"`+commandVehicleID+`"`) {
				t.Errorf("invalid rules version: %s", text)
			}
		default:
			t.Errorf("unexpected command: %s", text)
		}
		operations = append(operations, operation)
		_, _ = w.Write([]byte(`{"data":{"operation":"` + operation + `","aggregate":{"id":"` + commandVehicleID + `"}},"request_id":"` + testRequestID + `"}`))
	})
	ctx := context.Background()
	if _, err := c.ChallengeCreateTake(ctx, "900001", commandVehicleID, 1, commandVehicleID, 1, "challenge-create-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ChallengeAnswer(ctx, "900001", commandVehicleID, 1, 2, "challenge-answer-1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CheckoutAcceptRules(ctx, "900001", commandVehicleID, 1, commandVehicleID, "accept-rules-1", nil); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 3 {
		t.Fatalf("sent %d commands", len(operations))
	}
	if _, err := c.ChallengeAnswer(ctx, "900001", commandVehicleID, 1, 4, "invalid-answer", nil); err == nil {
		t.Fatal("invalid option accepted")
	}
}
