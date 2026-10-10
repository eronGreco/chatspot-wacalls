package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/purpshell/meowcaller/rtp"
)

// Four different corners distinguish clockwise from counterclockwise rotation.
// A solid-color fixture cannot catch a 180-degree error in the composed MP4.
func TestVideoRecordingOrientationMatchesLiveReceiver(t *testing.T) {
	s := videoTestService(t)
	dir := filepath.Join(s.cfg.RootDir, "ORIENTATION")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	filter := "color=c=black:s=160x120:r=10,drawbox=x=0:y=0:w=80:h=60:color=red:t=fill,drawbox=x=80:y=0:w=80:h=60:color=lime:t=fill,drawbox=x=0:y=60:w=80:h=60:color=blue:t=fill,drawbox=x=80:y=60:w=80:h=60:color=yellow:t=fill"
	cmd := exec.Command(s.cfg.Video.FFmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-threads", "1", "-filter_threads", "1", "-f", "lavfi", "-i", filter, "-frames:v", "20", "-an", "-c:v", "libx264", "-preset", "ultrafast", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-threads", "1", "-x264-params", "aud=1:keyint=5:repeat-headers=1", "-f", "h264", "pipe:1")
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	var frame []byte
	for _, nal := range rtp.SplitAnnexB(b) {
		if nal[0]&31 == 9 && len(frame) > 0 {
			frames = append(frames, frame)
			frame = nil
		}
		frame = append(frame, 0, 0, 0, 1)
		frame = append(frame, nal...)
	}
	frames = append(frames, frame)
	if len(frames) != 20 {
		t.Fatalf("fixture frames: %d", len(frames))
	}
	agent := capturedFixture(t, s, dir, "ORIENTATION", videoFixture(t, "magenta", "320x240", 20), 0, 0, 0)
	if err := os.Rename(filepath.Join(dir, agent[0].File), filepath.Join(dir, "capture-agent.mkv")); err != nil {
		t.Fatal(err)
	}
	agent[0].File = "capture-agent.mkv"
	origin := time.Unix(10000, 0)
	c := &videoCapture{svc: s, dir: dir, startedAt: origin, job: videoJob{CallID: "ORIENTATION", State: "recording"}}
	rotations := []uint16{0, 90, 180, 270}
	for i, au := range frames {
		if err := c.write(capturedVideoFrame{data: au, side: 1, stamp: uint32(i * 9000), rate: 90000, source: 42, epoch: 1, rotation: rotations[i/5], at: origin.Add(time.Duration(i) * 100 * time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.closeTrack(&c.tracks[1]); err != nil {
		t.Fatal(err)
	}
	if len(c.job.Segments) != 4 {
		t.Fatalf("orientation changes must preserve separate segments: %+v", c.job.Segments)
	}
	for _, side := range []string{"agent", "customer"} {
		writePCM16File(t, filepath.Join(dir, side+".pcm"), make([]int16, 2*recordingSampleRate))
	}
	j := &videoJob{CallID: "ORIENTATION", DurationMS: 2000, Segments: append(agent, c.job.Segments...)}
	out := filepath.Join(dir, "orientation.mp4")
	if err := s.assembleVideo(dir, j, out); err != nil {
		t.Fatal(err)
	}
	// In each segment, apply the same negative rotation as ctx.rotate(-CVO).
	// Corner order: top-left, top-right, bottom-left, bottom-right.
	expected := [][4][3]byte{
		{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 0}},
		{{0, 255, 0}, {255, 255, 0}, {255, 0, 0}, {0, 0, 255}},
		{{255, 255, 0}, {0, 0, 255}, {0, 255, 0}, {255, 0, 0}},
		{{0, 0, 255}, {255, 0, 0}, {255, 255, 0}, {0, 255, 0}},
	}
	points := [][2]int{{430, 60}, {530, 60}, {430, 180}, {530, 180}}
	for i, rotation := range rotations {
		at := float64(i)*0.5 + 0.2
		left := pixelAt(t, out, at, 160, 120)
		if left[0] < 180 || left[1] > 60 || left[2] < 180 {
			t.Fatalf("customer rotation %d changed agent track: %v", rotation, left)
		}
		for corner, point := range points {
			got := pixelAt(t, out, at, point[0], point[1])
			for channel, want := range expected[i][corner] {
				if (want == 255 && got[channel] < 180) || (want == 0 && got[channel] > 60) {
					t.Fatalf("rotation %d corner %d: got %v, want %v", rotation, corner, got, expected[i][corner])
				}
			}
		}
	}
}
