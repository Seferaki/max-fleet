package datamock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func syntheticPNG(t *testing.T, tone uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{tone, 10, 20, 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestEightPhotoSlotsReplaceAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	clock := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	mock, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	created, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "eight-photo-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	version := checkout.Inspection.Version
	for slot := 1; slot <= 7; slot++ {
		result, err := client.UploadInspectionPhoto(ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: checkout.Inspection.ID, Slot: slot, Version: version, SourceEventKey: fmt.Sprintf("message-%d", slot), IdempotencyKey: fmt.Sprintf("photo-key-%d", slot), ContentType: "image/png", Image: syntheticPNG(t, uint8(slot))})
		if err != nil {
			t.Fatalf("slot %d: %v", slot, err)
		}
		version = result.Inspection.Version
		if len(result.Inspection.OccupiedSlots) != slot || len(result.Inspection.MissingSlots) != 8-slot {
			t.Fatalf("slot %d count wrong: %+v", slot, result.Inspection)
		}
	}
	_, err = client.UploadInspectionPhoto(ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: checkout.Inspection.ID, Slot: 8, Version: version, SourceEventKey: "message-8", IdempotencyKey: "photo-key-8", ContentType: "image/png", Image: syntheticPNG(t, 1)})
	expectAPIError(t, err, "DUPLICATE_PHOTO")
	before, err := client.Inspection(ctx, driverID, checkout.Inspection.ID)
	if err != nil || len(before.OccupiedSlots) != 7 || before.Version != version {
		t.Fatalf("duplicate changed inspection: %+v %v", before, err)
	}
	_, err = client.InspectionConfirmPhotos(ctx, driverID, checkout.Inspection.ID, version, "confirm-seven", nil)
	var incomplete *dataapi.APIError
	if !errors.As(err, &incomplete) || incomplete.Code != "PHOTO_SET_INCOMPLETE" || len(incomplete.Details.MissingSlots) != 1 || incomplete.Details.MissingSlots[0] != 8 {
		t.Fatalf("seven-photo confirmation: %v", err)
	}
	eighth, err := client.UploadInspectionPhoto(ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: checkout.Inspection.ID, Slot: 8, Version: version, SourceEventKey: "message-8-new", IdempotencyKey: "photo-key-8-new", ContentType: "image/png", Image: syntheticPNG(t, 8)})
	if err != nil || len(eighth.Inspection.OccupiedSlots) != 8 || len(eighth.Inspection.MissingSlots) != 0 {
		t.Fatalf("eight slots: %+v %v", eighth, err)
	}
	confirmedResult, err := client.InspectionConfirmPhotos(ctx, driverID, checkout.Inspection.ID, eighth.Inspection.Version, "confirm-eight", nil)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := dataapi.DecodeAggregate[dataapi.Inspection](confirmedResult)
	if err != nil || confirmed.PhotosConfirmedAt == nil {
		t.Fatalf("confirmation absent: %+v %v", confirmed, err)
	}
	replacement, err := client.UploadInspectionPhoto(ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: checkout.Inspection.ID, Slot: 3, Version: confirmed.Version, SourceEventKey: "replace-slot-3", IdempotencyKey: "photo-replace-3", ContentType: "image/png", Image: syntheticPNG(t, 99)})
	if err != nil || len(replacement.Inspection.OccupiedSlots) != 8 || replacement.Inspection.PhotosConfirmedAt != nil {
		t.Fatalf("replace: %+v %v", replacement, err)
	}
	restarted, err := NewWithSnapshot("test-service-token", path, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	restoredClient := commandClient(t, restarted)
	restored, err := restoredClient.Inspection(ctx, driverID, checkout.Inspection.ID)
	if err != nil || len(restored.OccupiedSlots) != 8 || restored.Version != replacement.Inspection.Version {
		t.Fatalf("photo slots lost on restart: %+v %v", restored, err)
	}
	replay, err := restoredClient.UploadInspectionPhoto(ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: checkout.Inspection.ID, Slot: 3, Version: confirmed.Version, SourceEventKey: "replace-slot-3", IdempotencyKey: "photo-replace-3", ContentType: "image/png", Image: syntheticPNG(t, 99)})
	if err != nil || replay.AssetID != replacement.AssetID {
		t.Fatalf("photo idempotency lost: %+v %v", replay, err)
	}
}

func TestPhotoErrorsPreserveEarlierSlots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	mock, err := NewWithSnapshot("test-service-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	ctx := context.Background()
	created, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "photo-errors-hold", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	first, err := client.UploadInspectionPhoto(ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: checkout.Inspection.ID, Slot: 1, Version: 1, SourceEventKey: "first-message", IdempotencyKey: "first-photo-key", ContentType: "image/png", Image: syntheticPNG(t, 1)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.UploadInspectionPhoto(ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: checkout.Inspection.ID, Slot: 2, Version: first.Inspection.Version, SourceEventKey: "bad-media", IdempotencyKey: "bad-media-key", ContentType: "image/jpeg", Image: syntheticPNG(t, 2)})
	expectAPIError(t, err, "UNSUPPORTED_MEDIA")
	var oversized bytes.Buffer
	writer := multipart.NewWriter(&oversized)
	part, err := writer.CreateFormFile("image", "oversized.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(make([]byte, (10<<20)+1)); err != nil {
		t.Fatal(err)
	}
	_ = writer.WriteField("expected_version", fmt.Sprint(first.Inspection.Version))
	_ = writer.WriteField("source_event_key", "oversized")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/inspections/"+checkout.Inspection.ID+"/photos/2", &oversized)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer test-service-token")
	req.Header.Set("X-Contract-Version", "1.11")
	req.Header.Set("X-Request-ID", "11111111-1111-4111-8111-111111111111")
	req.Header.Set("X-Actor-Max-ID", driverID)
	req.Header.Set("Idempotency-Key", "oversized-photo")
	recorder := httptest.NewRecorder()
	mock.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status %d", recorder.Code)
	}
	_, err = client.UploadInspectionPhoto(ctx, "8000000000000000002", dataapi.InspectionPhotoInput{InspectionID: checkout.Inspection.ID, Slot: 2, Version: first.Inspection.Version, SourceEventKey: "wrong-owner", IdempotencyKey: "wrong-owner-key", ContentType: "image/png", Image: syntheticPNG(t, 2)})
	expectAPIError(t, err, "NOT_FOUND")
	originalSave := mock.saveSnapshot
	mock.saveSnapshot = func(stateSnapshot) error { return fmt.Errorf("synthetic storage failure") }
	_, err = client.UploadInspectionPhoto(ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: checkout.Inspection.ID, Slot: 2, Version: first.Inspection.Version, SourceEventKey: "failed-save", IdempotencyKey: "failed-save-key", ContentType: "image/png", Image: syntheticPNG(t, 2)})
	expectAPIError(t, err, "STORAGE_UNAVAILABLE")
	mock.saveSnapshot = originalSave
	inspection, err := client.Inspection(ctx, driverID, checkout.Inspection.ID)
	if err != nil || len(inspection.OccupiedSlots) != 1 || inspection.Version != first.Inspection.Version {
		t.Fatalf("failed photo changed earlier slot: %+v %v", inspection, err)
	}
	restarted, err := NewWithSnapshot("test-service-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.photos[checkout.Inspection.ID]) != 1 {
		t.Fatal("earlier photo lost on restart")
	}
	asset := restarted.photos[checkout.Inspection.ID][1]
	if err := os.Remove(filepath.Join(restarted.assetDir, asset.AssetID)); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithSnapshot("test-service-token", path, time.Now); err == nil {
		t.Fatal("missing photo asset accepted")
	}
}
