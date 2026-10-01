package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/rtp"
)

func videoTestService(t *testing.T) *recordingService {
	t.Helper()
	ff, e := exec.LookPath("ffmpeg")
	if e != nil {
		if envBool("REQUIRE_VIDEO_FFMPEG") {
			t.Fatal("ffmpeg required for recording integration tests")
		}
		t.Skip("ffmpeg not installed")
	}
	fp, e := exec.LookPath("ffprobe")
	if e != nil {
		if envBool("REQUIRE_VIDEO_FFMPEG") {
			t.Fatal("ffprobe required for recording integration tests")
		}
		t.Skip("ffprobe not installed")
	}
	cfg := videoRecordingConfig{Enabled: true, FFmpeg: ff, FFprobe: fp, MaxCalls: 8, MaxBytes: 128 << 20, MaxTotalBytes: 4096 << 20, MinFreeBytes: 1, MaxSeconds: 3600, Width: 320, Height: 240, FPS: 10, CRF: 32}
	return &recordingService{ctx: context.Background(), cfg: recordingConfig{RootDir: t.TempDir(), Video: cfg}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), active: map[string]*callRecorder{}, wake: make(chan struct{}, 1), videoWake: make(chan struct{}, 1)}
}
func videoFixture(t *testing.T, color, size string, frames int) [][]byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-threads", "1", "-filter_threads", "1", "-f", "lavfi", "-i", "color=c="+color+":s="+size+":r=10", "-frames:v", fmt.Sprint(frames), "-an", "-c:v", "libx264", "-preset", "ultrafast", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-threads", "1", "-x264-params", "aud=1:keyint=5:repeat-headers=1", "-f", "h264", "pipe:1")
	b, e := cmd.Output()
	if e != nil {
		t.Fatal(e)
	}
	var aus [][]byte
	var au []byte
	for _, nal := range rtp.SplitAnnexB(b) {
		if nal[0]&31 == 9 && len(au) > 0 {
			aus = append(aus, au)
			au = nil
		}
		au = append(au, 0, 0, 0, 1)
		au = append(au, nal...)
	}
	if len(au) > 0 {
		aus = append(aus, au)
	}
	if len(aus) != frames {
		t.Fatalf("fixture frames %d", len(aus))
	}
	return aus
}
func capturedFixture(t *testing.T, s *recordingService, dir, id string, frames [][]byte, side int, startMS int64, rotation uint16) []videoSegment {
	t.Helper()
	origin := time.Unix(10000, 0)
	c := &videoCapture{svc: s, dir: dir, startedAt: origin, job: videoJob{Version: 1, CallID: id, ConnectedAt: origin, State: "recording", Segments: []videoSegment{}}}
	for i := range c.tracks {
		c.tracks[i].index = -1
	}
	for i, au := range frames {
		stamp := uint32(i * 9000)
		arrival := startMS + int64(i)*100
		if i > 0 {
			arrival += int64(i%3) * 15
		}
		if err := c.write(capturedVideoFrame{data: au, side: side, stamp: stamp, rate: 90000, source: 42, epoch: 1, rotation: rotation, at: origin.Add(time.Duration(arrival) * time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range c.tracks {
		if err := c.closeTrack(&c.tracks[i]); err != nil {
			t.Fatal(err)
		}
	}
	return c.job.Segments
}
func pixelAt(t *testing.T, path string, seconds float64, x, y int) []byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-threads", "1", "-filter_threads", "1", "-ss", fmt.Sprint(seconds), "-i", path, "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-threads", "1", "pipe:1")
	b, e := cmd.Output()
	if e != nil || len(b) != 640*240*3 {
		t.Fatalf("decode frame: %v len %d", e, len(b))
	}
	p := (y*640 + x) * 3
	return b[p : p+3]
}
func TestVideoServerMuxPreservesTimelineAspectAndAudio(t *testing.T) {
	s := videoTestService(t)
	dir := filepath.Join(s.cfg.RootDir, "VIDEOCALL")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	red := videoFixture(t, "red", "640x360", 10)
	blue := videoFixture(t, "blue", "180x320", 10)
	// The two encoded tracks have independent start times and arrival jitter.
	segs := capturedFixture(t, s, dir, "VIDEOCALL", red, 0, 500, 0)
	// Move generated filenames before making a separate capture fixture.
	if e := os.Rename(filepath.Join(dir, segs[0].File), filepath.Join(dir, "capture-agent.mkv")); e != nil {
		t.Fatal(e)
	}
	segs[0].File = "capture-agent.mkv"
	customer := capturedFixture(t, s, dir, "VIDEOCALL", blue, 1, 1000, 0)
	segs = append(segs, customer...)
	for _, side := range []string{"agent", "customer"} {
		pcm := make([]int16, 3*recordingSampleRate)
		start := 8000
		if side == "customer" {
			start = 24000
		}
		for i := start; i < start+1600; i++ {
			pcm[i] = int16(10000 * math.Sin(float64(i)*2*math.Pi*440/recordingSampleRate))
		}
		writePCM16File(t, filepath.Join(dir, side+".pcm"), pcm)
	}
	j := &videoJob{CallID: "VIDEOCALL", DurationMS: 3000, Segments: segs}
	out := filepath.Join(dir, "final.mp4")
	if e := s.assembleVideo(dir, j, out); e != nil {
		t.Fatal(e)
	}
	black := func(p []byte) bool { return p[0] < 15 && p[1] < 15 && p[2] < 15 }
	if !black(pixelAt(t, out, 0.2, 160, 120)) || !black(pixelAt(t, out, 0.2, 480, 120)) {
		t.Fatal("video started before capture")
	}
	left := pixelAt(t, out, 0.8, 160, 120)
	if left[0] < 180 || left[1] > 30 || left[2] > 30 {
		t.Fatal("agent timeline/color", left)
	}
	if !black(pixelAt(t, out, 0.8, 480, 120)) {
		t.Fatal("customer started early")
	}
	if !black(pixelAt(t, out, 1.2, 160, 10)) {
		t.Fatal("horizontal video was stretched")
	}
	right := pixelAt(t, out, 1.2, 480, 120)
	if right[2] < 180 {
		t.Fatal("customer timeline/color", right)
	}
	if !black(pixelAt(t, out, 1.2, 330, 120)) {
		t.Fatal("portrait video was stretched")
	}
	if !black(pixelAt(t, out, 2.5, 160, 120)) || !black(pixelAt(t, out, 2.5, 480, 120)) {
		t.Fatal("camera-off interval held stale picture")
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-threads", "1", "-i", out, "-map", "0:a", "-f", "s16le", "-ac", "1", "-ar", "16000", "pipe:1")
	pcm, e := cmd.Output()
	if e != nil {
		t.Fatal(e)
	}
	power := func(start int) float64 {
		sum := float64(0)
		for i := start; i < start+1200; i++ {
			v := float64(int16(uint16(pcm[i*2]) | uint16(pcm[i*2+1])<<8))
			sum += v * v
		}
		return sum / 1200
	}
	if len(pcm) < 96000 || power(8000) < 1e6 || power(24000) < 1e6 || power(32000) > 1e4 {
		t.Fatal("mux lost/moved audio timeline")
	}
}
func TestVideoCaptureRotationAndClockResetStartNewIDRSegment(t *testing.T) {
	s := videoTestService(t)
	frames := videoFixture(t, "blue", "180x320", 10)
	dir := filepath.Join(s.cfg.RootDir, "RESET")
	os.Mkdir(dir, 0700)
	origin := time.Unix(10000, 0)
	c := &videoCapture{svc: s, dir: dir, startedAt: origin, job: videoJob{CallID: "RESET", State: "recording"}}
	for i, f := range frames {
		rotation := uint16(0)
		epoch := uint64(1)
		stamp := uint32(i * 100)
		if i >= 5 {
			rotation = 90
			epoch = 2
			stamp = uint32((i - 5) * 100)
		}
		if e := c.write(capturedVideoFrame{data: f, side: 1, stamp: stamp, rate: 1000, source: 1, rotation: rotation, epoch: epoch, at: origin.Add(time.Duration(i) * 100 * time.Millisecond)}); e != nil {
			t.Fatal(e)
		}
	}
	c.closeTrack(&c.tracks[1])
	if len(c.job.Segments) != 2 || c.job.Segments[1].StartMS != 500 || c.job.Segments[1].Rotation != 90 {
		t.Fatal(c.job.Segments)
	}
	for _, side := range []string{"agent", "customer"} {
		writePCM16File(t, filepath.Join(dir, side+".pcm"), make([]int16, 16000))
	}
	c.job.DurationMS = 1000
	out := filepath.Join(dir, "rotate.mp4")
	if err := s.assembleVideo(dir, &c.job, out); err != nil {
		t.Fatal(err)
	}
	before := pixelAt(t, out, 0.2, 330, 120)
	after := pixelAt(t, out, 0.7, 330, 120)
	if before[2] > 15 || after[2] < 150 {
		t.Fatal("orientation wasn't applied per segment", before, after)
	}
}
func TestVideoCleanupWaitsForBothConfirmedArtifacts(t *testing.T) {
	s := videoTestService(t)
	dir := filepath.Join(s.cfg.RootDir, "BOTH")
	os.Mkdir(dir, 0700)
	a := &recordingJob{CallID: "BOTH", State: "done", Confirmed: true, FileID: "audio1"}
	s.saveJob(dir, a)
	v := &videoJob{CallID: "BOTH", State: "queued", Segments: []videoSegment{{File: "capture-000.mkv"}}}
	saveVideoJob(dir, v)
	s.cleanupRecording(dir)
	if _, e := os.Stat(dir); e != nil {
		t.Fatal("deleted before video confirmation")
	}
	v.State = "done"
	v.Confirmed = true
	saveVideoJob(dir, v)
	s.cleanupRecording(dir)
	if _, e := os.Stat(dir); e != nil {
		t.Fatal("deleted without permanent video fileId")
	}
	v.FileID = "video1"
	saveVideoJob(dir, v)
	s.cleanupRecording(dir)
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("didn't clean confirmed artifacts")
	}
}
func TestVideoUploadRequiresPermanentIDAndRetriesWithoutBrowser(t *testing.T) {
	s := videoTestService(t)
	path := filepath.Join(s.cfg.RootDir, "test.mp4")
	os.WriteFile(path, []byte("testvideo"), 0600)
	confirmCount := 0
	uploadCount := 0
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/put" {
			uploadCount++
			if r.Method != "PUT" || string(body) != "testvideo" || r.Header.Get("Content-Type") != "video/mp4" {
				t.Error("invalid binary PUT")
			}
			return
		}
		if r.Header.Get("X-Signature") != s.signature(body) {
			t.Error("invalid HMAC")
		}
		var data map[string]any
		json.Unmarshal(body, &data)
		if data["artifact"] != "video" {
			t.Error(data)
		}
		switch r.URL.Query().Get("action") {
		case "upload-url":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "tempFileId": "temp-video", "urlUpload": api.URL + "/put"})
		case "confirm":
			confirmCount++
			id := ""
			if confirmCount > 1 {
				id = "video-permanent"
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "fileId": id})
		}
	}))
	defer api.Close()
	s.cfg.BaseURL = api.URL
	s.cfg.Secret = "test"
	s.httpClient = api.Client()
	j := &videoJob{CallID: "VIDEO1", Name: "call.mp4", Size: 9, DurationMS: 1000}
	if _, err := s.uploadVideoAndConfirm(j, path); err == nil {
		t.Fatal("accepted missing permanent fileId")
	}
	id, err := s.uploadVideoAndConfirm(j, path)
	if err != nil || id != "video-permanent" || uploadCount != 2 {
		t.Fatal(id, err, uploadCount)
	}
	if _, e := os.Stat(path); e != nil {
		t.Fatal("upload function removed original")
	}
}
func TestVideoCaptureQueueDoesNotBlockAndDrainsMemory(t *testing.T) {
	s := videoTestService(t)
	rec, e := newCallRecorder(s.cfg.RootDir, "QUEUE", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	defer rec.close()
	if e = s.startVideo(rec); e != nil {
		t.Fatal(e)
	}
	s.active["QUEUE"] = rec
	h := newMediaHistory(time.Now().UnixMilli(), true, true)
	rec.video.setMedia(h)
	var wg sync.WaitGroup
	for side := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				s.captureVideo("QUEUE", side, bytes.Repeat([]byte{0}, 1024), 0, 1000, 0, 0)
			}
		}()
	}
	wg.Wait()
	rec.video.close()
	if s.videoMemory.Load() != 0 || s.videoCaptures.Load() != 0 {
		t.Fatal("capture queue leaked memory")
	}
	s.captureVideo("QUEUE", 0, []byte{1}, 0, 1000, 0, 0)
	if s.videoMemory.Load() != 0 {
		t.Fatal("enqueue after close")
	}
}
func TestVideoRecoveryUsesRecordedTimeInsteadOfRestartTime(t *testing.T) {
	s := videoTestService(t)
	dir := filepath.Join(s.cfg.RootDir, "CRASH")
	os.Mkdir(dir, 0700)
	frames := videoFixture(t, "red", "320x180", 10)
	segments := capturedFixture(t, s, dir, "CRASH", frames, 0, 500, 0)
	segments[0].LastMS = 500 // last journal checkpoint predates persisted final frames
	j := &videoJob{CallID: "CRASH", State: "recording", ConnectedAt: time.Now().Add(-72 * time.Hour), Segments: segments}
	saveVideoJob(dir, j)
	if e := s.recoverVideoJob(dir); e != nil {
		t.Fatal(e)
	}
	j, e := loadVideoJob(dir)
	if e != nil || j.State != "queued" || !j.Partial || j.DurationMS < 1400 || j.DurationMS > 1600 {
		t.Fatal(j, e)
	}
}
func TestVideoParameterSetBounds(t *testing.T) {
	for _, sps := range [][]byte{{}, {0x67, 100, 0, 30}, {0x67, 255, 255, 255, 255, 255}} {
		if _, _, e := h264Dimensions(sps); e == nil {
			t.Fatal("accepted malformed SPS")
		}
	}
	if got := shortVideoError(strings.Repeat("ç", 600)); len([]rune(got)) > 500 {
		t.Fatal("error exceeds API cap")
	}
}

