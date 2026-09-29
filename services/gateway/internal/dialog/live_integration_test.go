package dialog

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/inboxworker"
	"github.com/Seferaki/max-fleet/services/gateway/internal/mapapi"
	"github.com/Seferaki/max-fleet/services/gateway/internal/maxsdk"
)

// This opt-in test runs the Go inbox worker and return dialog against a disposable
// synthetic Python Data API (PostgreSQL + S3). It never uses MAX credentials.
func TestLivePythonReturnDialog(t *testing.T) {
	if os.Getenv("MAX_FLEET_LIVE_DIALOG") != "1" {
		t.Skip("set MAX_FLEET_LIVE_DIALOG=1 for the live Go-to-Python dialog smoke")
	}

	baseURL := os.Getenv("MAX_FLEET_LIVE_DATA_API_URL")
	actorID := os.Getenv("MAX_FLEET_LIVE_DATA_API_ACTOR")
	actorTokenPath := os.Getenv("MAX_FLEET_LIVE_DATA_API_TOKEN_FILE")
	workerTokenPath := os.Getenv("MAX_FLEET_LIVE_DATA_API_WORKER_TOKEN_FILE")
	if baseURL == "" || actorTokenPath == "" || workerTokenPath == "" {
		t.Fatal("live dialog URL and private actor/worker token file paths are required")
	}
	if actorID == "" {
		actorID = "8000000000000000001"
	}
	maxID, err := strconv.ParseInt(actorID, 10, 64)
	if err != nil || maxID <= 0 {
		t.Fatal("live dialog actor must be a positive MAX user ID")
	}
	actorToken := readPrivateToken(t, actorTokenPath)
	workerToken := readPrivateToken(t, workerTokenPath)
	client, err := dataapi.New(dataapi.Config{BaseURL: baseURL, Token: actorToken, HTTPClient: &http.Client{Timeout: 15 * time.Second}})
	if err != nil {
		t.Fatalf("create actor Data API client: %v", err)
	}
	workerStore, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: baseURL, Token: workerToken, HTTPClient: &http.Client{Timeout: 15 * time.Second}})
	if err != nil {
		t.Fatalf("create inbox worker client: %v", err)
	}
	lostCompleteResponse := &lostResponseAfterCommitTransport{next: http.DefaultTransport}
	retryingClient, err := dataapi.New(dataapi.Config{BaseURL: baseURL, Token: actorToken,
		HTTPClient: &http.Client{Timeout: 15 * time.Second, Transport: lostCompleteResponse}})
	if err != nil {
		t.Fatalf("create retrying Data API client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	meta, err := client.Meta(ctx)
	if err != nil || meta.ContractVersion != dataapi.ContractVersion || meta.Mode != "real" {
		t.Fatalf("Python Data API meta mismatch: version=%q mode=%q err=%v", meta.ContractVersion, meta.Mode, err)
	}
	me, err := client.Me(ctx, actorID)
	if err != nil || !me.Allowed || me.Employee == nil || me.Employee.Role != "employee" || !me.Employee.CanStartTrip {
		t.Fatalf("synthetic driver access is unavailable: allowed=%t err=%v", me.Allowed, err)
	}
	state, err := client.State(ctx, actorID)
	if err != nil || state.Trip != nil || state.Checkout != nil {
		t.Fatalf("live dialog needs a clean synthetic driver: state=%+v err=%v", state, err)
	}
	available := true
	vehicles, err := client.Vehicles(ctx, actorID, dataapi.VehicleFilter{Available: &available, Limit: 10})
	if err != nil || len(vehicles.Items) == 0 {
		t.Fatalf("synthetic driver has no available vehicles: %v", err)
	}
	vehicle := vehicles.Items[0]
	key := func() string { return liveDialogKey(t) }

	created, err := client.CheckoutCreate(ctx, actorID, vehicle.ID, vehicle.Version, key(), nil)
	if err != nil {
		t.Fatalf("checkout.create: %v", err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatalf("decode checkout.create: %v", err)
	}
	holdRemaining := time.Until(checkout.ExpiresAt)
	if holdRemaining < 14*time.Minute || holdRemaining > 16*time.Minute {
		t.Fatalf("Python checkout hold is not the required 15 minutes: remaining=%s", holdRemaining)
	}
	createdChallenge, err := client.ChallengeCreateTake(ctx, actorID, checkout.ID, checkout.Version, vehicle.ID, vehicle.Version, key(), nil)
	if err != nil {
		t.Fatalf("take challenge: %v", err)
	}
	takeChallenge, err := dataapi.DecodeAggregate[dataapi.Challenge](createdChallenge)
	if err != nil {
		t.Fatalf("decode take challenge: %v", err)
	}
	answerLiveChallenge(t, ctx, client, actorID, takeChallenge, key())
	state, err = client.State(ctx, actorID)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout after take challenge: state=%+v err=%v", state, err)
	}
	rules, err := client.CurrentRules(ctx, actorID)
	if err != nil {
		t.Fatalf("current rules: %v", err)
	}
	if _, err = client.CheckoutAcceptRules(ctx, actorID, state.Checkout.ID, state.Checkout.Version, rules.ID, key(), nil); err != nil {
		t.Fatalf("checkout.accept_rules: %v", err)
	}
	state, err = client.State(ctx, actorID)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout before inspection: state=%+v err=%v", state, err)
	}
	fuel := 50
	noDamage := false
	startOdometer := int64(12000)
	if vehicle.CurrentOdometerKM != nil {
		startOdometer = *vehicle.CurrentOdometerKM + 50
	}
	inspectionResult, err := client.InspectionUpdate(ctx, actorID, state.Checkout.Inspection.ID, state.Checkout.Inspection.Version,
		dataapi.InspectionUpdateInput{FuelLevel: &fuel, OdometerKM: &startOdometer, NewDamage: &noDamage}, key(), nil)
	if err != nil {
		t.Fatalf("before inspection fields: %v", err)
	}
	inspection, err := dataapi.DecodeAggregate[dataapi.Inspection](inspectionResult)
	if err != nil {
		t.Fatalf("decode before inspection: %v", err)
	}
	for slot := 1; slot <= 8; slot++ {
		photoKey := key()
		uploaded, uploadErr := client.UploadInspectionPhoto(ctx, actorID, dataapi.InspectionPhotoInput{
			InspectionID: inspection.ID, Slot: slot, Version: inspection.Version, SourceEventKey: photoKey,
			IdempotencyKey: photoKey, ContentType: "image/png", Image: samplePhoto(t, uint8(slot)),
		})
		if uploadErr != nil {
			t.Fatalf("before photo %d/8: %v", slot, uploadErr)
		}
		inspection = uploaded.Inspection
	}
	if _, err = client.InspectionConfirmPhotos(ctx, actorID, inspection.ID, inspection.Version, key(), nil); err != nil {
		t.Fatalf("confirm before photos: %v", err)
	}
	state, err = client.State(ctx, actorID)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout before trip start: state=%+v err=%v", state, err)
	}
	if _, err = client.CheckoutSetNoNewIssues(ctx, actorID, state.Checkout.ID, state.Checkout.Version, key(), nil); err != nil {
		t.Fatalf("checkout.set_no_new_issues: %v", err)
	}
	state, err = client.State(ctx, actorID)
	if err != nil || state.Checkout == nil {
		t.Fatalf("checkout before start: state=%+v err=%v", state, err)
	}
	if _, err = client.CheckoutStart(ctx, actorID, state.Checkout.ID, state.Checkout.Version, key(), nil); err != nil {
		t.Fatalf("checkout.start: %v", err)
	}
	state, err = client.State(ctx, actorID)
	if err != nil || state.Trip == nil || state.Trip.Status != "active" || len(state.Trip.BeforeInspection.OccupiedSlots) != 8 {
		t.Fatalf("active trip does not retain 8 before photos: state=%+v err=%v", state, err)
	}
	trip := *state.Trip

	sender := &maxsdk.RecordingTransport{}
	fetcher := &syntheticPhotoFetcher{}
	processor := Bootstrap{Data: client, Commands: client, MAX: sender, Photos: fetcher, PhotoStore: client, Location: time.UTC}
	worker := inboxworker.Worker{ID: "live-dialog-smoke", Store: workerStore, Processor: &processor, Now: time.Now}
	deliver := func(event dataapi.NormalizedEvent) dataapi.InboxStored {
		t.Helper()
		stored, storeErr := workerStore.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event))
		if storeErr != nil {
			t.Fatalf("store MAX-like inbox event: %v", storeErr)
		}
		result, runErr := worker.RunOnce(ctx, 1)
		if runErr != nil || result.Acked != 1 {
			t.Fatalf("process live Go dialog event: result=%+v err=%v", result, runErr)
		}
		return stored
	}
	deliverCallback := func(payload string) {
		t.Helper()
		deliver(callbackItem(actorID, key(), payload, time.Now().UTC()).Event)
	}
	menu := func() {
		t.Helper()
		deliver(menuItem(actorID, key(), time.Now().UTC()).Event)
	}
	latest := func() maxsdk.RecordedText {
		t.Helper()
		messages := sender.Messages()
		if len(messages) == 0 {
			t.Fatal("Go dialog did not send a message")
		}
		return messages[len(messages)-1]
	}
	button := func(prefix string) string {
		t.Helper()
		for _, row := range latest().Buttons {
			for _, choice := range row {
				if strings.HasPrefix(choice.Payload, prefix) {
					return choice.Payload
				}
			}
		}
		t.Fatalf("Go dialog button %q missing in %+v", prefix, latest())
		return ""
	}

	deliverCallback("trip:" + trip.ID)
	returnIntent := button("return-intent:")
	deliverCallback(returnIntent)
	if !strings.Contains(latest().Text, "готовы оформить возврат") {
		t.Fatalf("return confirmation preview missing: %+v", latest())
	}
	deliverCallback(button("return-confirm:"))
	if !strings.Contains(latest().Text, "занятость автомобиля сохраняются") {
		t.Fatalf("return hold warning missing: %+v", latest())
	}
	menu()
	deliverCallback(button("return-math:"))
	returnChallengeMessage := latest()
	// The first button may be wrong; choose the option equal to the challenge sum.
	var a, b int
	for _, line := range strings.Split(returnChallengeMessage.Text, "\n") {
		if _, scanErr := fmt.Sscanf(line, "%d + %d = ?", &a, &b); scanErr == nil {
			break
		}
	}
	correctPayload := ""
	for _, row := range returnChallengeMessage.Buttons {
		for _, choice := range row {
			var value int
			if _, scanErr := fmt.Sscanf(choice.Text, "%d", &value); scanErr == nil && value == a+b {
				correctPayload = choice.Payload
			}
		}
	}
	if correctPayload == "" {
		t.Fatalf("correct return math option not found: %+v", returnChallengeMessage)
	}
	deliverCallback(correctPayload)
	if !strings.Contains(strings.ToLower(latest().Text), "проверка возврата пройдена") {
		t.Fatalf("return challenge did not advance: %+v", latest())
	}

	for _, field := range []string{"damage", "clean", "parking", "keys_lock"} {
		menu()
		deliverCallback(button("return-check:"))
		choices := latest().Buttons
		if len(choices) == 0 || len(choices[0]) == 0 {
			t.Fatalf("return checklist choices missing for %s: %+v", field, latest())
		}
		choice := choices[0][0].Payload
		if field == "damage" {
			if len(choices) < 2 || len(choices[1]) == 0 {
				t.Fatalf("damage=no choice missing: %+v", latest())
			}
			choice = choices[1][0].Payload
		}
		deliverCallback(choice)
	}
	state, err = client.State(ctx, actorID)
	if err != nil || state.Return == nil || nextReturnCheckField(state.Return.Inspection) != "" {
		t.Fatalf("return checklist did not persist: state=%+v err=%v", state, err)
	}
	menu()
	deliverCallback(button("return-photos:"))
	for slot := 1; slot <= 8; slot++ {
		fetcher.image = samplePhoto(t, uint8(150+slot))
		photoEvent := photoItem(actorID, key(), time.Now().UTC()).Event
		storedPhoto := deliver(photoEvent)
		if storedPhoto.Duplicate {
			t.Fatalf("fresh photo event was unexpectedly a duplicate: stored=%+v", storedPhoto)
		}
		if !strings.Contains(latest().Text, fmt.Sprintf("%d/8", slot)) {
			t.Fatalf("after photo %d did not advance: %+v", slot, latest())
		}
		if slot == 4 {
			state, err = client.State(ctx, actorID)
			if err != nil || state.Return == nil || len(state.Return.Inspection.OccupiedSlots) != 4 {
				t.Fatalf("mid-return photo checkpoint was not durable: state=%+v err=%v", state, err)
			}

			// Rebuild the in-memory Go dialog worker as after a process restart.
			sender = &maxsdk.RecordingTransport{}
			fetcher = &syntheticPhotoFetcher{}
			processor = Bootstrap{Data: client, Commands: client, MAX: sender, Photos: fetcher, PhotoStore: client, Location: time.UTC}
			worker = inboxworker.Worker{ID: "live-dialog-smoke-restarted", Store: workerStore, Processor: &processor, Now: time.Now}

			duplicate, storeErr := workerStore.StoreInbox(ctx, photoEvent, key())
			if storeErr != nil || !duplicate.Duplicate || duplicate.ID != storedPhoto.ID {
				t.Fatalf("replayed photo event did not resolve to the same inbox row after worker restart: original=%+v replay=%+v err=%v", storedPhoto, duplicate, storeErr)
			}
			replay, runErr := worker.RunOnce(ctx, 1)
			if runErr != nil || replay.Claimed != 0 || replay.Acked != 0 {
				t.Fatalf("acked photo event was processed again after restart: result=%+v err=%v", replay, runErr)
			}
			state, err = client.State(ctx, actorID)
			if err != nil || state.Return == nil || len(state.Return.Inspection.OccupiedSlots) != 4 {
				t.Fatalf("duplicate replay changed the durable photo count: state=%+v err=%v", state, err)
			}
			menu()
			deliverCallback(button("return-photos:"))
			if !strings.Contains(latest().Text, "сохранено 4/8") {
				t.Fatalf("Go dialog did not restore the four-photo checkpoint after restart: %+v", latest())
			}
		}
	}
	menu()
	deliverCallback(button("return-confirm-photos:"))
	menu()
	deliverCallback(button("return-fuel:"))
	fuelPayload := ""
	for _, row := range latest().Buttons {
		for _, choice := range row {
			if choice.Text == "75%" {
				fuelPayload = choice.Payload
			}
		}
	}
	if fuelPayload == "" {
		t.Fatalf("75%% fuel choice missing: %+v", latest())
	}
	deliverCallback(fuelPayload)
	menu()
	deliverCallback(button("return-odometer:"))
	returnOdometer := fmt.Sprintf("/odometer %d", startOdometer+50)
	odometerEvent := menuItem(actorID, key(), time.Now().UTC()).Event
	odometerEvent.Payload.Text = &returnOdometer
	deliver(odometerEvent)
	state, err = client.State(ctx, actorID)
	if err != nil || state.Return == nil || state.Return.ParkingLocation != nil {
		t.Fatalf("return odometer state unexpectedly changed map selection: state=%+v err=%v", state, err)
	}
	mapToken := "synthetic-live-map-verifier"
	mapNow := time.Now().UTC()
	verifier, err := mapapi.NewVerifier(mapToken, func() time.Time { return mapNow })
	if err != nil {
		t.Fatalf("create synthetic map verifier: %v", err)
	}
	mapHandler, err := mapapi.NewHandler(verifier, client, mapapi.Point{Latitude: 55.75, Longitude: 37.62})
	if err != nil {
		t.Fatalf("create live map handler: %v", err)
	}
	mapKey := key()
	mapBody := fmt.Sprintf(`{"expected_version":%d,"latitude":55.751,"longitude":37.621,"confirmed":true}`, state.Return.Version)
	mapRequest := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/returns/"+state.Return.ID+"/location", strings.NewReader(body))
		req.SetPathValue("id", state.Return.ID)
		req.Header.Set("Authorization", "MaxInitData "+signedSyntheticMapInitData(mapToken, actorID, mapNow))
		req.Header.Set("Idempotency-Key", mapKey)
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		mapHandler.Location(recorder, req)
		return recorder
	}
	invalidMapBody := strings.Replace(mapBody, `"confirmed":true`, `"confirmed":false`, 1)
	if response := mapRequest(invalidMapBody); response.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed map point was accepted: status=%d body=%s", response.Code, response.Body.String())
	}
	if response := mapRequest(mapBody); response.Code != http.StatusOK {
		t.Fatalf("manual map save failed: status=%d body=%s", response.Code, response.Body.String())
	}
	var mapResult struct {
		Data struct {
			ReturnID      string `json:"return_id"`
			ReturnVersion int64  `json:"return_version"`
			Source        string `json:"source"`
			Selected      bool   `json:"selected"`
		} `json:"data"`
	}
	if err = json.Unmarshal(mapRequest(mapBody).Body.Bytes(), &mapResult); err != nil || mapResult.Data.ReturnID != state.Return.ID ||
		mapResult.Data.ReturnVersion != state.Return.Version+1 || mapResult.Data.Source != "manual_map" || !mapResult.Data.Selected {
		t.Fatalf("manual map response mismatch: result=%+v err=%v", mapResult, err)
	}
	state, err = client.State(ctx, actorID)
	if err != nil || state.Return == nil || state.Return.Version != mapResult.Data.ReturnVersion || state.Return.ParkingLocation == nil || state.Return.ParkingLocation.Source != "manual_map" {
		t.Fatalf("manual map did not persist once: state=%+v err=%v", state, err)
	}
	menu()
	deliverCallback(button("return-summary:"))
	if !strings.Contains(latest().Text, "Фото после: 8/8") || !strings.Contains(latest().Text, "manual_map") {
		t.Fatalf("return summary does not include photos and manual map: %+v", latest())
	}
	processor = Bootstrap{Data: client, Commands: retryingClient, MAX: sender, Photos: fetcher, PhotoStore: client, Location: time.UTC}
	deliverCallback(button("return-complete:"))
	if !strings.Contains(latest().Text, "Возврат подтверждён") {
		t.Fatalf("Go dialog did not confirm completion: %+v", latest())
	}
	attempts, injected, sameRequest := lostCompleteResponse.stats()
	if attempts != 2 || injected != 1 || !sameRequest {
		t.Fatalf("lost-response retry did not replay the same complete request: attempts=%d injected=%d same_request=%t", attempts, injected, sameRequest)
	}
	completed, err := client.Trip(ctx, actorID, trip.ID)
	if err != nil || completed.Status != "completed" || completed.AfterInspection == nil ||
		len(completed.BeforeInspection.OccupiedSlots) != 8 || len(completed.AfterInspection.OccupiedSlots) != 8 ||
		completed.ParkingLocation == nil || completed.ParkingLocation.Source != "manual_map" {
		t.Fatalf("Python trip lacks completed 8+8/manual-map evidence: trip=%+v err=%v", completed, err)
	}
	vehicle, err = client.Vehicle(ctx, actorID, trip.VehicleID)
	if err != nil || vehicle.Status != "available" {
		t.Fatalf("completed vehicle was not safely released: vehicle=%+v err=%v", vehicle, err)
	}
	previous, err := client.PreviousInspection(ctx, actorID, trip.VehicleID)
	if err != nil || previous.Phase != "after" || len(previous.OccupiedSlots) != 8 {
		t.Fatalf("completed return is absent from previous inspection: inspection=%+v err=%v", previous, err)
	}
	previousPhoto, err := client.PreviousInspectionPhoto(ctx, actorID, trip.VehicleID, 1)
	if err != nil || previousPhoto.ContentType != "image/png" || !bytes.Equal(previousPhoto.Bytes, samplePhoto(t, 151)) {
		t.Fatalf("previous inspection image was not recoverable from private storage: type=%q err=%v", previousPhoto.ContentType, err)
	}

	adminID := os.Getenv("MAX_FLEET_LIVE_DATA_API_ADMIN")
	if adminID == "" {
		adminID = "8000000000000000003"
	}
	adminMe, err := client.Me(ctx, adminID)
	if err != nil || !adminMe.Allowed || adminMe.Employee == nil || adminMe.Employee.Role != "admin" {
		t.Fatalf("synthetic admin access is unavailable: allowed=%t err=%v", adminMe.Allowed, err)
	}
	deliverCallback("trip:" + trip.ID)
	deliverCallback(button("trip-post-issue:"))
	parkingCategory := ""
	for _, row := range latest().Buttons {
		for _, choice := range row {
			if strings.HasSuffix(choice.Payload, ":parking") {
				parkingCategory = choice.Payload
			}
		}
	}
	if parkingCategory == "" {
		t.Fatalf("post-return issue dialog omitted parking category: %+v", latest())
	}
	deliverCallback(parkingCategory)
	issueDescription := "/issue Синтетическое замечание по парковке после поездки"
	descriptionEvent := menuItem(actorID, key(), time.Now().UTC()).Event
	descriptionEvent.Payload.Text = &issueDescription
	deliver(descriptionEvent)
	state, err = client.State(ctx, actorID)
	if err != nil || state.Conversation == nil || state.Conversation.Flow != postReturnIssueFlow || state.Conversation.Step != "collect_photos" {
		t.Fatalf("post-return issue draft was not durable: state=%+v err=%v", state, err)
	}
	menu()
	deliverCallback(button("trip-post-issue-review:"))
	if !strings.Contains(latest().Text, "Синтетическое замечание по парковке после поездки") {
		t.Fatalf("Go dialog review omitted issue description: %+v", latest())
	}
	deliverCallback(button("trip-post-issue-submit:"))
	state, err = client.State(ctx, actorID)
	if err != nil || state.Conversation == nil || state.Conversation.Step != "done" || state.Conversation.Context.IssueID == nil {
		t.Fatalf("Go dialog did not durably finish issue submission: state=%+v err=%v", state, err)
	}
	issue, err := client.Issue(ctx, actorID, *state.Conversation.Context.IssueID)
	if err != nil || issue.Stage != "post_return" || issue.Category != "parking" || issue.Status != "open" || issue.AssignedTo != nil {
		t.Fatalf("post-return issue aggregate mismatch: issue=%+v err=%v", issue, err)
	}
	_, err = client.IssueResolve(ctx, actorID, issue.ID, issue.Version,
		dataapi.IssueResolveInput{Status: "in_progress"}, key(), nil)
	var apiErr *dataapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("employee unexpectedly took admin issue: err=%v", err)
	}
	adminSender := &maxsdk.RecordingTransport{}
	adminProcessor := Bootstrap{Data: client, Commands: client, MAX: adminSender, Location: time.UTC}
	adminWorker := inboxworker.Worker{ID: "live-dialog-admin-smoke", Store: workerStore, Processor: &adminProcessor, Now: time.Now}
	deliverAdmin := func(event dataapi.NormalizedEvent) {
		t.Helper()
		if _, storeErr := workerStore.StoreInbox(ctx, event, maxsdk.InboxIdempotencyKey(event)); storeErr != nil {
			t.Fatalf("store admin MAX-like event: %v", storeErr)
		}
		result, runErr := adminWorker.RunOnce(ctx, 1)
		if runErr != nil || result.Acked != 1 {
			t.Fatalf("process live admin dialog event: result=%+v err=%v", result, runErr)
		}
	}
	adminClick := func(payload string) {
		t.Helper()
		deliverAdmin(callbackItem(adminID, key(), payload, time.Now().UTC()).Event)
	}
	adminLatest := func() maxsdk.RecordedText {
		t.Helper()
		messages := adminSender.Messages()
		if len(messages) == 0 {
			t.Fatal("admin dialog did not send a message")
		}
		return messages[len(messages)-1]
	}
	adminButton := func(prefix string) string {
		t.Helper()
		for _, row := range adminLatest().Buttons {
			for _, choice := range row {
				if strings.HasPrefix(choice.Payload, prefix) {
					return choice.Payload
				}
			}
		}
		t.Fatalf("admin dialog button %q missing in %+v", prefix, adminLatest())
		return ""
	}
	deliverAdmin(menuItem(adminID, key(), time.Now().UTC()).Event)
	adminClick(adminButton("admin-issues:"))
	issueCard := "admin-issue:" + issue.ID
	issueListed := false
	for _, row := range adminLatest().Buttons {
		for _, choice := range row {
			if choice.Payload == issueCard {
				issueListed = true
			}
		}
	}
	if !issueListed {
		t.Fatalf("new post-return issue is missing from Go admin list: %+v", adminLatest())
	}
	adminClick(issueCard)
	if !strings.Contains(adminLatest().Text, "Категория: парковка") {
		t.Fatalf("Go admin issue card did not show the post-return issue: %+v", adminLatest())
	}
	adminClick(adminButton("admin-issue-take:"))
	assigned, err := client.Issue(ctx, adminID, issue.ID)
	if err != nil || assigned.Status != "in_progress" || assigned.AssignedTo == nil || *assigned.AssignedTo != adminMe.Employee.ID {
		t.Fatalf("Go admin dialog did not assign issue to the acting admin: issue=%+v err=%v", assigned, err)
	}
	adminClick(adminButton("admin-issue-begin:"))
	comment := "Комментарий: Синтетическая проверка завершена"
	commentEvent := menuItem(adminID, key(), time.Now().UTC()).Event
	commentEvent.Payload.Text = &comment
	deliverAdmin(commentEvent)
	adminClick(adminButton("admin-issue-confirm:"))
	resolved, err := client.Issue(ctx, adminID, issue.ID)
	if err != nil || resolved.Status != "resolved" || resolved.AssignedTo == nil || *resolved.AssignedTo != adminMe.Employee.ID || resolved.ResolvedBy == nil || *resolved.ResolvedBy != adminMe.Employee.ID {
		t.Fatalf("Go admin resolution lost assignment/actor audit: issue=%+v err=%v", resolved, err)
	}
}

