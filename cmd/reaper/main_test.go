package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeGitHub replaces the default HTTP transport for one test and counts calls.
func fakeGitHub(t *testing.T, status int, body string, err error) *int {
	t.Helper()
	calls := 0
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	return &calls
}

func TestReasonWithoutGitHub(t *testing.T) {
	r := &reaper{ttl: 72 * time.Hour}

	tests := []struct {
		name     string
		age      time.Duration
		inAppSet bool
		want     string
	}{
		{"no ApplicationSet entry is orphaned", time.Hour, false, "orphaned: no ApplicationSet entry"},
		{"orphan wins over expired ttl", 100 * time.Hour, false, "orphaned: no ApplicationSet entry"},
		{"older than ttl", 73 * time.Hour, true, "ttl expired: age 73h0m0s exceeds 72h0m0s"},
		{"exactly at ttl is kept", 72 * time.Hour, true, ""},
		{"fresh environment is kept", time.Hour, true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.reason(context.Background(), 12, tt.age, tt.inAppSet); got != tt.want {
				t.Errorf("reason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReasonChecksGitHub(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		err    error
		want   string
	}{
		{"closed PR", 200, `{"state":"closed"}`, nil, "pull request is closed"},
		{"open PR is kept", 200, `{"state":"open"}`, nil, ""},
		{"deleted PR (404) counts as closed", 404, ``, nil, "pull request is closed"},
		{"server error keeps the environment", 500, ``, nil, ""},
		{"rate limit keeps the environment", 403, ``, nil, ""},
		{"network error keeps the environment", 0, ``, errors.New("connection refused"), ""},
		{"garbage response keeps the environment", 200, `not json`, nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeGitHub(t, tt.status, tt.body, tt.err)
			r := &reaper{ttl: 72 * time.Hour, repo: "owner/repo"}
			if got := r.reason(context.Background(), 12, time.Hour, true); got != tt.want {
				t.Errorf("reason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReasonSkipsGitHubWhenAlreadyDecided(t *testing.T) {
	calls := fakeGitHub(t, 200, `{"state":"open"}`, nil)
	r := &reaper{ttl: 72 * time.Hour, repo: "owner/repo"}

	r.reason(context.Background(), 12, time.Hour, false)
	r.reason(context.Background(), 12, 100*time.Hour, true)

	if *calls != 0 {
		t.Errorf("GitHub was called %d times, want 0", *calls)
	}
}

func TestReasonSendsTokenWhenSet(t *testing.T) {
	var auth string
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		auth = req.Header.Get("Authorization")
		return &http.Response{StatusCode: 200, Status: "OK", Body: io.NopCloser(strings.NewReader(`{"state":"open"}`))}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = old })

	r := &reaper{ttl: 72 * time.Hour, repo: "owner/repo", token: "abc"}
	r.reason(context.Background(), 12, time.Hour, true)

	if auth != "Bearer abc" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer abc")
	}
}
