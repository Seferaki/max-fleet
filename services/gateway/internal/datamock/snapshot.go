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
	Version   int                         `json:"version"`
	SeedSHA   string                      `json:"seed_sha256"`
	Vehicles  []dataapi.Vehicle           `json:"vehicles"`
	Checkouts map[string]dataapi.Checkout `json:"checkouts"`
	Commands  map[string]commandRecord    `json:"commands"`
}

func (s *Server) snapshot() stateSnapshot {
	state := stateSnapshot{
		Version:   1,
		SeedSHA:   fmt.Sprintf("%x", sha256.Sum256(syntheticSeed)),
		Vehicles:  append([]dataapi.Vehicle(nil), s.vehicles...),
		Checkouts: make(map[string]dataapi.Checkout, len(s.checkouts)),
		Commands:  make(map[string]commandRecord, len(s.commands)),
	}
	for key, value := range s.checkouts {
		state.Checkouts[key] = value
	}
	for key, value := range s.commands {
		state.Commands[key] = value
	}
	return state
}

func (s *Server) restore(state stateSnapshot) {
	s.vehicles = state.Vehicles
	s.checkouts = state.Checkouts
	s.commands = state.Commands
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
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF || state.Version != 1 || state.SeedSHA != fmt.Sprintf("%x", sha256.Sum256(syntheticSeed)) || len(state.Vehicles) != 10 || state.Checkouts == nil || state.Commands == nil {
		return errors.New("data-mock: invalid snapshot; refusing to reset")
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
