package datamock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type contractScenarios struct {
	ContractVersion string `json:"contract_version"`
	Cases           []struct {
		ID     string `json:"id"`
		Expect struct {
			HTTP  int     `json:"http"`
			Error *string `json:"error"`
		} `json:"expect"`
	} `json:"cases"`
}

type scenarioContext struct {
	t      *testing.T
	client *dataapi.Client
	worker *dataapi.WorkerClient
	mock   *Server
	ctx    context.Context
	now    time.Time
	clock  *time.Time
}

func (s scenarioContext) hold(key string) dataapi.Checkout {
	s.t.Helper()
	result, err := s.client.CheckoutCreate(s.ctx, driverID, firstVehicleID, 1, key, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	hold, err := dataapi.DecodeAggregate[dataapi.Checkout](result)
	if err != nil {
		s.t.Fatal(err)
	}
	return hold
}

func (s scenarioContext) upload(inspectionID string, slot int, version int64, tone uint8, event string) (dataapi.PhotoUploadResult, error) {
	return s.client.UploadInspectionPhoto(s.ctx, driverID, dataapi.InspectionPhotoInput{InspectionID: inspectionID, Slot: slot, Version: version, SourceEventKey: event, IdempotencyKey: "scenario-photo-" + event, ContentType: "image/png", Image: syntheticPNG(s.t, tone)})
}

func (s scenarioContext) photoSet(count int) dataapi.Checkout {
	s.t.Helper()
	hold := s.hold("scenario-photo-hold")
	version := hold.Inspection.Version
	for slot := 1; slot <= count; slot++ {
		result, err := s.upload(hold.Inspection.ID, slot, version, uint8(slot), fmt.Sprintf("slot-%d", slot))
		if err != nil {
			s.t.Fatal(err)
		}
		version = result.Inspection.Version
	}
	hold.Inspection.Version = version
	return hold
}

func (s scenarioContext) readyCheckout() dataapi.Checkout {
	s.t.Helper()
	hold := s.photoSet(8)
	ready := s.mock.checkouts[hold.ID]
	stamp, yes := *s.clock, true
	rulesID := s.mock.rules.ID
	fuel, odometer := 75, int64(12010)
	ready.IntentConfirmedAt = &stamp
	ready.RulesAcceptedAt = &stamp
	ready.RulesVersionID = &rulesID
	ready.NoNewIssues = &yes
	ready.Inspection.FuelLevel = &fuel
	ready.Inspection.OdometerKM = &odometer
	ready.Inspection.PhotosConfirmedAt = &stamp
	ready.Inspection.MissingSlots = []int{}
	s.mock.checkouts[hold.ID] = ready
	return ready
}

func (s scenarioContext) rawActorRequest(method, path, body, key string) error {
	s.t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer test-service-token")
	request.Header.Set("X-Contract-Version", dataapi.ContractVersion)
	request.Header.Set("X-Request-ID", "99999999-9999-4999-8999-999999999999")
	request.Header.Set("X-Actor-Max-ID", driverID)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response := httptest.NewRecorder()
	s.mock.Handler().ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		return nil
	}
	var envelope struct {
		Error dataapi.APIError `json:"error"`
	}
	if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Error.Code == "" {
		s.t.Fatalf("invalid raw error response: %d", response.Code)
	}
	envelope.Error.Status = response.Code
	return &envelope.Error
}

