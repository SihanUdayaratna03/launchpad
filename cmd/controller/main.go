package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
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

	// Reject bodies larger than this. A webhook payload is a few KB; anything
	// larger is either a mistake or an attempt to exhaust memory.
	maxBodyBytes = 1 << 20 // 1 MiB
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
	secret []byte // shared secret for webhook signature verification
}

// verifySignature checks the GitHub HMAC-SHA256 signature over the raw body.
//
// GitHub computes HMAC-SHA256(secret, body) and sends it as
// X-Hub-Signature-256: sha256=<hex>. Anyone who does not know the secret
// cannot produce a valid signature for a body of their choosing.
func (c *controller) verifySignature(header string, body []byte) bool {
	const prefix = "sha256="
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}

	sent, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, c.secret)
	mac.Write(body)
	expected := mac.Sum(nil)

	// hmac.Equal is constant-time. A plain == would return as soon as two
	// bytes differ, and the timing difference leaks the signature one byte
	// at a time.
	return hmac.Equal(sent, expected)
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

	// The signature covers the raw bytes, so the body must be read in full
	// before it is parsed.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	if len(c.secret) > 0 {
		if !c.verifySignature(r.Header.Get("X-Hub-Signature-256"), body) {
			log.Printf("rejected webhook from %s: bad signature", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var event pullRequestEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	switch event.Action {
	case "opened", "reopened", "synchronize":
		err = c.addPreview(ctx, event.Number)
	case "closed":
		err = c.removePreview(ctx, event.Number)
	default:
		log.Printf("PR #%d: ignoring action %q", event.Number, event.Action)
		err = nil
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

// kubeConfig returns the in-cluster ServiceAccount credentials when running as
// a pod, and falls back to a local kubeconfig when running on a workstation.
func kubeConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		log.Printf("using in-cluster service account")
		return cfg, nil
	}

	path := os.Getenv("KUBECONFIG")
	if path == "" {
		path = os.Getenv("HOME") + "/.kube/config"
	}

	cfg, err = clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("no in-cluster config and no kubeconfig at %s: %w", path, err)
	}

	log.Printf("using kubeconfig at %s", path)
	return cfg, nil
}

func main() {
	cfg, err := kubeConfig()
	if err != nil {
		log.Fatal(err)
	}

	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}

	secret := []byte(os.Getenv("WEBHOOK_SECRET"))
	if len(secret) == 0 {
		log.Printf("WARNING: WEBHOOK_SECRET is not set, signature checks are disabled")
	} else {
		log.Printf("webhook signature verification enabled")
	}

	c := &controller{client: dyn, secret: secret}

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
