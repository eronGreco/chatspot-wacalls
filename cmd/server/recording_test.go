package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildStereoWAVChannelOrderAndPadding(t *testing.T) {
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "agent.pcm")
	customerPath := filepath.Join(dir, "customer.pcm")
	outPath := filepath.Join(dir, "call.wav")

	writePCM16File(t, agentPath, []int16{100, 200, 300})
	writePCM16File(t, customerPath, []int16{-100, -200})

	totalSamples, size, err := buildStereoWAV(agentPath, customerPath, outPath)
	if err != nil {
		t.Fatalf("buildStereoWAV: %v", err)
	}
	if totalSamples != 3 {
		t.Fatalf("total samples = %d, want 3", totalSamples)
	}
	if size != 44+3*4 {
		t.Fatalf("size = %d, want %d", size, 44+3*4)
	}

	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatalf("invalid wav header: %q %q", b[0:4], b[8:12])
	}
	if got := binary.LittleEndian.Uint16(b[22:24]); got != 2 {
		t.Fatalf("channels = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint32(b[24:28]); got != recordingSampleRate {
		t.Fatalf("sample rate = %d, want %d", got, recordingSampleRate)
	}

	want := []int16{100, -100, 200, -200, 300, 0}
	for i, w := range want {
		got := int16(binary.LittleEndian.Uint16(b[44+i*2 : 46+i*2]))
		if got != w {
			t.Fatalf("sample %d = %d, want %d", i, got, w)
		}
	}
}

func TestMonoWAVChunkPadsMissingTrackWithSilence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "track.pcm")
	writePCM16File(t, path, []int16{7, 8})

	b, err := monoWAVChunk(path, 0, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 44+8 {
		t.Fatalf("len = %d", len(b))
	}
	want := []int16{7, 8, 0, 0}
	for i, w := range want {
		got := int16(binary.LittleEndian.Uint16(b[44+i*2 : 46+i*2]))
		if got != w {
			t.Fatalf("sample %d = %d, want %d", i, got, w)
		}
	}
}

func TestCallRecorderAddsSilenceForLateTrack(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	rec, err := newCallRecorder(dir, "CALL-1", start)
	if err != nil {
		t.Fatal(err)
	}

	pcm := make([]float32, 320) // 20 ms at 16 kHz.
	if err := rec.writeAgent(pcm, start.Add(20*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := rec.writeCustomer(pcm, start.Add(120*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := rec.close(); err != nil {
		t.Fatal(err)
	}

	agentSamples, err := pcmSampleCount(filepath.Join(rec.dir, "agent.pcm"))
	if err != nil {
		t.Fatal(err)
	}
	customerSamples, err := pcmSampleCount(filepath.Join(rec.dir, "customer.pcm"))
	if err != nil {
		t.Fatal(err)
	}
	if agentSamples != 320 {
		t.Fatalf("agent samples = %d, want 320", agentSamples)
	}
	// Customer chunk is positioned at ~100 ms, so 1600 silence samples + 320 audio samples.
	if customerSamples != 1920 {
		t.Fatalf("customer samples = %d, want 1920", customerSamples)
	}
}

func TestSafeRecordingName(t *testing.T) {
	if got := safeRecordingName("../call:id@lid"); got != "___call_id_lid" {
		t.Fatalf("safe name = %q", got)
	}
}

func writePCM16File(t *testing.T, path string, samples []int16) {
	t.Helper()
	b := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(sample))
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
