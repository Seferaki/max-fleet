package datamock

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type stateSnapshot struct {
	Version      int                            `json:"version"`
	SeedSHA      string                         `json:"seed_sha256"`
	Vehicles     []dataapi.Vehicle              `json:"vehicles"`
	Employees    map[string]dataapi.Employee    `json:"employees"`
	Checkouts    map[string]dataapi.Checkout    `json:"checkouts"`
	Trips        map[string]dataapi.Trip        `json:"trips"`
	Commands     map[string]commandRecord       `json:"commands"`
	Photos       map[string]map[int]photoRecord `json:"photos"`
	PhotoResults map[string]photoAttempt        `json:"photo_results"`
	Challenges   map[string]mockChallenge       `json:"challenges"`
}

func (s *Server) snapshot() stateSnapshot {
	state := stateSnapshot{
		Version:      4,
		SeedSHA:      fmt.Sprintf("%x", sha256.Sum256(syntheticSeed)),
		Vehicles:     append([]dataapi.Vehicle(nil), s.vehicles...),
		Employees:    make(map[string]dataapi.Employee, len(s.employees)),
		Checkouts:    make(map[string]dataapi.Checkout, len(s.checkouts)),
		Trips:        make(map[string]dataapi.Trip, len(s.trips)),
		Commands:     make(map[string]commandRecord, len(s.commands)),
		Photos:       make(map[string]map[int]photoRecord, len(s.photos)),
		PhotoResults: make(map[string]photoAttempt, len(s.photoResults)),
		Challenges:   make(map[string]mockChallenge, len(s.challenges)),
	}
	for key, value := range s.checkouts {
		state.Checkouts[key] = value
	}
	for key, value := range s.employees {
		state.Employees[key] = value
	}
	for key, value := range s.trips {
		state.Trips[key] = value
	}
	for key, value := range s.commands {
		state.Commands[key] = value
	}
	for inspectionID, slots := range s.photos {
		copySlots := make(map[int]photoRecord, len(slots))
		for slot, record := range slots {
			copySlots[slot] = record
		}
		state.Photos[inspectionID] = copySlots
	}
	for key, value := range s.photoResults {
		state.PhotoResults[key] = value
	}
	for key, value := range s.challenges {
		state.Challenges[key] = value
	}
	return state
}

func (s *Server) restore(state stateSnapshot) {
	s.vehicles = state.Vehicles
	if state.Employees != nil {
		s.employees = state.Employees
	}
	s.checkouts = state.Checkouts
	if state.Trips != nil {
		s.trips = state.Trips
	}
	s.commands = state.Commands
	s.photos = state.Photos
	s.photoResults = state.PhotoResults
	s.challenges = state.Challenges
}

func (s *Server) persist() error {
	if s.saveSnapshot == nil {
		return nil
	}
	return s.saveSnapshot(s.snapshot())
}

func (s *Server) loadSnapshot(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Size() > 8<<20 {
		return errors.New("data-mock: snapshot inaccessible or too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("data-mock: cannot read snapshot")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state stateSnapshot
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF || (state.Version != 2 && state.Version != 3 && state.Version != 4) || state.SeedSHA != fmt.Sprintf("%x", sha256.Sum256(syntheticSeed)) || len(state.Vehicles) != 10 || state.Checkouts == nil || state.Commands == nil || state.Photos == nil || state.PhotoResults == nil || (state.Version >= 3 && state.Challenges == nil) || (state.Version == 4 && (state.Employees == nil || state.Trips == nil)) {
		return errors.New("data-mock: invalid snapshot; refusing to reset")
	}
	if state.Challenges == nil {
		state.Challenges = make(map[string]mockChallenge)
	}
	if state.Trips == nil {
		state.Trips = make(map[string]dataapi.Trip)
	}
	for _, slots := range state.Photos {
		for _, photo := range slots {
			if !validUUID(photo.AssetID) {
				return errors.New("data-mock: invalid photo asset")
			}
			data, err := os.ReadFile(filepath.Join(s.assetDir, photo.AssetID))
			if err != nil || len(data) == 0 || len(data) > 10<<20 || fmt.Sprintf("%x", sha256.Sum256(data)) != photo.SHA256 {
				return errors.New("data-mock: photo asset missing or damaged")
			}
		}
	}
	s.restore(state)
	return nil
}

func atomicSave(path string, state stateSnapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if err := json.NewEncoder(temp).Encode(state); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

// expireAndSave is called with mu held. It never exposes an expired state that
// failed to reach the snapshot file.
func (s *Server) expireAndSave(w http.ResponseWriter, requestID string) bool {
	before := s.snapshot()
	if !s.expireHolds() {
		return true
	}
	if err := s.persist(); err != nil {
		s.restore(before)
		s.fail(w, requestID, http.StatusServiceUnavailable, "TEMPORARY_FAILURE")
		return false
	}
	return true
}
