package datamock

import (
	"context"
	"encoding/json"
	"errors"
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
			mock, err := NewWithClock("test-service-token", func() time.Time { return now })
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
	if len(runs) != 8 {
		t.Fatal("scenario runner count changed")
	}
	for id := range runs {
		if !seen[id] {
			t.Fatalf("scenario %s missing from contract", id)
		}
	}
}
