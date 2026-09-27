package datamock

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerTokenIsSeparate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if _, err := NewWithSnapshotAndWorkerToken("same", "same", path, time.Now); err == nil {
		t.Fatal("equal data and worker tokens accepted")
	}
	if _, err := NewWithSnapshotAndWorkerToken("data", "", path, time.Now); err == nil {
		t.Fatal("missing worker token accepted")
	}
	if _, err := NewWithSnapshotAndWorkerToken("data", "worker", path, time.Now); err != nil {
		t.Fatalf("distinct worker token rejected: %v", err)
	}
}
