package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var appSetGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applicationsets",
}

const (
	appSetNamespace = "argocd"
	appSetName      = "previews"
	namespacePrefix = "preview-pr-"
)

var namespacePattern = regexp.MustCompile(`^preview-pr-(\d+)$`)

type reaper struct {
	dyn    dynamic.Interface
	k8s    kubernetes.Interface
	ttl    time.Duration
	dryRun bool
	repo   string
	token  string
}

func (r *reaper) appSetPRs(ctx context.Context) (map[int]bool, []interface{}, error) {
	obj, err := r.dyn.Resource(appSetGVR).
		Namespace(appSetNamespace).
		Get(ctx, appSetName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}

	spec, _ := obj.Object["spec"].(map[string]interface{})
	generators, _ := spec["generators"].([]interface{})
	if len(generators) == 0 {
		return nil, nil, fmt.Errorf("ApplicationSet has no generators")
	}

	gen, _ := generators[0].(map[string]interface{})
	list, _ := gen["list"].(map[string]interface{})
	elements, _ := list["elements"].([]interface{})

	prs := make(map[int]bool, len(elements))
	for _, e := range elements {
		m, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		s, _ := m["prNumber"].(string)
		if n, err := strconv.Atoi(s); err == nil {
			prs[n] = true
		}
	}

	return prs, elements, nil
}

func (r *reaper) removeFromAppSet(ctx context.Context, elements []interface{}, pr int) ([]interface{}, error) {
	want := strconv.Itoa(pr)
	kept := make([]interface{}, 0, len(elements))

	for _, e := range elements {
		if m, ok := e.(map[string]interface{}); ok && m["prNumber"] == want {
			continue
		}
		kept = append(kept, e)
	}

	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"generators": []interface{}{
				map[string]interface{}{
					"list": map[string]interface{}{"elements": kept},
				},
			},
		},
	}

	data, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}

	_, err = r.dyn.Resource(appSetGVR).
		Namespace(appSetNamespace).
		Patch(ctx, appSetName, "application/merge-patch+json", data, metav1.PatchOptions{})
	if err != nil {
		return nil, err
	}

	return kept, nil
}

func (r *reaper) pullRequestClosed(ctx context.Context, pr int) (bool, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", r.repo, pr)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	// A PR that no longer exists is never going to reopen.
	if resp.StatusCode == http.StatusNotFound {
		return true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("github returned %s", resp.Status)
	}

	var body struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, err
	}

	return body.State == "closed", nil
}

// reason returns why this environment should go, or "" to keep it.
func (r *reaper) reason(ctx context.Context, pr int, age time.Duration, inAppSet bool) string {
	if !inAppSet {
		return "orphaned: no ApplicationSet entry"
	}

	if age > r.ttl {
		return fmt.Sprintf("ttl expired: age %s exceeds %s", age.Round(time.Minute), r.ttl)
	}

	if r.repo != "" {
		closed, err := r.pullRequestClosed(ctx, pr)
		if err != nil {
			// An unreachable API is not evidence of a closed PR.
			log.Printf("pr #%d: cannot check github, keeping: %v", pr, err)
			return ""
		}
		if closed {
			return "pull request is closed"
		}
	}

	return ""
}

func (r *reaper) run(ctx context.Context) error {
	prs, elements, err := r.appSetPRs(ctx)
	if err != nil {
		return fmt.Errorf("read ApplicationSet: %w", err)
	}

	namespaces, err := r.k8s.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list namespaces: %w", err)
	}

	kept, destroyed := 0, 0

	for _, ns := range namespaces.Items {
		if !strings.HasPrefix(ns.Name, namespacePrefix) {
			continue
		}

		m := namespacePattern.FindStringSubmatch(ns.Name)
		if m == nil {
			log.Printf("%s: name does not carry a PR number, skipping", ns.Name)
			continue
		}
		pr, _ := strconv.Atoi(m[1])

		age := time.Since(ns.CreationTimestamp.Time)
		why := r.reason(ctx, pr, age, prs[pr])

		if why == "" {
			log.Printf("%s: keeping (age %s)", ns.Name, age.Round(time.Minute))
			kept++
			continue
		}

		if r.dryRun {
			log.Printf("%s: WOULD DESTROY — %s", ns.Name, why)
			destroyed++
			continue
		}

		log.Printf("%s: destroying — %s", ns.Name, why)

		// Remove the entry first, or Argo CD recreates the namespace.
		if prs[pr] {
			elements, err = r.removeFromAppSet(ctx, elements, pr)
			if err != nil {
				log.Printf("%s: could not patch ApplicationSet: %v", ns.Name, err)
				continue
			}
			delete(prs, pr)
		}

		if err := r.k8s.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil {
			log.Printf("%s: could not delete namespace: %v", ns.Name, err)
			continue
		}

		destroyed++
	}

	verb := "destroyed"
	if r.dryRun {
		verb = "would destroy"
	}
	log.Printf("done: %d kept, %d %s", kept, destroyed, verb)

	return nil
}

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
		log.Fatalf("dynamic client: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}

	ttl := 72 * time.Hour
	if v := os.Getenv("TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			log.Fatalf("TTL %q is not a duration: %v", v, err)
		}
		ttl = d
	}

	// Destructive by exception, not by default.
	dryRun := os.Getenv("DRY_RUN") != "false"

	r := &reaper{
		dyn:    dyn,
		k8s:    clientset,
		ttl:    ttl,
		dryRun: dryRun,
		repo:   os.Getenv("GITHUB_REPO"),
		token:  os.Getenv("GITHUB_TOKEN"),
	}

	log.Printf("reaper starting: ttl=%s dry_run=%v github_repo=%q", ttl, dryRun, r.repo)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := r.run(ctx); err != nil {
		log.Fatal(err)
	}
}
