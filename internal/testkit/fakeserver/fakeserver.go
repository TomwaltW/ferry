// Package fakeserver stands in for the local agent services the server driver
// talks to: `opencode serve` and `qwen serve`. The driver's own unit tests keep
// private fakes of the same shape inside internal/driver/server; these are the
// exported ones, so an end-to-end test can put a service behind a configured
// agent and drive the whole path from the fake gateway down to the service.
//
// Nothing here takes a *testing.T: a fixture is created, used and Closed, which
// is what lets the e2e package own the lifetime with t.Cleanup. The event
// payloads are written from the tables in internal/driver/server/eventmap.go,
// so a green run says the driver agrees with those tables — what a real service
// sends is what spec-p2 section 0 could not check.
package fakeserver

import (
	"encoding/json"
	"net/http"
	"time"
)

// stream is one open event-stream connection held by a fixture.
type stream struct {
	ch   chan string
	done chan struct{}
}

func newStream() *stream {
	return &stream{ch: make(chan string, 16), done: make(chan struct{})}
}

// send pushes one already framed payload, dropping it if the reader is gone.
func (s *stream) send(frame string) {
	select {
	case s.ch <- frame:
	case <-s.done:
	}
}

// pump writes frames to w until the stream is closed or the request ends.
// opening is written before the first flush, which is what pushes the response
// headers out to a client that would otherwise wait for them for ever.
func (s *stream) pump(w http.ResponseWriter, r *http.Request, opening string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	if opening != "" {
		_, _ = w.Write([]byte(opening))
	}
	flusher.Flush()

	for {
		select {
		case frame := <-s.ch:
			_, _ = w.Write([]byte(frame))
			flusher.Flush()
		case <-s.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// waitFor polls cond until it holds or the patience runs out, and reports
// whether it held. Both fixtures expose their waits through it so a test never
// has to sleep on a fixed delay.
func waitFor(patience time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(patience)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Patience is how long the Wait helpers wait before giving up.
const Patience = 5 * time.Second
