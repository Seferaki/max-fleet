package datamock

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

//go:embed seed.json
var syntheticSeed []byte

type seedFile struct {
	DemoOnly  bool   `json:"demo_only"`
	Rules     string `json:"rules"`
	Employees []struct {
		MaxUserID    string `json:"max_user_id"`
		DisplayName  string `json:"display_name"`
		Role         string `json:"role"`
		CanStartTrip bool   `json:"can_start_trip"`
	} `json:"employees"`
	Vehicles []struct {
		ID         string `json:"id"`
		Plate      string `json:"plate"`
		Model      string `json:"model"`
		OdometerKM int64  `json:"odometer_km"`
		FuelLevel  int    `json:"fuel_level"`
		Parking    struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"parking"`
		KeyInstructions string   `json:"key_instructions"`
		KnownIssues     []string `json:"known_issues"`
	} `json:"vehicles"`
}

type Server struct {
	mu                      sync.Mutex
	token                   string
	workerToken             string
	employees               map[string]dataapi.Employee
	vehicles                []dataapi.Vehicle
	checkouts               map[string]dataapi.Checkout
	trips                   map[string]dataapi.Trip
	returns                 map[string]dataapi.Return
	issues                  map[string]dataapi.Issue
	issueAssets             map[string]stagedIssueAsset
	stageResults            map[string]stageAttempt
	commands                map[string]commandRecord
	photos                  map[string]map[int]photoRecord
	photoResults            map[string]photoAttempt
	challenges              map[string]mockChallenge
	inbox                   map[string]mockInboxEvent
	inboxKeys               map[string]inboxKeyRecord
	inboxClaims             map[string]inboxClaimRecord
	inboxTransitions        map[string]inboxTransitionRecord
	inboxSequence           int64
	integrations            map[string]mockIntegration
	integrationLeases       map[string]integrationLeaseRecord
	integrationCheckpoints  map[string]integrationCheckpointRecord
	notifications           map[string]mockNotification
	notificationClaims      map[string]notificationClaimRecord
	notificationTransitions map[string]notificationTransitionRecord
	notificationSequence    int64
	rules                   dataapi.Rules
	now                     func() time.Time
	saveSnapshot            func(stateSnapshot) error
	assetDir                string
}

func New(token string) (*Server, error) {
	return newServer(token, "", "", time.Now)
}

func NewWithClock(token string, now func() time.Time) (*Server, error) {
	return newServer(token, "", "", now)
}

func NewWithSnapshot(token, path string, now func() time.Time) (*Server, error) {
	if path == "" {
		return nil, errors.New("data-mock: snapshot path required")
	}
	return newServer(token, "", path, now)
}

func NewWithSnapshotAndWorkerToken(token, workerToken, path string, now func() time.Time) (*Server, error) {
	if path == "" || workerToken == "" {
		return nil, errors.New("data-mock: snapshot and worker token required")
	}
	return newServer(token, workerToken, path, now)
}

func newServer(token, workerToken, snapshotPath string, now func() time.Time) (*Server, error) {
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("data-mock: service token required")
	}
	if workerToken == token && workerToken != "" || strings.ContainsAny(workerToken, "\r\n") {
		return nil, errors.New("data-mock: worker token must be separate")
	}
	if now == nil {
		return nil, errors.New("data-mock: clock required")
	}
	var seed seedFile
	if err := json.Unmarshal(syntheticSeed, &seed); err != nil || !seed.DemoOnly || seed.Rules == "" || len(seed.Vehicles) != 10 {
		return nil, errors.New("data-mock: invalid synthetic seed")
	}
	stamp := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	s := &Server{token: token, workerToken: workerToken, employees: make(map[string]dataapi.Employee), checkouts: make(map[string]dataapi.Checkout), trips: make(map[string]dataapi.Trip), returns: make(map[string]dataapi.Return), issues: make(map[string]dataapi.Issue), issueAssets: make(map[string]stagedIssueAsset), stageResults: make(map[string]stageAttempt), commands: make(map[string]commandRecord), photos: make(map[string]map[int]photoRecord), photoResults: make(map[string]photoAttempt), challenges: make(map[string]mockChallenge), inbox: make(map[string]mockInboxEvent), inboxKeys: make(map[string]inboxKeyRecord), inboxClaims: make(map[string]inboxClaimRecord), inboxTransitions: make(map[string]inboxTransitionRecord), integrations: map[string]mockIntegration{"demo-bot": {Data: dataapi.Integration{Key: "demo-bot", Mode: "polling", Version: 1, UpdatedAt: stamp}}}, integrationLeases: make(map[string]integrationLeaseRecord), integrationCheckpoints: make(map[string]integrationCheckpointRecord), notifications: make(map[string]mockNotification), notificationClaims: make(map[string]notificationClaimRecord), notificationTransitions: make(map[string]notificationTransitionRecord), now: now,
		rules: dataapi.Rules{ID: "90000000-0000-4000-8000-000000000001", VersionLabel: "demo-v1", Body: seed.Rules}}
	for i, item := range seed.Employees {
		id := fmt.Sprintf("80000000-0000-4000-8000-%012d", i+1)
		s.employees[item.MaxUserID] = dataapi.Employee{ID: id, MaxUserID: item.MaxUserID, DisplayName: item.DisplayName, Role: item.Role, CanStartTrip: item.CanStartTrip, Version: 1, UpdatedAt: stamp}
	}
	for i, item := range seed.Vehicles {
		parking := &dataapi.ParkingLocation{ID: fmt.Sprintf("90000000-0000-4000-8000-%012d", i+1), Latitude: item.Parking.Latitude, Longitude: item.Parking.Longitude, Source: "seed", ConfirmedAt: stamp}
		fuel := item.FuelLevel
		odo := item.OdometerKM
		v := dataapi.Vehicle{ID: item.ID, Plate: item.Plate, Make: "Демо", Model: item.Model, Description: "Синтетический автомобиль", KeyInstructions: item.KeyInstructions, Status: "available", CurrentParking: parking, CurrentFuel: &fuel, CurrentOdometerKM: &odo, FuelConfirmedAt: &stamp, OdometerConfirmedAt: &stamp, KnownNonblockingIssues: item.KnownIssues, Version: 1, UpdatedAt: stamp}
		if v.KnownNonblockingIssues == nil {
			v.KnownNonblockingIssues = []string{}
		}
		s.vehicles = append(s.vehicles, v)
	}
	if snapshotPath != "" {
		s.assetDir = snapshotPath + ".assets"
		s.saveSnapshot = func(state stateSnapshot) error { return atomicSave(snapshotPath, state) }
		if err := s.loadSnapshot(snapshotPath); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "partial", "mode": "mock"})
	})
	mux.HandleFunc("GET /internal/v1/meta", s.authorize(false, func(w http.ResponseWriter, r *http.Request, requestID string) {
		s.success(w, requestID, dataapi.Meta{ContractVersion: dataapi.ContractVersion, BuildSHA: "synthetic", Mode: "mock", Capabilities: []string{"read-fixtures"}})
	}))
	mux.HandleFunc("GET /internal/v1/me", s.authorize(true, func(w http.ResponseWriter, r *http.Request, requestID string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		actor := r.Header.Get("X-Actor-Max-ID")
		employee, found := s.employees[actor]
		var own *dataapi.Employee
		if found {
			own = &employee
		}
		s.success(w, requestID, dataapi.Me{Allowed: found, MaxUserID: actor, Employee: own})
	}))
	mux.HandleFunc("GET /internal/v1/rules/current", s.authorize(true, s.requireEmployee(func(w http.ResponseWriter, _ *http.Request, requestID string) {
		s.success(w, requestID, s.rules)
	})))
	mux.HandleFunc("GET /internal/v1/state", s.authorize(true, s.requireEmployee(s.currentState)))
	mux.HandleFunc("GET /internal/v1/checkouts/{id}", s.authorize(true, s.requireEmployee(s.checkout)))
	mux.HandleFunc("GET /internal/v1/trips/{id}", s.authorize(true, s.requireEmployee(s.trip)))
	mux.HandleFunc("GET /internal/v1/trips", s.authorize(true, s.requireEmployee(s.myTrips)))
	mux.HandleFunc("GET /internal/v1/admin/summary", s.authorize(true, s.requireEmployee(s.adminSummary)))
	mux.HandleFunc("GET /internal/v1/admin/employees", s.authorize(true, s.requireEmployee(s.adminEmployees)))
	mux.HandleFunc("GET /internal/v1/admin/employees/{id}", s.authorize(true, s.requireEmployee(s.adminEmployee)))
	mux.HandleFunc("GET /internal/v1/admin/trips", s.authorize(true, s.requireEmployee(s.adminTrips)))
	mux.HandleFunc("GET /internal/v1/admin/issues", s.authorize(true, s.requireEmployee(s.adminIssues)))
	mux.HandleFunc("GET /internal/v1/returns/{id}", s.authorize(true, s.requireEmployee(s.returnDraft)))
	mux.HandleFunc("GET /internal/v1/issues/{id}", s.authorize(true, s.requireEmployee(s.issue)))
	mux.HandleFunc("GET /internal/v1/inspections/{id}", s.authorize(true, s.requireEmployee(s.inspection)))
	mux.HandleFunc("POST /internal/v1/inspections/{id}/photos/{slot}", s.authorize(true, s.requireEmployee(s.uploadPhoto)))
	mux.HandleFunc("POST /internal/v1/assets/stage", s.authorize(true, s.requireEmployee(s.stageIssueAsset)))
	mux.HandleFunc("GET /internal/v1/assets/{id}/content", s.authorize(true, s.requireEmployee(s.assetContent)))
	mux.HandleFunc("POST /internal/v1/inbox", s.authorizeWorker(s.storeInbox))
	mux.HandleFunc("POST /internal/v1/inbox/claim", s.authorizeWorker(s.claimInbox))
	mux.HandleFunc("POST /internal/v1/inbox/{id}/ack", s.authorizeWorker(s.ackInbox))
	mux.HandleFunc("POST /internal/v1/inbox/{id}/retry", s.authorizeWorker(s.retryInbox))
	mux.HandleFunc("GET /internal/v1/integrations/{key}", s.authorizeWorker(s.getIntegration))
	mux.HandleFunc("POST /internal/v1/integrations/{key}/lease", s.authorizeWorker(s.leaseIntegration))
	mux.HandleFunc("POST /internal/v1/integrations/{key}/checkpoint", s.authorizeWorker(s.checkpointIntegration))
	mux.HandleFunc("POST /internal/v1/notifications/claim", s.authorizeWorker(s.claimNotifications))
	mux.HandleFunc("POST /internal/v1/notifications/{id}/ack", s.authorizeWorker(s.ackNotification))
	mux.HandleFunc("POST /internal/v1/notifications/{id}/retry", s.authorizeWorker(s.retryNotification))
	mux.HandleFunc("GET /internal/v1/vehicles", s.authorize(true, s.requireEmployee(s.listVehicles)))
	mux.HandleFunc("GET /internal/v1/vehicles/{id}", s.authorize(true, s.requireEmployee(s.vehicle)))
	mux.HandleFunc("GET /internal/v1/vehicles/{id}/previous-inspection", s.authorize(true, s.requireEmployee(s.previousInspection)))
	mux.HandleFunc("POST /internal/v1/commands", s.authorize(true, s.requireEmployee(s.execute)))
	mux.HandleFunc("GET /internal/v1/commands/{key}", s.authorize(true, s.requireEmployee(s.commandResult)))
	return mux
}

type route func(http.ResponseWriter, *http.Request, string)

func (s *Server) authorize(actorRequired bool, next route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if !validUUID(requestID) {
			requestID = newRequestID()
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) != 1 {
			s.fail(w, requestID, http.StatusUnauthorized, "INVALID_SERVICE_TOKEN")
			return
		}
		if r.Header.Get("X-Contract-Version") != dataapi.ContractVersion {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		if actorRequired && !validMaxID(r.Header.Get("X-Actor-Max-ID")) {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		next(w, r, requestID)
	}
}

func (s *Server) requireEmployee(next route) route {
	return func(w http.ResponseWriter, r *http.Request, requestID string) {
		s.mu.Lock()
		_, ok := s.employees[r.Header.Get("X-Actor-Max-ID")]
		s.mu.Unlock()
		if !ok {
			s.fail(w, requestID, http.StatusForbidden, "ACCESS_DENIED")
			return
		}
		next(w, r, requestID)
	}
}

func (s *Server) listVehicles(w http.ResponseWriter, r *http.Request, requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.expireAndSave(w, requestID) {
		return
	}
	query := r.URL.Query()
	for key := range query {
		if key != "available" && key != "limit" && key != "cursor" {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
	}
	available := query.Get("available")
	if available != "" && available != "true" && available != "false" {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	limit := 5
	if query.Has("limit") {
		value, err := strconv.Atoi(query.Get("limit"))
		if err != nil || value < 1 || value > 50 {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		limit = value
	}
	filtered := make([]dataapi.Vehicle, 0, len(s.vehicles))
	for _, v := range s.vehicles {
		if available == "true" && v.Status != "available" || available == "false" && v.Status == "available" {
			continue
		}
		filtered = append(filtered, v)
	}
	offset := 0
	if query.Has("cursor") {
		decoded, err := base64.RawURLEncoding.DecodeString(query.Get("cursor"))
		parts := strings.Split(string(decoded), ":")
		if err != nil || len(parts) != 3 || parts[1] != available || parts[2] != strconv.Itoa(limit) {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		offset, err = strconv.Atoi(parts[0])
		if err != nil || offset < 1 || offset >= len(filtered) {
			s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
	}
	end := min(offset+limit, len(filtered))
	var next *string
	if end < len(filtered) {
		encoded := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s:%d", end, available, limit)))
		next = &encoded
	}
	s.success(w, requestID, dataapi.Page[dataapi.Vehicle]{Items: filtered[offset:end], NextCursor: next})
}

func (s *Server) vehicle(w http.ResponseWriter, r *http.Request, requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.expireAndSave(w, requestID) {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	for _, v := range s.vehicles {
		if v.ID == id {
			s.success(w, requestID, v)
			return
		}
	}
	s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
}

func (s *Server) currentState(w http.ResponseWriter, r *http.Request, requestID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.expireAndSave(w, requestID) {
		return
	}
	employee := s.employees[r.Header.Get("X-Actor-Max-ID")]
	state := dataapi.CurrentState{ConversationVersion: 1}
	for _, checkout := range s.checkouts {
		if checkout.EmployeeID == employee.ID && checkout.Status == "holding" {
			current := checkout
			state.Checkout = &current
			state.NextStep = &current.Step
			break
		}
	}
	for _, trip := range s.trips {
		if trip.EmployeeID == employee.ID && (trip.Status == "active" || trip.Status == "returning") {
			current := trip
			state.Trip = &current
			step := "active_trip"
			state.NextStep = &step
			break
		}
	}
	if state.Trip != nil && state.Trip.ReturnID != nil {
		if current, found := s.returns[*state.Trip.ReturnID]; found && current.Status == "draft" {
			state.Return = &current
			state.NextStep = &current.Step
		}
	}
	s.success(w, requestID, state)
}

func (s *Server) returnDraft(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.returns[id]
	trip := s.trips[current.TripID]
	employee := s.employees[r.Header.Get("X-Actor-Max-ID")]
	if !found || trip.EmployeeID != employee.ID && employee.Role != "admin" {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.success(w, requestID, current)
}

func (s *Server) trip(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.trips[id]
	employee := s.employees[r.Header.Get("X-Actor-Max-ID")]
	if !found || current.EmployeeID != employee.ID && employee.Role != "admin" {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.success(w, requestID, current)
}

func (s *Server) checkout(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.expireAndSave(w, requestID) {
		return
	}
	current, found := s.checkouts[id]
	employee := s.employees[r.Header.Get("X-Actor-Max-ID")]
	if !found || current.EmployeeID != employee.ID && employee.Role != "admin" {
		s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
		return
	}
	s.success(w, requestID, current)
}

func (s *Server) inspection(w http.ResponseWriter, r *http.Request, requestID string) {
	id := r.PathValue("id")
	if !validUUID(id) {
		s.fail(w, requestID, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.expireAndSave(w, requestID) {
		return
	}
	employee := s.employees[r.Header.Get("X-Actor-Max-ID")]
	for _, checkout := range s.checkouts {
		if checkout.Inspection.ID == id && (checkout.EmployeeID == employee.ID || employee.Role == "admin") {
			s.success(w, requestID, checkout.Inspection)
			return
		}
	}
	for _, draft := range s.returns {
		if draft.Inspection.ID == id {
			trip := s.trips[draft.TripID]
			if trip.EmployeeID == employee.ID || employee.Role == "admin" {
				s.success(w, requestID, draft.Inspection)
				return
			}
		}
	}
	s.fail(w, requestID, http.StatusNotFound, "NOT_FOUND")
}

func (s *Server) success(w http.ResponseWriter, requestID string, value any) {
	writeJSON(w, http.StatusOK, struct {
		Data      any    `json:"data"`
		RequestID string `json:"request_id"`
	}{value, requestID})
}

func (s *Server) fail(w http.ResponseWriter, requestID string, status int, code string) {
	writeJSON(w, status, struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}{Error: struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}{Code: code, Message: code, Retryable: status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests}, RequestID: requestID})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func validUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func validMaxID(id string) bool {
	if id == "" || len(id) > 19 || id[0] == '0' {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}
