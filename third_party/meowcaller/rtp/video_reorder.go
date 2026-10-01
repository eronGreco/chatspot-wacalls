package rtp

import "time"

const (
	// Wait only when a sequence gap exists; ordered traffic has no added delay.
	VideoReorderMaxWait    = 80 * time.Millisecond
	VideoReorderMaxPackets = 128
	videoReorderMaxPayload = 1500
)

// VideoPacket is an authenticated H264 RTP payload. Each SSRC needs its own
// buffer. The caller retains ownership of immediately emitted payloads; queued
// payloads are copied because the transport reuses its receive buffer.
type VideoPacket struct {
	Sequence  uint16
	Timestamp uint32
	Marker    bool
	Payload   []byte
}

type queuedVideoPacket struct {
	packet  VideoPacket
	arrival time.Time
}

type VideoReorderStats struct {
	Received, Reordered, LateOrDuplicate, Missing, Rejected uint64
	Pending                                                 int
}

// VideoReorderBuffer implements RFC6184 section 7.1 ordering before H264
// depacketization. It is confined to the media receive goroutine, uses no timers
// or goroutines, and retains at most 128 payloads of 1500 bytes per SSRC.
// Timeouts are checked on the next arriving video packet. If traffic stops,
// the existing independent keyframe recovery watchdog handles the stall.
type VideoReorderBuffer struct {
	started bool
	next    uint16
	pending map[uint16]queuedVideoPacket
	stats   VideoReorderStats
}

func (b *VideoReorderBuffer) Stats() VideoReorderStats {
	s := b.stats
	s.Pending = len(b.pending)
	return s
}

// Push calls emit synchronously in sequence order. Gaps that exceed the time or
// queue budget are deliberately exposed to the assembler, which requests IDR.
func (b *VideoReorderBuffer) Push(p VideoPacket, now time.Time, emit func(VideoPacket)) {
	b.stats.Received++
	if len(p.Payload) > videoReorderMaxPayload {
		b.stats.Rejected++
		return
	}
	if !b.started {
		b.started = true
		b.next = p.Sequence
	}
	distance := int16(p.Sequence - b.next)
	if distance < 0 {
		b.stats.LateOrDuplicate++
		return
	}
	if distance == 0 {
		emit(p)
		b.next++
		b.drain(emit)
	} else {
		if _, duplicate := b.pending[p.Sequence]; duplicate {
			b.stats.LateOrDuplicate++
			return
		}
		// Make room before inserting, so the bound holds even during a burst.
		if len(b.pending) >= VideoReorderMaxPackets {
			b.skipGap(emit)
			b.pushAfterAdvance(p, now, emit)
		} else {
			b.queue(p, now)
		}
	}
	for len(b.pending) > 0 && b.oldestAge(now) >= VideoReorderMaxWait {
		b.skipGap(emit)
	}
}

func (b *VideoReorderBuffer) pushAfterAdvance(p VideoPacket, now time.Time, emit func(VideoPacket)) {
	distance := int16(p.Sequence - b.next)
	if distance < 0 {
		b.stats.LateOrDuplicate++
	} else if distance == 0 {
		emit(p)
		b.next++
		b.drain(emit)
	} else {
		b.queue(p, now)
	}
}

func (b *VideoReorderBuffer) queue(p VideoPacket, now time.Time) {
	if b.pending == nil {
		b.pending = make(map[uint16]queuedVideoPacket)
	}
	p.Payload = append([]byte(nil), p.Payload...)
	b.pending[p.Sequence] = queuedVideoPacket{p, now}
	b.stats.Reordered++
}

func (b *VideoReorderBuffer) drain(emit func(VideoPacket)) {
	for {
		p, ok := b.pending[b.next]
		if !ok {
			return
		}
		delete(b.pending, b.next)
		emit(p.packet)
		b.next++
	}
}

func (b *VideoReorderBuffer) oldestAge(now time.Time) time.Duration {
	var oldest time.Time
	for _, p := range b.pending {
		if oldest.IsZero() || p.arrival.Before(oldest) {
			oldest = p.arrival
		}
	}
	return now.Sub(oldest)
}

func (b *VideoReorderBuffer) skipGap(emit func(VideoPacket)) {
	var nearest uint16
	best := uint16(0xffff)
	for seq := range b.pending {
		if distance := seq - b.next; distance < best {
			best, nearest = distance, seq
		}
	}
	b.stats.Missing += uint64(best)
	b.next = nearest
	b.drain(emit)
}
