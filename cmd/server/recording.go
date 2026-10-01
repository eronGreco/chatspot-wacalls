package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wacalls/internal/voip/media"
)

const recordingSampleRate = 16000

type pcmTrack struct {
	file    *os.File
	samples int64
}

type callRecorder struct {
	mu        sync.Mutex
	callID    string
	dir       string
	startedAt time.Time
	agent     *pcmTrack
	customer  *pcmTrack
	closed    bool
	video     *videoCapture
}

func newCallRecorder(rootDir, callID string, startedAt time.Time) (*callRecorder, error) {
	dir := filepath.Join(rootDir, safeRecordingName(callID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create recording directory: %w", err)
	}

	agentFile, err := os.OpenFile(filepath.Join(dir, "agent.pcm"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open agent pcm: %w", err)
	}
	customerFile, err := os.OpenFile(filepath.Join(dir, "customer.pcm"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		agentFile.Close()
		return nil, fmt.Errorf("open customer pcm: %w", err)
	}

	agentInfo, err := agentFile.Stat()
	if err != nil {
		agentFile.Close()
		customerFile.Close()
		return nil, fmt.Errorf("stat agent pcm: %w", err)
	}
	customerInfo, err := customerFile.Stat()
	if err != nil {
		agentFile.Close()
		customerFile.Close()
		return nil, fmt.Errorf("stat customer pcm: %w", err)
	}

	return &callRecorder{
		callID:    callID,
		dir:       dir,
		startedAt: startedAt,
		agent:     &pcmTrack{file: agentFile, samples: agentInfo.Size() / 2},
		customer:  &pcmTrack{file: customerFile, samples: customerInfo.Size() / 2},
	}, nil
}

func (r *callRecorder) writeAgent(pcm []float32, now time.Time) error {
	return r.writeTrack(r.agent, pcm, now)
}

func (r *callRecorder) writeCustomer(pcm []float32, now time.Time) error {
	return r.writeTrack(r.customer, pcm, now)
}

func (r *callRecorder) writeTrack(track *pcmTrack, pcm []float32, now time.Time) error {
	if len(pcm) == 0 {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}

	elapsed := now.Sub(r.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	// Callbacks arrive after the samples in pcm were captured/decoded. Position
	// the start of the chunk at approximately now-duration instead of now.
	targetStart := int64(elapsed.Seconds()*recordingSampleRate) - int64(len(pcm))
	if targetStart < 0 {
		targetStart = 0
	}
	if targetStart > track.samples {
		if err := writeSilence(track.file, targetStart-track.samples); err != nil {
			return err
		}
		track.samples = targetStart
	}

	b := media.PCMFloat32ToInt16LE(pcm)
	if _, err := track.file.Write(b); err != nil {
		return err
	}
	track.samples += int64(len(pcm))

	return nil
}

func (r *callRecorder) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true

	var errs []error
	for _, track := range []*pcmTrack{r.agent, r.customer} {
		if track == nil || track.file == nil {
			continue
		}
		if err := track.file.Sync(); err != nil {
			errs = append(errs, err)
		}
		if err := track.file.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func writeSilence(w io.Writer, samples int64) error {
	if samples <= 0 {
		return nil
	}
	const chunkSamples = 8192
	zeros := make([]byte, chunkSamples*2)
	for samples > 0 {
		n := int64(chunkSamples)
		if samples < n {
			n = samples
		}
		if _, err := w.Write(zeros[:n*2]); err != nil {
			return err
		}
		samples -= n
	}
	return nil
}

func pcmSampleCount(path string) (int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return st.Size() / 2, nil
}

func buildStereoWAV(agentPath, customerPath, outputPath string) (int64, int64, error) {
	agentSamples, err := pcmSampleCount(agentPath)
	if err != nil {
		return 0, 0, err
	}
	customerSamples, err := pcmSampleCount(customerPath)
	if err != nil {
		return 0, 0, err
	}
	totalSamples := max(agentSamples, customerSamples)
	if totalSamples == 0 {
		return 0, 0, fmt.Errorf("recording has no audio samples")
	}

	agent, err := os.Open(agentPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, 0, err
	}
	if agent != nil {
		defer agent.Close()
	}
	customer, err := os.Open(customerPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, 0, err
	}
	if customer != nil {
		defer customer.Close()
	}

	tmp, err := os.CreateTemp(filepath.Dir(outputPath), ".recording-*.wav")
	if err != nil {
		return 0, 0, err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()

	dataBytes := totalSamples * 4
	if dataBytes > int64(^uint32(0))-36 {
		return 0, 0, fmt.Errorf("recording too large for RIFF/WAV")
	}
	if err := writeWAVHeader(tmp, 2, totalSamples); err != nil {
		return 0, 0, err
	}

	const chunkSamples = 8192
	aBuf := make([]byte, chunkSamples*2)
	cBuf := make([]byte, chunkSamples*2)
	out := make([]byte, chunkSamples*4)

	for pos := int64(0); pos < totalSamples; {
		n := int64(chunkSamples)
		if remaining := totalSamples - pos; remaining < n {
			n = remaining
		}
		clear(aBuf[:n*2])
		clear(cBuf[:n*2])
		if agent != nil && pos < agentSamples {
			readAtBestEffort(agent, aBuf[:n*2], pos*2)
		}
		if customer != nil && pos < customerSamples {
			readAtBestEffort(customer, cBuf[:n*2], pos*2)
		}
		for i := int64(0); i < n; i++ {
			copy(out[i*4:i*4+2], aBuf[i*2:i*2+2])
			copy(out[i*4+2:i*4+4], cBuf[i*2:i*2+2])
		}
		if _, err := tmp.Write(out[:n*4]); err != nil {
			return 0, 0, err
		}
		pos += n
	}

	if err := tmp.Sync(); err != nil {
		return 0, 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, 0, err
	}
	if err := os.Rename(tmpName, outputPath); err != nil {
		return 0, 0, err
	}
	keep = true
	return totalSamples, 44 + dataBytes, nil
}

func readAtBestEffort(f *os.File, dst []byte, off int64) {
	if len(dst) == 0 {
		return
	}
	_, _ = f.ReadAt(dst, off)
}

func monoWAVChunk(path string, startSample, sampleCount, totalSamples int64) ([]byte, error) {
	if startSample < 0 || sampleCount < 0 || startSample > totalSamples {
		return nil, fmt.Errorf("invalid wav chunk range")
	}
	if startSample+sampleCount > totalSamples {
		sampleCount = totalSamples - startSample
	}
	buf := make([]byte, 44+sampleCount*2)
	if err := writeWAVHeaderBytes(buf[:44], 1, sampleCount); err != nil {
		return nil, err
	}
	if sampleCount == 0 {
		return buf, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return buf, nil
		}
		return nil, err
	}
	defer f.Close()
	_, _ = f.ReadAt(buf[44:], startSample*2)
	return buf, nil
}

func writeWAVHeader(w io.Writer, channels uint16, samples int64) error {
	var header [44]byte
	if err := writeWAVHeaderBytes(header[:], channels, samples); err != nil {
		return err
	}
	_, err := w.Write(header[:])
	return err
}

func writeWAVHeaderBytes(header []byte, channels uint16, samples int64) error {
	if len(header) < 44 {
		return fmt.Errorf("wav header buffer too small")
	}
	if channels == 0 {
		return fmt.Errorf("wav channels must be positive")
	}
	dataSize := samples * int64(channels) * 2
	if dataSize < 0 || dataSize > int64(^uint32(0))-36 {
		return fmt.Errorf("wav data too large")
	}
	clear(header[:44])
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+dataSize))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], channels)
	binary.LittleEndian.PutUint32(header[24:28], recordingSampleRate)
	byteRate := uint32(recordingSampleRate) * uint32(channels) * 2
	binary.LittleEndian.PutUint32(header[28:32], byteRate)
	binary.LittleEndian.PutUint16(header[32:34], channels*2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataSize))
	return nil
}

func safeRecordingName(s string) string {
	if s == "" {
		return "unknown"
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b = append(b, c)
		} else {
			b = append(b, '_')
		}
	}
	return string(b)
}
