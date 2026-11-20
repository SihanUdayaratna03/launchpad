package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeOC is an in-memory set of Projects that records what was deleted.
type fakeOC struct {
	projects []ocProject
	deleted  []string
	listErr  error
	delErr   error
}

func (f *fakeOC) list(context.Context, string) ([]ocProject, error) {
	return f.projects, f.listErr
}

func (f *fakeOC) delete(_ context.Context, _, name string) error {
	if f.delErr != nil {
		return f.delErr
	}
	f.deleted = append(f.deleted, name)
	return nil
}

func aged(name string, age time.Duration) ocProject {
	return ocProject{name: name, created: time.Now().Add(-age)}
}

// ocRun sweeps once over projects and returns what the reaper deleted.
func ocRun(t *testing.T, f *fakeOC, dryRun bool) []string {
	t.Helper()
	r := &reaper{ttl: 72 * time.Hour, dryRun: dryRun, oc: f, ocNamespace: "default"}
	if err := r.runOpenChoreo(context.Background()); err != nil {
		t.Fatalf("runOpenChoreo() = %v", err)
	}
	return f.deleted
}

func TestOCReaperDeletesExpiredProjectsOnly(t *testing.T) {
	f := &fakeOC{projects: []ocProject{
		aged("pr-1", 100*time.Hour),
		aged("pr-2", time.Hour),
		aged("pr-3", 73*time.Hour),
	}}

	got := ocRun(t, f, false)
	if len(got) != 2 || got[0] != "pr-1" || got[1] != "pr-3" {
		t.Errorf("deleted %v, want [pr-1 pr-3]", got)
	}
}

// The reaper shares a cluster with OpenChoreo's own Projects and with anything
// a person created. Only pr-<number> is the controller's.
func TestOCReaperLeavesForeignProjectsAlone(t *testing.T) {
	f := &fakeOC{projects: []ocProject{
		aged("default", 1000*time.Hour),
		aged("my-app", 1000*time.Hour),
		aged("pr-abc", 1000*time.Hour),
		aged("pr-", 1000*time.Hour),
		aged("staging-pr-7", 1000*time.Hour),
		aged("pr-7-extra", 1000*time.Hour),
		aged("pr-7", 1000*time.Hour),
	}}

	got := ocRun(t, f, false)
	if len(got) != 1 || got[0] != "pr-7" {
		t.Errorf("deleted %v, want [pr-7]", got)
	}
}

func TestOCReaperDryRunDeletesNothing(t *testing.T) {
	f := &fakeOC{projects: []ocProject{aged("pr-1", 100*time.Hour)}}
	if got := ocRun(t, f, true); len(got) != 0 {
		t.Errorf("dry run deleted %v, want nothing", got)
	}
}

func TestOCReaperKeepsFreshProjects(t *testing.T) {
	f := &fakeOC{projects: []ocProject{aged("pr-1", time.Hour), aged("pr-2", 71*time.Hour)}}
	if got := ocRun(t, f, false); len(got) != 0 {
		t.Errorf("deleted %v, want nothing", got)
	}
}

// A Project whose delete fails must not stop the sweep: the next one still goes.
func TestOCReaperCarriesOnAfterAFailedDelete(t *testing.T) {
	f := &fakeOC{
		projects: []ocProject{aged("pr-1", 100*time.Hour), aged("pr-2", 100*time.Hour)},
		delErr:   errors.New("conflict"),
	}
	r := &reaper{ttl: 72 * time.Hour, oc: f, ocNamespace: "default"}
	if err := r.runOpenChoreo(context.Background()); err != nil {
		t.Errorf("a failed delete ended the sweep: %v", err)
	}
}

func TestOCReaperReportsAListFailure(t *testing.T) {
	f := &fakeOC{listErr: errors.New("forbidden")}
	r := &reaper{ttl: 72 * time.Hour, oc: f, ocNamespace: "default"}
	if err := r.runOpenChoreo(context.Background()); err == nil {
		t.Error("a failed list was reported as success")
	}
}

// The closed-pull-request rule is the Argo CD mode's, reused unchanged.
func TestOCReaperDeletesClosedPullRequests(t *testing.T) {
	fakeGitHub(t, 200, `{"state":"closed"}`, nil)
	f := &fakeOC{projects: []ocProject{aged("pr-5", time.Hour)}}

	r := &reaper{ttl: 72 * time.Hour, repo: "owner/repo", oc: f, ocNamespace: "default"}
	if err := r.runOpenChoreo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "pr-5" {
		t.Errorf("deleted %v, want [pr-5]", f.deleted)
	}
}

func TestOCReaperKeepsOpenPullRequests(t *testing.T) {
	fakeGitHub(t, 200, `{"state":"open"}`, nil)
	f := &fakeOC{projects: []ocProject{aged("pr-5", time.Hour)}}

	r := &reaper{ttl: 72 * time.Hour, repo: "owner/repo", oc: f, ocNamespace: "default"}
	if err := r.runOpenChoreo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 0 {
		t.Errorf("deleted %v while the pull request was open", f.deleted)
	}
}

// An unreachable GitHub is not evidence that a pull request closed.
func TestOCReaperKeepsWhenGitHubIsDown(t *testing.T) {
	fakeGitHub(t, 500, ``, nil)
	f := &fakeOC{projects: []ocProject{aged("pr-5", time.Hour)}}

	r := &reaper{ttl: 72 * time.Hour, repo: "owner/repo", oc: f, ocNamespace: "default"}
	if err := r.runOpenChoreo(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 0 {
		t.Errorf("deleted %v on a GitHub error", f.deleted)
	}
}
