package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// The OpenChoreo backend (BACKEND=openchoreo).
//
// Instead of adding an element to the Argo CD ApplicationSet, the controller
// asks OpenChoreo for one Project per pull request. A Project owns its own
// per-environment namespace on the data plane, so deleting it removes the
// whole preview, namespace included.
//
//	PR opened/pushed  ->  Project pr-N
//	                      ProjectReleaseBinding pr-N-<environment>
//	                      Component launchpad-demo-pr-N
//	                      Workload  launchpad-demo-pr-N-workload (image :<sha>)
//	PR closed         ->  all of the above deleted

const (
	ocGroup   = "openchoreo.dev"
	ocVersion = "v1alpha1"

	// The demo app is the one workload Launchpad previews, so its Component
	// name is fixed. Each PR gets its own Project, which is why the same
	// component can exist once per PR (names are made unique with -pr-N).
	ocComponentBase = "launchpad-demo"
	ocEndpoint      = "http"
	ocPort          = 8080
)

var (
	ocProjects   = schema.GroupVersionResource{Group: ocGroup, Version: ocVersion, Resource: "projects"}
	ocBindings   = schema.GroupVersionResource{Group: ocGroup, Version: ocVersion, Resource: "projectreleasebindings"}
	ocComponents = schema.GroupVersionResource{Group: ocGroup, Version: ocVersion, Resource: "components"}
	ocWorkloads  = schema.GroupVersionResource{Group: ocGroup, Version: ocVersion, Resource: "workloads"}
)

var (
	labelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	hostPattern  = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)
	// An image repository such as "sihanudayaratna/launchpad-demo", without a
	// tag: the controller appends the commit SHA itself.
	imagePattern = regexp.MustCompile(`^[a-z0-9]+([._/-][a-z0-9]+)*$`)
)

// ocStore is the small slice of the Kubernetes API the backend needs. The
// controller uses the dynamic client; tests use an in-memory store.
type ocStore interface {
	Create(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured) error
	Get(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error)
	Patch(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, data []byte) error
	Delete(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) error
}

type dynStore struct{ client dynamic.Interface }

func (s dynStore) Create(ctx context.Context, gvr schema.GroupVersionResource, ns string, obj *unstructured.Unstructured) error {
	_, err := s.client.Resource(gvr).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	return err
}

func (s dynStore) Get(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (*unstructured.Unstructured, error) {
	return s.client.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
}

func (s dynStore) Patch(ctx context.Context, gvr schema.GroupVersionResource, ns, name string, data []byte) error {
	_, err := s.client.Resource(gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, data, metav1.PatchOptions{})
	return err
}

func (s dynStore) Delete(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) error {
	return s.client.Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
}

type openchoreo struct {
	store       ocStore
	namespace   string // OpenChoreo namespace that holds the objects, e.g. "default"
	environment string // environment previews are bound to, e.g. "development"
	image       string // image repository; the PR's commit SHA is the tag
	host        string // gateway host[:port], e.g. development-default.openchoreoapis.localhost:19080
}

// newOpenchoreo validates its settings up front: they end up in object names,
// image references and PR comments, so a bad value should stop the controller
// at startup rather than on the first webhook.
func newOpenchoreo(store ocStore, namespace, environment, image, host string) (*openchoreo, error) {
	switch {
	case !labelPattern.MatchString(namespace):
		return nil, fmt.Errorf("invalid OC_NAMESPACE %q", namespace)
	case !labelPattern.MatchString(environment):
		return nil, fmt.Errorf("invalid OC_ENVIRONMENT %q", environment)
	case !imagePattern.MatchString(image):
		return nil, fmt.Errorf("invalid OC_IMAGE %q (repository only, no tag)", image)
	case !hostPattern.MatchString(host):
		return nil, fmt.Errorf("invalid OC_HOST %q", host)
	}
	return &openchoreo{store: store, namespace: namespace, environment: environment, image: image, host: host}, nil
}

func ocProjectName(pr int) string { return fmt.Sprintf("pr-%d", pr) }
func ocBindingName(o *openchoreo, pr int) string {
	return fmt.Sprintf("%s-%s", ocProjectName(pr), o.environment)
}
func ocComponentName(pr int) string { return fmt.Sprintf("%s-%s", ocComponentBase, ocProjectName(pr)) }
func ocWorkloadName(pr int) string  { return ocComponentName(pr) + "-workload" }

// url is where the gateway serves PR pr. OpenChoreo routes
// /<component>-<endpoint>/ and strips that prefix before the request reaches
// the container.
func (o *openchoreo) url(pr int) string {
	return fmt.Sprintf("http://%s/%s-%s/", o.host, ocComponentName(pr), ocEndpoint)
}

func (o *openchoreo) object(kind, name string, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": ocGroup + "/" + ocVersion,
		"kind":       kind,
		"metadata":   map[string]interface{}{"name": name, "namespace": o.namespace},
		"spec":       spec,
	}}
}

func (o *openchoreo) project(pr int) *unstructured.Unstructured {
	return o.object("Project", ocProjectName(pr), map[string]interface{}{
		"deploymentPipelineRef": map[string]interface{}{"kind": "DeploymentPipeline", "name": "default"},
		"type":                  map[string]interface{}{"kind": "ClusterProjectType", "name": "default"},
	})
}

func (o *openchoreo) binding(pr int) *unstructured.Unstructured {
	return o.object("ProjectReleaseBinding", ocBindingName(o, pr), map[string]interface{}{
		"owner":       map[string]interface{}{"projectName": ocProjectName(pr)},
		"environment": o.environment,
	})
}

