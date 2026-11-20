package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	ocShaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ocShaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// memStore is an in-memory ocStore that behaves like the API server for the
// three outcomes the backend cares about: created, already exists, not found.
type memStore struct {
	mu      sync.Mutex
	objects map[string]*unstructured.Unstructured
	creates int
	patches int
	failOn  string // resource name whose Create fails with a server error
}

func newMemStore() *memStore { return &memStore{objects: map[string]*unstructured.Unstructured{}} }

func key(gvr schema.GroupVersionResource, ns, name string) string {
	return gvr.Resource + "/" + ns + "/" + name
}

func (s *memStore) Create(_ context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := obj.Object["metadata"].(map[string]interface{})["name"].(string)
	if gvr.Resource == s.failOn {
		return fmt.Errorf("boom")
	}
	k := key(gvr, ns, name)
	if _, ok := s.objects[k]; ok {
		return errors.NewAlreadyExists(schema.GroupResource{Resource: gvr.Resource}, name)
	}
	s.objects[k] = obj
	s.creates++
	return nil
}

func (s *memStore) Get(_ context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.objects[key(gvr, ns, name)]; ok {
		return o, nil
	}
	return nil, errors.NewNotFound(schema.GroupResource{Resource: gvr.Resource}, name)
}

func (s *memStore) Patch(_ context.Context, gvr schema.GroupVersionResource, ns, name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key(gvr, ns, name)]
	if !ok {
		return errors.NewNotFound(schema.GroupResource{Resource: gvr.Resource}, name)
	}
	var p struct {
		Spec struct {
			Container struct {
				Image string `json:"image"`
			} `json:"container"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	o.Object["spec"].(map[string]interface{})["container"] = map[string]interface{}{"image": p.Spec.Container.Image}
	s.patches++
	return nil
}

func (s *memStore) Delete(_ context.Context, gvr schema.GroupVersionResource, ns, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key(gvr, ns, name)
	if _, ok := s.objects[k]; !ok {
		return errors.NewNotFound(schema.GroupResource{Resource: gvr.Resource}, name)
	}
	delete(s.objects, k)
	return nil
}

func (s *memStore) has(gvr schema.GroupVersionResource, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key(gvr, "default", name)]
	return ok
}

func testOC(t *testing.T, s ocStore) *openchoreo {
	t.Helper()
	o, err := newOpenchoreo(s, "default", "development", "sihanudayaratna/launchpad-demo", "development-default.openchoreoapis.localhost:19080")
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestOCAddCreatesTheFourObjects(t *testing.T) {
	s := newMemStore()
	o := testOC(t, s)

	got, err := o.add(context.Background(), 7, ocShaA)
	if err != nil || got != added {
		t.Fatalf("add() = %v, %v; want added, nil", got, err)
	}

	for _, c := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{ocProjects, "pr-7"},
		{ocBindings, "pr-7-development"},
		{ocComponents, "launchpad-demo-pr-7"},
		{ocWorkloads, "launchpad-demo-pr-7-workload"},
	} {
		if !s.has(c.gvr, c.name) {
			t.Errorf("%s %s was not created", c.gvr.Resource, c.name)
		}
	}
}

func TestOCObjectShapes(t *testing.T) {
	o := testOC(t, newMemStore())

	w := o.workload(7, ocShaA)
	if got, want := workloadImage(w), "sihanudayaratna/launchpad-demo:"+ocShaA; got != want {
		t.Errorf("image = %q, want %q", got, want)
	}
	spec := w.Object["spec"].(map[string]interface{})
	owner := spec["owner"].(map[string]interface{})
	if owner["projectName"] != "pr-7" || owner["componentName"] != "launchpad-demo-pr-7" {
		t.Errorf("workload owner = %v", owner)
	}
	ep := spec["endpoints"].(map[string]interface{})["http"].(map[string]interface{})
	if ep["type"] != "HTTP" || ep["port"] != int64(8080) {
		t.Errorf("endpoint = %v", ep)
	}

	b := o.binding(7).Object["spec"].(map[string]interface{})
	if b["environment"] != "development" || b["owner"].(map[string]interface{})["projectName"] != "pr-7" {
		t.Errorf("binding spec = %v", b)
	}

	if o.project(7).Object["metadata"].(map[string]interface{})["namespace"] != "default" {
		t.Error("project is not in the configured namespace")
	}
}

func TestOCReplayedWebhookWritesNothing(t *testing.T) {
	s := newMemStore()
	o := testOC(t, s)
	o.add(context.Background(), 7, ocShaA)
	creates := s.creates

	got, err := o.add(context.Background(), 7, ocShaA)
	if err != nil || got != unchanged {
		t.Fatalf("replay = %v, %v; want unchanged, nil", got, err)
	}
	if s.creates != creates || s.patches != 0 {
		t.Errorf("replay wrote: creates %d->%d, patches %d", creates, s.creates, s.patches)
	}
}

func TestOCNewCommitMovesTheImageOnly(t *testing.T) {
	s := newMemStore()
	o := testOC(t, s)
	o.add(context.Background(), 7, ocShaA)

	got, err := o.add(context.Background(), 7, ocShaB)
	if err != nil || got != updated {
		t.Fatalf("add() = %v, %v; want updated, nil", got, err)
	}
	w, _ := s.Get(context.Background(), ocWorkloads, "default", "launchpad-demo-pr-7-workload")
	if workloadImage(w) != "sihanudayaratna/launchpad-demo:"+ocShaB {
		t.Errorf("image = %q", workloadImage(w))
	}
	if s.patches != 1 {
		t.Errorf("patches = %d, want 1", s.patches)
	}
}

func TestOCRetryAfterPartialFailureCompletes(t *testing.T) {
	s := newMemStore()
	o := testOC(t, s)

	s.failOn = "components"
	if _, err := o.add(context.Background(), 7, ocShaA); err == nil {
		t.Fatal("expected the component failure to surface")
	}
	if s.has(ocWorkloads, "launchpad-demo-pr-7-workload") {
		t.Fatal("workload created despite earlier failure")
	}

	s.failOn = ""
	got, err := o.add(context.Background(), 7, ocShaA)
	if err != nil || got != added {
		t.Fatalf("retry = %v, %v; want added, nil", got, err)
	}
	if !s.has(ocWorkloads, "launchpad-demo-pr-7-workload") {
		t.Error("retry did not finish the job")
	}
}

func TestOCPreviewsAreIsolatedFromEachOther(t *testing.T) {
	s := newMemStore()
	o := testOC(t, s)
	o.add(context.Background(), 7, ocShaA)
	o.add(context.Background(), 8, ocShaB)

	if _, err := o.remove(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if s.has(ocProjects, "pr-7") || !s.has(ocProjects, "pr-8") || !s.has(ocWorkloads, "launchpad-demo-pr-8-workload") {
		t.Error("closing PR 7 touched PR 8")
	}
}

func TestOCRemoveDeletesEverything(t *testing.T) {
	s := newMemStore()
	o := testOC(t, s)
	o.add(context.Background(), 7, ocShaA)

	removed, err := o.remove(context.Background(), 7)
	if err != nil || !removed {
		t.Fatalf("remove() = %v, %v; want true, nil", removed, err)
	}
	if len(s.objects) != 0 {
		t.Errorf("%d objects left behind", len(s.objects))
	}
}

func TestOCRemoveWithoutPreviewIsNotAnError(t *testing.T) {
	o := testOC(t, newMemStore())
	removed, err := o.remove(context.Background(), 99)
	if err != nil || removed {
		t.Errorf("remove() = %v, %v; want false, nil", removed, err)
	}
}

func TestOCURL(t *testing.T) {
	want := "http://development-default.openchoreoapis.localhost:19080/launchpad-demo-pr-7-http/"
	if got := testOC(t, newMemStore()).url(7); got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

func TestNewOpenchoreoRejectsBadSettings(t *testing.T) {
	good := []string{"default", "development", "sihanudayaratna/launchpad-demo", "host.example:19080"}
	for i, bad := range []string{"Bad_NS", "dev elopment", "repo:tag", "host/path"} {
		args := append([]string(nil), good...)
		args[i] = bad
		if _, err := newOpenchoreo(newMemStore(), args[0], args[1], args[2], args[3]); err == nil {
			t.Errorf("setting %d = %q was accepted", i, bad)
		}
	}
}

func TestReadyURLBody(t *testing.T) {
	b := readyURLBody("http://h/p/", ocShaA)
	if !strings.HasPrefix(b, commentMarker) || !strings.Contains(b, "http://h/p/") || !strings.Contains(b, "`aaaaaaa`") {
		t.Errorf("body = %q", b)
	}
}

// The webhook path end to end: signed-less request in, objects out, comment
// body unaffected (no commenter configured).
func TestWebhookDrivesOpenChoreoBackend(t *testing.T) {
	s := newMemStore()
	c := &controller{oc: testOC(t, s)}

	post := func(action string, sha string) int {
		body := fmt.Sprintf(`{"action":%q,"number":7,"pull_request":{"head":{"sha":%q}}}`, action, sha)
		rec := httptest.NewRecorder()
		c.handleWebhook(rec, httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body)))
		return rec.Code
	}

	if code := post("opened", ocShaA); code != http.StatusAccepted || !s.has(ocProjects, "pr-7") {
		t.Fatalf("opened: code %d, project exists %v", code, s.has(ocProjects, "pr-7"))
	}
	if code := post("synchronize", ocShaB); code != http.StatusAccepted {
		t.Fatalf("synchronize: code %d", code)
	}
	if code := post("opened", "not-a-sha"); code != http.StatusBadRequest {
		t.Errorf("bad sha: code %d, want 400", code)
	}
	if code := post("closed", ""); code != http.StatusAccepted || s.has(ocProjects, "pr-7") {
		t.Fatalf("closed: code %d, project still exists %v", code, s.has(ocProjects, "pr-7"))
	}
}
