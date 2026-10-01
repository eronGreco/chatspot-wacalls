package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	recordingJobVersion = 1
	transcribeChunkSecs = 300
)

type recordingConfig struct {
	Enabled bool
	RootDir string
	BaseURL string
	Secret  string
}

type transcriptLine struct {
	Speaker string  `json:"speaker"`
	Start   float64 `json:"start"`
	Text    string  `json:"text"`
}

type recordingJob struct {
	Version           int               `json:"version"`
	CallID            string            `json:"callId"`
	MediaHistory      *CallMediaHistory `json:"mediaHistory,omitempty"`
	ConnectedAt       time.Time         `json:"connectedAt"`
	EndedAt           *time.Time        `json:"endedAt,omitempty"`
	State             string            `json:"state"`
	Name              string            `json:"name,omitempty"`
	MimeType          string            `json:"mimeType,omitempty"`
	Size              int64             `json:"size,omitempty"`
	TotalSamples      int64             `json:"totalSamples,omitempty"`
	Confirmed         bool              `json:"confirmed,omitempty"`
	FileID            string            `json:"fileId,omitempty"`
	AgentOffset       int64             `json:"agentOffsetSamples,omitempty"`
	CustomerOffset    int64             `json:"customerOffsetSamples,omitempty"`
	Lines             []transcriptLine  `json:"lines,omitempty"`
	TranscriptionDone bool              `json:"transcriptionDone,omitempty"`
	TranscriptError   string            `json:"transcriptError,omitempty"`
	Attempts          int               `json:"attempts,omitempty"`
	FirstFailureAt    *time.Time        `json:"firstFailureAt,omitempty"`
	NextAttemptAt     *time.Time        `json:"nextAttemptAt,omitempty"`
	LastError         string            `json:"lastError,omitempty"`
}

type recordingService struct {
	ctx        context.Context
	cfg        recordingConfig
	log        *slog.Logger
	httpClient *http.Client

	mu     sync.Mutex
	active map[string]*callRecorder
	wake   chan struct{}
}

func recordingConfigFromEnv(dbPath string) recordingConfig {
	enabled := envBool("RECORDING_ENABLED")
	root := strings.TrimSpace(os.Getenv("RECORDING_DIR"))
	if root == "" {
		root = filepath.Join(filepath.Dir(dbPath), "recordings")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("CHATSPOT_CALLS_URL")), "/")
	if baseURL == "" {
		baseURL = "https://calls.chatspot.com.br"
	}
	// Chatspot Calls validates recording requests with the same HMAC secret
	// used by the relay. Keep a single source of truth to avoid mismatched
	// credentials between the two services.
	secret := os.Getenv("WACALLS_PASSWORD")
	return recordingConfig{Enabled: enabled, RootDir: root, BaseURL: baseURL, Secret: secret}
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func newRecordingService(ctx context.Context, cfg recordingConfig, log *slog.Logger) (*recordingService, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if strings.TrimSpace(cfg.Secret) == "" {
		return nil, fmt.Errorf("RECORDING_ENABLED is set but WACALLS_PASSWORD is empty")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("invalid CHATSPOT_CALLS_URL %q", cfg.BaseURL)
	}
	if err := os.MkdirAll(cfg.RootDir, 0o700); err != nil {
		return nil, fmt.Errorf("create recording root: %w", err)
	}

	s := &recordingService{
		ctx:        ctx,
		cfg:        cfg,
		log:        log.With("component", "recordings"),
		httpClient: &http.Client{Timeout: 15 * time.Minute},
		active:     map[string]*callRecorder{},
		wake:       make(chan struct{}, 1),
	}
	if err := s.recoverJobs(); err != nil {
		return nil, err
	}
	go s.worker()
	s.signal()
	s.log.Info("server-side call recording enabled", "dir", cfg.RootDir, "target", cfg.BaseURL)
	return s, nil
}

