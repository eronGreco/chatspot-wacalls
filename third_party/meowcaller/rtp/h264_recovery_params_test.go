package rtp

import (
	"bytes"
	"testing"
)

func TestRecoveryIDRRestoresParametersReceivedWhileWaiting(t *testing.T) {
	var a H264AccessUnitAssembler
	a.Push(1, true, []byte{0x65, 1})
	a.Push(3, true, []byte{0x41, 2})
	sps := []byte{0x67, 0x42, 0xe0, 0x1f}
	pps := []byte{0x68, 1}
	if _, ok, _ := a.Push(4, true, sps); ok {
		t.Fatal("parameters alone must not end recovery")
	}
	a.Push(5, true, pps)
	frame, ok, _ := a.Push(6, true, []byte{0x65, 3})
	if !ok {
		t.Fatal("missing recovery frame")
	}
	nalus := SplitAnnexB(frame)
	if len(nalus) != 3 || !bytes.Equal(nalus[0], sps) || !bytes.Equal(nalus[1], pps) || nalus[2][0] != 0x65 {
		t.Fatalf("missing parameters: %x", frame)
	}
}
func TestChangedSPSDoesNotRestoreOldPPS(t *testing.T) {
	var a H264AccessUnitAssembler
	a.Push(1, true, []byte{0x67, 0x42, 0xe0, 0x1f})
	a.Push(2, true, []byte{0x68, 1})
	a.Push(3, true, []byte{0x67, 0x64, 0, 0x28})
	frame, _, _ := a.Push(4, true, []byte{0x65, 2})
	if len(SplitAnnexB(frame)) != 1 {
		t.Fatal("must not prepend mismatched old PPS")
	}
}
