package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// The ApplicationSet that Argo CD watches. Adding an element here causes a
// preview environment to appear; removing one causes it to be destroyed.
var appSetGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applicationsets",
}

const (
	appSetNamespace = "argocd"
	appSetName      = "previews"
)

type pullRequestEvent struct {
	Action      string `json:"action"`
	Number      int    `json:"number"`
	PullRequest struct {
		Title string `json:"title"`
		Head  struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}

type controller struct {
	client dynamic.Interface
}

// readElements returns the current list of elements in the ApplicationSet's
// first (list) generator.
func (c *controller) readElements(ctx context.Context) ([]interface{}, error) {
	obj, err := c.client.Resource(appSetGVR).
		Namespace(appSetNamespace).
		Get(ctx, appSetName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	spec, ok := obj.Object["spec"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("ApplicationSet has no spec")
	}

	generators, ok := spec["generators"].([]interface{})
	if !ok || len(generators) == 0 {
		return nil, fmt.Errorf("ApplicationSet has no generators")
	}

	gen, ok := generators[0].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected generator shape")
	}

	list, ok := gen["list"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("first generator is not a list generator")
	}

	elements, _ := list["elements"].([]interface{})
	return elements, nil
}

// writeElements replaces the generator's element list.
func (c *controller) writeElements(ctx context.Context, elements []interface{}) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"generators": []interface{}{
				map[string]interface{}{
					"list": map[string]interface{}{
						"elements": elements,
					},
				},
			},
		},
	}

	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	_, err = c.client.Resource(appSetGVR).
		Namespace(appSetNamespace).
		Patch(ctx, appSetName, "application/merge-patch+json", data, metav1.PatchOptions{})
	return err
}

// addPreview is idempotent: replaying the same webhook does not duplicate.
func (c *controller) addPreview(ctx context.Context, pr int) error {
	elements, err := c.readElements(ctx)
	if err != nil {
		return err
	}

	want := fmt.Sprintf("%d", pr)
	for _, e := range elements {
		if m, ok := e.(map[string]interface{}); ok && m["prNumber"] == want {
			log.Printf("PR #%d: preview already exists", pr)
			return nil
		}
	}

	elements = append(elements, map[string]interface{}{"prNumber": want})
	if err := c.writeElements(ctx, elements); err != nil {
		return err
	}

	log.Printf("PR #%d: preview created", pr)
	return nil
}

func (c *controller) removePreview(ctx context.Context, pr int) error {
	elements, err := c.readElements(ctx)
	if err != nil {
		return err
	}

	want := fmt.Sprintf("%d", pr)
	kept := make([]interface{}, 0, len(elements))
	removed := false

	for _, e := range elements {
		if m, ok := e.(map[string]interface{}); ok && m["prNumber"] == want {
			removed = true
			continue
		}
		kept = append(kept, e)
	}

	if !removed {
		log.Printf("PR #%d: no preview to remove", pr)
		return nil
	}

	if err := c.writeElements(ctx, kept); err != nil {
		return err
	}

	log.Printf("PR #%d: preview destroyed", pr)
	return nil
}

func (c *controller) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var event pullRequestEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	var err error

	switch event.Action {
	case "opened", "reopened", "synchronize":
		err = c.addPreview(ctx, event.Number)
	case "closed":
		err = c.removePreview(ctx, event.Number)
	default:
		log.Printf("PR #%d: ignoring action %q", event.Number, event.Action)
	}

	if err != nil {
		if errors.IsNotFound(err) {
			log.Printf("ApplicationSet %q not found in namespace %q", appSetName, appSetNamespace)
		} else {
			log.Printf("PR #%d: %v", event.Number, err)
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func main() {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = os.Getenv("HOME") + "/.kube/config"
	}

	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		log.Fatalf("kubeconfig: %v", err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}

	c := &controller{client: dyn}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	http.HandleFunc("/webhook", c.handleWebhook)

	log.Printf("launchpad-controller listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
