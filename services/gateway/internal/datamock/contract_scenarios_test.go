package datamock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	mock   *Server
	ctx    context.Context
	now    time.Time
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
		"checkout.busy": func(s scenarioContext) error {
			s.hold("scenario-busy-one")
			_, err := s.client.CheckoutCreate(s.ctx, "8000000000000000002", firstVehicleID, 1, "scenario-busy-two", nil)
			if len(s.mock.checkouts) != 1 {
				s.t.Fatal("busy checkout created another hold")
			}
			return err
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
		"schema.same-key-different-body": func(s scenarioContext) error {
			s.hold("scenario-same-key")
			_, err := s.client.CheckoutCreate(s.ctx, driverID, "10000000-0000-4000-8000-000000000002", 1, "scenario-same-key", nil)
			if len(s.mock.checkouts) != 1 {
				s.t.Fatal("conflicting key changed checkout state")
			}
			return err
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
			mock, err := NewWithSnapshot("test-service-token", filepath.Join(t.TempDir(), "state.json"), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			s := scenarioContext{t: t, client: commandClient(t, mock), mock: mock, ctx: context.Background(), now: now}
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
	if len(runs) != 12 {
		t.Fatal("scenario runner count changed")
	}
	for id := range runs {
		if !seen[id] {
			t.Fatalf("scenario %s missing from contract", id)
		}
	}
}
