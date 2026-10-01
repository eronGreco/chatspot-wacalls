package meowcaller

import (
	"fmt"
	"sync"
	"time"
)

const videoRecoveryInterval = time.Second
const videoReceiveStall = 3 * time.Second

type videoRecoveryStream struct {
	lastFrame time.Time
	lastPLI   time.Time
	waiting   bool
	requested bool
}

type videoRecovery struct {
	mu      sync.Mutex
	streams map[uint32]*videoRecoveryStream
	send    func(uint32) error
}

func newVideoRecovery(send func(uint32) error) *videoRecovery {
	return &videoRecovery{streams: make(map[uint32]*videoRecoveryStream), send: send}
}

func (r *videoRecovery) observe(ssrc uint32, now time.Time, complete, idr, damaged bool) {
	// Source of truth: https://github.com/purpshell/meowcaller/blob/c48c3e2a243c672c942cbf0c941d14161720d540/engine_media.go#L964-L1056
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.streams[ssrc]
	if s == nil {
		s = &videoRecoveryStream{lastFrame: now, waiting: true}
		r.streams[ssrc] = s
	}
	if damaged {
		s.waiting = true
	}
	if complete {
		s.lastFrame = now
		if idr {
			s.waiting = false
			s.requested = false
		}
	}
}

func (r *videoRecovery) request() {
	// Source of truth: https://www.rfc-editor.org/rfc/rfc4585#section-6.3.1
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.streams {
		s.requested = true
	}
}

func (r *videoRecovery) forget(ssrc uint32) {
	r.mu.Lock()
	delete(r.streams, ssrc)
	r.mu.Unlock()
}

func (r *videoRecovery) tick(now time.Time) {
	// Source of truth: https://www.rfc-editor.org/rfc/rfc4585#section-6.3.1
	r.mu.Lock()
	var due []uint32
	for ssrc, s := range r.streams {
		if !s.waiting && !s.requested && now.Sub(s.lastFrame) < videoReceiveStall {
			continue
		}
		if !s.lastPLI.IsZero() && now.Sub(s.lastPLI) < videoRecoveryInterval {
			continue
		}
		s.lastPLI = now
		due = append(due, ssrc)
	}
	r.mu.Unlock()
	for _, ssrc := range due {
		_ = r.send(ssrc)
	}
}

// RequestVideoKeyframe requests a fresh IDR from authenticated remote video
// streams. This asks the remote peer; OnVideoKeyframeRequest asks our encoder.
func (c *Call) RequestVideoKeyframe() error {
	// Source of truth: https://www.rfc-editor.org/rfc/rfc4585#section-6.3.1
	if c == nil || c.eng == nil {
		return fmt.Errorf("meowcaller: no active video receive transport")
	}
	c.eng.mu.Lock()
	m := c.eng.calls[c.id]
	var recovery *videoRecovery
	if m != nil {
		recovery = m.videoRecovery
	}
	c.eng.mu.Unlock()
	if recovery == nil {
		return fmt.Errorf("meowcaller: no active video receive transport")
	}
	recovery.request()
	return nil
}