func (o *openchoreo) component(pr int) *unstructured.Unstructured {
	return o.object("Component", ocComponentName(pr), map[string]interface{}{
		"owner":         map[string]interface{}{"projectName": ocProjectName(pr)},
		"componentType": map[string]interface{}{"kind": "ClusterComponentType", "name": "deployment/service"},
		"autoDeploy":    true,
	})
}

func (o *openchoreo) imageFor(sha string) string { return o.image + ":" + sha }

func (o *openchoreo) workload(pr int, sha string) *unstructured.Unstructured {
	return o.object("Workload", ocWorkloadName(pr), map[string]interface{}{
		"owner": map[string]interface{}{
			"projectName":   ocProjectName(pr),
			"componentName": ocComponentName(pr),
		},
		"container": map[string]interface{}{"image": o.imageFor(sha)},
		"endpoints": map[string]interface{}{
			ocEndpoint: map[string]interface{}{
				"type":       "HTTP",
				"port":       int64(ocPort),
				"visibility": []interface{}{"external"},
			},
		},
	})
}

// workloadImage digs spec.container.image out of a Workload.
func workloadImage(obj *unstructured.Unstructured) string {
	spec, _ := obj.Object["spec"].(map[string]interface{})
	container, _ := spec["container"].(map[string]interface{})
	image, _ := container["image"].(string)
	return image
}

// ensure creates obj and treats "already there" as success, so every step can
// be replayed after a partial failure or a repeated webhook.
func (o *openchoreo) ensure(ctx context.Context, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	err := o.store.Create(ctx, gvr, o.namespace, obj)
	if err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("create %s %s: %w", gvr.Resource, obj.Object["metadata"].(map[string]interface{})["name"], err)
	}
	return nil
}

// add brings PR pr's preview up at commit sha. It is idempotent: a replayed
// webhook for the same commit changes nothing and reports unchanged.
func (o *openchoreo) add(ctx context.Context, pr int, sha string) (change, error) {
	for _, step := range []struct {
		gvr schema.GroupVersionResource
		obj *unstructured.Unstructured
	}{
		{ocProjects, o.project(pr)},
		{ocBindings, o.binding(pr)},
		{ocComponents, o.component(pr)},
	} {
		if err := o.ensure(ctx, step.gvr, step.obj); err != nil {
			return unchanged, err
		}
	}

	err := o.store.Create(ctx, ocWorkloads, o.namespace, o.workload(pr, sha))
	if err == nil {
		return added, nil
	}
	if !errors.IsAlreadyExists(err) {
		return unchanged, fmt.Errorf("create workloads %s: %w", ocWorkloadName(pr), err)
	}

	// The workload exists: a new push only needs the image tag moved.
	current, err := o.store.Get(ctx, ocWorkloads, o.namespace, ocWorkloadName(pr))
	if err != nil {
		return unchanged, fmt.Errorf("get workloads %s: %w", ocWorkloadName(pr), err)
	}
	if workloadImage(current) == o.imageFor(sha) {
		return unchanged, nil
	}

	patch, err := json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{"container": map[string]interface{}{"image": o.imageFor(sha)}},
	})
	if err != nil {
		return unchanged, err
	}
	if err := o.store.Patch(ctx, ocWorkloads, o.namespace, ocWorkloadName(pr), patch); err != nil {
		return unchanged, fmt.Errorf("patch workloads %s: %w", ocWorkloadName(pr), err)
	}
	return updated, nil
}

// remove tears PR pr's preview down and reports whether anything existed.
// Deleting the Project is what removes the environment; the other deletes only
// make sure nothing is left behind in the control plane. Every delete tolerates
// "not found", so closing a PR that never had a preview is not an error.
func (o *openchoreo) remove(ctx context.Context, pr int) (bool, error) {
	removed := false
	for _, step := range []struct {
		gvr  schema.GroupVersionResource
		name string
	}{
		{ocProjects, ocProjectName(pr)},
		{ocBindings, ocBindingName(o, pr)},
		{ocComponents, ocComponentName(pr)},
		{ocWorkloads, ocWorkloadName(pr)},
	} {
		err := o.store.Delete(ctx, step.gvr, o.namespace, step.name)
		switch {
		case err == nil:
			removed = true
		case errors.IsNotFound(err):
		default:
			return removed, fmt.Errorf("delete %s %s: %w", step.gvr.Resource, step.name, err)
		}
	}
	return removed, nil
}

// addOpenChoreo and removeOpenChoreo mirror addPreview and removePreview for
// the Argo CD backend, including the log lines and metrics.
func (c *controller) addOpenChoreo(ctx context.Context, pr int, sha string) (change, error) {
	previewMu.Lock()
	defer previewMu.Unlock()

	result, err := c.oc.add(ctx, pr, sha)
	switch {
	case err != nil:
		return unchanged, err
	case result == unchanged:
		log.Printf("PR #%d: preview already at %s", pr, sha[:7])
	case result == added:
		log.Printf("PR #%d: OpenChoreo project %s created at %s", pr, ocProjectName(pr), sha[:7])
		c.metrics.event("created")
	default:
		log.Printf("PR #%d: preview updated to %s", pr, sha[:7])
		c.metrics.event("updated")
	}
	return result, nil
}

func (c *controller) removeOpenChoreo(ctx context.Context, pr int) error {
	previewMu.Lock()
	defer previewMu.Unlock()

	removed, err := c.oc.remove(ctx, pr)
	if err != nil {
		return err
	}
	if !removed {
		log.Printf("PR #%d: no preview to remove", pr)
		return nil
	}
	log.Printf("PR #%d: OpenChoreo project %s deleted", pr, ocProjectName(pr))
	c.metrics.event("destroyed")
	return nil
}
