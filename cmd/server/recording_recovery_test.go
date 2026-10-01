package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordingRecoveryQueuesInterruptedCall(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "CALL-RECOVER")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writePCM16File(t, filepath.Join(dir, "agent.pcm"), []int16{1, 2, 3})
	writePCM16File(t, filepath.Join(dir, "customer.pcm"), []int16{4, 5, 6})

	s := &recordingService{
		ctx:    context.Background(),
		cfg:    recordingConfig{Enabled: true, RootDir: root},
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		active: map[string]*callRecorder{},
		wake:   make(chan struct{}, 1),
	}
	started := time.Now().Add(-time.Minute).UTC()
	job := &recordingJob{
		Version:     recordingJobVersion,
		CallID:      "CALL-RECOVER",
		ConnectedAt: started,
		State:       "recording",
	}
	if err := s.saveJob(dir, job); err != nil {
		t.Fatal(err)
	}

	if err := s.recoverJobs(); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.loadJob(dir)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != "queued" {
		t.Fatalf("state = %q, want queued", recovered.State)
	}
	if recovered.EndedAt == nil {
		t.Fatal("recovered recording should have an endedAt")
	}
	if recovered.EndedAt.Sub(recovered.ConnectedAt) > time.Millisecond {
		t.Fatal("recovery added server downtime to recorded duration")
	}
	if recovered.LastError == "" {
		t.Fatal("recovery should explain that the server restarted")
	}
}

func TestCompletedRecordingJobNeedsNoAudioOrNetwork(t *testing.T) {
	s := &recordingService{ctx: context.Background()}
	job := &recordingJob{CallID: "CALL-DONE", State: "done"}
	if err := s.processJob(t.TempDir(), job); err != nil {
		t.Fatalf("completed job should be a no-op: %v", err)
	}
}
