package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// The OpenChoreo mode of the reaper (BACKEND=openchoreo).
//
// In the Argo CD mode the reaper walks the preview namespaces and uses the
// ApplicationSet as the list of environments that should exist. With OpenChoreo
// there is no such list: the Project named pr-N *is* the record of the preview,
// and deleting it takes the environment's namespace with it. So this mode walks
// the Projects instead, and the orphan rule does not apply.
//
// It exists because a close webhook can be missed, and without it a Project
// would then live forever.

var ocProjectsGVR = schema.GroupVersionResource{
	Group:    "openchoreo.dev",
	Version:  "v1alpha1",
	Resource: "projects",
}

// Only Projects named by the controller are ever touched. OpenChoreo's own
// Projects (default, and anything a person made) do not match.
var ocProjectPattern = regexp.MustCompile(`^pr-(\d+)$`)

// ocProject is the little that the reaper needs to know about a Project.
type ocProject struct {
	name    string
	created time.Time
}

// ocClient is the slice of the API the OpenChoreo mode uses. The real one is
// the dynamic client; tests use an in-memory list.
type ocClient interface {
	list(ctx context.Context, namespace string) ([]ocProject, error)
	delete(ctx context.Context, namespace, name string) error
}

type dynOC struct{ client dynamic.Interface }

func (d dynOC) list(ctx context.Context, namespace string) ([]ocProject, error) {
	objs, err := d.client.Resource(ocProjectsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	projects := make([]ocProject, 0, len(objs.Items))
	for _, obj := range objs.Items {
		meta, _ := obj.Object["metadata"].(map[string]interface{})
		name, _ := meta["name"].(string)
		if name == "" {
			continue
		}

		// An unparsable timestamp leaves created at the zero time, which makes
		// the Project look ancient. Skip it rather than delete on a guess.
		stamp, _ := meta["creationTimestamp"].(string)
		created, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			log.Printf("project %s: cannot read creationTimestamp %q, skipping: %v", name, stamp, err)
			continue
		}

		projects = append(projects, ocProject{name: name, created: created})
	}
	return projects, nil
}

func (d dynOC) delete(ctx context.Context, namespace, name string) error {
	return d.client.Resource(ocProjectsGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// runOpenChoreo is the OpenChoreo counterpart of run. The TTL and closed-pull-
// request rules are the same ones the Argo CD mode uses, which is why it calls
// reason with inAppSet true: there is no ApplicationSet here, so the orphan rule
// has nothing to say.
func (r *reaper) runOpenChoreo(ctx context.Context) error {
	projects, err := r.oc.list(ctx, r.ocNamespace)
	if err != nil {
		return fmt.Errorf("list projects in %s: %w", r.ocNamespace, err)
	}

	kept, destroyed := 0, 0

	for _, p := range projects {
		m := ocProjectPattern.FindStringSubmatch(p.name)
		if m == nil {
			// Not one of ours. Someone else's Project is not the reaper's business.
			continue
		}
		pr, _ := strconv.Atoi(m[1])

		age := time.Since(p.created)
		why := r.reason(ctx, pr, age, true)

		if why == "" {
			log.Printf("project %s: keeping (age %s)", p.name, age.Round(time.Minute))
			kept++
			continue
		}

		if r.dryRun {
			log.Printf("project %s: WOULD DESTROY — %s", p.name, why)
			destroyed++
			continue
		}

		log.Printf("project %s: destroying — %s", p.name, why)
		if err := r.oc.delete(ctx, r.ocNamespace, p.name); err != nil {
			log.Printf("project %s: could not delete: %v", p.name, err)
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