func (s *recordingService) start(callID string, connectedAt time.Time) {
	if s == nil || callID == "" {
		return
	}
	s.mu.Lock()
	if _, ok := s.active[callID]; ok {
		s.mu.Unlock()
		return
	}
	rec, err := newCallRecorder(s.cfg.RootDir, callID, connectedAt)
	if err != nil {
		s.mu.Unlock()
		s.log.Error("start recording failed", "call_id", callID, "err", err)
		return
	}
	job := &recordingJob{
		Version:     recordingJobVersion,
		CallID:      callID,
		ConnectedAt: connectedAt.UTC(),
		State:       "recording",
	}
	if err := s.saveJob(rec.dir, job); err != nil {
		_ = rec.close()
		s.mu.Unlock()
		s.log.Error("persist recording job failed", "call_id", callID, "err", err)
		return
	}
	s.active[callID] = rec
	s.mu.Unlock()
	s.log.Info("recording started", "call_id", callID)
}

func (s *recordingService) writeAgent(callID string, pcm []float32) {
	if s == nil || len(pcm) == 0 {
		return
	}
	s.mu.Lock()
	rec := s.active[callID]
	s.mu.Unlock()
	if rec == nil {
		return
	}
	if err := rec.writeAgent(pcm, time.Now()); err != nil {
		s.log.Error("write agent recording failed", "call_id", callID, "err", err)
	}
}

func (s *recordingService) writeCustomer(callID string, pcm []float32) {
	if s == nil || len(pcm) == 0 {
		return
	}
	s.mu.Lock()
	rec := s.active[callID]
	s.mu.Unlock()
	if rec == nil {
		return
	}
	if err := rec.writeCustomer(pcm, time.Now()); err != nil {
		s.log.Error("write customer recording failed", "call_id", callID, "err", err)
	}
}

func (s *recordingService) finish(callID string) {
	if s == nil || callID == "" {
		return
	}
	s.mu.Lock()
	rec := s.active[callID]
	if rec != nil {
		delete(s.active, callID)
	}
	s.mu.Unlock()
	if rec == nil {
		return
	}
	if err := rec.close(); err != nil {
		s.log.Warn("close recording tracks failed", "call_id", callID, "err", err)
	}

	job, err := s.loadJob(rec.dir)
	if err != nil {
		job = &recordingJob{Version: recordingJobVersion, CallID: callID, ConnectedAt: rec.startedAt.UTC()}
	}
	now := time.Now().UTC()
	job.EndedAt = &now
	if h, err := loadMediaManifest(rec.dir); err == nil {
		h.close(now.UnixMilli())
		job.MediaHistory = h
	}
	job.State = "queued"
	job.NextAttemptAt = nil
	if err := s.saveJob(rec.dir, job); err != nil {
		s.log.Error("queue recording failed", "call_id", callID, "err", err)
		return
	}
	s.log.Info("recording queued", "call_id", callID)
	s.signal()
}

func (s *recordingService) signal() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *recordingService) recoverJobs() error {
	entries, err := os.ReadDir(s.cfg.RootDir)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.cfg.RootDir, entry.Name())
		job, err := s.loadJob(dir)
		if err != nil {
			s.log.Error("unreadable recording job retained for recovery", "directory", dir, "err", err)
			continue
		}
		if job.State == "recording" {
			job.State = "queued"
			job.EndedAt = &now
			if h, err := loadMediaManifest(dir); err == nil {
				h.close(now.UnixMilli())
				job.MediaHistory = h
			}
			job.LastError = "server restarted while call was being recorded"
			if err := s.saveJob(dir, job); err != nil {
				return fmt.Errorf("recover recording job %s: %w", job.CallID, err)
			}
		}
	}
	return nil
}

func (s *recordingService) worker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.wake:
			s.processOneDue()
		case <-ticker.C:
			s.processOneDue()
		}
	}
}

