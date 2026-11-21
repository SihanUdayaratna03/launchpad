package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeGitHub struct {
	mu       sync.Mutex
	comments []issueComment
	requests []string
	auth     string
	lastBody string
	failList bool
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.auth = r.Header.Get("Authorization")

	switch {
	case r.Method == http.MethodGet:
		if f.failList {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(f.comments)
	case r.Method == http.MethodPost || r.Method == http.MethodPatch:
		data, _ := io.ReadAll(r.Body)
		var in map[string]string
		json.Unmarshal(data, &in)
		f.lastBody = in["body"]
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("{}"))
	}
}

func newTestCommenter(f *fakeGitHub) (*commenter, func()) {
	srv := httptest.NewServer(f)
	return &commenter{token: "tok", baseURL: srv.URL, http: srv.Client()}, srv.Close
}

func TestCommentCreatedWhenNoneExists(t *testing.T) {
	f := &fakeGitHub{comments: []issueComment{{ID: 1, Body: "looks good"}}}
	c, stop := newTestCommenter(f)
	defer stop()

	if err := c.upsert(context.Background(), "o/r", 12, readyURLBody(previewURL("pr-12.x.nip.io", ""), shaA), true); err != nil {
		t.Fatal(err)
	}
	last := f.requests[len(f.requests)-1]
	if last != "POST /repos/o/r/issues/12/comments" {
		t.Errorf("last request = %q, want a POST", last)
	}
	if !strings.Contains(f.lastBody, "http://pr-12.x.nip.io") || !strings.Contains(f.lastBody, shaA[:7]) {
		t.Errorf("body missing url or commit: %q", f.lastBody)
	}
	if f.auth != "Bearer tok" {
		t.Errorf("auth header = %q", f.auth)
	}
}

func TestCommentEditedWhenOursExists(t *testing.T) {
	f := &fakeGitHub{comments: []issueComment{
		{ID: 1, Body: "looks good"},
		{ID: 2, Body: deployingBody(shaA)},
	}}
	c, stop := newTestCommenter(f)
	defer stop()

	if err := c.upsert(context.Background(), "o/r", 12, readyURLBody("h", shaB), true); err != nil {
		t.Fatal(err)
	}
	if last := f.requests[len(f.requests)-1]; last != "PATCH /repos/o/r/issues/comments/2" {
		t.Errorf("last request = %q, want PATCH of comment 2", last)
	}
}

func TestCommentMarkerMustBeAtStart(t *testing.T) {
	f := &fakeGitHub{comments: []issueComment{{ID: 1, Body: "quoting " + commentMarker}}}
	c, stop := newTestCommenter(f)
	defer stop()

	c.upsert(context.Background(), "o/r", 12, "x", true)
	if last := f.requests[len(f.requests)-1]; !strings.HasPrefix(last, "POST") {
		t.Errorf("someone else's comment was edited: %q", last)
	}
}

func TestCloseNeverCreatesComment(t *testing.T) {
	f := &fakeGitHub{}
	c, stop := newTestCommenter(f)
	defer stop()

	if err := c.upsert(context.Background(), "o/r", 12, removedBody(), false); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.requests {
		if strings.HasPrefix(r, "POST") {
			t.Errorf("created a comment on close: %v", f.requests)
		}
	}
}

func TestCloseEditsExistingComment(t *testing.T) {
	f := &fakeGitHub{comments: []issueComment{{ID: 5, Body: readyURLBody("h", shaA)}}}
	c, stop := newTestCommenter(f)
	defer stop()

	c.upsert(context.Background(), "o/r", 12, removedBody(), false)
	if last := f.requests[len(f.requests)-1]; last != "PATCH /repos/o/r/issues/comments/5" {
		t.Errorf("last request = %q", last)
	}
	if !strings.Contains(f.lastBody, "destroyed") {
		t.Errorf("body = %q", f.lastBody)
	}
}

func TestCommentAPIErrorIsReturned(t *testing.T) {
	f := &fakeGitHub{failList: true}
	c, stop := newTestCommenter(f)
	defer stop()

	if err := c.upsert(context.Background(), "o/r", 12, "x", true); err == nil {
		t.Error("expected an error when GitHub fails")
	}
}

func TestInvalidRepoNeverCallsGitHub(t *testing.T) {
	f := &fakeGitHub{}
	c, stop := newTestCommenter(f)
	defer stop()

	for _, repo := range []string{"", "o", "o/r/extra", "../x/y", "o/r?x=1", "o/ r"} {
		if err := c.upsert(context.Background(), repo, 1, "x", true); err == nil {
			t.Errorf("repo %q accepted", repo)
		}
	}
	if len(f.requests) != 0 {
		t.Errorf("GitHub was called: %v", f.requests)
	}
}

func TestNewCommenterWithoutTokenIsNil(t *testing.T) {
	if newCommenter("") != nil {
		t.Error("no token must disable commenting")
	}
	if newCommenter("t") == nil {
		t.Error("token must enable commenting")
	}
}

func TestReadyBodyStartsWithMarker(t *testing.T) {
	for _, b := range []string{readyURLBody("h", shaA), deployingBody(shaA), removedBody()} {
		if !strings.HasPrefix(b, commentMarker) {
			t.Errorf("body does not start with the marker: %q", b)
		}
	}
}

func TestReadyBodyCarriesTheScheme(t *testing.T) {
	for _, tt := range []struct{ resolver, want string }{
		{"", "http://pr-12.x.nip.io"},
		{"le", "https://pr-12.x.nip.io"},
	} {
		body := readyURLBody(previewURL("pr-12.x.nip.io", tt.resolver), shaA)
		if !strings.Contains(body, tt.want) {
			t.Errorf("resolver %q: body %q does not contain %q", tt.resolver, body, tt.want)
		}
	}
}
