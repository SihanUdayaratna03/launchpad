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
	"time"

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
	Action     string `json:"action"`
	Number     int    `json:"number"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
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

	baseDomain  string     // e.g. 13-211-45-67.nip.io; empty disables preview URLs
	tlsResolver string     // Traefik certificate resolver; empty serves previews over plain http
	comments    *commenter // nil disables PR comments
	metrics     *metrics   // nil records nothing

	oc *openchoreo // non-nil when BACKEND=openchoreo; nil means the Argo CD backend
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

// addPreview is idempotent: replaying the same webhook writes nothing. A new
// commit on an existing PR changes the element's sha, which makes Argo CD roll
// the preview onto the image built for that commit.
func (c *controller) addPreview(ctx context.Context, pr int, sha string) (change, error) {
	if c.oc != nil {
		return c.addOpenChoreo(ctx, pr, sha)
	}

	previewMu.Lock()
	defer previewMu.Unlock()

	elements, err := c.readElements(ctx)
	if err != nil {
		return unchanged, err
	}

	next, result := upsertElement(elements, pr, sha, previewHost(c.baseDomain, pr), c.tlsResolver)
	if result == unchanged {
		log.Printf("PR #%d: preview already at %s", pr, sha[:7])
		return unchanged, nil
	}

	if err := c.writeElements(ctx, next); err != nil {
		return unchanged, err
	}

	if result == added {
		log.Printf("PR #%d: preview created at %s", pr, sha[:7])
		c.metrics.event("created")
	} else {
		log.Printf("PR #%d: preview updated to %s", pr, sha[:7])
		c.metrics.event("updated")
	}
	return result, nil
}

// announce keeps the PR comment in step with the preview. A GitHub failure is
// logged and never fails the webhook: the preview itself already exists, and a
// retry would only repeat work.
func (c *controller) announce(ctx context.Context, repo string, pr int, body string, create bool) {
	if c.comments == nil {
		return
	}
	if err := c.comments.upsert(ctx, repo, pr, body, create); err != nil {
		log.Printf("PR #%d: comment failed: %v", pr, err)
		c.metrics.commentFailed()
	}
}

func (c *controller) removePreview(ctx context.Context, pr int) error {
	if c.oc != nil {
		return c.removeOpenChoreo(ctx, pr)
	}

	previewMu.Lock()
	defer previewMu.Unlock()

	elements, err := c.readElements(ctx)
	if err != nil {
		return err
	}

	kept, removed := removeElement(elements, pr)
	if !removed {
		log.Printf("PR #%d: no preview to remove", pr)
		return nil
	}

	if err := c.writeElements(ctx, kept); err != nil {
		return err
	}

	log.Printf("PR #%d: preview destroyed", pr)
	c.metrics.event("destroyed")
	return nil
}

func (c *controller) handleWebhook(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	result := "accepted"
	defer func() { c.metrics.webhook(result, time.Since(start)) }()

	if r.Method != http.MethodPost {
		result = "bad_request"
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// The signature covers the raw bytes, so the body must be read in full
	// before it is parsed.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		result = "bad_request"
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	if len(c.secret) > 0 {
		if !c.verifySignature(r.Header.Get("X-Hub-Signature-256"), body) {
			log.Printf("rejected webhook from %s: bad signature", r.RemoteAddr)
			result = "unauthorized"
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var event pullRequestEvent
	if err := json.Unmarshal(body, &event); err != nil {
		result = "bad_request"
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	switch event.Action {
	case "opened", "reopened", "synchronize":
		sha := event.PullRequest.Head.SHA
		if !commitPattern.MatchString(sha) {
			log.Printf("PR #%d: rejecting %q event with invalid head sha", event.Number, event.Action)
			result = "bad_request"
			http.Error(w, "bad commit sha", http.StatusBadRequest)
			return
		}
		var result change
		result, err = c.addPreview(ctx, event.Number, sha)
		if err == nil && result != unchanged {
			body := deployingBody(sha)
			if c.oc != nil {
				body = readyURLBody(c.oc.url(event.Number), sha)
			} else if host := previewHost(c.baseDomain, event.Number); host != "" {
				body = readyURLBody(previewURL(host, c.tlsResolver), sha)
			}
			c.announce(ctx, event.Repository.FullName, event.Number, body, true)
		}
	case "closed":
		err = c.removePreview(ctx, event.Number)
		if err == nil {
			c.announce(ctx, event.Repository.FullName, event.Number, removedBody(), false)
		}
	default:
		log.Printf("PR #%d: ignoring action %q", event.Number, event.Action)
		result = "ignored"
		err = nil
	}

	if err != nil {
		result = "error"
		if c.oc == nil && errors.IsNotFound(err) {
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

	c := &controller{
		client:      dyn,
		secret:      secret,
		baseDomain:  os.Getenv("BASE_DOMAIN"),
		tlsResolver: os.Getenv("TLS_RESOLVER"),
		comments:    newCommenter(os.Getenv("GITHUB_TOKEN")),
		metrics:     newMetrics(),
	}

	if c.tlsResolver != "" {
		if !resolverPattern.MatchString(c.tlsResolver) {
			log.Fatalf("TLS_RESOLVER %q is not a valid resolver name", c.tlsResolver)
		}
		log.Printf("previews are served over https, certificates from resolver %q", c.tlsResolver)
	}

	switch backend := os.Getenv("BACKEND"); backend {
	case "", "argocd":
		log.Printf("backend: argocd (ApplicationSet %s/%s)", appSetNamespace, appSetName)
		if c.baseDomain == "" {
			log.Printf("BASE_DOMAIN is not set, previews get no URL")
		}
	case "openchoreo":
		c.oc, err = newOpenchoreo(dynStore{dyn},
			envOr("OC_NAMESPACE", "default"),
			envOr("OC_ENVIRONMENT", "development"),
			envOr("OC_IMAGE", "sihanudayaratna/launchpad-demo"),
			envOr("OC_HOST", "development-default.openchoreoapis.localhost:19080"))
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("backend: openchoreo (namespace %s, environment %s, image %s)", c.oc.namespace, c.oc.environment, c.oc.image)
	default:
		log.Fatalf("unknown BACKEND %q (want argocd or openchoreo)", backend)
	}
	if c.comments == nil {
		log.Printf("GITHUB_TOKEN is not set, PR comments are disabled")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	http.HandleFunc("/webhook", c.handleWebhook)
	http.HandleFunc("/metrics", c.metrics.handler)
	log.Printf("launchpad-controller listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