func answerLiveChallenge(t *testing.T, ctx context.Context, client *dataapi.Client, actorID string, challenge dataapi.Challenge, key string) {
	t.Helper()
	var a, b int
	if _, err := fmt.Sscanf(challenge.Question, "%d + %d = ?", &a, &b); err != nil {
		t.Fatalf("parse synthetic math challenge: %v", err)
	}
	for option, value := range challenge.Options {
		if value == a+b {
			if _, err := client.ChallengeAnswer(ctx, actorID, challenge.ID, challenge.Version, option, key, nil); err != nil {
				t.Fatalf("answer synthetic math challenge: %v", err)
			}
			return
		}
	}
	t.Fatal("synthetic math challenge has no correct option")
}

func liveDialogKey(t *testing.T) string {
	t.Helper()
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatalf("generate unique live idempotency key: %v", err)
	}
	return "live-dialog-" + hex.EncodeToString(bytes[:])
}

func signedSyntheticMapInitData(token, actorID string, now time.Time) string {
	fields := url.Values{
		"auth_date": {strconv.FormatInt(now.Unix(), 10)},
		"user":      {fmt.Sprintf(`{"id":%s}`, actorID)},
	}
	keys := make([]string, 0, len(fields))
	for name := range fields {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, name := range keys {
		lines = append(lines, name+"="+fields.Get(name))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secret.Write([]byte(token))
	signature := hmac.New(sha256.New, secret.Sum(nil))
	_, _ = signature.Write([]byte(strings.Join(lines, "\n")))
	fields.Set("hash", hex.EncodeToString(signature.Sum(nil)))
	return fields.Encode()
}

func readPrivateToken(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read private Data API credential file: %v", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		t.Fatal("private Data API credential file is empty")
	}
	return token
}

// lostResponseAfterCommit forwards return.complete to Python, discards its
// committed 200 response, and returns one synthetic 503 to exercise Go retry.
type lostResponseAfterCommitTransport struct {
	next       http.RoundTripper
	mu         sync.Mutex
	injected   bool
	requestIDs []string
	keys       []string
	bodies     [][]byte
}

func (r *lostResponseAfterCommitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/commands") {
		return r.next.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	var command struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(body, &command); err != nil || command.Operation != "return.complete" {
		return r.next.RoundTrip(req)
	}

	r.mu.Lock()
	r.requestIDs = append(r.requestIDs, req.Header.Get("X-Request-ID"))
	r.keys = append(r.keys, req.Header.Get("Idempotency-Key"))
	r.bodies = append(r.bodies, append([]byte(nil), body...))
	shouldInject := !r.injected
	r.mu.Unlock()
	response, err := r.next.RoundTrip(req)
	if err != nil || response.StatusCode != http.StatusOK || !shouldInject {
		return response, err
	}
	r.mu.Lock()
	r.injected = true
	r.mu.Unlock()
	_ = response.Body.Close()
	requestID := req.Header.Get("X-Request-ID")
	errorBody := fmt.Sprintf(`{"error":{"code":"SYNTHETIC_RESPONSE_LOST","message":"synthetic test dropped committed response","retryable":true,"details":{}},"request_id":%q}`, requestID)
	return &http.Response{
		StatusCode:    http.StatusServiceUnavailable,
		Status:        "503 Service Unavailable",
		Header:        http.Header{"Content-Type": []string{"application/json"}, "Retry-After": []string{"0"}},
		Body:          io.NopCloser(strings.NewReader(errorBody)),
		ContentLength: int64(len(errorBody)),
		Request:       req,
	}, nil
}

func (r *lostResponseAfterCommitTransport) stats() (attempts, injected int, sameRequest bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.injected {
		injected = 1
	}
	attempts = len(r.requestIDs)
	sameRequest = attempts == 2 && r.requestIDs[0] != "" && r.requestIDs[0] == r.requestIDs[1] &&
		r.keys[0] != "" && r.keys[0] == r.keys[1] && bytes.Equal(r.bodies[0], r.bodies[1])
	return attempts, injected, sameRequest
}