func (s scenarioContext) readyReturn(damage, parkingAllowed bool) (dataapi.Trip, dataapi.Return) {
	s.t.Helper()
	tripID := "20000000-0000-4000-8000-000000000001"
	returnID := "30000000-0000-4000-8000-000000000001"
	inspectionID := "40000000-0000-4000-8000-000000000001"
	employee := s.mock.employees[driverID]
	employee.ActiveTripID = &tripID
	s.mock.employees[driverID] = employee
	s.mock.vehicles[0].Status = "in_trip"
	beforeOdo, afterOdo, fuel := int64(12000), int64(12025), 50
	clean, yes := true, true
	trip := dataapi.Trip{ID: tripID, VehicleID: firstVehicleID, EmployeeID: employee.ID, Status: "returning", ReturnID: &returnID, BeforeInspection: dataapi.Inspection{OdometerKM: &beforeOdo}, Issues: []dataapi.Issue{}, Version: 2, UpdatedAt: s.now}
	if damage {
		trip.Issues = append(trip.Issues, dataapi.Issue{ID: "50000000-0000-4000-8000-000000000001", Stage: "after", InspectionID: &inspectionID, Category: "body_damage"})
	}
	s.mock.trips[tripID] = trip
	draft := dataapi.Return{ID: returnID, TripID: tripID, Status: "draft", IntentConfirmedAt: &s.now, ParkingLocation: &dataapi.ParkingLocation{ID: "60000000-0000-4000-8000-000000000001", Latitude: 55.75, Longitude: 37.62, Source: "manual_map", ConfirmedAt: s.now}, Version: 1, UpdatedAt: s.now,
		Inspection: dataapi.Inspection{ID: inspectionID, Phase: "after", Status: "draft", FuelLevel: &fuel, OdometerKM: &afterOdo, NewDamage: &damage, CabinClean: &clean, ParkingAllowed: &parkingAllowed, KeysReturned: &yes, CarLocked: &yes, OccupiedSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}, MissingSlots: []int{}, PhotosConfirmedAt: &s.now, Version: 1, UpdatedAt: s.now}}
	s.mock.returns[returnID] = draft
	return trip, draft
}

