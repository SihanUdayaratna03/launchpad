package main

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Webhook outcomes and preview actions are fixed sets. They are all exported
// at zero from the start so rate() and increase() have a series to work with
// before the first event.
var (
	webhookResults = []string{"accepted", "ignored", "bad_request", "unauthorized", "error"}
	previewActions = []string{"created", "updated", "destroyed"}
)

// metrics is a small, dependency-free Prometheus exporter. A nil *metrics is
// valid and records nothing, so tests that build a bare controller still work.
type metrics struct {
	mu              sync.Mutex
	webhooks        map[string]uint64
	events          map[string]uint64
	commentFailures uint64
	durationSum     float64
	durationCount   uint64
}

func newMetrics() *metrics {
	m := &metrics{webhooks: map[string]uint64{}, events: map[string]uint64{}}
	for _, r := range webhookResults {
		m.webhooks[r] = 0
	}
	for _, a := range previewActions {
		m.events[a] = 0
	}
	return m
}

func (m *metrics) webhook(result string, d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.webhooks[result]++
	m.durationSum += d.Seconds()
	m.durationCount++
}

func (m *metrics) event(action string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[action]++
}

func (m *metrics) commentFailed() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commentFailures++
}

func writeLabelled(w io.Writer, name, label string, values map[string]uint64) {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s{%s=%q} %d\n", name, label, k, values[k])
	}
}

// write renders the Prometheus text exposition format, version 0.0.4.
func (m *metrics) write(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()

	fmt.Fprintln(w, "# HELP launchpad_webhook_requests_total Webhook requests by outcome.")
	fmt.Fprintln(w, "# TYPE launchpad_webhook_requests_total counter")
	writeLabelled(w, "launchpad_webhook_requests_total", "result", m.webhooks)

	fmt.Fprintln(w, "# HELP launchpad_preview_events_total Preview environment changes made to the ApplicationSet.")
	fmt.Fprintln(w, "# TYPE launchpad_preview_events_total counter")
	writeLabelled(w, "launchpad_preview_events_total", "action", m.events)

	fmt.Fprintln(w, "# HELP launchpad_comment_failures_total Failed attempts to comment on a pull request.")
	fmt.Fprintln(w, "# TYPE launchpad_comment_failures_total counter")
	fmt.Fprintf(w, "launchpad_comment_failures_total %d\n", m.commentFailures)

	fmt.Fprintln(w, "# HELP launchpad_webhook_duration_seconds Time spent handling webhook requests.")
	fmt.Fprintln(w, "# TYPE launchpad_webhook_duration_seconds summary")
	fmt.Fprintf(w, "launchpad_webhook_duration_seconds_sum %g\n", m.durationSum)
	fmt.Fprintf(w, "launchpad_webhook_duration_seconds_count %d\n", m.durationCount)
}

func (m *metrics) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if m == nil {
		return
	}
	m.write(w)
}
