package datamock

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

func TestVehicleSnapshotCorrectionDuringHoldPreservesPhotosAndRecovers(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 9, 29, 16, 15, 0, 0, time.UTC)
	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	mock, err := NewWithSnapshot("test-service-token", snapshotPath, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	client := commandClient(t, mock)
	created, err := client.CheckoutCreate(ctx, driverID, firstVehicleID, 1, "odometer-hold-create", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := dataapi.DecodeAggregate[dataapi.Checkout](created)
	if err != nil {
		t.Fatal(err)
	}
	checkout.IntentConfirmedAt = &clock
	mock.checkouts[checkout.ID] = checkout
	accepted, err := client.CheckoutAcceptRules(ctx, driverID, checkout.ID, checkout.Version, mock.rules.ID, "odometer-hold-rules", nil)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err = dataapi.DecodeAggregate[dataapi.Checkout](accepted)
	if err != nil {
		t.Fatal(err)
	}
	seedCorrectionPhotos(t, mock, &checkout.Inspection, clock)
	mock.checkouts[checkout.ID] = checkout
	if err := mock.persist(); err != nil {
		t.Fatal(err)
	}
	checkoutBefore := mock.checkouts[checkout.ID]
	photoBefore := clonePhotoSlots(mock.photos[checkout.Inspection.ID])

	wrongValue := int64(11900)
	_, err = client.InspectionUpdate(ctx, driverID, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{OdometerKM: &wrongValue}, "odometer-hold-first-attempt", nil)
	expectAPIError(t, err, "ODOMETER_ROLLBACK")

	vehicle, err := client.Vehicle(ctx, adminActorID, firstVehicleID)
	if err != nil {
		t.Fatal(err)
	}
	input := dataapi.VehicleSnapshotCorrectionInput{Reason: "Проверили фото панели", OdometerKM: &wrongValue, Confirmation: true}
	_, err = client.VehicleCorrectSnapshot(ctx, driverID, firstVehicleID, vehicle.Version, input, "odometer-hold-not-admin", nil)
	expectAPIError(t, err, "ADMIN_REQUIRED")

	fuel := 50
	inputWithFuel := input
	inputWithFuel.FuelLevel = &fuel
	_, err = client.VehicleCorrectSnapshot(ctx, adminActorID, firstVehicleID, vehicle.Version, inputWithFuel, "odometer-hold-extra-field", nil)
	expectAPIError(t, err, "INVALID_STATE")
	_, err = client.VehicleCorrectSnapshot(ctx, adminActorID, firstVehicleID, vehicle.Version-1, input, "odometer-hold-stale", nil)
	expectAPIError(t, err, "STALE_VERSION")
	if len(mock.vehicleCorrections) != 0 || !reflect.DeepEqual(mock.checkouts[checkout.ID], checkoutBefore) || len(mock.photos[checkout.Inspection.ID]) != 8 {
		t.Fatal("denied, stale, or invalid correction changed active hold or photos")
	}

	corrected, err := client.VehicleCorrectSnapshot(ctx, adminActorID, firstVehicleID, vehicle.Version, input, "odometer-hold-correction", nil)
	if err != nil {
		t.Fatal(err)
	}
	correctedVehicle, err := dataapi.DecodeAggregate[dataapi.Vehicle](corrected)
	if err != nil || correctedVehicle.Version != vehicle.Version+1 || correctedVehicle.Status != "holding" || correctedVehicle.CurrentOdometerKM == nil || *correctedVehicle.CurrentOdometerKM != wrongValue {
		t.Fatalf("active hold correction: vehicle=%+v err=%v", correctedVehicle, err)
	}
	replayed, err := client.VehicleCorrectSnapshot(ctx, adminActorID, firstVehicleID, vehicle.Version, input, "odometer-hold-correction", nil)
	if err != nil || string(replayed.Aggregate) != string(corrected.Aggregate) || len(mock.vehicleCorrections) != 1 {
		t.Fatalf("correction retry duplicated audit or changed result: %+v %v", replayed, err)
	}
	changedInput := input
	otherValue := int64(11800)
	changedInput.OdometerKM = &otherValue
	_, err = client.VehicleCorrectSnapshot(ctx, adminActorID, firstVehicleID, vehicle.Version, changedInput, "odometer-hold-correction", nil)
	expectAPIError(t, err, "IDEMPOTENCY_CONFLICT")

	checkoutAfterCorrection := mock.checkouts[checkout.ID]
	if checkoutAfterCorrection.Version != checkoutBefore.Version ||
		checkoutAfterCorrection.Inspection.Version != checkoutBefore.Inspection.Version ||
		!reflect.DeepEqual(checkoutAfterCorrection.Inspection.OccupiedSlots, checkoutBefore.Inspection.OccupiedSlots) ||
		!reflect.DeepEqual(mock.photos[checkout.Inspection.ID], photoBefore) ||
		checkoutAfterCorrection.Status != "holding" || mock.vehicleCorrections[0].AssignmentKind != "checkout" ||
		mock.vehicleCorrections[0].AssignmentID == nil || *mock.vehicleCorrections[0].AssignmentID != checkout.ID {
		t.Fatal("correction changed hold/photos or failed to audit the active checkout")
	}

	restarted, err := NewWithSnapshot("test-service-token", snapshotPath, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.vehicleCorrections) != 1 || len(restarted.photos[checkout.Inspection.ID]) != 8 {
		t.Fatalf("snapshot lost correction audit or one of 8 photos: audit=%d photos=%d", len(restarted.vehicleCorrections), len(restarted.photos[checkout.Inspection.ID]))
	}
	restartedClient := commandClient(t, restarted)
	acceptedOdometry, err := restartedClient.InspectionUpdate(ctx, driverID, checkout.Inspection.ID, checkout.Inspection.Version, dataapi.InspectionUpdateInput{OdometerKM: &wrongValue}, "odometer-hold-retry-after-correction", nil)
	if err != nil {
		t.Fatal(err)
	}
	acceptedInspection, err := dataapi.DecodeAggregate[dataapi.Inspection](acceptedOdometry)
	if err != nil || acceptedInspection.OdometerKM == nil || *acceptedInspection.OdometerKM != wrongValue ||
		!reflect.DeepEqual(acceptedInspection.OccupiedSlots, checkoutBefore.Inspection.OccupiedSlots) ||
		len(restarted.photos[checkout.Inspection.ID]) != 8 {
		t.Fatalf("driver retry did not preserve completed photo set: %+v %v", acceptedInspection, err)
	}
}

func TestVehicleSnapshotCorrectionDuringReturnChangesOnlyRollbackBaseline(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 9, 29, 16, 30, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	tripID := "50000000-0000-4000-8000-000000000099"
	returnID := "60000000-0000-4000-8000-000000000099"
	checkoutID := "20000000-0000-4000-8000-000000000099"
	beforeInspectionID := "40000000-0000-4000-8000-000000000099"
	afterInspectionID := "40000000-0000-4000-8000-000000000098"
	startOdometer := int64(50000)
	fuel := 50
	noDamage, clean, safe := false, true, true
	employee := mock.employees[driverID]
	employee.ActiveTripID = &tripID
	mock.employees[driverID] = employee
	vehicleIndex := mock.vehicleIndex(firstVehicleID)
	vehicle := mock.vehicles[vehicleIndex]
	vehicle.Status = "in_trip"
	vehicle.Version = 2
	vehicle.CurrentOdometerKM = &startOdometer
	mock.vehicles[vehicleIndex] = vehicle
	trip := dataapi.Trip{
		ID: tripID, VehicleID: firstVehicleID, EmployeeID: employee.ID, CheckoutID: checkoutID,
		Status: "returning", StartedAt: clock.Add(-time.Hour), ReturnID: &returnID,
		BeforeInspection: dataapi.Inspection{ID: beforeInspectionID, Phase: "before", Status: "finalized", OdometerKM: &startOdometer, Version: 2},
		Issues:           []dataapi.Issue{}, Version: 3, UpdatedAt: clock,
	}
	location := &dataapi.ParkingLocation{ID: "90000000-0000-4000-8000-000000000099", Latitude: 55.75, Longitude: 37.62, Source: "manual_map", ConfirmedAt: clock}
	after := dataapi.Inspection{
		ID: afterInspectionID, Phase: "after", Status: "draft", FuelLevel: &fuel,
		NewDamage: &noDamage, CabinClean: &clean, ParkingAllowed: &safe, KeysReturned: &safe, CarLocked: &safe,
		OccupiedSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}, MissingSlots: []int{}, PhotosConfirmedAt: &clock, Version: 1, UpdatedAt: clock,
	}
	draft := dataapi.Return{ID: returnID, TripID: tripID, Status: "draft", Step: "inspection", IntentConfirmedAt: &clock, ParkingLocation: location, Inspection: after, Version: 1, UpdatedAt: clock}
	tripCopyBefore := trip
	mock.trips[tripID] = trip
	mock.returns[returnID] = draft
	client := commandClient(t, mock)

	oldReported := int64(49999)
	_, err = client.InspectionUpdate(ctx, driverID, afterInspectionID, after.Version, dataapi.InspectionUpdateInput{OdometerKM: &oldReported}, "return-odo-before-fix", nil)
	expectAPIError(t, err, "ODOMETER_ROLLBACK")

	correctedValue := int64(47000)
	input := dataapi.VehicleSnapshotCorrectionInput{Reason: "Подтверждено фото панели", OdometerKM: &correctedValue, Confirmation: true}
	vehicleView, err := client.Vehicle(ctx, adminActorID, firstVehicleID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.VehicleCorrectSnapshot(ctx, adminActorID, firstVehicleID, vehicleView.Version, input, "return-odo-correction", nil); err != nil {
		t.Fatal(err)
	}
	if mock.vehicles[vehicleIndex].Status != "in_trip" || mock.trips[tripID].Status != "returning" ||
		mock.trips[tripID].BeforeInspection.OdometerKM == nil || *mock.trips[tripID].BeforeInspection.OdometerKM != startOdometer ||
		mock.returns[returnID].Status != "draft" || mock.employees[driverID].ActiveTripID == nil ||
		*mock.employees[driverID].ActiveTripID != tripID || len(mock.vehicleCorrections) != 1 {
		t.Fatal("active return correction changed inspection or assignment")
	}
	belowCorrected := int64(46999)
	_, err = client.InspectionUpdate(ctx, driverID, afterInspectionID, after.Version, dataapi.InspectionUpdateInput{OdometerKM: &belowCorrected}, "return-odo-below-corrected", nil)
	expectAPIError(t, err, "ODOMETER_ROLLBACK")

	actualReturn := int64(47200)
	update, err := client.InspectionUpdate(ctx, driverID, afterInspectionID, after.Version, dataapi.InspectionUpdateInput{OdometerKM: &actualReturn}, "return-odo-above-corrected", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataapi.DecodeAggregate[dataapi.Inspection](update); err != nil {
		t.Fatal(err)
	}
	completed, err := client.ReturnComplete(ctx, driverID, returnID, mock.returns[returnID].Version, "return-odo-complete-after-fix", nil)
	if err != nil {
		t.Fatal(err)
	}
	finalReturn, err := dataapi.DecodeAggregate[dataapi.Return](completed)
	if err != nil || finalReturn.Status != "completed" || finalReturn.Inspection.OdometerKM == nil || *finalReturn.Inspection.OdometerKM != actualReturn {
		t.Fatalf("corrected-baseline return failed: %+v %v", finalReturn, err)
	}
	finalTrip := mock.trips[tripID]
	if finalTrip.BeforeInspection.OdometerKM == nil || *finalTrip.BeforeInspection.OdometerKM != startOdometer ||
		tripCopyBefore.BeforeInspection.OdometerKM == nil || *tripCopyBefore.BeforeInspection.OdometerKM != startOdometer ||
		finalTrip.AfterInspection == nil || finalTrip.AfterInspection.OdometerKM == nil || *finalTrip.AfterInspection.OdometerKM != actualReturn ||
		mock.vehicles[vehicleIndex].CurrentOdometerKM == nil || *mock.vehicles[vehicleIndex].CurrentOdometerKM != actualReturn {
		t.Fatal("return completion overwrote the immutable before-inspection or lost corrected odometer")
	}
	if mock.vehicleCorrections[0].AssignmentKind != "trip" || mock.vehicleCorrections[0].AssignmentID == nil || *mock.vehicleCorrections[0].AssignmentID != tripID {
		t.Fatal("active trip correction is missing audit assignment")
	}
}

func TestVehicleCorrectionAuditUpgradesVersion16Snapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	mock, err := NewWithSnapshot("test-service-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.persist(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	state["version"] = json.RawMessage("16")
	delete(state, "vehicle_corrections")
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	upgraded, err := NewWithSnapshot("test-service-token", path, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.vehicleCorrections == nil || len(upgraded.vehicleCorrections) != 0 {
		t.Fatalf("v16 migration did not initialize audit list: %#v", upgraded.vehicleCorrections)
	}
	if err := upgraded.persist(); err != nil {
		t.Fatal(err)
	}
	var persisted map[string]json.RawMessage
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if string(persisted["version"]) != "17" || string(persisted["vehicle_corrections"]) != "[]" {
		t.Fatalf("snapshot upgrade missing v17 audit list: version=%s audits=%s", persisted["version"], persisted["vehicle_corrections"])
	}
}

func seedCorrectionPhotos(t *testing.T, mock *Server, inspection *dataapi.Inspection, now time.Time) {
	t.Helper()
	if err := os.MkdirAll(mock.assetDir, 0700); err != nil {
		t.Fatal(err)
	}
	photos := make(map[int]photoRecord, 8)
	inspection.OccupiedSlots = []int{1, 2, 3, 4, 5, 6, 7, 8}
	inspection.MissingSlots = []int{}
	inspection.PhotosConfirmedAt = &now
	for slot := 1; slot <= 8; slot++ {
		assetID := fmt.Sprintf("70000000-0000-4000-8000-%012d", slot)
		content := []byte(fmt.Sprintf("synthetic-before-photo-%d", slot))
		if err := os.WriteFile(filepath.Join(mock.assetDir, assetID), content, 0600); err != nil {
			t.Fatal(err)
		}
		photos[slot] = photoRecord{AssetID: assetID, SHA256: fmt.Sprintf("%x", sha256.Sum256(content)), ContentType: "image/png"}
	}
	mock.photos[inspection.ID] = photos
}
func clonePhotoSlots(input map[int]photoRecord) map[int]photoRecord {
	copy := make(map[int]photoRecord, len(input))
	for slot, photo := range input {
		copy[slot] = photo
	}
	return copy
}

func TestVehicleSnapshotCorrectionRacesReturnCompletionWithoutLosingTrip(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 9, 29, 16, 45, 0, 0, time.UTC)
	mock, err := NewWithClock("test-service-token", func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	tripID := "50000000-0000-4000-8000-000000000097"
	returnID := "60000000-0000-4000-8000-000000000097"
	checkoutID := "20000000-0000-4000-8000-000000000097"
	beforeID := "40000000-0000-4000-8000-000000000097"
	afterID := "40000000-0000-4000-8000-000000000096"
	startOdometer, correctedOdometer, reportedOdometer := int64(50000), int64(47000), int64(47200)
	fuel, version := 50, int64(1)
	noDamage, clean, safe := false, true, true
	employee := mock.employees[driverID]
	employee.ActiveTripID = &tripID
	mock.employees[driverID] = employee
	vehicleIndex := mock.vehicleIndex(firstVehicleID)
	vehicle := mock.vehicles[vehicleIndex]
	vehicle.Status, vehicle.Version, vehicle.CurrentOdometerKM = "in_trip", 2, &startOdometer
	mock.vehicles[vehicleIndex] = vehicle
	mock.trips[tripID] = dataapi.Trip{
		ID: tripID, VehicleID: firstVehicleID, EmployeeID: employee.ID, CheckoutID: checkoutID,
		Status: "returning", StartedAt: clock.Add(-time.Hour), ReturnID: &returnID,
		BeforeInspection: dataapi.Inspection{ID: beforeID, Phase: "before", Status: "finalized", OdometerKM: &startOdometer, Version: 2},
		Issues:           []dataapi.Issue{}, Version: 3, UpdatedAt: clock,
	}
	location := &dataapi.ParkingLocation{ID: "90000000-0000-4000-8000-000000000097", Latitude: 55.75, Longitude: 37.62, Source: "manual_map", ConfirmedAt: clock}
	inspection := dataapi.Inspection{
		ID: afterID, Phase: "after", Status: "draft", FuelLevel: &fuel, OdometerKM: &reportedOdometer,
		NewDamage: &noDamage, CabinClean: &clean, ParkingAllowed: &safe, KeysReturned: &safe, CarLocked: &safe,
		OccupiedSlots: []int{1, 2, 3, 4, 5, 6, 7, 8}, MissingSlots: []int{}, PhotosConfirmedAt: &clock, Version: version, UpdatedAt: clock,
	}
	mock.returns[returnID] = dataapi.Return{ID: returnID, TripID: tripID, Status: "draft", IntentConfirmedAt: &clock, ParkingLocation: location, Inspection: inspection, Version: 1, UpdatedAt: clock}
	client := commandClient(t, mock)
	input := dataapi.VehicleSnapshotCorrectionInput{Reason: "Сверили фото панели", OdometerKM: &correctedOdometer, Confirmation: true}

	correctionResult := make(chan error, 1)
	completionResult := make(chan error, 1)
	go func() {
		_, err := client.VehicleCorrectSnapshot(ctx, adminActorID, firstVehicleID, vehicle.Version, input, "return-race-correction", nil)
		correctionResult <- err
	}()
	go func() {
		_, err := client.ReturnComplete(ctx, driverID, returnID, 1, "return-race-completion", nil)
		completionResult <- err
	}()
	if err := <-correctionResult; err != nil {
		t.Fatalf("audited correction lost against return completion: %v", err)
	}
	if err := <-completionResult; err != nil {
		expectAPIError(t, err, "ODOMETER_ROLLBACK")
		if _, retryErr := client.ReturnComplete(ctx, driverID, returnID, 1, "return-race-completion", nil); retryErr != nil {
			t.Fatalf("return retry after correction: %v", retryErr)
		}
	}
	if mock.trips[tripID].Status != "completed" || mock.returns[returnID].Status != "completed" ||
		mock.vehicles[vehicleIndex].Status != "available" || len(mock.vehicleCorrections) != 1 ||
		mock.trips[tripID].BeforeInspection.OdometerKM == nil || *mock.trips[tripID].BeforeInspection.OdometerKM != startOdometer ||
		mock.vehicles[vehicleIndex].CurrentOdometerKM == nil || *mock.vehicles[vehicleIndex].CurrentOdometerKM != reportedOdometer {
		t.Fatal("racing correction/completion lost the trip, changed before-inspection, or duplicated its audit")
	}
}
