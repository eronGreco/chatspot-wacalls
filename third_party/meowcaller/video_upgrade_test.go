package meowcaller

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	waBinary "github.com/polymorfa/hypermeow/binary"
	"github.com/rs/zerolog"
)

func videoUpgradeFixture(t *testing.T) (*engine, *Call, *[]waBinary.Node) {
	t.Helper()
	var sent []waBinary.Node
	e := &engine{c: &Client{log: zerolog.Nop()}, calls: map[string]*engineCall{"c": {videoTx: &videoSender{}}}}
	e.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		sent = append(sent, node.GetChildren()[0])
		return nil
	}
	c := &Call{eng: e, id: "c"}
	e.calls["c"].call = c
	return e, c, &sent
}

func peerVideoState(e *engine, state int) {
	e.onVideoStanza(&waBinary.Node{Tag: "video", Attrs: waBinary.Attrs{"call-id": "c", "state": strconv.Itoa(state)}})
}

func assertVideoStates(t *testing.T, sent []waBinary.Node, want ...int) {
	t.Helper()
	got := make([]int, 0, len(sent))
	for _, node := range sent {
		ag := node.AttrGetter()
		state, _ := strconv.Atoi(ag.String("state"))
		got = append(got, state)
		if state == 1 && ag.OptionalString("dec") != "H264" {
			t.Fatal("enabled camera did not advertise H264")
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sent video states %v, want %v", got, want)
	}
}

func assertVideoSender(t *testing.T, e *engine, active, gated bool) {
	t.Helper()
	sender := e.calls["c"].videoTx
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.active != active || sender.sendGated != gated {
		t.Fatalf("sender active=%t gated=%t, want %t/%t", sender.active, sender.sendGated, active, gated)
	}
}

func TestCustomerStartsVideoAcceptanceEnablesBothDirections(t *testing.T) {
	e, c, sent := videoUpgradeFixture(t)
	peerVideoState(e, 11)
	if err := c.AcceptVideoWithCamera(true); err != nil {
		t.Fatal(err)
	}
	assertVideoStates(t, *sent, 4, 1) // No crossed state-11 request.
	assertVideoSender(t, e, true, false)
	peerVideoState(e, 1)
	local, remote := c.VideoActivity()
	if !local || !remote {
		t.Fatal("accepted customer upgrade did not enable both directions")
	}
}

func TestAgentStartsVideoThenAcceptsReciprocalCustomerCamera(t *testing.T) {
	e, c, sent := videoUpgradeFixture(t)
	if err := c.StartVideo(); err != nil {
		t.Fatal(err)
	}
	assertVideoSender(t, e, true, true)
	peerVideoState(e, 4)
	assertVideoSender(t, e, true, false)
	peerVideoState(e, 11)
	if err := c.AcceptVideoWithCamera(true); err != nil {
		t.Fatal(err)
	}
	peerVideoState(e, 1)
	assertVideoStates(t, *sent, 11, 1, 4, 1)
	local, remote := c.VideoActivity()
	if !local || !remote {
		t.Fatal("reciprocal customer camera was not accepted")
	}
}

func TestReceiveOnlyThenCameraStartDoesNotRequestAnotherUpgrade(t *testing.T) {
	e, c, sent := videoUpgradeFixture(t)
	peerVideoState(e, 3)
	if err := c.AcceptVideoWithCamera(false); err != nil {
		t.Fatal(err)
	}
	assertVideoSender(t, e, false, false)
	local, remote := c.VideoActivity()
	if local || remote {
		t.Fatal("receive-only acceptance fabricated active cameras")
	}
	// Enabling our camera can precede the peer's camera-on announcement.
	if err := c.StartVideo(); err != nil {
		t.Fatal(err)
	}
	assertVideoStates(t, *sent, 6, 4, 1)
	assertVideoSender(t, e, true, false)
	peerVideoState(e, 1)
	if err := c.StopVideo(); err != nil {
		t.Fatal(err)
	}
	peerVideoState(e, 4) // Delayed acknowledgement must not restart it.
	assertVideoSender(t, e, false, false)
	local, remote = c.VideoActivity()
	if local || !remote {
		t.Fatal("camera stop lost receive-only video")
	}
	if err := c.StartVideo(); err != nil {
		t.Fatal(err)
	}
	assertVideoStates(t, *sent, 6, 4, 1, 6, 1)
}

func TestCameraAcceptanceRequiresPendingRequestAndRecoversFailures(t *testing.T) {
	for _, failState := range []int{4, 1} {
		t.Run(strconv.Itoa(failState), func(t *testing.T) {
			e, c, sent := videoUpgradeFixture(t)
			if err := c.AcceptVideoWithCamera(true); err == nil {
				t.Fatal("camera enabled without pending customer request")
			}
			assertVideoSender(t, e, false, false)
			peerVideoState(e, 11)
			e.sendCallNode = func(_ context.Context, node waBinary.Node) error {
				ag := node.GetChildren()[0].AttrGetter()
				if ag.String("state") == strconv.Itoa(failState) {
					return errors.New("transport unavailable")
				}
				return nil
			}
			if err := c.AcceptVideoWithCamera(true); err == nil {
				t.Fatal("missing signaling error")
			}
			assertVideoSender(t, e, false, false)
			m := e.calls["c"]
			if m.localVideo || m.peerVideoUpgrade != (failState == 4) || m.videoNegotiated != (failState == 1) {
				t.Fatal("failed acceptance left unsafe or unrecoverable state")
			}
			e.sendCallNode = func(_ context.Context, node waBinary.Node) error {
				*sent = append(*sent, node.GetChildren()[0])
				return nil
			}
			if failState == 4 {
				if err := c.AcceptVideoWithCamera(true); err != nil {
					t.Fatal(err)
				}
				assertVideoStates(t, *sent, 4, 1)
			} else {
				if err := c.StartVideo(); err != nil {
					t.Fatal(err)
				}
				assertVideoStates(t, *sent, 1)
			}
			assertVideoSender(t, e, true, false)
		})
	}
}

func TestStopDuringCameraAcceptanceCannotBeUndoneByLateSignaling(t *testing.T) {
	for _, blockedState := range []int{4, 1} {
		t.Run(strconv.Itoa(blockedState), func(t *testing.T) {
			e, c, _ := videoUpgradeFixture(t)
			peerVideoState(e, 11)
			entered, release := make(chan struct{}), make(chan struct{})
			e.sendCallNode = func(_ context.Context, node waBinary.Node) error {
				ag := node.GetChildren()[0].AttrGetter()
				if ag.String("state") == strconv.Itoa(blockedState) {
					if blockedState == 1 {
						assertVideoSender(t, e, true, true)
					}
					close(entered)
					<-release
				}
				return nil
			}
			done := make(chan error, 1)
			go func() { done <- c.AcceptVideoWithCamera(true) }()
			<-entered
			if err := c.StopVideo(); err != nil {
				t.Fatal(err)
			}
			close(release)
			<-done
			assertVideoSender(t, e, false, false)
			peerVideoState(e, 4)
			local, _ := c.VideoActivity()
			if local {
				t.Fatal("late signaling reopened a stopped camera")
			}
		})
	}
}

func TestStopWhileAnnouncingAcceptedAgentCameraRemainsStopped(t *testing.T) {
	e, c, _ := videoUpgradeFixture(t)
	if err := c.StartVideo(); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e.sendCallNode = func(_ context.Context, node waBinary.Node) error {
		ag := node.GetChildren()[0].AttrGetter()
		if ag.String("state") == "1" {
			close(entered)
			<-release
		}
		return nil
	}
	go func() { peerVideoState(e, 4); close(done) }()
	<-entered
	if err := c.StopVideo(); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	assertVideoSender(t, e, false, false)
	local, _ := c.VideoActivity()
	if local {
		t.Fatal("late camera-on announcement undid StopVideo")
	}
}