func (s *recordingService) processOneDue() {
	dir, job, err := s.nextDueJob()
	if err != nil {
		s.log.Error("scan recording queue failed", "err", err)
		return
	}
	if job == nil {
		return
	}

	if err := s.processJob(dir, job); err != nil {
		s.handleJobError(dir, job, err)
		return
	}

	if !job.Confirmed || job.State != "done" {
		s.handleJobError(dir, job, permanentError{fmt.Errorf("refusing to remove recording without confirmed delivery")})
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		s.log.Warn("remove completed recording directory failed", "call_id", job.CallID, "err", err)
		return
	}
	s.log.Info("recording delivered and local audio removed", "call_id", job.CallID, "file_id", job.FileID)
	s.signal()
}

func (s *recordingService) nextDueJob() (string, *recordingJob, error) {
	entries, err := os.ReadDir(s.cfg.RootDir)
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()
	type candidate struct {
		dir string
		job *recordingJob
	}
	var candidates []candidate
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.cfg.RootDir, entry.Name())
		job, err := s.loadJob(dir)
		if err != nil || job.State == "recording" || job.State == "failed" {
			continue
		}
		if job.NextAttemptAt != nil && job.NextAttemptAt.After(now) {
			continue
		}
		candidates = append(candidates, candidate{dir: dir, job: job})
	}
	if len(candidates) == 0 {
		return "", nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].job.ConnectedAt.Before(candidates[j].job.ConnectedAt)
	})
	return candidates[0].dir, candidates[0].job, nil
}

func (s *recordingService) handleJobError(dir string, job *recordingJob, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && s.ctx.Err() != nil {
		return
	}
	var p permanentError
	if errors.As(err, &p) {
		s.log.Error("recording delivery permanently failed", "call_id", job.CallID, "err", p.err)
		s.notifyFailed(job.CallID, p.Error())
		job.State = "failed"
		job.LastError = p.Error()
		job.NextAttemptAt = nil
		if saveErr := s.saveJob(dir, job); saveErr != nil {
			s.log.Error("persist retained failed recording job failed", "call_id", job.CallID, "err", saveErr)
		}
		s.signal()
		return
	}

	now := time.Now().UTC()
	if job.FirstFailureAt == nil {
		job.FirstFailureAt = &now
	}

	job.Attempts++
	delay := recordingRetryDelay(job.Attempts)
	next := now.Add(delay)
	job.NextAttemptAt = &next
	job.LastError = err.Error()
	if saveErr := s.saveJob(dir, job); saveErr != nil {
		s.log.Error("persist recording retry failed", "call_id", job.CallID, "err", saveErr)
		return
	}
	s.log.Warn("recording delivery will retry", "call_id", job.CallID, "after", delay, "err", err)
	time.AfterFunc(delay, s.signal)
}

func recordingRetryDelay(attempt int) time.Duration {
	delays := []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}
	if attempt <= 0 {
		return delays[0]
	}
	if attempt <= len(delays) {
		return delays[attempt-1]
	}
	return 30 * time.Minute
}

func (s *recordingService) notifyFailed(callID, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]any{"callId": callID, "error": message})
	if err != nil {
		return
	}
	u := s.endpoint("failed")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", s.signature(body))
	resp, err := s.httpClient.Do(req)
	if err == nil && resp != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}
}

func (s *recordingService) saveJob(dir string, job *recordingJob) error {
	job.Version = recordingJobVersion
	body, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".job-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, "job.json"))
}

func (s *recordingService) loadJob(dir string) (*recordingJob, error) {
	body, err := os.ReadFile(filepath.Join(dir, "job.json"))
	if err != nil {
		return nil, err
	}
	var job recordingJob
	if err := json.Unmarshal(body, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func classifyHTTPError(stage string, status int) error {
	err := fmt.Errorf("%s returned HTTP %d", stage, status)
	// The call row is populated asynchronously from WaCalls events. A recording
	// can therefore reach Chatspot Calls a little before the corresponding row
	// is visible. Treat dependency/configuration races as retryable instead of
	// deleting the only copy of the recording. Authentication/validation errors
	// remain permanent.
	switch status {
	case http.StatusNotFound, http.StatusRequestTimeout, http.StatusConflict, http.StatusFailedDependency, http.StatusTooManyRequests:
		return retryableError{err}
	}
	if status >= 500 {
		return retryableError{err}
	}
	return permanentError{err}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