func TestVideoAudioOnlyCallsDoNotConsumeCaptureSlots(t *testing.T) {
	s := videoTestService(t)
	s.cfg.Video.MaxCalls = 1
	first, e := newCallRecorder(s.cfg.RootDir, "AUDIOONLY", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	defer first.close()
	second, e := newCallRecorder(s.cfg.RootDir, "VIDEOTWO", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	defer second.close()
	if e = s.startVideo(first); e != nil {
		t.Fatal(e)
	}
	if e = s.startVideo(second); e != nil {
		t.Fatal(e)
	}
	if s.videoCaptures.Load() != 0 {
		t.Fatal("audio calls consumed video slots")
	}
	second.video.setMedia(newMediaHistory(time.Now().UnixMilli(), true, false))
	if s.videoCaptures.Load() != 1 {
		t.Fatal("video slot wasn't reserved")
	}
	first.video.close()
	second.video.close()
	job, e := loadVideoJob(first.dir)
	if e != nil || job.State != "done" || job.Requested {
		t.Fatal("audio-only call incorrectly requested video", job, e)
	}
}
func TestVideoCaptureLimitsRetainOriginalsAndLeaveAudioAvailable(t *testing.T) {
	s := videoTestService(t)
	s.cfg.Video.MaxCalls = 1
	rec, e := newCallRecorder(s.cfg.RootDir, "LIMITONE", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	defer rec.close()
	s.startVideo(rec)
	s.active[rec.callID] = rec
	rec.video.setMedia(newMediaHistory(time.Now().UnixMilli(), true, true))
	other, e := newCallRecorder(s.cfg.RootDir, "LIMITTWO", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	defer other.close()
	s.startVideo(other)
	other.video.setMedia(newMediaHistory(time.Now().UnixMilli(), true, true))
	if !other.video.halted.Load() {
		t.Fatal("simultaneous capture cap ignored")
	}
	if e = other.writeAgent([]float32{0.25, 0.5}, time.Now()); e != nil {
		t.Fatal("video limit blocked audio", e)
	}
	other.video.close()
	frames := videoFixture(t, "red", "320x180", 2)
	rec.video.close()
	dir := filepath.Join(s.cfg.RootDir, "SIZELIMIT")
	os.Mkdir(dir, 0700)
	origin := time.Now()
	s.cfg.Video.MaxBytes = int64(len(frames[0]) + 1)
	c := &videoCapture{svc: s, dir: dir, startedAt: origin, job: videoJob{CallID: "SIZELIMIT", State: "recording"}}
	if e = c.write(capturedVideoFrame{data: frames[0], rate: 1000, at: origin}); e != nil {
		t.Fatal(e)
	}
	if e = c.write(capturedVideoFrame{data: frames[1], stamp: 100, rate: 1000, at: origin.Add(100 * time.Millisecond)}); e == nil {
		t.Fatal("size cap ignored")
	}
	c.closeTrack(&c.tracks[0])
	if _, e = os.Stat(filepath.Join(dir, c.job.Segments[0].File)); e != nil {
		t.Fatal("original capture lost")
	}
}
func TestTimedInboundVideoTapWorksWithoutBrowserBridge(t *testing.T) {
	sink := newOrientedVideoSink()
	var gotStamp, gotSSRC uint32
	var gotRotation uint16
	sink.onVideo = func(au []byte, stamp, ssrc uint32, rotation uint16) {
		gotStamp = stamp
		gotSSRC = ssrc
		gotRotation = rotation
	}
	sink.SetOrientation(1)
	if e := sink.WriteTimedVideo([]byte{0, 0, 0, 1, 0x65}, 90000, 42); e != nil {
		t.Fatal(e)
	}
	if gotStamp != 90000 || gotSSRC != 42 || gotRotation != 90 {
		t.Fatal("server tap depended on browser rendering")
	}
}
