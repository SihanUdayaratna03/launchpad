package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var started = time.Date(2026, 11, 14, 10, 30, 0, 0, time.UTC)

func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func fullEnv() environment {
	return loadEnvironment(fakeEnv(map[string]string{
		"PR_NUMBER": "12",
		"NAMESPACE": "preview-pr-12",
		"POD_NAME":  "preview-pr-12-7d9f-xk2lp",
		"TTL":       "72h",
	}), started)
}

func get(t *testing.T, env environment, now time.Time, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	newHandler(env, func() time.Time { return now }).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func TestPageShowsEnvironmentDetails(t *testing.T) {
	commit = "abc1234def5678"
	t.Cleanup(func() { commit = "dev" })

	code, body := get(t, fullEnv(), started.Add(time.Hour), "/")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	for _, want := range []string{
		"#12", "abc1234", "preview-pr-12", "preview-pr-12-7d9f-xk2lp",
		"14 Nov 2026, 10:30 UTC", "17 Nov 2026, 10:30 UTC", "2d 23h", "Live",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	if strings.Contains(body, ">abc1234def5678<") {
		t.Error("commit should be shortened in the body")
	}
}

func TestCountdown(t *testing.T) {
	env := fullEnv()

	tests := []struct {
		name          string
		elapsed       time.Duration
		wantRemaining string
		wantPercent   int
		wantExpired   bool
	}{
		{"just created", 0, "3d 0h", 0, false},
		{"half way", 36 * time.Hour, "1d 12h", 50, false},
		{"last hours", 71 * time.Hour, "1h 0m", 98, false},
		{"last minutes", 72*time.Hour - 90*time.Second, "1m", 99, false},
		{"expired", 73 * time.Hour, "expired", 100, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := env.render(started.Add(tt.elapsed))
			if got.Remaining != tt.wantRemaining {
				t.Errorf("Remaining = %q, want %q", got.Remaining, tt.wantRemaining)
			}
			if got.Percent != tt.wantPercent {
				t.Errorf("Percent = %d, want %d", got.Percent, tt.wantPercent)
			}
			if got.Expired != tt.wantExpired {
				t.Errorf("Expired = %v, want %v", got.Expired, tt.wantExpired)
			}
		})
	}
}

func TestExpiredPageSaysSo(t *testing.T) {
	_, body := get(t, fullEnv(), started.Add(100*time.Hour), "/")
	if !strings.Contains(body, "Expiring") || strings.Contains(body, ">Live<") {
		t.Error("an expired environment should not be shown as live")
	}
}

func TestDefaultsWhenEnvIsEmpty(t *testing.T) {
	env := loadEnvironment(fakeEnv(nil), started)
	got := env.render(started)
	if got.PR != "?" || got.Namespace != "unknown" || got.Pod != "unknown" || got.TTL != "72h" || got.Commit != "dev" {
		t.Errorf("unexpected defaults: %+v", got)
	}
}

func TestInvalidTTLFallsBackTo72h(t *testing.T) {
	env := loadEnvironment(fakeEnv(map[string]string{"TTL": "soon"}), started)
	if got := env.render(started); got.TTL != "72h" || got.Remaining != "3d 0h" {
		t.Errorf("TTL = %q, Remaining = %q", got.TTL, got.Remaining)
	}
}

func TestEnvironmentValuesAreHTMLEscaped(t *testing.T) {
	env := loadEnvironment(fakeEnv(map[string]string{"PR_NUMBER": "<script>alert(1)</script>"}), started)
	if _, body := get(t, env, started, "/"); strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("PR number was rendered unescaped")
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler(fullEnv(), time.Now).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
		t.Errorf("CSP = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

func TestHealthzAndUnknownPath(t *testing.T) {
	env := fullEnv()

	if code, body := get(t, env, started, "/healthz"); code != http.StatusOK || body != "ok" {
		t.Errorf("/healthz = %d %q", code, body)
	}
	if code, _ := get(t, env, started, "/nope"); code != http.StatusNotFound {
		t.Errorf("/nope = %d, want 404", code)
	}
}
