package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMediaTimelineTransitionsAndIndependentDirections(t *testing.T) {
	for _, initialVideo := range []bool{false, true} {
		h := newMediaHistory(1000, initialVideo, initialVideo)
		h.transition(2000, true, true)
		h.transition(3000, false, true)
		if h.Segments[len(h.Segments)-1].Media != "video" {
			t.Fatal("one camera off became audio")
		}
		h.transition(4000, false, false)
		h.transition(5000, true, false)
		h.close(6000)
		if h.Profile != "mixed" || *h.EndedAt != 6000 {
			t.Fatal(h)
		}
		for i, seg := range h.Segments {
			if seg.EndMS == nil || *seg.EndMS < seg.StartMS {
				t.Fatal(seg)
			}
			if i > 0 && *h.Segments[i-1].EndMS != seg.StartMS {
				t.Fatal("timeline gap")
			}
		}
	}
}

func TestMediaHistoryZeroDurationDuplicateAndBudget(t *testing.T) {
	h := newMediaHistory(1000, false, false)
	h.transition(1000, true, true)
	if h.Profile != "video" || len(h.Segments) != 1 {
		t.Fatal("zero audio interval invented mixed", h)
	}
	if h.transition(1100, true, true) {
		t.Fatal("duplicate produced transition")
	}
	for i := 1; i < 2000; i++ {
		h.transition(1000+int64(i), i%2 == 0, false)
	}
	if !h.Truncated || len(h.Segments) > maxMediaSegments || h.Profile != "mixed" {
		t.Fatal(h)
	}
	h.close(5000)
	if h.Segments[len(h.Segments)-1].EndMS == nil {
		t.Fatal("truncated tail should have explicit known boundary")
	}
	before := h.clone()
	if h.transition(6000, false, false) {
		t.Fatal("ended history accepted transition")
	}
	before.Segments[0].Media = "changed"
	if h.Segments[0].Media == "changed" {
		t.Fatal("snapshot aliased")
	}
}

func TestMediaHistoryBrokerPreservesHistoryAndSSE(t *testing.T) {
	b := NewBroker()
	sub := b.subscribe("x")
	defer b.unsubscribe(sub)
	at := int64(1000)
	b.upsertCall(CallRecord{CallID: "c", SessionID: "s", Status: StatusConnected, connectedAt: &at, Media: "audio"})
	b.setVideoActivity("c", false, true, 2000)
	// The status path must retain accumulated media history across other phase updates.
	r, _ := b.getCall("c")
	r.Status = StatusHeld
	b.upsertCall(*r)
	h, _ := b.setVideoActivity("c", false, false, 3000)
	if h.Profile != "mixed" {
		t.Fatal(h)
	}
	b.endCall("c", "done")
	var ended CallMediaHistory
	found := false
	for len(sub.ch) > 0 {
		var ev struct {
			Type         string            `json:"type"`
			MediaHistory *CallMediaHistory `json:"mediaHistory"`
		}
		if err := json.Unmarshal(<-sub.ch, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "call-ended" {
			if ev.MediaHistory == nil {
				t.Fatal("no final history")
			}
			ended = *ev.MediaHistory
			found = true
		}
	}
	if !found || ended.Profile != "mixed" || ended.EndedAt == nil {
		t.Fatal(ended)
	}
}

func TestMediaHistoryRecordingPersistsAndRecoversWithoutBrowser(t *testing.T) {
	root := t.TempDir()
	s := &recordingService{ctx: context.Background(), cfg: recordingConfig{RootDir: root}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), active: map[string]*callRecorder{}, wake: make(chan struct{}, 1)}
	at := time.Now().Add(-time.Second).Truncate(time.Millisecond)
	s.start("C", at)
	h := newMediaHistory(at.UnixMilli(), false, false)
	h.transition(at.UnixMilli()+100, true, false)
	s.observeMedia("C", h)
	dir := filepath.Join(root, "C")
	persisted, err := loadMediaManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Profile != "mixed" || persisted.ConnectedAt != at.UnixMilli() {
		t.Fatal(persisted)
	}
	// Simulate restart: close open tracks then recover the on-disk job with a new service.
	if err = s.active["C"].close(); err != nil {
		t.Fatal(err)
	}
	if err = s.recoverJobs(); err != nil {
		t.Fatal(err)
	}
	job, err := s.loadJob(dir)
	if err != nil {
		t.Fatal(err)
	}
	if job.MediaHistory == nil || job.MediaHistory.Profile != "mixed" || job.MediaHistory.EndedAt == nil {
		t.Fatal(job)
	}
	data, err := os.ReadFile(filepath.Join(dir, "media.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest RecordingMediaManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.VideoRecordingSupported || manifest.AudioSampleRate != 16000 || manifest.Clock != "server_connected" {
		t.Fatal(manifest)
	}
}

func TestMediaHistoryConcurrentSnapshotsAndTransitions(t *testing.T) {
	b := NewBroker()
	b.upsertCall(CallRecord{CallID: "c", Status: StatusConnected})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				b.setVideoActivity("c", i%2 == 0, false, time.Now().UnixMilli())
				r, _ := b.getCall("c")
				if r != nil {
					_, _ = json.Marshal(r)
				}
			}
		}()
	}
	wg.Wait()
}