func TestContractScenarioSubsetAgainstHTTPMock(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "contracts", "scenarios", "v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec contractScenarios
	if err := json.Unmarshal(data, &spec); err != nil || spec.ContractVersion != dataapi.ContractVersion {
		t.Fatalf("scenario contract: %v", err)
	}
	runs := map[string]func(s scenarioContext) error{
		"identity.admin": func(s scenarioContext) error {
			summary, err := s.client.AdminSummary(s.ctx, "8000000000000000003")
			if err == nil && summary.Available != 10 {
				s.t.Fatal("admin did not receive fleet summary")
			}
			return err
		},
		"identity.employee": func(s scenarioContext) error {
			me, err := s.client.Me(s.ctx, driverID)
			if err == nil && (!me.Allowed || me.Employee == nil || me.Employee.Role != "employee") {
				s.t.Fatal("employee identity facts failed")
			}
			return err
		},
		"identity.unknown": func(s scenarioContext) error {
			_, err := s.client.Vehicles(s.ctx, "9000000000000000001", dataapi.VehicleFilter{})
			return err
		},
		"identity.blocked-return": func(s scenarioContext) error {
			actor := "8000000000000000004"
			employee := s.mock.employees[actor]
			tripID := "20000000-0000-4000-8000-000000000004"
			employee.ActiveTripID = &tripID
			s.mock.employees[actor] = employee
			s.mock.vehicles[0].Status = "in_trip"
			s.mock.trips[tripID] = dataapi.Trip{ID: tripID, VehicleID: firstVehicleID, EmployeeID: employee.ID, Status: "active", Version: 1, UpdatedAt: s.now, Issues: []dataapi.Issue{}, MissingData: []string{}}
			_, newErr := s.client.CheckoutCreate(s.ctx, actor, s.mock.vehicles[1].ID, 1, "scenario-blocked-new-hold", nil)
			var apiErr *dataapi.APIError
			if !errors.As(newErr, &apiErr) || apiErr.Code != "CANNOT_START_TRIP" {
				s.t.Fatalf("blocked actor could start another trip: %v", newErr)
			}
			result, err := s.client.TripBeginReturn(s.ctx, actor, tripID, 1, "scenario-blocked-return", nil)
			if err != nil {
				return err
			}
			draft, err := dataapi.DecodeAggregate[dataapi.Return](result)
			if err != nil || draft.Status != "draft" || s.mock.trips[tripID].Status != "returning" || draft.TripID != tripID {
				s.t.Fatalf("blocked actor could not return own trip: %+v %v", draft, err)
			}
			return nil
		},
		"identity.wrong-owner": func(s scenarioContext) error {
			_, draft := s.readyReturn(false, true)
			_, err := s.client.Return(s.ctx, "8000000000000000002", draft.ID)
			return err
		},
		"vehicles.free": func(s scenarioContext) error {
			available := true
			page, err := s.client.Vehicles(s.ctx, driverID, dataapi.VehicleFilter{Available: &available})
			found := false
			for _, item := range page.Items {
				if item.ID == firstVehicleID {
					found = true
				}
			}
			if err == nil && !found {
				s.t.Fatal("DEMO-001 missing from free vehicles")
			}
			return err
		},
		"vehicles.holding": func(s scenarioContext) error {
			hold := s.hold("scenario-holding")
			if hold.ExpiresAt.Sub(s.now) != 15*time.Minute {
				s.t.Fatal("hold TTL differs from 15 minutes")
			}
			available := true
			page, err := s.client.Vehicles(s.ctx, driverID, dataapi.VehicleFilter{Available: &available})
			for _, item := range page.Items {
				if item.ID == firstVehicleID {
					s.t.Fatal("holding vehicle still free")
				}
			}
			return err
		},
		"vehicles.incomplete": func(s scenarioContext) error {
			s.mock.vehicles[0].CurrentParking = nil
			s.mock.vehicles[1].KeyInstructions = ""
			available := true
			page, err := s.client.Vehicles(s.ctx, driverID, dataapi.VehicleFilter{Available: &available, Limit: 50})
			if err != nil {
				return err
			}
			for _, vehicle := range page.Items {
				if vehicle.ID == s.mock.vehicles[0].ID || vehicle.ID == s.mock.vehicles[1].ID {
					s.t.Fatal("incomplete vehicle offered as available")
				}
			}
			if len(page.Items) != 8 {
				s.t.Fatalf("expected 8 eligible vehicles, got %d", len(page.Items))
			}
			summary, err := s.client.AdminSummary(s.ctx, "8000000000000000003")
			if err != nil || summary.Available != 8 {
				s.t.Fatalf("admin available count differs from list: %+v %v", summary, err)
			}
			_, err = s.client.CheckoutCreate(s.ctx, driverID, firstVehicleID, 1, "scenario-incomplete-hold", nil)
			var apiErr *dataapi.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != "VEHICLE_UNAVAILABLE" {
				s.t.Fatalf("incomplete vehicle could be held: %v", err)
			}
			return nil
		},
		"vehicles.known-issue": func(s scenarioContext) error {
			s.mock.vehicles[0].KnownNonblockingIssues = []string{"Синтетический скол краски"}
			vehicle, err := s.client.Vehicle(s.ctx, driverID, firstVehicleID)
			if err != nil || len(vehicle.KnownNonblockingIssues) != 1 {
				s.t.Fatalf("known issue hidden: %+v %v", vehicle, err)
			}
			available := true
			page, err := s.client.Vehicles(s.ctx, driverID, dataapi.VehicleFilter{Available: &available, Limit: 50})
			if err != nil || len(page.Items) != 10 {
				s.t.Fatalf("known issue blocked availability: %d %v", len(page.Items), err)
			}
			hold := s.hold("scenario-known-issue-hold")
			if hold.VehicleID != firstVehicleID || s.mock.vehicles[0].Status != "holding" {
				s.t.Fatal("known issue blocked checkout")
			}
			return nil
		},
		"checkout.busy": func(s scenarioContext) error {
			s.hold("scenario-busy-one")
			_, err := s.client.CheckoutCreate(s.ctx, "8000000000000000002", firstVehicleID, 1, "scenario-busy-two", nil)
			if len(s.mock.checkouts) != 1 {
				s.t.Fatal("busy checkout created another hold")
			}
			return err
		},
		"checkout.happy": func(s scenarioContext) error {
			ready := s.readyCheckout()
			result, err := s.client.CheckoutStart(s.ctx, driverID, ready.ID, ready.Version, "scenario-happy-start", nil)
			if err != nil {
				return err
			}
			trip, err := dataapi.DecodeAggregate[dataapi.Trip](result)
			if err != nil || trip.Status != "active" || len(s.mock.trips) != 1 || s.mock.checkouts[ready.ID].Status != "started" || s.mock.vehicles[0].Status != "in_trip" || s.mock.employees[driverID].ActiveTripID == nil || *s.mock.employees[driverID].ActiveTripID != trip.ID {
				s.t.Fatalf("checkout did not atomically become one trip: %+v %v", trip, err)
			}
			_, secondErr := s.client.CheckoutCreate(s.ctx, "8000000000000000002", firstVehicleID, s.mock.vehicles[0].Version, "scenario-happy-second-driver", nil)
			var apiErr *dataapi.APIError
			if !errors.As(secondErr, &apiErr) || apiErr.Code != "VEHICLE_UNAVAILABLE" || len(s.mock.trips) != 1 {
				s.t.Fatalf("second assignment was not blocked: %v", secondErr)
			}
			return nil
		},
		"checkout.issue-before": func(s scenarioContext) error {
			hold := s.hold("scenario-before-issue-hold")
			input := dataapi.IssueCreateInput{Category: "mechanical", Description: "Синтетическая неисправность", InspectionID: &hold.Inspection.ID}
			result, err := s.client.IssueCreate(s.ctx, driverID, firstVehicleID, s.mock.vehicles[0].Version, input, "scenario-before-issue", nil)
			if err != nil {
				return err
			}
			issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
			if err != nil || issue.Stage != "before" || issue.Status != "open" || !issue.BlocksIssuance || s.mock.checkouts[hold.ID].Status != "rejected" || s.mock.vehicles[0].Status != "unavailable" || !s.mock.vehicles[0].NeedsReview || len(s.mock.trips) != 0 {
				s.t.Fatalf("before issue did not cancel hold and block vehicle: %+v %v", issue, err)
			}
			if _, err := s.client.CheckoutCreate(s.ctx, "8000000000000000002", firstVehicleID, s.mock.vehicles[0].Version, "scenario-before-issue-second", nil); err == nil {
				s.t.Fatal("blocked vehicle was issued")
			}
			return nil
		},
		"checkout.race-loser": func(s scenarioContext) error {
			type outcome struct {
				result dataapi.CommandResult
				err    error
			}
			start := make(chan struct{})
			results := make(chan outcome, 2)
			for _, actor := range []string{driverID, "8000000000000000002"} {
				actor := actor
				go func() {
					<-start
					result, err := s.client.CheckoutCreate(s.ctx, actor, firstVehicleID, 1, "scenario-race-"+actor, nil)
					results <- outcome{result, err}
				}()
			}
			close(start)
			first, second := <-results, <-results
			winner, loser := first, second
			if winner.err != nil {
				winner, loser = second, first
			}
			if winner.err != nil || loser.err == nil || len(s.mock.checkouts) != 1 || s.mock.vehicles[0].Status != "holding" {
				s.t.Fatalf("race did not select exactly one winner: %v / %v", first.err, second.err)
			}
			checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](winner.result)
			if err != nil || checkout.Status != "holding" || s.mock.checkouts[checkout.ID].ID != checkout.ID {
				s.t.Fatalf("winning hold missing: %+v %v", checkout, err)
			}
			return loser.err
		},
		"checkout.cancel": func(s scenarioContext) error {
			hold := s.hold("scenario-cancel-hold")
			_, err := s.client.CheckoutCancel(s.ctx, driverID, hold.ID, hold.Version, "scenario-cancel", nil)
			if err != nil {
				return err
			}
			vehicle, err := s.client.Vehicle(s.ctx, driverID, firstVehicleID)
			if err == nil && vehicle.Status != "available" {
				s.t.Fatal("cancel did not release vehicle")
			}
			return err
		},
		"checkout.expired": func(s scenarioContext) error {
			hold := s.hold("scenario-expired-hold")
			s.mock.now = func() time.Time { return s.now.Add(15*time.Minute + time.Second) }
			_, err := s.client.CheckoutStart(s.ctx, driverID, hold.ID, hold.Version, "scenario-expired-start", nil)
			if len(s.mock.trips) != 0 {
				s.t.Fatal("expired hold created trip")
			}
			return err
		},
		"inspection.zero": func(s scenarioContext) error {
			hold := s.hold("scenario-zero-photos")
			_, err := s.client.InspectionConfirmPhotos(s.ctx, driverID, hold.Inspection.ID, hold.Inspection.Version, "scenario-confirm-zero", nil)
			var apiErr *dataapi.APIError
			if errors.As(err, &apiErr) && len(apiErr.Details.MissingSlots) != 8 {
				s.t.Fatal("zero photos did not report eight missing slots")
			}
			return err
		},
		"inspection.seven": func(s scenarioContext) error {
			hold := s.photoSet(7)
			_, err := s.client.InspectionConfirmPhotos(s.ctx, driverID, hold.Inspection.ID, hold.Inspection.Version, "scenario-confirm-seven", nil)
			var apiErr *dataapi.APIError
			if errors.As(err, &apiErr) && (len(apiErr.Details.MissingSlots) != 1 || apiErr.Details.MissingSlots[0] != 8) {
				s.t.Fatal("seven photos did not report slot 8")
			}
			inspection, readErr := s.client.Inspection(s.ctx, driverID, hold.Inspection.ID)
			if readErr != nil || len(inspection.OccupiedSlots) != 7 {
				s.t.Fatal("seven photos were lost")
			}
			return err
		},
		"inspection.eight": func(s scenarioContext) error {
			hold := s.photoSet(8)
			result, err := s.client.InspectionConfirmPhotos(s.ctx, driverID, hold.Inspection.ID, hold.Inspection.Version, "scenario-confirm-eight", nil)
			if err != nil {
				return err
			}
			inspection, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
			if err == nil && (inspection.PhotosConfirmedAt == nil || len(inspection.OccupiedSlots) != 8) {
				s.t.Fatal("eight photos not confirmed")
			}
			return err
		},
		"inspection.duplicate-event": func(s scenarioContext) error {
			hold := s.hold("scenario-duplicate-event-hold")
			first, err := s.upload(hold.Inspection.ID, 1, hold.Inspection.Version, 31, "repeat-event")
			if err != nil {
				return err
			}
			again, err := s.upload(hold.Inspection.ID, 1, hold.Inspection.Version, 31, "repeat-event")
			if err == nil && (again.AssetID != first.AssetID || again.Inspection.Version != first.Inspection.Version) {
				s.t.Fatal("photo retry changed asset or version")
			}
			return err
		},
		"inspection.duplicate-hash": func(s scenarioContext) error {
			hold := s.hold("scenario-duplicate-hash-hold")
			first, err := s.upload(hold.Inspection.ID, 1, hold.Inspection.Version, 32, "hash-first")
			if err != nil {
				return err
			}
			_, err = s.upload(hold.Inspection.ID, 2, first.Inspection.Version, 32, "hash-second")
			inspection, readErr := s.client.Inspection(s.ctx, driverID, hold.Inspection.ID)
			if readErr != nil || len(inspection.OccupiedSlots) != 1 {
				s.t.Fatal("duplicate hash occupied second slot")
			}
			return err
		},
		"inspection.replace": func(s scenarioContext) error {
			hold := s.photoSet(8)
			result, err := s.client.InspectionConfirmPhotos(s.ctx, driverID, hold.Inspection.ID, hold.Inspection.Version, "scenario-confirm-replace", nil)
			if err != nil {
				return err
			}
			confirmed, err := dataapi.DecodeAggregate[dataapi.Inspection](result)
			if err != nil {
				return err
			}
			before := make(map[int]string)
			for slot, photo := range s.mock.photos[hold.Inspection.ID] {
				before[slot] = photo.AssetID
			}
			replaced, err := s.upload(hold.Inspection.ID, 3, confirmed.Version, 99, "replace-slot-three")
			if err != nil {
				return err
			}
			if replaced.Inspection.Version != confirmed.Version+1 || replaced.Inspection.PhotosConfirmedAt != nil || len(replaced.Inspection.OccupiedSlots) != 8 {
				s.t.Fatal("replacement did not invalidate photo confirmation")
			}
			for slot, photo := range s.mock.photos[hold.Inspection.ID] {
				if slot != 3 && photo.AssetID != before[slot] {
					s.t.Fatal("replacement changed another slot")
				}
			}
			return nil
		},
		"inspection.storage-error": func(s scenarioContext) error {
			hold := s.photoSet(1)
			first := s.mock.photos[hold.Inspection.ID][1].AssetID
			s.mock.saveSnapshot = func(stateSnapshot) error { return os.ErrPermission }
			_, err := s.upload(hold.Inspection.ID, 2, hold.Inspection.Version, 41, "storage-fail")
			if len(s.mock.photos[hold.Inspection.ID]) != 1 || s.mock.photos[hold.Inspection.ID][1].AssetID != first {
				s.t.Fatal("failed upload changed saved slots")
			}
			return err
		},
		"return.success": func(s scenarioContext) error {
			trip, draft := s.readyReturn(false, true)
			result, err := s.client.ReturnComplete(s.ctx, driverID, draft.ID, draft.Version, "scenario-return-success", nil)
			if err != nil {
				return err
			}
			completed, err := dataapi.DecodeAggregate[dataapi.Return](result)
			if err == nil && (completed.Status != "completed" || s.mock.trips[trip.ID].Status != "completed" || s.mock.vehicles[0].Status != "available") {
				s.t.Fatal("successful return did not release vehicle")
			}
			return err
		},
		"return.damage": func(s scenarioContext) error {
			trip, draft := s.readyReturn(true, true)
			_, err := s.client.ReturnComplete(s.ctx, driverID, draft.ID, draft.Version, "scenario-return-damage", nil)
			if err == nil && (s.mock.trips[trip.ID].Status != "completed" || s.mock.vehicles[0].Status != "unavailable" || !s.mock.vehicles[0].NeedsReview) {
				s.t.Fatal("damage return released vehicle")
			}
			return err
		},
		"return.post-return-issue": func(s scenarioContext) error {
			trip, draft := s.readyReturn(false, true)
			if _, err := s.client.ReturnComplete(s.ctx, driverID, draft.ID, draft.Version, "scenario-post-complete", nil); err != nil {
				return err
			}
			completed := s.mock.trips[trip.ID]
			result, err := s.client.IssueCreate(s.ctx, driverID, firstVehicleID, s.mock.vehicles[0].Version,
				dataapi.IssueCreateInput{Category: "mechanical", Description: "Позднее замечен звук", TripID: &trip.ID}, "scenario-post-issue", nil)
			if err != nil {
				return err
			}
			issue, err := dataapi.DecodeAggregate[dataapi.Issue](result)
			if err == nil && (issue.Stage != "post_return" || s.mock.trips[trip.ID].Version != completed.Version || s.mock.trips[trip.ID].AfterInspection.Version != completed.AfterInspection.Version || !s.mock.vehicles[0].NeedsReview || s.mock.vehicles[0].Status != "unavailable" || len(s.mock.notifications) != 2) {
				s.t.Fatal("post-return issue changed completed snapshot or missed review/notification")
			}
			return err
		},
		"return.post-return-foreign": func(s scenarioContext) error {
			trip, draft := s.readyReturn(false, true)
			if _, err := s.client.ReturnComplete(s.ctx, driverID, draft.ID, draft.Version, "scenario-foreign-complete", nil); err != nil {
				return err
			}
			_, err := s.client.IssueCreate(s.ctx, "8000000000000000002", firstVehicleID, s.mock.vehicles[0].Version,
				dataapi.IssueCreateInput{Category: "mechanical", Description: "Чужая поездка", TripID: &trip.ID}, "scenario-foreign-issue", nil)
			if len(s.mock.issues) != 0 || len(s.mock.notifications) != 1 {
				s.t.Fatal("foreign issue changed state or sent notification")
			}
			return err
		},
		"return.unsafe": func(s scenarioContext) error {
			trip, draft := s.readyReturn(false, false)
			_, err := s.client.ReturnComplete(s.ctx, driverID, draft.ID, draft.Version, "scenario-return-unsafe", nil)
			if s.mock.trips[trip.ID].Status != "returning" || s.mock.returns[draft.ID].Status != "draft" {
				s.t.Fatal("unsafe return completed trip")
			}
			return err
		},
		"return.cancel-new": func(s scenarioContext) error {
			tripID := "20000000-0000-4000-8000-000000000001"
			s.mock.trips[tripID] = dataapi.Trip{ID: tripID, VehicleID: firstVehicleID, EmployeeID: s.mock.employees[driverID].ID, Status: "active", Version: 1, UpdatedAt: s.now}
			firstResult, err := s.client.TripBeginReturn(s.ctx, driverID, tripID, 1, "scenario-begin-one", nil)
			if err != nil {
				return err
			}
			first, err := dataapi.DecodeAggregate[dataapi.Return](firstResult)
			if err != nil {
				return err
			}
			if _, err = s.client.ReturnCancel(s.ctx, driverID, first.ID, first.Version, "scenario-cancel-return", nil); err != nil {
				return err
			}
			trip, err := s.client.Trip(s.ctx, driverID, tripID)
			if err != nil {
				return err
			}
			if trip.Status != "active" || trip.ReturnID != nil {
				s.t.Fatal("cancel did not restore active trip")
			}
			secondResult, err := s.client.TripBeginReturn(s.ctx, driverID, tripID, trip.Version, "scenario-begin-two", nil)
			if err != nil {
				return err
			}
			second, err := dataapi.DecodeAggregate[dataapi.Return](secondResult)
			if err == nil && (second.ID == first.ID || second.Inspection.ID == first.Inspection.ID || len(second.Inspection.OccupiedSlots) != 0 || len(second.Inspection.MissingSlots) != 8 || second.ParkingLocation != nil) {
				s.t.Fatal("new return inherited stale data")
			}
			return err
		},
		"schema.stale": func(s scenarioContext) error {
			trip, draft := s.readyReturn(false, true)
			_, err := s.client.ReturnComplete(s.ctx, driverID, draft.ID, draft.Version+1, "scenario-stale-complete", nil)
			if s.mock.trips[trip.ID].Status != "returning" {
				s.t.Fatal("stale return completed trip")
			}
			return err
		},
		"schema.limit": func(s scenarioContext) error {
			err := s.rawActorRequest(http.MethodGet, "/internal/v1/vehicles?limit=51", "", "")
			if len(s.mock.vehicles) != 10 {
				s.t.Fatal("invalid limit changed vehicle data")
			}
			return err
		},
		"schema.malformed-uuid": func(s scenarioContext) error {
			err := s.rawActorRequest(http.MethodPost, "/internal/v1/commands", `{"operation":"checkout.create","target_id":"not-a-uuid","expected_version":1,"payload":{}}`, "scenario-invalid-uuid")
			if len(s.mock.checkouts) != 0 {
				s.t.Fatal("malformed UUID changed checkout state")
			}
			return err
		},
		"schema.null-omitted": func(s scenarioContext) error {
			err := s.rawActorRequest(http.MethodPost, "/internal/v1/commands", `{"operation":"checkout.create","target_id":"10000000-0000-4000-8000-000000000001","expected_version":1}`, "scenario-omitted-payload")
			if len(s.mock.checkouts) != 0 {
				s.t.Fatal("omitted payload changed checkout state")
			}
			return err
		},
		"schema.unknown-enum": func(s scenarioContext) error {
			hold := s.hold("scenario-invalid-fuel-hold")
			body := fmt.Sprintf(`{"operation":"inspection.update","target_id":"%s","expected_version":%d,"payload":{"fuel_level":37}}`, hold.Inspection.ID, hold.Inspection.Version)
			err := s.rawActorRequest(http.MethodPost, "/internal/v1/commands", body, "scenario-invalid-fuel")
			if s.mock.checkouts[hold.ID].Inspection.FuelLevel != nil || s.mock.checkouts[hold.ID].Inspection.Version != hold.Inspection.Version {
				s.t.Fatal("invalid fuel changed inspection")
			}
			return err
		},
		"schema.same-key-different-body": func(s scenarioContext) error {
			s.hold("scenario-same-key")
			_, err := s.client.CheckoutCreate(s.ctx, driverID, "10000000-0000-4000-8000-000000000002", 1, "scenario-same-key", nil)
			if len(s.mock.checkouts) != 1 {
				s.t.Fatal("conflicting key changed checkout state")
			}
			return err
		},
		"delivery.lease-expired": func(s scenarioContext) error {
			var event dataapi.NormalizedEvent
			if err := json.Unmarshal(inboxFixture(s.t), &event); err != nil {
				s.t.Fatal(err)
			}
			stored, err := s.worker.StoreInbox(s.ctx, event, "scenario-store-lease")
			if err != nil {
				s.t.Fatal(err)
			}
			claim, err := s.worker.ClaimInbox(s.ctx, "scenario-worker", 1, "scenario-claim-lease")
			if err != nil || len(claim.Items) != 1 || claim.Items[0].ID != stored.ID {
				s.t.Fatalf("lease setup: %+v %v", claim, err)
			}
			*s.clock = claim.Items[0].LeaseExpiresAt.Add(time.Second)
			_, err = s.worker.AckInbox(s.ctx, stored.ID, claim.Items[0].LeaseToken, "scenario-ack-expired")
			for _, item := range s.mock.inbox {
				if item.Stored.ID == stored.ID && item.Status != "leased" {
					s.t.Fatalf("expired lease changed inbox state: %s", item.Status)
				}
			}
			return err
		},
		"delivery.timeout-after-commit": func(s scenarioContext) error {
			ready := s.readyCheckout()
			var attempts int
			var firstRequestID, firstBody string
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					s.t.Errorf("proxy read: %v", err)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				attempts++
				if attempts == 1 {
					firstRequestID, firstBody = r.Header.Get("X-Request-ID"), string(body)
					response := httptest.NewRecorder()
					s.mock.Handler().ServeHTTP(response, r)
					if response.Code != http.StatusOK || len(s.mock.trips) != 1 {
						s.t.Errorf("first command was not committed: %d", response.Code)
						return
					}
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						s.t.Errorf("cannot drop committed response: %v", err)
						return
					}
					_ = connection.Close()
					return
				}
				if attempts == 2 && (r.Header.Get("X-Request-ID") != firstRequestID || string(body) != firstBody || r.Header.Get("Idempotency-Key") != "scenario-start-lost-response") {
					s.t.Error("retry changed command identity or body")
				}
				s.mock.Handler().ServeHTTP(w, r)
			}))
			s.t.Cleanup(proxy.Close)
			client, err := dataapi.New(dataapi.Config{BaseURL: proxy.URL + "/internal/v1", Token: "test-service-token"})
			if err != nil {
				s.t.Fatal(err)
			}
			result, err := client.CheckoutStart(s.ctx, driverID, ready.ID, ready.Version, "scenario-start-lost-response", nil)
			if err != nil {
				return err
			}
			trip, err := dataapi.DecodeAggregate[dataapi.Trip](result)
			if err != nil || attempts != 2 || len(s.mock.trips) != 1 || s.mock.trips[trip.ID].Status != "active" || len(s.mock.notifications) != 1 {
				s.t.Fatalf("retry duplicated trip or notification: %+v %v, attempts=%d", trip, err, attempts)
			}
			return nil
		},
	}
	seen := map[string]bool{}
	for _, item := range spec.Cases {
		if seen[item.ID] {
			t.Fatalf("duplicate scenario %s", item.ID)
		}
		seen[item.ID] = true
		run, selected := runs[item.ID]
		if !selected {
			continue
		}
		t.Run(item.ID, func(t *testing.T) {
			now := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
			mock, err := NewWithSnapshotAndWorkerToken("test-service-token", "worker-token", filepath.Join(t.TempDir(), "state.json"), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(mock.Handler())
			t.Cleanup(server.Close)
			worker, err := dataapi.NewWorker(dataapi.WorkerConfig{BaseURL: server.URL + "/internal/v1", Token: "worker-token"})
			if err != nil {
				t.Fatal(err)
			}
			s := scenarioContext{t: t, client: commandClient(t, mock), worker: worker, mock: mock, ctx: context.Background(), now: now, clock: &now}
			err = run(s)
			status, code := 200, ""
			if err != nil {
				var apiErr *dataapi.APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("non-HTTP failure: %v", err)
				}
				status, code = apiErr.Status, apiErr.Code
			}
			wantCode := ""
			if item.Expect.Error != nil {
				wantCode = *item.Expect.Error
			}
			if status != item.Expect.HTTP || code != wantCode {
				t.Fatalf("scenario %s: got %d %s, want %d %s", item.ID, status, code, item.Expect.HTTP, wantCode)
			}
		})
	}
	if len(runs) != 36 {
		t.Fatal("scenario runner count changed")
	}
	for id := range runs {
		if !seen[id] {
			t.Fatalf("scenario %s missing from contract", id)
		}
	}
}
