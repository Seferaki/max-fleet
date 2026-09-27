package dataapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestPhotoUploadRetryKeepsMultipartAndKey(t *testing.T) {
	var calls int
	var firstBody []byte
	var firstRequestID string
	image := []byte("synthetic-image-bytes")
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/internal/v1/inspections/40000000-0000-4000-8000-000000000001/photos/3" || r.Header.Get("Idempotency-Key") != "photo-key-1" || r.Header.Get("X-Actor-Max-ID") != "900001" {
			t.Error("incorrect photo request")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if calls == 1 {
			firstBody = body
			firstRequestID = r.Header.Get("X-Request-ID")
		} else if !bytes.Equal(firstBody, body) || r.Header.Get("X-Request-ID") != firstRequestID {
			t.Error("photo retry changed body or request ID")
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err := r.ParseMultipartForm(11 << 20); err != nil {
			t.Error(err)
		}
		if r.FormValue("expected_version") != "2" || r.FormValue("source_event_key") != "message-3" {
			t.Error("incorrect photo fields")
		}
		file, header, err := r.FormFile("image")
		if err != nil {
			t.Error(err)
		} else {
			defer file.Close()
			content, _ := io.ReadAll(file)
			if !bytes.Equal(content, image) || header.Header.Get("Content-Type") != "image/jpeg" {
				t.Error("incorrect photo bytes or MIME")
			}
		}
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"STORAGE_UNAVAILABLE","message":"later","retryable":true},"request_id":"` + testRequestID + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"asset_id":"50000000-0000-4000-8000-000000000001","sha256":"` + strings.Repeat("a", 64) + `","inspection":{"id":"40000000-0000-4000-8000-000000000001"}},"request_id":"` + testRequestID + `"}`))
	})
	result, err := c.UploadInspectionPhoto(context.Background(), "900001", InspectionPhotoInput{InspectionID: "40000000-0000-4000-8000-000000000001", Slot: 3, Version: 2, SourceEventKey: "message-3", IdempotencyKey: "photo-key-1", ContentType: "image/jpeg", Image: image})
	if err != nil || calls != 2 || result.AssetID != "50000000-0000-4000-8000-000000000001" {
		t.Fatalf("upload: calls=%d result=%+v err=%v", calls, result, err)
	}
}

func TestPhotoUploadRejectsInvalidInputAndDuplicateError(t *testing.T) {
	var calls int
	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"DUPLICATE_PHOTO","message":"duplicate","retryable":false},"request_id":"` + testRequestID + `"}`))
	})
	input := InspectionPhotoInput{InspectionID: "40000000-0000-4000-8000-000000000001", Slot: 3, Version: 2, SourceEventKey: "message-3", IdempotencyKey: "photo-key-2", ContentType: "image/jpeg", Image: []byte("synthetic")}
	input.Slot = 9
	if _, err := c.UploadInspectionPhoto(context.Background(), "900001", input); err == nil {
		t.Fatal("slot 9 accepted")
	}
	input.Slot = 3
	input.Image = make([]byte, (10<<20)+1)
	if _, err := c.UploadInspectionPhoto(context.Background(), "900001", input); err == nil {
		t.Fatal("large photo accepted")
	}
	input.Image = []byte("synthetic")
	_, err := c.UploadInspectionPhoto(context.Background(), "900001", input)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "DUPLICATE_PHOTO" || calls != 1 {
		t.Fatalf("duplicate: calls=%d err=%v", calls, err)
	}
}

func TestStageIssueAssetRetriesSameMultipart(t *testing.T) {
	var calls int
	var firstBody []byte
	var firstRequestID string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/internal/v1/assets/stage" || r.Header.Get("Idempotency-Key") != "stage-issue-key-1" || r.Header.Get("X-Actor-Max-ID") != "900001" {
			t.Error("incorrect stage request")
		}
		body, _ := io.ReadAll(r.Body)
		if calls == 1 {
			firstBody, firstRequestID = body, r.Header.Get("X-Request-ID")
		} else if !bytes.Equal(body, firstBody) || r.Header.Get("X-Request-ID") != firstRequestID {
			t.Error("stage retry changed body or request ID")
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err := r.ParseMultipartForm(11 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("purpose") != "issue" || r.FormValue("scope_type") != "inspection" || r.FormValue("scope_id") != "40000000-0000-4000-8000-000000000001" || r.FormValue("source_event_key") != "issue-photo-1" {
			t.Error("incorrect stage fields")
		}
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"STORAGE_UNAVAILABLE","message":"later","retryable":true},"request_id":"` + testRequestID + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"asset_id":"50000000-0000-4000-8000-000000000001","expires_at":"2026-09-27T09:30:00Z"},"request_id":"` + testRequestID + `"}`))
	})
	input := IssueStageInput{ScopeType: "inspection", ScopeID: "40000000-0000-4000-8000-000000000001", SourceEventKey: "issue-photo-1", IdempotencyKey: "stage-issue-key-1", ContentType: "image/png", Image: []byte("synthetic")}
	result, err := c.StageIssueAsset(context.Background(), "900001", input)
	if err != nil || calls != 2 || result.AssetID != "50000000-0000-4000-8000-000000000001" {
		t.Fatalf("stage: calls=%d result=%+v err=%v", calls, result, err)
	}
	input.ScopeType = "other"
	if _, err := c.StageIssueAsset(context.Background(), "900001", input); err == nil {
		t.Fatal("unknown issue scope accepted")
	}
}
