package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/purpshell/meowcaller/rtp"
	"golang.org/x/sys/unix"
)

const videoQueueBytes = 32 << 20
const videoFrameMaxBytes = 2 << 20
const maxVideoCaptureSegments = 128

type videoRecordingConfig struct {
	Enabled       bool
	FFmpeg        string
	FFprobe       string
	MaxCalls      int
	MaxBytes      int64
	MaxTotalBytes int64
	MinFreeBytes  int64
	MaxSeconds    int
	Width         int
	Height        int
	FPS           int
	CRF           int
}

func boundedEnv(name string, fallback, min, max int) int {
	v, e := strconv.Atoi(os.Getenv(name))
	if e != nil || v < min || v > max {
		return fallback
	}
	return v
}
func videoConfigFromEnv() videoRecordingConfig {
	return videoRecordingConfig{Enabled: envBool("VIDEO_RECORDING_ENABLED"), FFmpeg: "ffmpeg", FFprobe: "ffprobe", MaxCalls: boundedEnv("VIDEO_RECORDING_MAX_CALLS", 8, 1, 64), MaxBytes: int64(boundedEnv("VIDEO_RECORDING_MAX_MB", 128, 16, 1024)) << 20, MaxTotalBytes: int64(boundedEnv("VIDEO_RECORDING_DISK_MB", 4096, 256, 65536)) << 20, MinFreeBytes: 512 << 20, MaxSeconds: boundedEnv("VIDEO_RECORDING_MAX_SECONDS", 3600, 60, 14400), Width: 320, Height: 240, FPS: 10, CRF: 32}
}
func validateVideoConfig(c *videoRecordingConfig) error {
	if !c.Enabled {
		return nil
	}
	var err error
	if c.FFmpeg, err = exec.LookPath(c.FFmpeg); err != nil {
		return fmt.Errorf("video recording requires ffmpeg: %w", err)
	}
	if c.FFprobe, err = exec.LookPath(c.FFprobe); err != nil {
		return fmt.Errorf("video recording requires ffprobe: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	encoders, err := exec.CommandContext(ctx, c.FFmpeg, "-hide_banner", "-encoders").Output()
	if err != nil {
		return fmt.Errorf("read ffmpeg encoders: %w", err)
	}
	x264, aac := false, false
	for line := range strings.SplitSeq(string(encoders), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 {
			x264 = x264 || fields[1] == "libx264"
			aac = aac || fields[1] == "aac"
		}
	}
	if !x264 || !aac {
		return fmt.Errorf("video recording requires FFmpeg libx264 and AAC encoders")
	}
	if c.MaxCalls < 1 || c.MaxBytes < 1 || c.MaxTotalBytes < 1 || c.MaxSeconds < 1 || c.Width != 320 || c.Height != 240 || c.FPS != 10 || c.CRF != 32 {
		return fmt.Errorf("invalid video recording limits/profile")
	}
	return nil
}

type videoSegment struct {
	File     string `json:"file"`
	Side     string `json:"side"`
	StartMS  int64  `json:"startMs"`
	LastMS   int64  `json:"lastMs"`
	Rotation uint16 `json:"rotation"`
}
type videoJob struct {
	MaxDurationMS int64          `json:"maxDurationMs,omitempty"`
	Requested     bool           `json:"requested"`
	Version       int            `json:"version"`
	CallID        string         `json:"callId"`
	ConnectedAt   time.Time      `json:"connectedAt"`
	State         string         `json:"state"`
	DurationMS    int64          `json:"durationMs"`
	Segments      []videoSegment `json:"segments"`
	Partial       bool           `json:"partial"`
	CaptureError  string         `json:"captureError,omitempty"`
	Name          string         `json:"name,omitempty"`
	Size          int64          `json:"size,omitempty"`
	Confirmed     bool           `json:"confirmed"`
	FileID        string         `json:"fileId,omitempty"`
	Attempts      int            `json:"attempts"`
	NextAttemptAt *time.Time     `json:"nextAttemptAt,omitempty"`
	LastError     string         `json:"lastError,omitempty"`
}
type capturedVideoFrame struct {
	data     []byte
	side     int
	source   uint32
	stamp    uint32
	rate     uint32
	rotation uint16
	epoch    uint64
	at       time.Time
}
type videoTrackCapture struct {
	sps, pps []byte
	file     *os.File
	index    int
	epoch    uint64
	source   uint32
	origin   uint32
	baseMS   int64
	lastMS   int64
	rotation uint16
}
type videoCapture struct {
	svc       *recordingService
	dir       string
	startedAt time.Time
	mu        sync.Mutex // protects enqueue/close, never held during disk I/O
	closed    bool
	startOnce sync.Once
	endedAt   time.Time
	halted    atomic.Bool
	queue     chan capturedVideoFrame
	done      chan struct{}
	enabled   [2]atomic.Bool
	epoch     [2]atomic.Uint64
	dropped   atomic.Int64
	job       videoJob // owned exclusively by writer until done closes
	tracks    [2]videoTrackCapture
	bytes     int64
	lastSave  time.Time
}

func (s *recordingService) videoSupported() bool { return s != nil && s.cfg.Video.Enabled }
func (s *recordingService) startVideo(rec *callRecorder) error {
	if !s.videoSupported() {
		return nil
	}
	job := videoJob{Version: 1, CallID: rec.callID, ConnectedAt: rec.startedAt.UTC(), State: "recording", MaxDurationMS: int64(s.cfg.Video.MaxSeconds) * 1000, Segments: []videoSegment{}}
	cap := &videoCapture{svc: s, dir: rec.dir, startedAt: rec.startedAt, queue: make(chan capturedVideoFrame, 32), done: make(chan struct{}), job: job}
	for i := range cap.tracks {
		cap.tracks[i].index = -1

	}
	if err := saveVideoJob(rec.dir, &cap.job); err != nil {
		return err
	}
	rec.video = cap

	return nil
}
func (c *videoCapture) activate() {
	c.job.Requested = true
	reserved := false
	for {
		active := c.svc.videoCaptures.Load()
		if active >= int64(c.svc.cfg.Video.MaxCalls) {
			break
		}
		if c.svc.videoCaptures.CompareAndSwap(active, active+1) {
			reserved = true
			break
		}
	}
	err := c.svc.videoDiskAllowed()
	if !reserved || err != nil {
		if reserved {
			c.svc.videoCaptures.Add(-1)
		}
		c.halted.Store(true)
		c.job.State = "unavailable"
		c.job.CaptureError = "Limite de gravações de vídeo simultâneas atingido."
		if err != nil {
			c.job.CaptureError = err.Error()
		}
		if e := saveVideoJob(c.dir, &c.job); e != nil {
			c.svc.log.Error("persist unavailable video capture failed", "call_id", c.job.CallID, "err", e)
		}
		close(c.done)
		return
	}
	if e := saveVideoJob(c.dir, &c.job); e != nil {
		c.partial(e.Error())
	}
	go c.run()
}
func (s *recordingService) videoDiskAllowed() error {
	s.videoDiskMu.Lock()
	defer s.videoDiskMu.Unlock()
	return s.videoDiskErr
}
func (s *recordingService) refreshVideoDisk() {
	var st unix.Statfs_t
	err := unix.Statfs(s.cfg.RootDir, &st)
	if err == nil && uint64(st.Bavail)*uint64(st.Bsize) < uint64(s.cfg.Video.MinFreeBytes) {
		err = fmt.Errorf("Espaço livre insuficiente para gravar vídeo.")
	}
	var size int64
	if err == nil {
		err = filepath.WalkDir(s.cfg.RootDir, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() {
				info, e := d.Info()
				if e != nil {
					if os.IsNotExist(e) {
						return nil
					}
					return e
				}
				size += info.Size()
			}
			return nil
		})
		if err == nil && size >= s.cfg.Video.MaxTotalBytes {
			err = fmt.Errorf("Limite de espaço da fila de gravações atingido.")
		}
	}
	s.videoDiskMu.Lock()
	s.videoDiskErr = err
	s.videoDiskMu.Unlock()
}
func (s *recordingService) videoDiskWorker() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.refreshVideoDisk()
		}
	}
}
func (s *recordingService) captureVideo(id string, side int, data []byte, stamp, rate, source uint32, rotation uint16) {
	if !s.videoSupported() || len(data) == 0 || side < 0 || side > 1 {
		return
	}
	s.mu.Lock()
	rec := s.active[id]
	s.mu.Unlock()
	if rec == nil || rec.video == nil {
		return
	}
	cap := rec.video
	if !cap.enabled[side].Load() || cap.halted.Load() {
		return
	}
	if len(data) > videoFrameMaxBytes {
		cap.dropped.Add(1)
		return
	}
	reserved := int64(len(data))
	if s.videoMemory.Add(reserved) > videoQueueBytes {
		s.videoMemory.Add(-reserved)
		cap.dropped.Add(1)
		return
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.closed {
		s.videoMemory.Add(-reserved)
		return
	}
	f := capturedVideoFrame{data: bytes.Clone(data), side: side, stamp: stamp, rate: rate, source: source, rotation: rotation, epoch: cap.epoch[side].Load(), at: time.Now()}
	select {
	case cap.queue <- f:
	default:
		s.videoMemory.Add(-reserved)
		cap.dropped.Add(1)
	}
}
func (c *videoCapture) setMedia(h *CallMediaHistory) {
	if h == nil || len(h.Segments) == 0 {
		return
	}
	v := h.Segments[len(h.Segments)-1]
	if v.LocalVideo || v.RemoteVideo {
		c.startOnce.Do(c.activate)
	}
	for side, on := range []bool{v.LocalVideo, v.RemoteVideo} {
		if c.enabled[side].Swap(on) != on {
			c.epoch[side].Add(1)
		}
	}
}
func (c *videoCapture) close() {
	c.startOnce.Do(func() {
		c.job.State = "done"
		if err := saveVideoJob(c.dir, &c.job); err != nil {
			c.svc.log.Error("persist audio-only video state failed", "call_id", c.job.CallID, "err", err)
		}
		c.halted.Store(true)
		close(c.done)
	})
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.endedAt = time.Now()
		close(c.queue)
	}
	c.mu.Unlock()
	<-c.done
}
func (c *videoCapture) partial(message string) {
	c.job.Partial = true
	if c.job.CaptureError == "" {
		c.job.CaptureError = message
	}
}
func (c *videoCapture) closeTrack(t *videoTrackCapture) error {
	if t.file == nil {
		return nil
	}
	err := t.file.Sync()
	ce := t.file.Close()
	t.file = nil

	if err != nil {
		return err
	}
	return ce
}
func (c *videoCapture) run() {
	defer close(c.done)
	defer c.svc.videoCaptures.Add(-1)
	stopped := false
	dropped := int64(0)
	for f := range c.queue {
		if d := c.dropped.Load(); d != dropped {
			c.partial("Fila de captura cheia; alguns quadros não puderam ser gravados.")
			dropped = d
			for i := range c.tracks {
				_ = c.closeTrack(&c.tracks[i])
			}
		}
		if !stopped {
			if err := c.write(f); err != nil {
				c.partial(err.Error())
				stopped = true
				c.halted.Store(true)
			}
		}
		c.svc.videoMemory.Add(-int64(len(f.data)))
	}
	for i := range c.tracks {
		if err := c.closeTrack(&c.tracks[i]); err != nil {
			c.partial(err.Error())
		}
	}
	c.job.DurationMS = min(int64(c.svc.cfg.Video.MaxSeconds)*1000, max(int64(100), c.endedAt.Sub(c.startedAt).Milliseconds()))
	c.job.State = "queued"
	if len(c.job.Segments) == 0 {
		c.job.State = "unavailable"
		if c.job.CaptureError == "" {
			c.job.CaptureError = "Nenhum quadro de vídeo decodificável foi recebido."
		}
	}
	if err := saveVideoJob(c.dir, &c.job); err != nil {
		c.svc.log.Error("persist video capture failed", "call_id", c.job.CallID, "err", err)
	}
	if c.job.Partial {
		c.svc.log.Warn("video capture partial", "call_id", c.job.CallID, "reason", c.job.CaptureError, "dropped_frames", c.dropped.Load())
	}
}
func (c *videoCapture) write(f capturedVideoFrame) error {
	elapsed := max(int64(0), f.at.Sub(c.startedAt).Milliseconds())
	if elapsed > int64(c.svc.cfg.Video.MaxSeconds)*1000 {
		return fmt.Errorf("Limite de duração da gravação de vídeo atingido.")
	}
	if c.bytes+int64(len(f.data)) > c.svc.cfg.Video.MaxBytes {
		return fmt.Errorf("Limite de tamanho da captura de vídeo atingido.")
	}
	if err := c.svc.videoDiskAllowed(); err != nil {
		return err
	}
	t := &c.tracks[f.side]
	changed := false
	for _, nal := range rtp.SplitAnnexB(f.data) {
		if len(nal) == 0 {
			continue
		}
		switch nal[0] & 31 {
		case 7:
			if !bytes.Equal(t.sps, nal) {
				changed = true
				t.sps = bytes.Clone(nal)
			}
		case 8:
			if !bytes.Equal(t.pps, nal) {
				changed = true
				t.pps = bytes.Clone(nal)
			}
		}
	}
	if f.rotation != 0 && f.rotation != 90 && f.rotation != 180 && f.rotation != 270 {
		f.rotation = 0
	}
	if t.file != nil && t.epoch == f.epoch && elapsed-t.lastMS > 1000 {
		c.partial("Interrupção no fluxo de vídeo; os intervalos sem quadros foram preservados.")
	}
	reset := t.file == nil || changed || t.epoch != f.epoch || t.source != f.source || t.rotation != f.rotation || elapsed-t.lastMS > 1000
	pts := elapsed
	if !reset && f.rate > 0 {
		delta := int64(int32(f.stamp - t.origin))
		pts = t.baseMS + delta*1000/int64(f.rate)
		if delta < 0 || pts < t.lastMS || absMS(pts-elapsed) > 2000 {
			reset = true
			pts = elapsed
		}
	}
	if reset {
		if err := c.closeTrack(t); err != nil {
			return err
		}
		if !rtp.AUHasIDR(f.data) || len(t.sps) < 4 || len(t.pps) == 0 {
			return nil
		}
		if len(c.job.Segments) >= maxVideoCaptureSegments {
			return fmt.Errorf("Limite de trechos de vídeo atingido.")
		}
		side := "agent"
		if f.side == 1 {
			side = "customer"
		}
		file := fmt.Sprintf("capture-%03d.mkv", len(c.job.Segments))
		vf, err := newVideoMKV(filepath.Join(c.dir, file), t.sps, t.pps)
		if err != nil {
			return err
		}
		t.file = vf
		t.index = len(c.job.Segments)
		t.origin = f.stamp
		t.baseMS = elapsed
		t.epoch = f.epoch
		t.source = f.source
		t.rotation = f.rotation

		pts = elapsed
		c.job.Segments = append(c.job.Segments, videoSegment{File: file, Side: side, StartMS: pts, LastMS: pts, Rotation: f.rotation})
		if err := saveVideoJob(c.dir, &c.job); err != nil {
			return err
		}
	}
	if pts < t.lastMS {
		return nil
	}
	if err := writeVideoMKV(t.file, f.data, pts-t.baseMS, rtp.AUHasIDR(f.data)); err != nil {
		return err
	}
	t.lastMS = pts
	c.job.Segments[t.index].LastMS = pts
	c.bytes += int64(len(f.data))
	if time.Since(c.lastSave) > 2*time.Second {
		if err := t.file.Sync(); err != nil {
			return err
		}
		if err := saveVideoJob(c.dir, &c.job); err != nil {
			return err
		}
		c.lastSave = time.Now()
	}
	return nil
}
func absMS(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
func saveVideoJob(dir string, j *videoJob) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".video-job-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, "video-job.json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func loadVideoJob(dir string) (*videoJob, error) {
	b, e := os.ReadFile(filepath.Join(dir, "video-job.json"))
	if e != nil {
		return nil, e
	}
	var j videoJob
	e = json.Unmarshal(b, &j)
	return &j, e
}
