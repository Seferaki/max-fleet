package dialog

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

type tripViewData struct {
	*dataapi.Client
	trip          dataapi.Trip
	myTripLimits  []int
	myTripCursors []string
	myTripPages   map[string]dataapi.Page[dataapi.Trip]
	photoCalls    int
	photoErr      error
}

func TestMyTripsShowFivePerPageAndKeepCursorPrivate(t *testing.T) {
	now := time.Date(2026, 9, 29, 5, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	me, err := actor.Me(context.Background(), "8000000000000000001")
	if err != nil || me.Employee == nil {
		t.Fatal(err)
	}
	items := make([]dataapi.Trip, 6)
	for index := range items {
		items[index] = dataapi.Trip{ID: fmt.Sprintf("40000000-0000-4000-8000-%012d", index+1), EmployeeID: me.Employee.ID, Status: "completed"}
	}
	next := "opaque-private-cursor"
	data := &tripViewData{Client: actor, myTripPages: map[string]dataapi.Page[dataapi.Trip]{
		"":   {Items: items[:5], NextCursor: &next},
		next: {Items: items[5:]},
	}}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: data, MAX: sender}
	owner := "8000000000000000001"
	list := menuItem(owner, "history-first", now)
	command := "/trips"
	list.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	first := sender.Messages()[0]
	if len(first.Buttons) != 6 || first.Buttons[5][0].Payload != "trip-list:mine:2" || strings.Contains(fmt.Sprint(first.Buttons), next) || data.myTripLimits[0] != 5 || data.myTripCursors[0] != "" {
		t.Fatalf("first history page: %+v, limits=%v cursors=%v", first, data.myTripLimits, data.myTripCursors)
	}
	if err := processor.Handle(context.Background(), callbackItem(owner, "history-second", "trip-list:mine:2", now)); err != nil {
		t.Fatal(err)
	}
	second := sender.Messages()[1]
	if len(second.Buttons) != 2 || second.Buttons[0][0].Payload != "trip:"+items[5].ID || second.Buttons[1][0].Payload != "trip-list:mine:1" || len(data.myTripLimits) != 3 || data.myTripLimits[1] != 5 || data.myTripLimits[2] != 5 || data.myTripCursors[2] != next {
		t.Fatalf("second history page: %+v, limits=%v cursors=%v", second, data.myTripLimits, data.myTripCursors)
	}
	if err := processor.Handle(context.Background(), callbackItem(owner, "history-invalid", "trip-list:mine:21", now)); err != nil || len(data.myTripLimits) != 3 || !strings.Contains(sender.Messages()[2].Text, "недоступен") {
		t.Fatalf("invalid history page: %v %+v", err, sender.Messages()[2])
	}
	foreign := items[0]
	foreign.EmployeeID = "another-employee"
	data.myTripPages[""] = dataapi.Page[dataapi.Trip]{Items: []dataapi.Trip{foreign}}
	foreignList := menuItem(owner, "history-foreign-row", now)
	foreignList.Event.Payload.Text = &command
	if err := processor.Handle(context.Background(), foreignList); err == nil || !strings.Contains(err.Error(), "invalid owned trip list projection") || len(sender.Messages()) != 3 {
		t.Fatalf("foreign list row leaked: %v %+v", err, sender.Messages())
	}
}

