package rtp

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestVideoReorderFUAFromObservedArrivalPattern(t *testing.T) {
	nalu := bytes.Repeat([]byte{0x42}, 6200)
	nalu[0] = 0x41
	fragments := PackageH264NALU(nalu)
	if len(fragments) < 5 {
		t.Fatal("need fragmented interframe")
	}
	order := []int{0, 2, 3, 4, 1}
	for i := 5; i < len(fragments); i++ {
		order = append(order, i)
	}
	var b VideoReorderBuffer
	var a H264AccessUnitAssembler
	var got []byte
	now := time.Unix(1, 0)
	for _, i := range order {
		b.Push(VideoPacket{Sequence: uint16(9984 + i), Timestamp: 8762760, Marker: i == len(fragments)-1, Payload: fragments[i]}, now, func(p VideoPacket) {
			f, complete, recovery := a.Push(p.Sequence, p.Marker, p.Payload)
			if recovery {
				t.Fatal("reordering falsely triggered keyframe recovery")
			}
			if complete {
				got = f
			}
		})
		now = now.Add(time.Millisecond)
	}
	want := append([]byte{0, 0, 0, 1}, nalu...)
	if !bytes.Equal(got, want) {
		t.Fatalf("interframe not reconstructed: %d bytes", len(got))
	}
	if b.Stats().Missing != 0 || b.Stats().Pending != 0 {
		t.Fatal(b.Stats())
	}
}

func TestVideoReorderLossExpiresAndLateDoesNotBreakIDR(t *testing.T) {
	var b VideoReorderBuffer
	var a H264AccessUnitAssembler
	now := time.Unix(1, 0)
	recovered := false
	triggers := 0
	emit := func(p VideoPacket) {
		_, complete, recovery := a.Push(p.Sequence, p.Marker, p.Payload)
		if recovery {
			triggers++
		}
		if complete && p.Sequence == 4 {
			recovered = true
		}
	}
	b.Push(VideoPacket{Sequence: 1, Marker: true, Payload: []byte{0x41, 1}}, now, emit)
	b.Push(VideoPacket{Sequence: 3, Marker: true, Payload: []byte{0x41, 3}}, now.Add(time.Millisecond), emit)
	if triggers != 0 {
		t.Fatal("gap should wait before declaring loss")
	}
	b.Push(VideoPacket{Sequence: 4, Marker: true, Payload: []byte{0x65, 4}}, now.Add(VideoReorderMaxWait+time.Millisecond), emit)
	b.Push(VideoPacket{Sequence: 2, Marker: true, Payload: []byte{0x41, 2}}, now.Add(time.Second), emit)
	if triggers != 1 || !recovered || b.Stats().Missing != 1 || b.Stats().LateOrDuplicate != 1 {
		t.Fatal(triggers, recovered, b.Stats())
	}
}

func TestVideoReorderWrapDuplicateAndCopiesQueuedPayload(t *testing.T) {
	var b VideoReorderBuffer
	now := time.Unix(1, 0)
	var order []uint16
	emit := func(p VideoPacket) {
		order = append(order, p.Sequence)
		if p.Sequence == 0 && p.Payload[0] != 9 {
			t.Fatal("queued payload aliased receive buffer")
		}
	}
	b.Push(VideoPacket{Sequence: 65534}, now, emit)
	payload := []byte{9}
	b.Push(VideoPacket{Sequence: 0, Payload: payload}, now, emit)
	payload[0] = 1
	b.Push(VideoPacket{Sequence: 0}, now, emit)
	b.Push(VideoPacket{Sequence: 65535}, now, emit)
	b.Push(VideoPacket{Sequence: 65534}, now, emit)
	if fmt.Sprint(order) != "[65534 65535 0]" || b.Stats().LateOrDuplicate != 2 {
		t.Fatal(order, b.Stats())
	}
}

func TestVideoReorderMemoryBudgetAndMalformedPayload(t *testing.T) {
	var b VideoReorderBuffer
	now := time.Unix(1, 0)
	emit := func(VideoPacket) {}
	b.Push(VideoPacket{Sequence: 1}, now, emit)
	payload := make([]byte, videoReorderMaxPayload)
	for seq := 3; seq < 1500; seq += 2 {
		b.Push(VideoPacket{Sequence: uint16(seq), Payload: payload}, now, emit)
		if b.Stats().Pending > VideoReorderMaxPackets {
			t.Fatal("packet budget exceeded")
		}
	}
	b.Push(VideoPacket{Sequence: 1501, Payload: make([]byte, 1501)}, now, emit)
	if b.Stats().Rejected != 1 || b.Stats().Missing == 0 {
		t.Fatal(b.Stats())
	}
}

// Sanitized sequence/timestamp/marker metadata from the authorized 2026-10-01
// test. The log has second-resolution times; simulated 1ms spacing is explicit.
func TestVideoReorderReplayAuthorizedLog(t *testing.T) {
	data, err := os.ReadFile("testdata/video-arrival-order.txt")
	if err != nil {
		t.Fatal(err)
	}
	var b VideoReorderBuffer
	now := time.Unix(1, 0)
	var emitted []uint16
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var seq uint16
		var ts uint32
		var marker bool
		if _, err = fmt.Sscanf(line, "%d %d %t", &seq, &ts, &marker); err != nil {
			t.Fatal(err)
		}
		b.Push(VideoPacket{Sequence: seq, Timestamp: ts, Marker: marker, Payload: []byte{0x41}}, now, func(p VideoPacket) { emitted = append(emitted, p.Sequence) })
		now = now.Add(time.Millisecond)
	}
	if len(emitted) != 993 || emitted[0] != 9509 || emitted[992] != 10501 {
		t.Fatal(len(emitted))
	}
	for i, seq := range emitted {
		if seq != uint16(9509+i) {
			t.Fatalf("out of order at %d: %d", i, seq)
		}
	}
	if b.Stats().Missing != 0 || b.Stats().Pending != 0 {
		t.Fatal(b.Stats())
	}
}

func BenchmarkVideoReorderOrdered(b *testing.B) {
	var queue VideoReorderBuffer
	now := time.Unix(1, 0)
	payload := make([]byte, 1200)
	emit := func(VideoPacket) {}
	b.ReportAllocs()
	b.SetBytes(1200)
	for i := 0; i < b.N; i++ {
		queue.Push(VideoPacket{Sequence: uint16(i), Payload: payload}, now, emit)
	}
}

func BenchmarkVideoReorder500Streams(b *testing.B) {
	queues := make([]VideoReorderBuffer, 500)
	now := time.Unix(1, 0)
	payload := make([]byte, 1200)
	emit := func(VideoPacket) {}
	b.ReportAllocs()
	b.SetBytes(1200)
	for i := 0; i < b.N; i++ {
		queues[i%500].Push(VideoPacket{Sequence: uint16(i / 500), Payload: payload}, now, emit)
	}
}
