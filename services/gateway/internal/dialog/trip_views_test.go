package dialog

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type tripViewData struct {
	*dataapi.Client
	trip       dataapi.Trip
	photoCalls int
	photoErr   error
}

func (d *tripViewData) Trip(_ context.Context, actor, id string) (dataapi.Trip, error) {
	if id != d.trip.ID || actor != "8000000000000000001" && actor != "8000000000000000003" {
		return dataapi.Trip{}, &dataapi.APIError{Status: http.StatusNotFound, Code: "NOT_FOUND"}
	}
	return d.trip, nil
}

func (d *tripViewData) MyTrips(_ context.Context, actor string, _ int, _ string) (dataapi.Page[dataapi.Trip], error) {
	if actor != "8000000000000000001" {
		return dataapi.Page[dataapi.Trip]{}, nil
	}
	return dataapi.Page[dataapi.Trip]{Items: []dataapi.Trip{d.trip}}, nil
}

func (d *tripViewData) AdminTrips(_ context.Context, actor string, _ dataapi.AdminTripFilter) (dataapi.Page[dataapi.Trip], error) {
	if actor != "8000000000000000003" {
		return dataapi.Page[dataapi.Trip]{}, &dataapi.APIError{Status: http.StatusForbidden, Code: "ACCESS_DENIED"}
	}
	return dataapi.Page[dataapi.Trip]{Items: []dataapi.Trip{d.trip}}, nil
}

func (d *tripViewData) TripInspectionPhoto(_ context.Context, actor, id, phase string, slot int) (dataapi.AssetContent, error) {
	d.photoCalls++
	if d.photoErr != nil {
		return dataapi.AssetContent{}, d.photoErr
	}
	if id != d.trip.ID || slot != 3 || actor != "8000000000000000001" && actor != "8000000000000000003" {
		return dataapi.AssetContent{}, &dataapi.APIError{Status: http.StatusNotFound, Code: "NOT_FOUND"}
	}
	return dataapi.AssetContent{ContentType: "image/png", Bytes: []byte("synthetic-photo-" + phase)}, nil
}

type recordedTripImage struct {
	*maxsdk.RecordingTransport
	images []string
}

func (s *recordedTripImage) SendImage(_ context.Context, userID int64, caption, contentType string, data []byte) (string, error) {
	s.images = append(s.images, caption+"|"+contentType+"|"+string(data))
	return "synthetic-image-message", nil
}

func TestTripPhotoDialogOwnerAdminPhaseAndVersion(t *testing.T) {
	now := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	const tripID = "40000000-0000-4000-8000-000000000001"
	before := dataapi.Inspection{ID: "50000000-0000-4000-8000-000000000001", Phase: "before", Status: "finalized", OccupiedSlots: []int{3}}
	after := dataapi.Inspection{ID: "50000000-0000-4000-8000-000000000002", Phase: "after", Status: "finalized", OccupiedSlots: []int{3}}
	data := &tripViewData{Client: actor, trip: dataapi.Trip{ID: tripID, Status: "active", Version: 2, BeforeInspection: before, AfterInspection: &after}}
	sender := &recordedTripImage{RecordingTransport: &maxsdk.RecordingTransport{}}
	processor := Bootstrap{Data: data, MAX: sender}
	ctx := context.Background()
	owner := "8000000000000000001"
	listItem := menuItem(owner, "mine", now)
	listCommand := "/trips"
	listItem.Event.Payload.Text = &listCommand
	if err := processor.Handle(ctx, listItem); err != nil {
		t.Fatal(err)
	}
	if buttons := sender.Messages()[0].Buttons; len(buttons) != 1 || buttons[0][0].Payload != "trip:"+tripID {
		t.Fatalf("my trip list: %+v", buttons)
	}
	if err := processor.Handle(ctx, callbackItem(owner, "detail", "trip:"+tripID, now)); err != nil {
		t.Fatal(err)
	}
	if buttons := sender.Messages()[1].Buttons; len(buttons) != 2 || buttons[0][0].Payload != "photo-phase:"+tripID+":2:before" {
		t.Fatalf("active trip exposed after: %+v", buttons)
	}
	if err := processor.Handle(ctx, callbackItem(owner, "phase", "photo-phase:"+tripID+":2:before", now)); err != nil {
		t.Fatal(err)
	}
	if buttons := sender.Messages()[2].Buttons; len(buttons) != 2 || buttons[0][0].Payload != "photo-view:"+tripID+":2:before:3" {
		t.Fatalf("photo angles: %+v", buttons)
	}
	if err := processor.Handle(ctx, callbackItem(owner, "photo", "photo-view:"+tripID+":2:before:3", now)); err != nil || len(sender.images) != 1 || !strings.Contains(sender.images[0], "synthetic-photo-before") {
		t.Fatalf("owner photo: %v %+v", err, sender.images)
	}
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "other", "photo-view:"+tripID+":2:before:3", now)); err != nil || len(sender.images) != 1 || !strings.Contains(sender.Messages()[3].Text, "недоступна") {
		t.Fatalf("other actor photo: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(owner, "early-after", "photo-view:"+tripID+":2:after:3", now)); err != nil || len(sender.images) != 1 {
		t.Fatalf("active after leaked: %v %+v", err, sender.images)
	}
	data.trip.Status, data.trip.Version = "completed", 3
	if err := processor.Handle(ctx, callbackItem(owner, "stale", "photo-view:"+tripID+":2:before:3", now)); err != nil || len(sender.images) != 1 || !strings.Contains(sender.Messages()[5].Text, "изменились") {
		t.Fatalf("stale view: %v %+v", err, sender.Messages())
	}
	admin := "8000000000000000003"
	if err := processor.Handle(ctx, callbackItem(admin, "admin-list", "trip-list:admin:1", now)); err != nil || sender.Messages()[6].Buttons[0][0].Payload != "trip:"+tripID {
		t.Fatalf("admin list: %v %+v", err, sender.Messages())
	}
	if err := processor.Handle(ctx, callbackItem(admin, "admin-detail", "trip:"+tripID, now)); err != nil || len(sender.Messages()[7].Buttons) != 3 {
		t.Fatalf("admin trip phases: %v %+v", err, sender.Messages())
	}
	data.photoErr = &dataapi.APIError{Status: http.StatusServiceUnavailable, Code: "STORAGE_UNAVAILABLE", Retryable: true}
	if err := processor.Handle(ctx, callbackItem(admin, "admin-after-failed", "photo-view:"+tripID+":3:after:3", now)); err == nil || len(sender.images) != 1 {
		t.Fatalf("failed read sent image: %v %+v", err, sender.images)
	}
	data.photoErr = nil
	if err := processor.Handle(ctx, callbackItem(admin, "admin-after", "photo-view:"+tripID+":3:after:3", now)); err != nil || len(sender.images) != 2 || !strings.Contains(sender.images[1], "synthetic-photo-after") {
		t.Fatalf("admin after photo: %v %+v", err, sender.images)
	}
	if data.photoCalls != 3 {
		t.Fatalf("unexpected private photo reads: %d", data.photoCalls)
	}
}
