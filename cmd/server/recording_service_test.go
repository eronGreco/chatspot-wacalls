package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRecordingServiceUploadsTranscribesAndCompletes(t *testing.T) {
	const secret = "test-secret"
	var mu sync.Mutex
	var uploaded []byte
	var doneBody map[string]any
	transcribeCalls := 0

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upload" {
			if r.Method != http.MethodPut {
				t.Errorf("upload method = %s", r.Method)
			}
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			uploaded = append([]byte(nil), b...)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.URL.Path != "/api/public/wacalls/recording" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if got, want := r.Header.Get("X-Signature"), testSignature(secret, body); got != want {
			t.Errorf("signature = %q, want %q", got, want)
		}

		switch r.URL.Query().Get("action") {
		case "upload-url":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"tempFileId": "temp-1",
				"urlUpload":  srv.URL + "/upload",
			})
		case "confirm":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "fileId": "file-1"})
		case "transcribe":
			transcribeCalls++
			speaker := r.URL.Query().Get("speaker")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"lines": []map[string]any{{
					"speaker": speaker,
					"start":   0,
					"text":    speaker + " text",
				}},
			})
		case "done":
			if err := json.Unmarshal(body, &doneBody); err != nil {
				t.Errorf("decode done body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			t.Errorf("unexpected action %q", r.URL.Query().Get("action"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	dir := filepath.Join(root, "CALL1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writePCM16File(t, filepath.Join(dir, "agent.pcm"), []int16{1, 2, 3, 4})
	writePCM16File(t, filepath.Join(dir, "customer.pcm"), []int16{-1, -2, -3, -4})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &recordingService{
		ctx:        ctx,
		cfg:        recordingConfig{Enabled: true, RootDir: root, BaseURL: srv.URL, Secret: secret},
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		httpClient: srv.Client(),
		active:     map[string]*callRecorder{},
		wake:       make(chan struct{}, 1),
	}
	job := &recordingJob{
		Version:     recordingJobVersion,
		CallID:      "CALL1",
		ConnectedAt: time.Date(2026, 9, 29, 20, 0, 0, 0, time.UTC),
		State:       "queued",
	}
	job.MediaHistory = newMediaHistory(job.ConnectedAt.UnixMilli(), false, false)
	job.MediaHistory.transition(job.ConnectedAt.UnixMilli()+1000, true, false)
	job.MediaHistory.close(job.ConnectedAt.UnixMilli() + 2000)
	if err := s.saveJob(dir, job); err != nil {
		t.Fatal(err)
	}
	if err := s.processJob(dir, job); err != nil {
		t.Fatalf("processJob: %v", err)
	}

	if !job.Confirmed || job.FileID != "file-1" {
		t.Fatalf("confirmation state = confirmed:%v file:%q", job.Confirmed, job.FileID)
	}
	if !job.TranscriptionDone || job.State != "done" {
		t.Fatalf("final state = transcription:%v state:%q", job.TranscriptionDone, job.State)
	}
	if transcribeCalls != 2 {
		t.Fatalf("transcribe calls = %d, want 2", transcribeCalls)
	}
	mu.Lock()
	gotUpload := append([]byte(nil), uploaded...)
	mu.Unlock()
	if len(gotUpload) < 44 || string(gotUpload[0:4]) != "RIFF" {
		t.Fatalf("uploaded file is not wav: %d bytes", len(gotUpload))
	}
	if doneBody == nil {
		t.Fatal("done was not called")
	}
	mh, ok := doneBody["mediaHistory"].(map[string]any)
	if !ok || mh["profile"] != "mixed" || doneBody["videoRecordingSupported"] != false {
		t.Fatal("missing authoritative recording metadata", doneBody)
	}
	lines, ok := doneBody["lines"].([]any)
	if !ok || len(lines) != 2 {
		t.Fatalf("done lines = %#v", doneBody["lines"])
	}
}

func TestRecordingConfigUsesRelaySecret(t *testing.T) {
	t.Setenv("RECORDING_ENABLED", "1")
	t.Setenv("WACALLS_PASSWORD", "relay-secret")
	t.Setenv("CHATSPOT_CALLS_URL", "https://calls.example.test")
	t.Setenv("RECORDING_DIR", "")

	cfg := recordingConfigFromEnv("/data/wacalls.db")
	if !cfg.Enabled {
		t.Fatal("recording should be enabled")
	}
	if cfg.Secret != "relay-secret" {
		t.Fatalf("secret = %q", cfg.Secret)
	}
	if cfg.RootDir != filepath.Join("/data", "recordings") {
		t.Fatalf("root dir = %q", cfg.RootDir)
	}
}

func TestRecordingRetryDelay(t *testing.T) {
	want := []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}
	for i, d := range want {
		if got := recordingRetryDelay(i + 1); got != d {
			t.Fatalf("attempt %d = %s, want %s", i+1, got, d)
		}
	}
}

func TestRecordingHTTPClassificationKeepsEventuallyConsistentFailures(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusConflict, http.StatusFailedDependency, http.StatusTooManyRequests, http.StatusBadGateway} {
		var r retryableError
		if !errors.As(classifyHTTPError("test", status), &r) {
			t.Fatalf("HTTP %d should be retryable", status)
		}
	}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestEntityTooLarge} {
		var p permanentError
		if !errors.As(classifyHTTPError("test", status), &p) {
			t.Fatalf("HTTP %d should be permanent", status)
		}
	}
}

func testSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestPostJSONSignsExactBody(t *testing.T) {
	const secret = "abc"
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		if !hmac.Equal(mustDecodeHex(t, r.Header.Get("X-Signature")), mustDecodeHex(t, testSignature(secret, gotBody))) {
			t.Error("signature does not match exact body")
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	s := &recordingService{ctx: context.Background(), cfg: recordingConfig{BaseURL: server.URL, Secret: secret}, httpClient: server.Client()}
	var out map[string]any
	if err := s.postJSON("done", map[string]any{"callId": "X", "lines": []any{}}, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(gotBody, []byte(`"callId":"X"`)) {
		t.Fatalf("unexpected body %s", gotBody)
	}
}

func mustDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return b
}
