package traffic

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"relay/internal/bucket"
)

// Level is a rung of the degradation ladder (DESIGN §5). Each rung gives
// less; none gives something wrong.
type Level int

const (
	L0Full          Level = iota // full pipeline
	L1Lite                       // fewer chunks, no redraft (verify failure => refuse)
	L2Busy                       // "busy, try again in N seconds", N from the limiter
	L3RetrievalOnly              // daily budget gone: cited passages, no model call
)

func (l Level) String() string { return [...]string{"L0", "L1", "L2", "L3"}[l] }

var (
	ErrQueueFull = errors.New("queue full")
	ErrTooLarge  = errors.New("request larger than the token limit")
)

// AdmissionConfig holds the knobs the burst and overload runs tune.
type AdmissionConfig struct {
	MaxQueue int           // waiting requests beyond this get L2 at once
	Service  time.Duration // expected model time once a call starts
}

// Decision is what admission chose for one arriving request.
type Decision struct {
	Level    Level
	RetryIn  time.Duration // L2 only: when to try again, from the limiter's view
	Wait     time.Duration // estimated queue wait at arrival
	QueueLen int           // requests waiting at arrival (results.csv)
	Reason   string
}

// Admission is one FIFO queue in front of the limiter, plus the ladder.
type Admission struct {
	cfg AdmissionConfig
	lim *Limiter
	now func() time.Time

	mu           sync.Mutex
	queue        *list.List // of *waiter, front = next to run
	queuedTokens int        // tokens of everyone waiting, for estimates

	kick chan struct{} // wakes the dispatcher when the queue changes
	stop chan struct{}
}

type waiter struct {
	tokens int
	ready  chan *Reservation // buffered 1; nil reservation = can never fit
	elem   *list.Element     // nil once the waiter has left the queue
}

// NewAdmission starts the dispatcher; call Close when done.
func NewAdmission(lim *Limiter, cfg AdmissionConfig, now func() time.Time) *Admission {
	if now == nil {
		now = time.Now
	}
	a := &Admission{cfg: cfg, lim: lim, now: now, queue: list.New(),
		kick: make(chan struct{}, 1), stop: make(chan struct{})}
	go a.dispatch()
	return a
}

func (a *Admission) Close() { close(a.stop) }

// Decide picks the rung for a request arriving now that must finish by
// deadline. fullTokens/liteTokens are its L0 and L1 estimates.
//
// The wait estimate is the limiter's: how long until this request AND
// everyone queued ahead of it fit in the buckets. It then takes the best
// rung that still finishes in time; it does not queue a request that
// would only time out in the queue.
func (a *Admission) Decide(fullTokens, liteTokens int, deadline time.Time) Decision {
	a.mu.Lock()
	n, ahead := a.queue.Len(), a.queuedTokens
	a.mu.Unlock()

	left := deadline.Sub(a.now())
	fits := func(wait time.Duration) bool { return wait+a.cfg.Service <= left }

	waitLite, why := a.lim.Wait(n+1, ahead+liteTokens)
	d := Decision{QueueLen: n, Wait: waitLite}
	switch {
	case !a.lim.Fits(liteTokens):
		d.Level, d.Reason = L3RetrievalOnly, "request can never fit the token limit"
		return d
	case why == ByDay && !fits(waitLite):
		d.Level, d.Reason = L3RetrievalOnly, "daily budget exhausted"
		return d
	case n >= a.cfg.MaxQueue:
		d.Level, d.RetryIn, d.Reason = L2Busy, waitLite, "queue full"
		return d
	}

	if waitFull, _ := a.lim.Wait(n+1, ahead+fullTokens); a.lim.Fits(fullTokens) && fits(waitFull) {
		d.Level, d.Wait, d.Reason = L0Full, waitFull, "fits"
		return d
	}
	if fits(waitLite) {
		d.Level, d.Reason = L1Lite, "only the lighter answer fits in time"
		return d
	}
	d.Level, d.RetryIn, d.Reason = L2Busy, waitLite, fmt.Sprintf("would wait %s (%s limit)", waitLite.Round(time.Millisecond), why)
	return d
}

// Acquire joins the back of the queue and returns once the limiter has
// reserved room for `tokens`. If ctx ends first (the person gave up) the
// request leaves the queue at once, so it never blocks anyone behind it.
func (a *Admission) Acquire(ctx context.Context, tokens int) (*Reservation, error) {
	w := &waiter{tokens: tokens, ready: make(chan *Reservation, 1)}
	a.mu.Lock()
	if a.queue.Len() >= a.cfg.MaxQueue {
		a.mu.Unlock()
		return nil, ErrQueueFull
	}
	w.elem = a.queue.PushBack(w)
	a.queuedTokens += tokens
	a.mu.Unlock()
	a.wake()

	select {
	case r := <-w.ready:
		if r == nil {
			return nil, ErrTooLarge
		}
		return r, nil
	case <-ctx.Done():
		a.mu.Lock()
		if w.elem != nil {
			a.leave(w)
		}
		a.mu.Unlock()
		// Granted in the same instant we gave up: hand the room back.
		select {
		case r := <-w.ready:
			if r != nil {
				r.Cancel()
			}
		default:
		}
		a.wake()
		return nil, ctx.Err()
	}
}

// QueueLen is the number of requests waiting.
func (a *Admission) QueueLen() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.queue.Len()
}

// dispatch serves the front of the queue: reserve if the limiter allows,
// otherwise sleep until it would, or until the queue changes.
func (a *Admission) dispatch() {
	for {
		a.mu.Lock()
		front := a.queue.Front()
		if front == nil {
			a.mu.Unlock()
			select {
			case <-a.kick:
				continue
			case <-a.stop:
				return
			}
		}
		w := front.Value.(*waiter)
		r, wait, _ := a.lim.TryReserve(w.tokens)
		if r != nil || wait == bucket.Never {
			a.leave(w)
			w.ready <- r // nil when it can never fit
			a.mu.Unlock()
			continue
		}
		a.mu.Unlock()

		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-a.kick:
			t.Stop()
		case <-a.stop:
			t.Stop()
			return
		}
	}
}

// leave removes w from the queue. Caller holds a.mu.
func (a *Admission) leave(w *waiter) {
	a.queue.Remove(w.elem)
	a.queuedTokens -= w.tokens
	w.elem = nil
}

// wake nudges the dispatcher without blocking.
func (a *Admission) wake() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}
