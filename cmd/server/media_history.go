package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const maxMediaSegments = 512

type MediaSegment struct {
	StartMS     int64  `json:"startMs"`
	EndMS       *int64 `json:"endMs,omitempty"`
	Media       string `json:"media"`
	LocalVideo  bool   `json:"localVideo"`
	RemoteVideo bool   `json:"remoteVideo"`
}

type CallMediaHistory struct {
	Version     int            `json:"version"`
	ConnectedAt int64          `json:"connectedAt"`
	EndedAt     *int64         `json:"endedAt,omitempty"`
	Profile     string         `json:"profile"`
	Segments    []MediaSegment `json:"segments"`
	Truncated   bool           `json:"truncated"`
	// Preserve classification even if an abusive number of changes exhausts the timeline budget.
	hadAudio bool
	hadVideo bool
}

func newMediaHistory(at int64, local, remote bool) *CallMediaHistory {
	h := &CallMediaHistory{Version: 1, ConnectedAt: at}
	h.transition(at, local, remote)
	return h
}

func (h *CallMediaHistory) clone() *CallMediaHistory {
	if h == nil {
		return nil
	}
	cp := *h
	cp.Segments = append([]MediaSegment(nil), h.Segments...)
	for i := range cp.Segments {
		if end := cp.Segments[i].EndMS; end != nil {
			v := *end
			cp.Segments[i].EndMS = &v
		}
	}
	if h.EndedAt != nil {
		v := *h.EndedAt
		cp.EndedAt = &v
	}
	return &cp
}

func (h *CallMediaHistory) transition(at int64, local, remote bool) bool {
	if h == nil || h.EndedAt != nil {
		return false
	}
	mode := mediaKind(local || remote)
	offset := at - h.ConnectedAt
	if offset < 0 {
		offset = 0
	}
	n := len(h.Segments)
	if n > 0 && !h.Truncated {
		prev := &h.Segments[n-1]
		if prev.LocalVideo == local && prev.RemoteVideo == remote {
			return false
		}
		if offset < prev.StartMS {
			offset = prev.StartMS
		}
		if offset == prev.StartMS {
			// No duration elapsed. Replace the instantaneous change instead of inventing a mixed interval.
			h.Segments = h.Segments[:n-1]
			n--
		} else {
			prev.EndMS = &offset
			if prev.Media == "video" {
				h.hadVideo = true
			} else {
				h.hadAudio = true
			}
		}
	}
	if n >= maxMediaSegments || h.Truncated {
		h.Truncated = true
		if mode == "video" {
			h.hadVideo = true
		} else {
			h.hadAudio = true
		}
	} else {
		h.Segments = append(h.Segments, MediaSegment{StartMS: offset, Media: mode, LocalVideo: local, RemoteVideo: remote})
	}
	h.profile(mode)
	return true
}

func (h *CallMediaHistory) profile(current string) {
	audio, video := h.hadAudio, h.hadVideo
	if current == "video" {
		video = true
	} else {
		audio = true
	}
	if audio && video {
		h.Profile = "mixed"
	} else if video {
		h.Profile = "video"
	} else {
		h.Profile = "audio"
	}
}

func (h *CallMediaHistory) close(at int64) {
	if h == nil || h.EndedAt != nil {
		return
	}
	if at < h.ConnectedAt {
		at = h.ConnectedAt
	}
	h.EndedAt = &at
	if n := len(h.Segments); n > 0 && !h.Truncated {
		end := at - h.ConnectedAt
		if end < h.Segments[n-1].StartMS {
			end = h.Segments[n-1].StartMS
			at = h.ConnectedAt + end
			h.EndedAt = &at
		}
		h.Segments[n-1].EndMS = &end
	}
}

// setVideoActivity updates both directions atomically. One camera off does not
// turn the whole call into audio while the other remains negotiated and active.
func (b *Broker) setVideoActivity(id string, local, remote bool, now int64) (*CallMediaHistory, bool) {
	b.mu.Lock()
	c := b.calls[id]
	if c == nil {
		b.mu.Unlock()
		return nil, false
	}
	changed := c.localVideo != local || c.remoteVideo != remote
	c.localVideo, c.remoteVideo = local, remote
	c.Media = mediaKind(local || remote)
	h := c.MediaHistory.clone()
	if h != nil {
		changed = h.transition(now, local, remote) || changed
		c.MediaHistory = h
	}
	snapshot := h.clone()
	b.mu.Unlock()
	if changed {
		b.broadcastCallList()
		rec, _ := b.getCall(id)
		if rec != nil {
			b.broadcast(map[string]any{"type": "call-status", "sessionId": rec.SessionID, "id": rec.CallID, "owner": rec.Owner, "status": rec.Status, "peer": rec.Peer, "peerPhone": rec.PeerPhone, "media": rec.Media, "startedAt": rec.StartedAt, "mediaHistory": rec.MediaHistory})
		}
	}
	return snapshot, changed
}

// observeMedia persists only small transition metadata, never video files or
// browser data. It is independent from the eventual container/muxing worker.
func (s *recordingService) observeMedia(id string, h *CallMediaHistory) {
	if s == nil || h == nil {
		return
	}
	s.mu.Lock()
	rec := s.active[id]
	s.mu.Unlock()
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.closed {
		return
	}
	if err := saveMediaManifest(rec.dir, h); err != nil {
		s.log.Error("persist media timeline failed", "call_id", id, "err", err)
	}
}

type RecordingMediaManifest struct {
	Version                 int               `json:"version"`
	Clock                   string            `json:"clock"`
	AudioSampleRate         int               `json:"audioSampleRate"`
	VideoRecordingSupported bool              `json:"videoRecordingSupported"`
	VideoRecordingStatus    string            `json:"videoRecordingStatus"`
	MediaHistory            *CallMediaHistory `json:"mediaHistory"`
}

func saveMediaManifest(dir string, h *CallMediaHistory) error {
	manifest := RecordingMediaManifest{Version: 1, Clock: "server_connected", AudioSampleRate: recordingSampleRate, VideoRecordingStatus: "unavailable", MediaHistory: h}
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".media-*.json")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(body); err != nil {
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
	return os.Rename(name, filepath.Join(dir, "media.json"))
}

func loadMediaManifest(dir string) (*CallMediaHistory, error) {
	body, err := os.ReadFile(filepath.Join(dir, "media.json"))
	if err != nil {
		return nil, err
	}
	var manifest RecordingMediaManifest
	if err = json.Unmarshal(body, &manifest); err != nil {
		return nil, err
	}
	return manifest.MediaHistory, nil
}

func (s *Session) syncCallMedia(c interface {
	VideoActivity() (bool, bool)
	ID() string
}) {
	local, remote := c.VideoActivity()
	h, changed := s.mgr.broker.setVideoActivity(c.ID(), local, remote, time.Now().UnixMilli())
	if changed {
		s.mgr.recordings.observeMedia(c.ID(), h)
	}
}