func TestTripHistoryShowsAdminCloseAndOwnedIssuesByVersion(t *testing.T) {
	now := time.Date(2026, 9, 29, 5, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	me, err := actor.Me(context.Background(), "8000000000000000001")
	if err != nil || me.Employee == nil {
		t.Fatal(err)
	}
	const tripID = "40000000-0000-4000-8000-000000000001"
	issues := make([]dataapi.Issue, 6)
	for index := range issues {
		issues[index] = dataapi.Issue{Category: "mechanical", Status: "open", Description: fmt.Sprintf("Замечание %d", index+1)}
	}
	data := &tripViewData{Client: actor, trip: dataapi.Trip{ID: tripID, EmployeeID: me.Employee.ID, Status: "closed_by_admin", Version: 3, MissingData: []string{"after_photos"}, Issues: issues}}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: data, MAX: sender}
	owner := "8000000000000000001"
	ctx := context.Background()
	if err := processor.Handle(ctx, callbackItem(owner, "history-detail", "trip:"+tripID, now)); err != nil {
		t.Fatal(err)
	}
	detail := sender.Messages()[0]
	if !strings.Contains(detail.Text, "Закрыто администратором") || !strings.Contains(detail.Text, "after_photos") || detail.Buttons[0][0].Payload != "trip-issues:"+tripID+":3:1" {
		t.Fatalf("admin close detail: %+v", detail)
	}
	if err := processor.Handle(ctx, callbackItem(owner, "history-issues-1", detail.Buttons[0][0].Payload, now)); err != nil {
		t.Fatal(err)
	}
	first := sender.Messages()[1]
	if !strings.Contains(first.Text, "Замечание 5") || strings.Contains(first.Text, "Замечание 6") || first.Buttons[0][0].Payload != "trip-issues:"+tripID+":3:2" {
		t.Fatalf("first issue page: %+v", first)
	}
	if err := processor.Handle(ctx, callbackItem(owner, "history-issues-2", first.Buttons[0][0].Payload, now)); err != nil {
		t.Fatal(err)
	}
	second := sender.Messages()[2]
	if !strings.Contains(second.Text, "Замечание 6") || second.Buttons[0][0].Payload != "trip-issues:"+tripID+":3:1" {
		t.Fatalf("second issue page: %+v", second)
	}
	if err := processor.Handle(ctx, callbackItem("8000000000000000002", "history-foreign-issues", detail.Buttons[0][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[3].Text, "недоступна") {
		t.Fatalf("foreign issue read: %v %+v", err, sender.Messages()[3])
	}
	data.trip.Version = 4
	if err := processor.Handle(ctx, callbackItem(owner, "history-stale-issues", detail.Buttons[0][0].Payload, now)); err != nil || !strings.Contains(sender.Messages()[4].Text, "изменились") {
		t.Fatalf("stale issue read: %v %+v", err, sender.Messages()[4])
	}
	data.trip.EmployeeID = "another-employee"
	if err := processor.Handle(ctx, callbackItem(owner, "history-mismatched-owner", "trip:"+tripID, now)); err != nil || !strings.Contains(sender.Messages()[5].Text, "недоступна") || strings.Contains(sender.Messages()[5].Text, "Замечание") {
		t.Fatalf("mismatched owner leaked trip: %v %+v", err, sender.Messages()[5])
	}
	if err := processor.Handle(ctx, callbackItem("8000000000000000003", "history-admin", "trip:"+tripID, now)); err != nil || !strings.Contains(sender.Messages()[6].Text, "Закрыто администратором") {
		t.Fatalf("admin trip access: %v %+v", err, sender.Messages()[6])
	}
}

func TestAdminCloseActionOnlyAppearsForAdminOnOpenTrip(t *testing.T) {
	now := time.Date(2026, 9, 29, 5, 0, 0, 0, time.UTC)
	actor, _, closeServer := mockClients(t, now)
	defer closeServer()
	owner, err := actor.Me(context.Background(), "8000000000000000001")
	if err != nil || owner.Employee == nil {
		t.Fatal(err)
	}
	const tripID = "40000000-0000-4000-8000-000000000001"
	data := &tripViewData{Client: actor, trip: dataapi.Trip{ID: tripID, VehicleID: "10000000-0000-4000-8000-000000000001", EmployeeID: owner.Employee.ID, Status: "active", Version: 7}}
	sender := &maxsdk.RecordingTransport{}
	processor := Bootstrap{Data: data, MAX: sender}
	adminID := "8000000000000000003"
	startPayload := "admin-close:start:" + tripID + ":7"

	if err := processor.Handle(context.Background(), callbackItem(adminID, "admin-close-active", "trip:"+tripID, now)); err != nil {
		t.Fatal(err)
	}
	if !hasButton(sender.Messages()[0].Buttons, startPayload) {
		t.Fatalf("active trip detail did not expose admin close: %+v", sender.Messages()[0])
	}

	if err := processor.Handle(context.Background(), callbackItem("8000000000000000001", "employee-close-active", "trip:"+tripID, now)); err != nil {
		t.Fatal(err)
	}
	if hasButton(sender.Messages()[1].Buttons, startPayload) {
		t.Fatalf("employee trip detail exposed admin close: %+v", sender.Messages()[1])
	}

	data.trip.Status, data.trip.Version = "returning", 8
	if err := processor.Handle(context.Background(), callbackItem(adminID, "admin-close-returning", "trip:"+tripID, now)); err != nil {
		t.Fatal(err)
	}
	if !hasButton(sender.Messages()[2].Buttons, "admin-close:start:"+tripID+":8") {
		t.Fatalf("returning trip detail did not expose admin close: %+v", sender.Messages()[2])
	}

	data.trip.Status, data.trip.Version = "completed", 9
	if err := processor.Handle(context.Background(), callbackItem(adminID, "admin-close-completed", "trip:"+tripID, now)); err != nil {
		t.Fatal(err)
	}
	if hasButton(sender.Messages()[3].Buttons, "admin-close:start:"+tripID+":9") {
		t.Fatalf("completed trip detail exposed admin close: %+v", sender.Messages()[3])
	}
}

func (d *tripViewData) Trip(_ context.Context, actor, id string) (dataapi.Trip, error) {
	if id != d.trip.ID || actor != "8000000000000000001" && actor != "8000000000000000003" {
		return dataapi.Trip{}, &dataapi.APIError{Status: http.StatusNotFound, Code: "NOT_FOUND"}
	}
	return d.trip, nil
}

func (d *tripViewData) MyTrips(_ context.Context, actor string, limit int, cursor string) (dataapi.Page[dataapi.Trip], error) {
	d.myTripLimits = append(d.myTripLimits, limit)
	d.myTripCursors = append(d.myTripCursors, cursor)
	if actor != "8000000000000000001" {
		return dataapi.Page[dataapi.Trip]{}, nil
	}
	if d.myTripPages != nil {
		return d.myTripPages[cursor], nil
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
	me, err := actor.Me(context.Background(), "8000000000000000001")
	if err != nil || me.Employee == nil {
		t.Fatal(err)
	}
	const tripID = "40000000-0000-4000-8000-000000000001"
	before := dataapi.Inspection{ID: "50000000-0000-4000-8000-000000000001", Phase: "before", Status: "finalized", OccupiedSlots: []int{3}}
	after := dataapi.Inspection{ID: "50000000-0000-4000-8000-000000000002", Phase: "after", Status: "finalized", OccupiedSlots: []int{3}}
	data := &tripViewData{Client: actor, trip: dataapi.Trip{ID: tripID, EmployeeID: me.Employee.ID, Status: "active", Version: 2, BeforeInspection: before, AfterInspection: &after}}
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
	if buttons := sender.Messages()[1].Buttons; len(buttons) != 4 || buttons[2][0].Payload != "photo-phase:"+tripID+":2:before" {
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
	data.trip.EmployeeID = "another-employee"
	if err := processor.Handle(ctx, callbackItem(owner, "forged-photo-owner", "photo-view:"+tripID+":3:after:3", now)); err != nil || data.photoCalls != 3 || !strings.Contains(sender.Messages()[len(sender.Messages())-1].Text, "недоступна") {
		t.Fatalf("foreign trip reached private photo reader: %v, calls=%d, messages=%+v", err, data.photoCalls, sender.Messages())
	}
}
