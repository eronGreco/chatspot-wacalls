package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

func (s *recordingService) processJob(dir string, job *recordingJob) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if job.CallID == "" {
		return permanentError{fmt.Errorf("recording job has empty callId")}
	}
	if job.State == "done" {
		return nil
	}

	agentPath := filepath.Join(dir, "agent.pcm")
	customerPath := filepath.Join(dir, "customer.pcm")
	if job.Name == "" || job.TotalSamples == 0 || job.Size == 0 {
		stamp := job.ConnectedAt.UTC().Format("20060102-150405")
		job.Name = fmt.Sprintf("call_%s_%s.wav", safeRecordingName(job.CallID), stamp)
		job.MimeType = "audio/wav"
		finalPath := filepath.Join(dir, job.Name)
		totalSamples, size, err := buildStereoWAV(agentPath, customerPath, finalPath)
		if err != nil {
			return permanentError{fmt.Errorf("build stereo wav: %w", err)}
		}
		job.TotalSamples = totalSamples
		job.Size = size
		job.State = "uploading"
		if err := s.saveJob(dir, job); err != nil {
			return err
		}
	}
	finalPath := filepath.Join(dir, job.Name)

	if !job.Confirmed {
		fileID, err := s.uploadAndConfirm(job, finalPath)
		if err != nil {
			return err
		}
		job.Confirmed = true
		job.FileID = fileID
		job.State = "transcribing"
		job.Attempts = 0
		job.FirstFailureAt = nil
		job.NextAttemptAt = nil
		job.LastError = ""
		if err := s.saveJob(dir, job); err != nil {
			return err
		}
	}

	if !job.TranscriptionDone {
		if err := s.transcribeJob(dir, job, agentPath, customerPath); err != nil {
			var p permanentError
			if errors.As(err, &p) {
				job.Lines = nil
				job.TranscriptError = p.Error()
				job.TranscriptionDone = true
				job.State = "done_pending"
				if saveErr := s.saveJob(dir, job); saveErr != nil {
					return saveErr
				}
			} else {
				return err
			}
		}
	}

	if !job.TranscriptionDone {
		job.TranscriptionDone = true
		job.State = "done_pending"
		if err := s.saveJob(dir, job); err != nil {
			return err
		}
	}

	sort.SliceStable(job.Lines, func(i, j int) bool {
		if job.Lines[i].Start == job.Lines[j].Start {
			return job.Lines[i].Speaker < job.Lines[j].Speaker
		}
		return job.Lines[i].Start < job.Lines[j].Start
	})
	lines := job.Lines
	if lines == nil {
		lines = []transcriptLine{}
	}
	var doneResp struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := s.postJSON("done", map[string]any{
		"callId":                  job.CallID,
		"lines":                   lines,
		"error":                   nilIfEmpty(job.TranscriptError),
		"mediaHistory":            job.MediaHistory,
		"videoRecordingSupported": false,
	}, &doneResp); err != nil {
		return err
	}
	if !doneResp.OK {
		if doneResp.Error == "" {
			doneResp.Error = "done returned ok=false"
		}
		return permanentError{errors.New(doneResp.Error)}
	}
	job.State = "done"
	job.Attempts = 0
	job.FirstFailureAt = nil
	job.NextAttemptAt = nil
	job.LastError = ""
	return s.saveJob(dir, job)
}

func (s *recordingService) uploadAndConfirm(job *recordingJob, finalPath string) (string, error) {
	for freshURLAttempt := 0; freshURLAttempt < 2; freshURLAttempt++ {
		var uploadResp struct {
			OK              bool   `json:"ok"`
			TempFileID      string `json:"tempFileId"`
			URLUpload       string `json:"urlUpload"`
			AlreadyUploaded bool   `json:"alreadyUploaded"`
			FileID          string `json:"fileId"`
			Error           string `json:"error"`
		}
		if err := s.postJSON("upload-url", map[string]any{
			"callId":   job.CallID,
			"mimeType": job.MimeType,
			"name":     job.Name,
		}, &uploadResp); err != nil {
			return "", err
		}
		if !uploadResp.OK {
			if uploadResp.Error == "" {
				uploadResp.Error = "upload-url returned ok=false"
			}
			return "", permanentError{errors.New(uploadResp.Error)}
		}
		if uploadResp.AlreadyUploaded {
			return uploadResp.FileID, nil
		}
		if uploadResp.TempFileID == "" || uploadResp.URLUpload == "" {
			return "", permanentError{fmt.Errorf("upload-url returned no tempFileId/urlUpload")}
		}

		status, err := s.putFile(uploadResp.URLUpload, finalPath, job.MimeType)
		if err != nil {
			return "", err
		}
		if status == http.StatusForbidden {
			continue
		}
		if status < 200 || status >= 300 {
			return "", classifyHTTPError("upload", status)
		}

		var confirmResp struct {
			OK     bool   `json:"ok"`
			FileID string `json:"fileId"`
			Error  string `json:"error"`
		}
		if err := s.postJSON("confirm", map[string]any{
			"callId":     job.CallID,
			"tempFileId": uploadResp.TempFileID,
			"mimeType":   job.MimeType,
			"size":       job.Size,
			"layout":     "stereo:left=agent,right=customer",
		}, &confirmResp); err != nil {
			return "", err
		}
		if !confirmResp.OK {
			if confirmResp.Error == "" {
				confirmResp.Error = "confirm returned ok=false"
			}
			return "", permanentError{errors.New(confirmResp.Error)}
		}
		return confirmResp.FileID, nil
	}
	return "", retryableError{fmt.Errorf("presigned upload URL expired twice")}
}

