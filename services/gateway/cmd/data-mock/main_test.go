package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceTokenSources(t *testing.T) {
	t.Setenv("DATA_API_TOKEN", "")
	t.Setenv("DATA_API_TOKEN_FILE", "")
	if _, err := serviceToken(); err == nil {
		t.Fatal("missing token accepted")
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("file-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_API_TOKEN_FILE", path)
	if got, err := serviceToken(); err != nil || got != "file-test-token" {
		t.Fatalf("file token: %v", err)
	}
	t.Setenv("DATA_API_TOKEN", "env-test-token")
	if _, err := serviceToken(); err == nil {
		t.Fatal("two token sources accepted")
	}
}

func TestWorkerTokenSources(t *testing.T) {
	t.Setenv("WORKER_API_TOKEN", "")
	t.Setenv("WORKER_API_TOKEN_FILE", "")
	if _, err := readToken("WORKER_API_TOKEN", "WORKER_API_TOKEN_FILE"); err == nil {
		t.Fatal("missing worker token accepted")
	}
	path := filepath.Join(t.TempDir(), "worker-token")
	if err := os.WriteFile(path, []byte("worker-file-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKER_API_TOKEN_FILE", path)
	if got, err := readToken("WORKER_API_TOKEN", "WORKER_API_TOKEN_FILE"); err != nil || got != "worker-file-token" {
		t.Fatalf("worker file token: %v", err)
	}
	t.Setenv("WORKER_API_TOKEN", "worker-env-token")
	if _, err := readToken("WORKER_API_TOKEN", "WORKER_API_TOKEN_FILE"); err == nil {
		t.Fatal("two worker token sources accepted")
	}
}
