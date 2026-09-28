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
	"sort"

	"github.com/Seferaki/max-fleet/services/gateway/internal/dataapi"
)

type stateSnapshot struct {
	Version                 int                                     `json:"version"`
	SeedSHA                 string                                  `json:"seed_sha256"`
	Vehicles                []dataapi.Vehicle                       `json:"vehicles"`
	Employees               map[string]dataapi.Employee             `json:"employees"`
	Checkouts               map[string]dataapi.Checkout             `json:"checkouts"`
	Trips                   map[string]dataapi.Trip                 `json:"trips"`
	Returns                 map[string]dataapi.Return               `json:"returns"`
	Issues                  map[string]dataapi.Issue                `json:"issues"`
	IssueAssets             map[string]stagedIssueAsset             `json:"issue_assets"`
	StageResults            map[string]stageAttempt                 `json:"stage_results"`
	Commands                map[string]commandRecord                `json:"commands"`
	Photos                  map[string]map[int]photoRecord          `json:"photos"`
	PhotoResults            map[string]photoAttempt                 `json:"photo_results"`
	Challenges              map[string]mockChallenge                `json:"challenges"`
	Inbox                   map[string]mockInboxEvent               `json:"inbox"`
	InboxKeys               map[string]inboxKeyRecord               `json:"inbox_keys"`
	InboxClaims             map[string]inboxClaimRecord             `json:"inbox_claims"`
	InboxTransitions        map[string]inboxTransitionRecord        `json:"inbox_transitions"`
	InboxSequence           int64                                   `json:"inbox_sequence"`
	Integrations            map[string]mockIntegration              `json:"integrations"`
	IntegrationLeases       map[string]integrationLeaseRecord       `json:"integration_leases"`
	IntegrationCheckpoints  map[string]integrationCheckpointRecord  `json:"integration_checkpoints"`
	Notifications           map[string]mockNotification             `json:"notifications"`
	NotificationClaims      map[string]notificationClaimRecord      `json:"notification_claims"`
	NotificationTransitions map[string]notificationTransitionRecord `json:"notification_transitions"`
	NotificationSequence    int64                                   `json:"notification_sequence"`
}

