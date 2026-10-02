package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The only video delivery worker is deliberately separate from the WAV/AI
// worker. No transcoding or uploading runs in a media callback.
func (s *recordingService) videoWorker() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.videoWake:
		case <-ticker.C:
		}
		s.processOneVideo()
	}
}
func (s *recordingService) processOneVideo() {
	entries, err := os.ReadDir(s.cfg.RootDir)
	if err != nil {
		return
	}
	var dir string
	var job *videoJob
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		d := filepath.Join(s.cfg.RootDir, entry.Name())
		audio, ae := s.loadJob(d)
		if ae != nil || audio.State == "recording" {
			continue
		}
		j, e := loadVideoJob(d)
		if e != nil || j.State == "recording" || j.State == "done" || j.State == "failed" {
			continue
		}
		if j.NextAttemptAt != nil && j.NextAttemptAt.After(time.Now()) {
			continue
		}
		if job == nil || j.ConnectedAt.Before(job.ConnectedAt) {
			job = j
			dir = d
		}
	}
	if job == nil {
		return
	}
	if err := s.processVideoJob(dir, job); err != nil {
		if s.ctx.Err() != nil {
			return
		}
		job.Attempts++
		job.LastError = err.Error()
		var p permanentError
		if errors.As(err, &p) {
			job.State = "failed"
			job.NextAttemptAt = nil
			_ = s.notifyVideo(job, "failed", err.Error())
		} else {
			next := time.Now().Add(recordingRetryDelay(job.Attempts))
			job.NextAttemptAt = &next
			time.AfterFunc(recordingRetryDelay(job.Attempts), s.signal)
		}
		if e := saveVideoJob(dir, job); e != nil {
			s.log.Error("persist video retry failed", "call_id", job.CallID, "err", e)
		}
		s.log.Warn("video recording delivery retained", "call_id", job.CallID, "state", job.State, "attempt", job.Attempts, "err", err)
		s.signal()
		return
	}
	s.cleanupRecording(dir)
	s.signal()
}
func (s *recordingService) processVideoJob(dir string, j *videoJob) error {
	if j.CallID == "" {
		return permanentError{fmt.Errorf("empty video callId")}
	}
	if j.State == "unavailable" {
		if err := s.notifyVideo(j, "unavailable", j.CaptureError); err != nil {
			return err
		}
		j.State = "done"
		return saveVideoJob(dir, j)
	}
	if j.State == "done" {
		return nil
	}
	if !s.videoSupported() {
		return retryableError{fmt.Errorf("video worker disabled; original captures retained")}
	}
	if j.Name == "" {
		j.Name = "call_" + safeRecordingName(j.CallID) + "_" + j.ConnectedAt.UTC().Format("20060102-150405") + ".mp4"
	}
	output := filepath.Join(dir, j.Name)
	if !j.Confirmed {
		if j.Size <= 0 {
			j.State = "processing"
			if err := saveVideoJob(dir, j); err != nil {
				return err
			}
			if err := s.notifyVideo(j, "processing", j.CaptureError); err != nil {
				return err
			}
			if err := s.assembleVideo(dir, j, output); err != nil {
				return err
			}
			st, err := os.Stat(output)
			if err != nil {
				return err
			}
			j.Size = st.Size()
			if j.Size <= 0 {
				return fmt.Errorf("empty composed MP4")
			}
			j.State = "uploading"
			if err := saveVideoJob(dir, j); err != nil {
				return err
			}
		}
		if err := s.notifyVideo(j, "uploading", j.CaptureError); err != nil {
			return err
		}
		fileID, err := s.uploadVideoAndConfirm(j, output)
		if err != nil {
			return err
		}
		j.FileID = fileID
		j.Confirmed = true
	}
	j.State = "done"
	j.NextAttemptAt = nil
	j.LastError = ""
	if err := saveVideoJob(dir, j); err != nil {
		return err
	}
	s.log.Info("server video recording delivered", "call_id", j.CallID, "file_id", j.FileID, "size", j.Size, "duration_ms", j.DurationMS, "partial", j.Partial)
	return nil
}
func (s *recordingService) notifyVideo(j *videoJob, status, message string) error {
	var response struct {
		OK bool `json:"ok"`
	}
	err := s.postJSON("video-status", map[string]any{"callId": j.CallID, "status": status, "partial": j.Partial, "error": shortVideoError(message), "durationMs": j.DurationMS}, &response)
	if err != nil {
		return err
	}
	if !response.OK {
		return retryableError{fmt.Errorf("video-status returned ok=false")}
	}
	return nil
}
func (s *recordingService) uploadVideoAndConfirm(j *videoJob, path string) (string, error) {
	for range 2 {
		var up struct {
			OK              bool   `json:"ok"`
			TempFileID      string `json:"tempFileId"`
			URLUpload       string `json:"urlUpload"`
			AlreadyUploaded bool   `json:"alreadyUploaded"`
			FileID          string `json:"fileId"`
		}
		if err := s.postJSON("upload-url", map[string]any{"callId": j.CallID, "artifact": "video", "mimeType": "video/mp4", "name": j.Name}, &up); err != nil {
			return "", err
		}
		if !up.OK {
			return "", retryableError{fmt.Errorf("video upload-url returned ok=false")}
		}
		if up.AlreadyUploaded {
			if up.FileID == "" {
				return "", retryableError{fmt.Errorf("confirmed video returned empty fileId")}
			}
			return up.FileID, nil
		}
		if up.TempFileID == "" || up.URLUpload == "" {
			return "", retryableError{fmt.Errorf("video upload-url missing fields")}
		}
		status, err := s.putFile(up.URLUpload, path, "video/mp4")
		if err != nil {
			return "", err
		}
		if status == http.StatusForbidden {
			continue
		}
		if status < 200 || status >= 300 {
			return "", classifyHTTPError("video upload", status)
		}
		var confirmed struct {
			OK     bool   `json:"ok"`
			FileID string `json:"fileId"`
		}
		if err := s.postJSON("confirm", map[string]any{"callId": j.CallID, "artifact": "video", "tempFileId": up.TempFileID, "mimeType": "video/mp4", "size": j.Size, "durationMs": j.DurationMS, "layout": "side-by-side:agent-left,customer-right", "partial": j.Partial, "error": j.CaptureError}, &confirmed); err != nil {
			return "", err
		}
		if !confirmed.OK || confirmed.FileID == "" {
			return "", retryableError{fmt.Errorf("video confirm returned no permanent fileId")}
		}
		return confirmed.FileID, nil
	}
	return "", retryableError{fmt.Errorf("presigned video upload URL expired twice")}
}

