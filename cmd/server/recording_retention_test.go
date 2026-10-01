package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordingRetentionAfterDeliveryFailure(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry-after-24h", true: "permanent"}[permanent], func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
			defer endpoint.Close()
			root := t.TempDir()
			dir := filepath.Join(root, "CALL")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			audio := filepath.Join(dir, "agent.pcm")
			if err := os.WriteFile(audio, []byte{1, 2}, 0o600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-48 * time.Hour)
			job := &recordingJob{CallID: "CALL", State: "queued", FirstFailureAt: &old}
			s := &recordingService{ctx: context.Background(), cfg: recordingConfig{RootDir: root, BaseURL: endpoint.URL}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), httpClient: endpoint.Client(), wake: make(chan struct{}, 1)}
			failure := error(errors.New("upload failed"))
			if permanent {
				failure = permanentError{failure}
			}
			s.handleJobError(dir, job, failure)
			if b, err := os.ReadFile(audio); err != nil || len(b) != 2 {
				t.Fatalf("audio was lost: %v", err)
			}
			persisted, err := s.loadJob(dir)
			if err != nil {
				t.Fatal(err)
			}
			if permanent {
				if persisted.State != "failed" {
					t.Fatalf("state = %s", persisted.State)
				}
				_, due, err := s.nextDueJob()
				if err != nil || due != nil {
					t.Fatalf("failed job must not block queue: %v", err)
				}
			} else if persisted.NextAttemptAt == nil || persisted.Attempts != 1 {
				t.Fatal("transient failure must remain scheduled even after 24h")
			}
		})
	}
}

func TestRecordingRecoveryPreservesUnreadableOldJob(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ORPHAN")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.pcm"), []byte{1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	s := &recordingService{cfg: recordingConfig{RootDir: root}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := s.recoverJobs(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.pcm")); err != nil {
		t.Fatalf("orphan audio lost: %v", err)
	}
}

func TestRecordingCleanupRequiresConfirmedDelivery(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfirmed", true: "confirmed"}[confirmed], func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
			defer endpoint.Close()
			root := t.TempDir()
			dir := filepath.Join(root, "CALL")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			job := &recordingJob{CallID: "CALL", State: "done", Confirmed: confirmed, FileID: "file-1"}
			s := &recordingService{ctx: context.Background(), cfg: recordingConfig{RootDir: root, BaseURL: endpoint.URL}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), httpClient: endpoint.Client(), wake: make(chan struct{}, 1)}
			if err := s.saveJob(dir, job); err != nil {
				t.Fatal(err)
			}
			s.processOneDue()
			_, err := os.Stat(dir)
			if confirmed && !os.IsNotExist(err) {
				t.Fatal("confirmed completed job was not cleaned up")
			}
			if !confirmed && err != nil {
				t.Fatalf("unconfirmed job lost: %v", err)
			}
		})
	}
}