func (s *Server) snapshot() stateSnapshot {
	state := stateSnapshot{
		Version:                 14,
		SeedSHA:                 fmt.Sprintf("%x", sha256.Sum256(syntheticSeed)),
		Vehicles:                append([]dataapi.Vehicle(nil), s.vehicles...),
		Employees:               make(map[string]dataapi.Employee, len(s.employees)),
		Checkouts:               make(map[string]dataapi.Checkout, len(s.checkouts)),
		Trips:                   make(map[string]dataapi.Trip, len(s.trips)),
		Returns:                 make(map[string]dataapi.Return, len(s.returns)),
		Issues:                  make(map[string]dataapi.Issue, len(s.issues)),
		IssueAssets:             make(map[string]stagedIssueAsset, len(s.issueAssets)),
		StageResults:            make(map[string]stageAttempt, len(s.stageResults)),
		Commands:                make(map[string]commandRecord, len(s.commands)),
		Photos:                  make(map[string]map[int]photoRecord, len(s.photos)),
		PhotoResults:            make(map[string]photoAttempt, len(s.photoResults)),
		Challenges:              make(map[string]mockChallenge, len(s.challenges)),
		Inbox:                   make(map[string]mockInboxEvent, len(s.inbox)),
		InboxKeys:               make(map[string]inboxKeyRecord, len(s.inboxKeys)),
		InboxClaims:             make(map[string]inboxClaimRecord, len(s.inboxClaims)),
		InboxTransitions:        make(map[string]inboxTransitionRecord, len(s.inboxTransitions)),
		InboxSequence:           s.inboxSequence,
		Integrations:            make(map[string]mockIntegration, len(s.integrations)),
		IntegrationLeases:       make(map[string]integrationLeaseRecord, len(s.integrationLeases)),
		IntegrationCheckpoints:  make(map[string]integrationCheckpointRecord, len(s.integrationCheckpoints)),
		Notifications:           make(map[string]mockNotification, len(s.notifications)),
		NotificationClaims:      make(map[string]notificationClaimRecord, len(s.notificationClaims)),
		NotificationTransitions: make(map[string]notificationTransitionRecord, len(s.notificationTransitions)),
		NotificationSequence:    s.notificationSequence,
	}
	for key, value := range s.checkouts {
		state.Checkouts[key] = value
	}
	for key, value := range s.employees {
		state.Employees[key] = value
	}
	for key, value := range s.trips {
		copiedIssues := make([]dataapi.Issue, len(value.Issues))
		copy(copiedIssues, value.Issues)
		value.Issues = copiedIssues
		state.Trips[key] = value
	}
	for key, value := range s.returns {
		state.Returns[key] = value
	}
	for key, value := range s.issues {
		state.Issues[key] = value
	}
	for key, value := range s.issueAssets {
		state.IssueAssets[key] = value
	}
	for key, value := range s.stageResults {
		state.StageResults[key] = value
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
	for key, value := range s.inbox {
		state.Inbox[key] = value
	}
	for key, value := range s.inboxKeys {
		state.InboxKeys[key] = value
	}
	for key, value := range s.inboxClaims {
		state.InboxClaims[key] = value
	}
	for key, value := range s.inboxTransitions {
		state.InboxTransitions[key] = value
	}
	for key, value := range s.integrations {
		state.Integrations[key] = value
	}
	for key, value := range s.integrationLeases {
		state.IntegrationLeases[key] = value
	}
	for key, value := range s.integrationCheckpoints {
		state.IntegrationCheckpoints[key] = value
	}
	for key, value := range s.notifications {
		state.Notifications[key] = value
	}
	for key, value := range s.notificationClaims {
		state.NotificationClaims[key] = value
	}
	for key, value := range s.notificationTransitions {
		state.NotificationTransitions[key] = value
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
	if state.Returns != nil {
		s.returns = state.Returns
	}
	if state.Issues != nil {
		s.issues = state.Issues
	}
	if state.IssueAssets != nil {
		s.issueAssets = state.IssueAssets
	}
	if state.StageResults != nil {
		s.stageResults = state.StageResults
	}
	s.commands = state.Commands
	s.photos = state.Photos
	s.photoResults = state.PhotoResults
	s.challenges = state.Challenges
	s.inbox = state.Inbox
	s.inboxKeys = state.InboxKeys
	s.inboxClaims = state.InboxClaims
	s.inboxTransitions = state.InboxTransitions
	s.inboxSequence = state.InboxSequence
	s.integrations = state.Integrations
	s.integrationLeases = state.IntegrationLeases
	s.integrationCheckpoints = state.IntegrationCheckpoints
	s.notifications = state.Notifications
	s.notificationClaims = state.NotificationClaims
	s.notificationTransitions = state.NotificationTransitions
	s.notificationSequence = state.NotificationSequence
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
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF || (state.Version < 2 || state.Version > 14) || state.SeedSHA != fmt.Sprintf("%x", sha256.Sum256(syntheticSeed)) || len(state.Vehicles) != 10 || state.Checkouts == nil || state.Commands == nil || state.Photos == nil || state.PhotoResults == nil || (state.Version >= 3 && state.Challenges == nil) || (state.Version >= 4 && (state.Employees == nil || state.Trips == nil)) || (state.Version >= 5 && state.Returns == nil) || (state.Version >= 6 && state.Issues == nil) || (state.Version >= 7 && (state.IssueAssets == nil || state.StageResults == nil)) || (state.Version >= 8 && (state.Inbox == nil || state.InboxKeys == nil)) || (state.Version >= 9 && state.InboxClaims == nil) || (state.Version >= 10 && state.InboxTransitions == nil) || (state.Version >= 11 && (state.Integrations == nil || state.IntegrationLeases == nil)) || (state.Version >= 12 && state.IntegrationCheckpoints == nil) || (state.Version >= 13 && (state.Notifications == nil || state.NotificationClaims == nil)) || (state.Version >= 14 && state.NotificationTransitions == nil) {
		return errors.New("data-mock: invalid snapshot; refusing to reset")
	}
	if state.Challenges == nil {
		state.Challenges = make(map[string]mockChallenge)
	}
	if state.Trips == nil {
		state.Trips = make(map[string]dataapi.Trip)
	}
	if state.Returns == nil {
		state.Returns = make(map[string]dataapi.Return)
	}
	if state.Issues == nil {
		state.Issues = make(map[string]dataapi.Issue)
	}
	if state.IssueAssets == nil {
		state.IssueAssets = make(map[string]stagedIssueAsset)
	}
	if state.StageResults == nil {
		state.StageResults = make(map[string]stageAttempt)
	}
	if state.Inbox == nil {
		state.Inbox = make(map[string]mockInboxEvent)
	}
	if state.InboxKeys == nil {
		state.InboxKeys = make(map[string]inboxKeyRecord)
	}
	if state.InboxClaims == nil {
		state.InboxClaims = make(map[string]inboxClaimRecord)
	}
	if state.InboxTransitions == nil {
		state.InboxTransitions = make(map[string]inboxTransitionRecord)
	}
	if state.Integrations == nil {
		state.Integrations = s.integrations
	}
	if state.IntegrationLeases == nil {
		state.IntegrationLeases = make(map[string]integrationLeaseRecord)
	}
	if state.IntegrationCheckpoints == nil {
		state.IntegrationCheckpoints = make(map[string]integrationCheckpointRecord)
	}
	if state.Notifications == nil {
		state.Notifications = make(map[string]mockNotification)
	}
	if state.NotificationClaims == nil {
		state.NotificationClaims = make(map[string]notificationClaimRecord)
	}
	if state.NotificationTransitions == nil {
		state.NotificationTransitions = make(map[string]notificationTransitionRecord)
	}
	if state.Version < 9 {
		identities := make([]string, 0, len(state.Inbox))
		for identity := range state.Inbox {
			identities = append(identities, identity)
		}
		sort.Slice(identities, func(i, j int) bool {
			left, right := state.Inbox[identities[i]], state.Inbox[identities[j]]
			if left.Stored.StoredAt.Equal(right.Stored.StoredAt) {
				return left.Stored.ID < right.Stored.ID
			}
			return left.Stored.StoredAt.Before(right.Stored.StoredAt)
		})
		for _, identity := range identities {
			state.InboxSequence++
			event := state.Inbox[identity]
			event.Sequence = state.InboxSequence
			state.Inbox[identity] = event
		}
	} else {
		seen := make(map[int64]bool, len(state.Inbox))
		for _, event := range state.Inbox {
			if event.Sequence < 1 || event.Sequence > state.InboxSequence || seen[event.Sequence] {
				return errors.New("data-mock: invalid inbox sequence")
			}
			seen[event.Sequence] = true
		}
	}
	if state.Version >= 13 {
		seen := make(map[int64]bool, len(state.Notifications))
		for id, notification := range state.Notifications {
			if !validUUID(id) || id != notification.ID || notification.Sequence < 1 || notification.Sequence > state.NotificationSequence || seen[notification.Sequence] || !validMaxID(notification.Recipient) || !validUUID(notification.Event.ResourceID) {
				return errors.New("data-mock: invalid notification queue")
			}
			seen[notification.Sequence] = true
		}
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
	for _, asset := range state.IssueAssets {
		if !validUUID(asset.ID) {
			return errors.New("data-mock: invalid staged issue asset")
		}
		data, err := os.ReadFile(filepath.Join(s.assetDir, asset.ID))
		if err != nil || len(data) == 0 || len(data) > 10<<20 || fmt.Sprintf("%x", sha256.Sum256(data)) != asset.SHA256 {
			return errors.New("data-mock: staged issue asset missing or damaged")
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
