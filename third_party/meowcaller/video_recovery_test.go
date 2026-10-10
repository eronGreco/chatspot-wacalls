package meowcaller

import (
	"errors"
	"github.com/purpshell/meowcaller/rtp"
	"sync"
	"testing"
	"time"
)

func TestVideoRecoveryRepeatsUntilIDR(t *testing.T) {
	n := 0
	r := newVideoRecovery(func(uint32) error { n++; return nil })
	now := time.Unix(100, 0)
	r.observe(7, now, true, true, false)
	r.observe(7, now, false, false, true)
	r.tick(now)
	r.tick(now.Add(500 * time.Millisecond))
	r.tick(now.Add(time.Second))
	if n != 2 {
		t.Fatalf("attempts=%d", n)
	}
	r.observe(7, now.Add(1100*time.Millisecond), true, false, false)
	r.tick(now.Add(2 * time.Second))
	if n != 3 {
		t.Fatal("delta must not stop retries")
	}
	r.observe(7, now.Add(2100*time.Millisecond), true, true, false)
	r.tick(now.Add(3 * time.Second))
	if n != 3 {
		t.Fatal("IDR must stop retries")
	}
}
func TestVideoRecoveryBrowserRequestAndStall(t *testing.T) {
	n := 0
	r := newVideoRecovery(func(uint32) error { n++; return nil })
	now := time.Unix(100, 0)
	r.request()
	r.tick(now)
	if n != 0 {
		t.Fatal("unauthenticated stream")
	}
	r.observe(1, now, true, true, false)
	r.request()
	r.tick(now)
	if n != 1 {
		t.Fatal("browser request missing")
	}
	r.observe(1, now.Add(time.Second), true, true, false)
	r.tick(now.Add(2 * time.Second))
	if n != 1 {
		t.Fatal("recovered stream")
	}
	r.tick(now.Add(4 * time.Second))
	if n != 2 {
		t.Fatal("stall not detected")
	}
	r.forget(1)
	r.tick(now.Add(6 * time.Second))
	if n != 2 {
		t.Fatal("removed stream")
	}
}
func TestVideoRecoveryFailedSendDoesNotStopRetries(t *testing.T) {
	n := 0
	r := newVideoRecovery(func(uint32) error { n++; return errors.New("transport") })
	now := time.Unix(100, 0)
	r.observe(1, now, false, false, false)
	r.tick(now)
	r.tick(now.Add(time.Second))
	if n != 2 {
		t.Fatalf("attempts=%d", n)
	}
}
func TestVideoRecoveryAssemblerGapRetriesAndResumes(t *testing.T) {
	var a rtp.H264AccessUnitAssembler
	n := 0
	r := newVideoRecovery(func(uint32) error { n++; return nil })
	now := time.Unix(100, 0)
	push := func(seq uint16, payload []byte) bool {
		frame, complete, damaged := a.Push(seq, true, payload)
		r.observe(1, now, complete, complete && rtp.AUHasIDR(frame), damaged)
		return complete
	}
	if !push(1, []byte{0x65, 1}) {
		t.Fatal("initial IDR")
	}
	if push(3, []byte{0x41, 2}) {
		t.Fatal("damaged delta")
	}
	r.tick(now)
	r.tick(now.Add(time.Second))
	r.tick(now.Add(2 * time.Second))
	if n != 3 {
		t.Fatalf("attempts=%d", n)
	}
	if !push(4, []byte{0x65, 3}) {
		t.Fatal("new IDR")
	}
	r.tick(now.Add(2500 * time.Millisecond))
	if n != 3 {
		t.Fatal("recovery must stop requests")
	}
}
func TestVideoRecoveryConcurrentCallbacks(t *testing.T) {
	var mu sync.Mutex
	n := 0
	r := newVideoRecovery(func(uint32) error { mu.Lock(); n++; mu.Unlock(); return nil })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				now := time.Unix(int64(j+100), 0)
				r.observe(1, now, true, j%4 == 0, false)
				r.request()
				r.tick(now)
			}
		}()
	}
	wg.Wait()
}