func (s *recordingService) putFile(rawURL, path, mimeType string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, permanentError{err}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, permanentError{err}
	}
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPut, rawURL, f)
	if err != nil {
		return 0, permanentError{err}
	}
	req.Header.Set("Content-Type", mimeType)
	req.ContentLength = st.Size()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, retryableError{err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

func (s *recordingService) transcribeJob(dir string, job *recordingJob, agentPath, customerPath string) error {
	chunkSamples := int64(transcribeChunkSecs * recordingSampleRate)
	tracks := []struct {
		speaker string
		path    string
		offset  *int64
	}{
		{speaker: "agent", path: agentPath, offset: &job.AgentOffset},
		{speaker: "customer", path: customerPath, offset: &job.CustomerOffset},
	}
	for _, track := range tracks {
		for *track.offset < job.TotalSamples {
			n := chunkSamples
			if remaining := job.TotalSamples - *track.offset; remaining < n {
				n = remaining
			}
			wav, err := monoWAVChunk(track.path, *track.offset, n, job.TotalSamples)
			if err != nil {
				return permanentError{err}
			}
			lines, err := s.transcribeChunk(job.CallID, track.speaker, *track.offset/recordingSampleRate, wav)
			if err != nil {
				return err
			}
			job.Lines = append(job.Lines, lines...)
			*track.offset += n
			if err := s.saveJob(dir, job); err != nil {
				return err
			}
		}
	}
	job.TranscriptionDone = true
	job.State = "done_pending"
	return s.saveJob(dir, job)
}

func (s *recordingService) transcribeChunk(callID, speaker string, offsetSecs int64, body []byte) ([]transcriptLine, error) {
	u := s.endpoint("transcribe")
	q := u.Query()
	q.Set("callId", callID)
	q.Set("speaker", speaker)
	q.Set("offset", strconv.FormatInt(offsetSecs, 10))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, permanentError{err}
	}
	req.Header.Set("Content-Type", "audio/wav")
	req.Header.Set("X-Signature", s.signature(body))
	req.ContentLength = int64(len(body))
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, retryableError{err}
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, retryableError{err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyHTTPError("transcribe", resp.StatusCode)
	}
	var out struct {
		OK    bool             `json:"ok"`
		Lines []transcriptLine `json:"lines"`
		Error string           `json:"error"`
	}
	if err := json.Unmarshal(responseBody, &out); err != nil {
		return nil, retryableError{fmt.Errorf("decode transcribe response: %w", err)}
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "transcribe returned ok=false"
		}
		return nil, permanentError{errors.New(out.Error)}
	}
	return out.Lines, nil
}

func (s *recordingService) postJSON(action string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return permanentError{err}
	}
	u := s.endpoint(action)
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return permanentError{err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", s.signature(body))
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return retryableError{err}
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return retryableError{err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return classifyHTTPError(action, resp.StatusCode)
	}
	if out != nil && len(responseBody) > 0 {
		if err := json.Unmarshal(responseBody, out); err != nil {
			return retryableError{fmt.Errorf("decode %s response: %w", action, err)}
		}
	}
	return nil
}

func (s *recordingService) endpoint(action string) *url.URL {
	u, _ := url.Parse(s.cfg.BaseURL + "/api/public/wacalls/recording")
	q := u.Query()
	q.Set("action", action)
	u.RawQuery = q.Encode()
	return u
}

func (s *recordingService) signature(body []byte) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.Secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
