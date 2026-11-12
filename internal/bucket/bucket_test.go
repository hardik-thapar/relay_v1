package bucket

import (
	"testing"
	"time"
)

var t0 = time.Unix(0, 0)

func TestWaitRefillAndUntilFull(t *testing.T) {
	b := New(60, time.Minute, -1, t0) // 1 unit per second
	b.Charge(60, t0)
	if w := b.Wait(1, t0); w != time.Second {
		t.Errorf("wait = %v, want 1s", w)
	}
	if w := b.Wait(1, t0.Add(time.Second)); w != 0 {
		t.Errorf("after 1s wait = %v, want 0", w)
	}
	if got := b.UntilFull(t0.Add(time.Second)); got != 59*time.Second {
		t.Errorf("untilFull = %v", got)
	}
}

func TestStartLevelAndNegative(t *testing.T) {
	b := New(10, time.Second, 0, t0) // starts empty
	if b.Remaining(t0) != 0 {
		t.Error("start=0 should be empty")
	}
	b.Charge(5, t0) // overdraw: unknown output tokens arrived
	if w := b.Wait(1, t0); w != 600*time.Millisecond {
		t.Errorf("wait after overdraw = %v, want 600ms", w)
	}
}

func TestRefundCapsAtCapacity(t *testing.T) {
	b := New(10, time.Second, -1, t0)
	b.Charge(-100, t0)
	if b.Remaining(t0) != 10 {
		t.Errorf("refund overfilled: %d", b.Remaining(t0))
	}
}

func TestLowerToNeverRaises(t *testing.T) {
	b := New(100, time.Minute, -1, t0)
	b.LowerTo(40, t0)
	b.LowerTo(90, t0)
	if b.Remaining(t0) != 40 {
		t.Errorf("remaining = %d, want 40", b.Remaining(t0))
	}
}

func TestWaitRoundsUp(t *testing.T) {
	// 1000 / (8000/60) is 7.4999999… in floats; sleeping that long would
	// wake just before the units exist.
	b := New(8000, time.Minute, -1, t0)
	b.Charge(5000, t0)
	w := b.Wait(4000, t0)
	if w != 7500*time.Millisecond {
		t.Fatalf("wait = %v, want 7.5s", w)
	}
	if b.Wait(4000, t0.Add(w)) != 0 {
		t.Error("units must be there after sleeping the returned wait")
	}
}

func TestTooLargeNeverFits(t *testing.T) {
	b := New(10, time.Second, -1, t0)
	if b.Wait(11, t0) != Never {
		t.Error("request larger than capacity must never fit")
	}
}

func TestNilNeverLimits(t *testing.T) {
	var b *Bucket
	if b.Wait(1e9, t0) != 0 {
		t.Error("nil bucket must never limit")
	}
	b.Charge(1, t0)
	b.LowerTo(0, t0)
}
