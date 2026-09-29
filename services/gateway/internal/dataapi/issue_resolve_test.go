package dataapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestIssueResolveClientPayloadDependsOnStatus(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var envelope map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		var operation string
		if err := json.Unmarshal(envelope["operation"], &operation); err != nil || operation != "issue.resolve" {
			t.Fatalf("operation = %q, %v", operation, err)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(envelope["payload"], &payload); err != nil {
			t.Fatal(err)
		}
		if calls == 1 {
			var status string
			if err := json.Unmarshal(payload["status"], &status); err != nil || status != "in_progress" || len(payload) != 1 {
				t.Fatalf("take-in-work payload must contain only status: %s", envelope["payload"])
			}
		} else {
			var status string
			if err := json.Unmarshal(payload["status"], &status); err != nil || status != "resolved" ||
				len(payload) != 3 || payload["comment"] == nil || string(payload["confirmation"]) != "true" {
				t.Fatalf("terminal payload must contain comment and confirmation: %s", envelope["payload"])
			}
		}
		_, _ = io.WriteString(w, `{"data":{"operation":"issue.resolve","aggregate":{"id":"`+commandVehicleID+`"},"correct":null,"attempts_remaining":null,"challenge_proof_id":null},"request_id":"`+testRequestID+`"}`)
	})

	ctx := context.Background()
	if _, err := c.IssueResolve(ctx, "900001", commandVehicleID, 1, IssueResolveInput{Status: "in_progress"}, "issue-resolve-take-work-01", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.IssueResolve(ctx, "900001", commandVehicleID, 2, IssueResolveInput{Status: "resolved", Comment: "Проверено", Confirmation: true}, "issue-resolve-terminal-01", nil); err != nil {
		t.Fatal(err)
	}
	for _, input := range []IssueResolveInput{
		{Status: "in_progress", Comment: "Не предусмотрено"},
		{Status: "resolved", Confirmation: true},
		{Status: "known_nonblocking", Comment: "Комментарий без подтверждения"},
	} {
		if _, err := c.IssueResolve(ctx, "900001", commandVehicleID, 3, input, "invalid", nil); err == nil {
			t.Fatalf("invalid issue action accepted: %+v", input)
		}
	}
	if calls != 2 {
		t.Fatalf("sent %d commands, want only the two valid requests", calls)
	}
}
