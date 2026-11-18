package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func render(m *metrics) string {
	var b bytes.Buffer
	m.write(&b)
	return b.String()
}

func TestMetricsStartAtZero(t *testing.T) {
	out := render(newMetrics())
	for _, want := range []string{
		`launchpad_webhook_requests_total{result="accepted"} 0`,
		`launchpad_webhook_requests_total{result="unauthorized"} 0`,
		`launchpad_preview_events_total{action="created"} 0`,
		`launchpad_preview_events_total{action="destroyed"} 0`,
		`launchpad_comment_failures_total 0`,
		`launchpad_webhook_duration_seconds_count 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMetricsCountEvents(t *testing.T) {
	m := newMetrics()
	m.webhook("accepted", 200*time.Millisecond)
	m.webhook("accepted", 300*time.Millisecond)
	m.webhook("error", 0)
	m.event("created")
	m.event("updated")
	m.event("updated")
	m.commentFailed()

	out := render(m)
	for _, want := range []string{
		`launchpad_webhook_requests_total{result="accepted"} 2`,
		`launchpad_webhook_requests_total{result="error"} 1`,
		`launchpad_preview_events_total{action="created"} 1`,
		`launchpad_preview_events_total{action="updated"} 2`,
		`launchpad_comment_failures_total 1`,
		`launchpad_webhook_duration_seconds_sum 0.5`,
		`launchpad_webhook_duration_seconds_count 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMetricsOutputIsStableAndTyped(t *testing.T) {
	m := newMetrics()
	if render(m) != render(m) {
		t.Error("output differs between two renders")
	}
	out := render(m)
	for _, name := range []string{
		"launchpad_webhook_requests_total", "launchpad_preview_events_total",
		"launchpad_comment_failures_total", "launchpad_webhook_duration_seconds",
	} {
		if !strings.Contains(out, "# HELP "+name+" ") || !strings.Contains(out, "# TYPE "+name+" ") {
			t.Errorf("%s has no HELP/TYPE lines", name)
		}
	}
}

func TestNilMetricsAreSafe(t *testing.T) {
	var m *metrics
	m.webhook("accepted", time.Second)
	m.event("created")
	m.commentFailed()

	rec := httptest.NewRecorder()
	m.handler(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestMetricsHandlerContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	newMetrics().handler(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Errorf("content type = %q", got)
	}
}

func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookResultsAreClassified(t *testing.T) {
	const secret = "s3cret"

	tests := []struct {
		name       string
		method     string
		body       string
		signature  string
		wantResult string
		wantCode   int
	}{
		{"wrong method", http.MethodGet, "", "", "bad_request", http.StatusMethodNotAllowed},
		{"bad signature", http.MethodPost, `{}`, "sha256=00", "unauthorized", http.StatusUnauthorized},
		{"bad payload", http.MethodPost, `not json`, sign(secret, `not json`), "bad_request", http.StatusBadRequest},
		{"invalid sha", http.MethodPost, `{"action":"opened","number":1,"pull_request":{"head":{"sha":"zzz"}}}`,
			sign(secret, `{"action":"opened","number":1,"pull_request":{"head":{"sha":"zzz"}}}`), "bad_request", http.StatusBadRequest},
		{"ignored action", http.MethodPost, `{"action":"labeled","number":1}`,
			sign(secret, `{"action":"labeled","number":1}`), "ignored", http.StatusAccepted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMetrics()
			c := &controller{secret: []byte(secret), metrics: m}

			req := httptest.NewRequest(tt.method, "/webhook", strings.NewReader(tt.body))
			req.Header.Set("X-Hub-Signature-256", tt.signature)
			rec := httptest.NewRecorder()
			c.handleWebhook(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			out := render(m)
			if want := `launchpad_webhook_requests_total{result="` + tt.wantResult + `"} 1`; !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
			if !strings.Contains(out, "launchpad_webhook_duration_seconds_count 1") {
				t.Errorf("duration was not recorded:\n%s", out)
			}
		})
	}
}
