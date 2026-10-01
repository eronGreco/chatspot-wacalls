package meowcaller

import (
	"context"
	"errors"
	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/rs/zerolog"
	"strconv"
	"testing"
)

func TestVideoActivityExcludesPendingAndKeepsRemote(t *testing.T) {
	e := &engine{calls: map[string]*engineCall{"c": {localVideo: true, videoGate: true}}}
	c := &Call{eng: e, id: "c"}
	local, remote := c.VideoActivity()
	if local || remote {
		t.Fatal("pending upgrade counted as video")
	}
	e.calls["c"].remoteVideo = true
	local, remote = c.VideoActivity()
	if local || !remote {
		t.Fatal("remote activity lost during pending local upgrade")
	}
	e.calls["c"].videoGate = false
	local, remote = c.VideoActivity()
	if !local || !remote {
		t.Fatal("accepted video directions lost")
	}
	e.calls["c"].localVideo = false
	local, remote = c.VideoActivity()
	if local || !remote {
		t.Fatal("local camera off disabled remote")
	}
	e.calls["c"].remoteVideo = false
	local, remote = c.VideoActivity()
	if local || remote {
		t.Fatal("both cameras off did not return to audio")
	}
}

func TestVideoActivityFollowsActualUpgradeStanzas(t *testing.T) {
	e := &engine{c: &Client{log: zerolog.Nop()}, calls: map[string]*engineCall{"c": {}}, sendCallNode: func(context.Context, waBinary.Node) error { return nil }}
	c := &Call{eng: e, id: "c"}
	e.calls["c"].call = c
	send := func(state int) {
		e.onVideoStanza(&waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{"call-id": "c", "state": strconv.Itoa(state)}})
	}
	assert := func(localWant, remoteWant bool) {
		t.Helper()
		local, remote := c.VideoActivity()
		if local != localWant || remote != remoteWant {
			t.Fatalf("video state=(%t,%t), want (%t,%t)", local, remote, localWant, remoteWant)
		}
	}
	send(11)
	assert(false, false) // remote upgrade request alone is not video
	send(5)
	assert(false, false) // rejected remains audio
	e.calls["c"].localVideo = true
	e.calls["c"].videoGate = true
	assert(false, false)
	send(4)
	assert(true, false) // accepted local upgrade
	send(1)
	assert(true, true) // remote camera enabled
	send(6)
	assert(true, false) // stopping remote keeps local video
	e.calls["c"].localVideo = false
	assert(false, false)
	send(1)
	assert(false, true) // remote starts again, receive-only video
	send(0)
	assert(false, false)
}

func TestRejectVideoPreservesCamerasAndPendingOnSendFailure(t *testing.T) {
	e := &engine{c: &Client{log: zerolog.Nop()}, calls: map[string]*engineCall{"c": {localVideo: true, remoteVideo: true, peerVideoUpgrade: true}}, sendCallNode: func(context.Context, waBinary.Node) error { return nil }}
	c := &Call{eng: e, id: "c"}
	e.calls["c"].call = c
	if err := c.RejectVideo(); err != nil {
		t.Fatal(err)
	}
	local, remote := c.VideoActivity()
	if !local || !remote || e.calls["c"].peerVideoUpgrade {
		t.Fatal("reject changed active cameras or retained pending request")
	}
	if err := c.RejectVideo(); err == nil {
		t.Fatal("accepted duplicate rejection without a pending request")
	}
	e.calls["c"].peerVideoUpgrade = true
	e.sendCallNode = func(context.Context, waBinary.Node) error { return errors.New("transport unavailable") }
	if err := c.RejectVideo(); err == nil {
		t.Fatal("missing transport failure")
	}
	local, remote = c.VideoActivity()
	if !local || !remote || !e.calls["c"].peerVideoUpgrade {
		t.Fatal("failed reject did not preserve cameras and recover pending request")
	}
}

func TestReceiveOnlyVideoAcceptanceDoesNotEnableLocalCamera(t *testing.T) {
	e := &engine{c: &Client{log: zerolog.Nop()}, calls: map[string]*engineCall{"c": {peerVideoUpgrade: true}}, sendCallNode: func(context.Context, waBinary.Node) error { return nil }}
	c := &Call{eng: e, id: "c"}
	e.calls["c"].call = c
	if err := c.AcceptVideo(); err != nil {
		t.Fatal(err)
	}
	local, remote := c.VideoActivity()
	if local || remote {
		t.Fatal("accept fabricated active camera before peer enabled")
	}
	e.onVideoStanza(&waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{"call-id": "c", "state": "1"}})
	local, remote = c.VideoActivity()
	if local || !remote {
		t.Fatal("receive-only direction not preserved")
	}
}