// Both workers may complete in either order. Their journals are independent;
// originals/PCM/WAV/MP4 are removed together only after every artifact is done.
// Failure, unreadable journals and missing permanent IDs always retain files.
func (s *recordingService) cleanupRecording(dir string) {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	audio, err := s.loadJob(dir)
	if err != nil || audio.State != "done" || !audio.Confirmed || audio.FileID == "" {
		return
	}
	video, err := loadVideoJob(dir)
	if err == nil {
		if video.State != "done" {
			return
		}
		if len(video.Segments) > 0 && (!video.Confirmed || video.FileID == "") {
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		s.log.Warn("remove confirmed recording files failed", "call_id", audio.CallID, "err", err)
		return
	}
	s.log.Info("confirmed recording files removed", "call_id", audio.CallID, "audio_file_id", audio.FileID)
}

type tailWriter struct{ b []byte }

func (w *tailWriter) Write(b []byte) (int, error) {
	n := len(b)
	w.b = append(w.b, b...)
	if len(w.b) > 8192 {
		w.b = bytes.Clone(w.b[len(w.b)-8192:])
	}
	return n, nil
}
func runVideoCommand(ctx context.Context, binary string, args ...string) error {
	command := binary
	commandArgs := args
	// Lower background encoder priority where nice is available (including Alpine).
	if nice, err := exec.LookPath("nice"); err == nil {
		command = nice
		commandArgs = append([]string{"-n", "10", binary}, args...)
	}
	cmd := exec.CommandContext(ctx, command, commandArgs...)
	var tail tailWriter
	cmd.Stderr = &tail
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("video processing: %w: %s", err, strings.TrimSpace(string(tail.b)))
	}
	return nil
}
func ffVideoArgs() []string {
	return []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-threads", "1", "-filter_threads", "1", "-filter_complex_threads", "1"}
}
func encodeVideoArgs() []string {
	return []string{"-an", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "32", "-maxrate", "96k", "-bufsize", "192k", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-g", "20", "-threads", "1", "-fps_mode", "cfr", "-r", "10"}
}
func videoSeconds(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }
func quantizeMS(ms int64) int64    { return max(int64(0), (ms+50)/100*100) }

func (s *recordingService) assembleVideo(dir string, j *videoJob, output string) error {
	if j.DurationMS < 100 {
		return permanentError{fmt.Errorf("video duration missing")}
	}
	// Refuse corrupted jobs that would expand an unbounded timeline/filter graph.
	if len(j.Segments) > maxVideoCaptureSegments || j.DurationMS > 14400000 {
		return permanentError{fmt.Errorf("invalid video recording bounds")}
	}
	ctx, cancel := context.WithTimeout(s.ctx, min(3*time.Hour, time.Duration(j.DurationMS)*time.Millisecond*3+5*time.Minute))
	defer cancel()
	work := filepath.Join(dir, "video-work")
	if err := os.MkdirAll(work, 0700); err != nil {
		return err
	}
	// Normalized temporary tracks are reproducible; failed attempts don't pile up.
	if err := os.RemoveAll(work); err != nil {
		return err
	}
	if err := os.MkdirAll(work, 0700); err != nil {
		return err
	}
	duration := quantizeMS(j.DurationMS)
	j.DurationMS = duration
	valid := 0
	for _, side := range []string{"agent", "customer"} {
		n, err := s.normalizeVideoTrack(ctx, dir, work, j, side, duration)
		if err != nil {
			return err
		}
		valid += n
	}
	if valid == 0 {
		return permanentError{fmt.Errorf("no decodable video segment; original captures retained")}
	}
	agent := filepath.Join(work, "agent.txt")
	customer := filepath.Join(work, "customer.txt")
	// Raw PCM remains authoritative even when video preparation wins the race
	// against WAV uploading/transcription. Build a separate temporary WAV so the
	// two workers never overwrite the same file.
	_, _, err := buildStereoWAV(filepath.Join(dir, "agent.pcm"), filepath.Join(dir, "customer.pcm"), filepath.Join(work, "mix.wav"))
	if err != nil {
		return err
	}
	args := ffVideoArgs()
	args = append(args, "-f", "concat", "-safe", "1", "-i", agent, "-f", "concat", "-safe", "1", "-threads", "1", "-i", customer, "-threads", "1", "-i", filepath.Join(work, "mix.wav"), "-filter_complex", "[0:v]setpts=PTS-STARTPTS[a];[1:v]setpts=PTS-STARTPTS[b];[a][b]hstack=inputs=2[v];[2:a]pan=mono|c0=0.5*c0+0.5*c1,apad[audio]", "-map", "[v]", "-map", "[audio]", "-t", videoSeconds(duration), "-c:v", "libx264", "-preset", "ultrafast", "-crf", "32", "-maxrate", "192k", "-bufsize", "384k", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-g", "20", "-threads", "1", "-r", "10", "-c:a", "aac", "-b:a", "48k", "-ar", "16000", "-ac", "1", "-movflags", "+faststart", "-f", "mp4", output+".tmp")
	if err := runVideoCommand(ctx, s.cfg.Video.FFmpeg, args...); err != nil {
		return err
	}
	if err := s.validateComposedVideo(ctx, output+".tmp", duration); err != nil {
		return err
	}
	f, err := os.OpenFile(output+".tmp", os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = f.Sync()
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err := os.Rename(output+".tmp", output); err != nil {
		return err
	}
	return os.RemoveAll(work)
}
func (s *recordingService) normalizeVideoTrack(ctx context.Context, dir, work string, j *videoJob, side string, duration int64) (int, error) {
	segments := []videoSegment{}
	for _, seg := range j.Segments {
		if seg.Side == side {
			segments = append(segments, seg)
		}
	}
	sort.SliceStable(segments, func(i, k int) bool { return segments[i].StartMS < segments[k].StartMS })
	var list strings.Builder
	cursor := int64(0)
	index := 0
	valid := 0
	writeClip := func(seg *videoSegment, ms int64) error {
		if ms <= 0 {
			return nil
		}
		name := fmt.Sprintf("%s-%03d.mp4", side, index)
		index++
		path := filepath.Join(work, name)
		args := ffVideoArgs()
		if seg == nil {
			args = append(args, "-f", "lavfi", "-i", "color=c=black:s=320x240:r=10")
		} else {
			if filepath.Base(seg.File) != seg.File || !strings.HasPrefix(seg.File, "capture-") || !strings.HasSuffix(seg.File, ".mkv") {
				return permanentError{fmt.Errorf("unsafe capture path")}
			}
			args = append(args, "-i", filepath.Join(dir, seg.File))
			rotate := ""
			// Match the live receiver: WhatsApp CVO is corrected by -rotation.
			// Rotating 90/270 in the opposite direction inverts only that track.
			switch seg.Rotation {
			case 90:
				rotate = "transpose=cclock,"
			case 180:
				rotate = "hflip,vflip,"
			case 270:
				rotate = "transpose=clock,"
			}
			args = append(args, "-vf", rotate+"setpts=PTS-STARTPTS,fps=10:start_time=0,scale=320:240:force_original_aspect_ratio=decrease,pad=320:240:(ow-iw)/2:(oh-ih)/2:black,setsar=1,tpad=stop_mode=clone:stop_duration=0.1")
		}
		args = append(args, "-t", videoSeconds(ms))
		args = append(args, encodeVideoArgs()...)
		args = append(args, path)
		if err := runVideoCommand(ctx, s.cfg.Video.FFmpeg, args...); err != nil {
			if seg == nil {
				return err
			}
			j.Partial = true
			if j.CaptureError == "" {
				j.CaptureError = "Um trecho de vídeo não pôde ser decodificado."
			}
			return writeBlackFallback(ctx, s.cfg.Video.FFmpeg, path, ms, &list, name)
		}
		if seg != nil {
			valid++
		}
		fmt.Fprintf(&list, "file '%s'\nduration %s\n", name, videoSeconds(ms))
		return nil
	}
	for i, seg := range segments {
		start := min(duration, max(cursor, quantizeMS(seg.StartMS)))
		if start > cursor {
			if err := writeClip(nil, start-cursor); err != nil {
				return valid, err
			}
			cursor = start
		}
		end := min(duration, quantizeMS(seg.LastMS+100))
		if i+1 < len(segments) {
			end = min(end, quantizeMS(segments[i+1].StartMS))
		}
		if end > cursor {
			if err := writeClip(&seg, end-cursor); err != nil {
				return valid, err
			}
			cursor = end
		}
	}
	if duration > cursor {
		if err := writeClip(nil, duration-cursor); err != nil {
			return valid, err
		}
	}
	return valid, os.WriteFile(filepath.Join(work, side+".txt"), []byte(list.String()), 0600)
}
func writeBlackFallback(ctx context.Context, ffmpeg, path string, ms int64, list *strings.Builder, name string) error {
	args := ffVideoArgs()
	args = append(args, "-f", "lavfi", "-i", "color=c=black:s=320x240:r=10", "-t", videoSeconds(ms))
	args = append(args, encodeVideoArgs()...)
	args = append(args, path)
	if err := runVideoCommand(ctx, ffmpeg, args...); err != nil {
		return err
	}
	fmt.Fprintf(list, "file '%s'\nduration %s\n", name, videoSeconds(ms))
	return nil
}
func (s *recordingService) validateComposedVideo(ctx context.Context, path string, ms int64) error {
	cmd := exec.CommandContext(ctx, s.cfg.Video.FFprobe, "-v", "error", "-show_entries", "format=duration:stream=codec_name,codec_type,width,height", "-of", "json", path)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	var p struct {
		Streams []struct {
			CodecName     string `json:"codec_name"`
			CodecType     string `json:"codec_type"`
			Width, Height int
		}
		Format struct {
			Duration string `json:"duration"`
		}
	}
	if err = json.Unmarshal(out, &p); err != nil {
		return err
	}
	audio, video := false, false
	for _, st := range p.Streams {
		if st.CodecType == "audio" && st.CodecName == "aac" {
			audio = true
		}
		if st.CodecType == "video" && st.CodecName == "h264" && st.Width == 640 && st.Height == 240 {
			video = true
		}
	}
	seconds, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil || !audio || !video || absMS(int64(seconds*1000)-ms) > 250 {
		return fmt.Errorf("invalid composed MP4 streams/duration")
	}
	return nil
}

func (s *recordingService) recoverVideoJob(dir string) error {
	j, err := loadVideoJob(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.State != "recording" {
		return nil
	}
	j.Partial = true
	j.CaptureError = "O servidor reiniciou durante a captura; gravação recuperada até os últimos quadros persistidos."
	duration := int64(0)
	for i := range j.Segments {
		seg := &j.Segments[i]
		if filepath.Base(seg.File) != seg.File {
			return fmt.Errorf("unsafe video recovery path")
		}
		if s.videoSupported() {
			ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
			cmd := exec.CommandContext(ctx, s.cfg.Video.FFprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time", "-of", "csv=p=0", filepath.Join(dir, seg.File))
			var pts videoPTSWriter
			cmd.Stdout = &pts
			cmd.Stderr = io.Discard
			_ = cmd.Run()
			cancel()
			pts.consume(true)
			if pts.seen {
				seg.LastMS = max(seg.LastMS, seg.StartMS+int64(pts.max*1000))
			}

		}
		duration = max(duration, seg.LastMS+100)
	}
	for _, side := range []string{"agent.pcm", "customer.pcm"} {
		samples, e := pcmSampleCount(filepath.Join(dir, side))
		if e == nil {
			duration = max(duration, samples*1000/recordingSampleRate)
		}
	}
	limit := j.MaxDurationMS
	if limit < 100 || limit > 14400000 {
		limit = 14400000
	}
	j.DurationMS = min(limit, max(int64(100), duration))
	j.State = "queued"
	j.NextAttemptAt = nil
	if len(j.Segments) == 0 {
		if !j.Requested {
			j.State = "done"
		} else {
			j.State = "unavailable"
		}
	}
	return saveVideoJob(dir, j)
}

func shortVideoError(s string) string {
	r := []rune(s)
	if len(r) > 480 {
		return string(r[:480]) + "…"
	}
	return s
}

// ffprobe packet timing is streamed, never accumulated for a long call. A
// truncated final Cluster doesn't invalidate earlier complete packet times.
type videoPTSWriter struct {
	pending string
	max     float64
	seen    bool
}

func (w *videoPTSWriter) Write(b []byte) (int, error) {
	w.pending += string(b)
	w.consume(false)
	return len(b), nil
}
func (w *videoPTSWriter) consume(final bool) {
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 {
			if !final {
				if len(w.pending) > 4096 {
					w.pending = ""
				}
				return
			}
			i = len(w.pending)
		}
		line := strings.TrimSpace(w.pending[:i])
		if v, e := strconv.ParseFloat(line, 64); e == nil && v >= 0 && v <= 14400 {
			w.max = max(w.max, v)
			w.seen = true
		}
		if i == len(w.pending) {
			w.pending = ""
			return
		}
		w.pending = w.pending[i+1:]
	}
}
