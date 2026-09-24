package main

import "testing"

func ownerPtr(s string) *string { return &s }

func TestOwnerActiveCall(t *testing.T) {
	b := NewBroker()
	b.upsertCall(CallRecord{SessionID: "s1", CallID: "c1", Owner: ownerPtr("op-A"), Status: StatusConnected})
	b.upsertCall(CallRecord{SessionID: "s1", CallID: "c2", Owner: ownerPtr("op-B"), Status: StatusRinging})

	if got := b.ownerActiveCall("op-A"); got != "c1" {
		t.Fatalf("op-A should own c1, got %q", got)
	}
	if got := b.ownerActiveCall("op-C"); got != "" {
		t.Fatalf("op-C owns nothing, got %q", got)
	}
	if got := b.ownerActiveCall(""); got != "" {
		t.Fatalf("empty owner must return empty, got %q", got)
	}

	b.endCall("c1", "done")
	if got := b.ownerActiveCall("op-A"); got != "" {
		t.Fatalf("op-A's call ended, expected empty, got %q", got)
	}
}


func TestPeerPhonePersistsIntoHistory(t *testing.T) {
	b := NewBroker()
	b.upsertCall(CallRecord{
		SessionID: "s1",
		CallID: "c-phone",
		Direction: "inbound",
		Peer: "144512865284260@lid",
		PeerPhone: "5537933002826",
		Status: StatusRinging,
	})

	b.endCall("c-phone", "user")

	rows := b.historyRows("s1", 10)
	if len(rows) != 1 {
		t.Fatalf("expected 1 history row, got %d", len(rows))
	}
	if rows[0].Peer != "144512865284260@lid" {
		t.Fatalf("peer changed unexpectedly: %q", rows[0].Peer)
	}
	if rows[0].PeerPhone != "5537933002826" {
		t.Fatalf("expected real peerPhone in history, got %q", rows[0].PeerPhone)
	}
}
