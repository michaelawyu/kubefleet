/*
Copyright 2026 The KubeFleet Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package annotatedobjectwatcher

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	kferrors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	controllerName = "AnnotatedObjectWatcher"

	derivedPlacementPolicyCleanupFinalizer = "kubefleet.dev/derived-placement-policy-cleanup"

	derivedPlacementPolicyNameFmt = "%s-%s"
)

// derivedPlacementPolicyName returns the name of the placement policy derived from an annotated
// object of the given kind and name. The kind is lowercased since it is title-cased (e.g.
// "Deployment") while a Kubernetes object name must be a lowercase RFC 1123 subdomain.
func derivedPlacementPolicyName(kind, name string) string {
	return fmt.Sprintf(derivedPlacementPolicyNameFmt, strings.ToLower(kind), name)
}

type Request struct {
	schema.GroupVersionKind
	types.NamespacedName
}

type Reconciler struct {
	HubClient client.Client
}

func (r *Reconciler) Reconcile(ctx context.Context, req Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "request", req.NamespacedName, "controller", controllerName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends", "request", req.NamespacedName, "controller", controllerName, "latency", latency)
	}()

	// Retrieve the annotated object as an unstructured object.
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(req.GroupVersionKind)
	if err := r.HubClient.Get(ctx, req.NamespacedName, object); err != nil {
		if apierrors.IsNotFound(err) {
			// The object is gone; there is nothing left to reconcile.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, kferrors.NewAPIServerError(err, "failed to get the annotated object", false, "obj", req)
	}

	envBasedClusterSelectorsRawStr, found := object.GetAnnotations()[placementv1alpha1.ClusterSelectorsAnnotation]
	if !object.GetDeletionTimestamp().IsZero() || !found {
		// The object is marked for deletion or no longer targeted for placement.
		if err := r.deletePlacementPolicyFor(ctx, object); err != nil {
			klog.ErrorS(err, "Failed to delete placement policy for the annotated object", append(errors.Args(err), "obj", req, "controller", controllerName)...)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Add a finalizer to the object if it doesn't already have one.
	if !controllerutil.ContainsFinalizer(object, derivedPlacementPolicyCleanupFinalizer) {
		controllerutil.AddFinalizer(object, derivedPlacementPolicyCleanupFinalizer)
		if err := r.HubClient.Update(ctx, object); err != nil {
			return ctrl.Result{}, kferrors.NewAPIServerError(err, "failed to add the cleanup finalizer to the annotated object", false, "obj", req)
		}
	}

	// Parse the environment-based cluster selectors from the annotation.
	regions, err := parseRegionBasedClusterSelectors(envBasedClusterSelectorsRawStr)
	if err != nil {
		return ctrl.Result{}, kferrors.NewUserError(err, "failed to parse the environment-based cluster selectors", false, "obj", req)
	}

	additionalResRefs, err := r.collectAdditionalResourceManifests(ctx, object)
	if err != nil {
		wrappedErr := kferrors.Wraps(err, "", "request", req, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to collect additional resource manifests for the deployment", kferrors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	placementPolicy := &placementv1alpha1.PlacementPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      derivedPlacementPolicyName(object.GetKind(), object.GetName()),
			Namespace: object.GetNamespace(),
		},
	}
	resOp, err := ctrl.CreateOrUpdate(ctx, r.HubClient, placementPolicy, func() error {
		// Add the cluster by region selector.
		clusterByRegionSelectors := make([]placementv1alpha1.ClusterSelector, 0, len(regions))
		for _, placeTo := range regions {
			clusterByRegionSelectors = append(clusterByRegionSelectors, placementv1alpha1.ClusterSelector{
				Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
					{
						MatchLabels: map[string]string{
							"topology.kubernetes.io/region": placeTo,
						},
					},
				},
			})
		}
		placementPolicy.Spec.ClusterSelectors = clusterByRegionSelectors

		// Add the resource selectors, starting with the workload (the Deployment itself) followed by
		// any additional resources it references.
		resourceSelectors := make([]placementv1alpha1.ResourceSelector, 0, len(additionalResRefs)+1)
		resourceSelectors = append(resourceSelectors, placementv1alpha1.ResourceSelector{
			Kind:       req.Kind,
			APIGroup:   req.Group,
			APIVersion: req.Version,
			Name:       object.GetName(),
		})
		resourceSelectors = append(resourceSelectors, additionalResRefs...)
		placementPolicy.Spec.ResourceSelectors = resourceSelectors
		return nil
	})
	if err != nil {
		wrappedErr := errors.NewAPIServerError(err, "", false,
			"request", req, "placementPolicy", client.ObjectKeyFromObject(placementPolicy), "op", resOp, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to create or update placement policy for the annotated object", kferrors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}
	klog.V(2).InfoS("Created or updated placement policy for the annotated object",
		"request", req, "placementPolicy", client.ObjectKeyFromObject(placementPolicy), "op", resOp, "controller", controllerName)
	return ctrl.Result{}, nil
}

func (r *Reconciler) deletePlacementPolicyFor(ctx context.Context, obj *unstructured.Unstructured) error {
	if controllerutil.ContainsFinalizer(obj, derivedPlacementPolicyCleanupFinalizer) {
		// Delete the corresponding placement policy (if any).
		placement := placementv1alpha1.PlacementPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      derivedPlacementPolicyName(obj.GetKind(), obj.GetName()),
				Namespace: obj.GetNamespace(),
			},
		}
		if err := r.HubClient.Delete(ctx, &placement); err != nil && !apierrors.IsNotFound(err) {
			return errors.NewAPIServerError(err, "failed to delete the placement policy for the annotated object", false)
		}

		// Remove the finalizer so that the annotated object can be deleted.
		controllerutil.RemoveFinalizer(obj, derivedPlacementPolicyCleanupFinalizer)
		if err := r.HubClient.Update(ctx, obj); err != nil {
			return errors.NewAPIServerError(err, "failed to remove finalizer from the annotated object", false)
		}
	}
	return nil
}

// parseRegionBasedClusterSelectors parses an environment-based cluster selectors annotation value
// into a sorted list of regions.
//
// The value is a list of semicolon-separated segments, each in the env=REGION format, e.g.
// "env=A;env=B".
func parseRegionBasedClusterSelectors(raw string) ([]string, error) {
	segments := strings.Split(raw, ";")
	regions := make([]string, 0, len(segments))
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}

		key, region, found := strings.Cut(segment, "=")
		if !found || key != "env" {
			return nil, fmt.Errorf("cluster selector segment %q is not in the env=REGION format", segment)
		}
		region = strings.TrimSpace(region)
		if region == "" {
			return nil, fmt.Errorf("cluster selector segment %q has an empty region", segment)
		}
		regions = append(regions, region)
	}
	if len(regions) == 0 {
		return nil, fmt.Errorf("no cluster selectors found")
	}

	sort.Strings(regions)
	return regions, nil
}

func (r *Reconciler) collectAdditionalResourceManifests(
	ctx context.Context, object *unstructured.Unstructured,
) ([]placementv1alpha1.ResourceSelector, error) {
	res := []placementv1alpha1.ResourceSelector{}

	if object.GetKind() != "Deployment" {
		return nil, nil
	}

	var deploy appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &deploy); err != nil {
		return nil, kferrors.NewUnexpectedError(err, "failed to convert the unstructured object into a deployment")
	}

	podTemplateSpec := deploy.Spec.Template.Spec
	volumes := podTemplateSpec.Volumes
	if len(volumes) == 0 {
		return nil, nil
	}

	for idx := range volumes {
		vol := &volumes[idx]

		switch {
		case vol.ConfigMap != nil:
			res = append(res, placementv1alpha1.ResourceSelector{
				Kind:       "ConfigMap",
				Name:       vol.ConfigMap.Name,
				APIGroup:   "",
				APIVersion: "v1",
			})
		case vol.Secret != nil:
			res = append(res, placementv1alpha1.ResourceSelector{
				Kind:       "Secret",
				Name:       vol.Secret.SecretName,
				APIGroup:   "",
				APIVersion: "v1",
			})
		}
	}
	return res, nil
}

// SetupWithManager registers the controller with the manager.
//
// The controller watches the two kinds this package knows how to place from an annotation --
// Deployments and ORASManifests -- directly through the manager's cache; an event on either
// enqueues a request for the object itself, tagged with its kind, since Reconcile reads that kind
// off the request rather than off the watched object (whose TypeMeta the cache leaves empty).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[Request](mgr).
		Named(controllerName).
		Watches(
			&appsv1.Deployment{},
			handler.TypedEnqueueRequestsFromMapFunc(mapObjectToRequest(utils.DeploymentGVK)),
		).
		Watches(
			&placementv1alpha1.ORASManifests{},
			handler.TypedEnqueueRequestsFromMapFunc(mapObjectToRequest(orasManifestsGVK)),
		).
		Complete(r)
}

// orasManifestsGVK is the GVK tagged onto requests enqueued for a watched ORASManifests object.
var orasManifestsGVK = placementv1alpha1.GroupVersion.WithKind("ORASManifests")

// mapObjectToRequest returns a map function that enqueues a request for the watched object, tagged
// with the given kind. The kind is fixed per watch rather than read off the object because objects
// delivered through the manager's cache do not carry it on their own (TypeMeta is left empty).
func mapObjectToRequest(gvk schema.GroupVersionKind) handler.TypedMapFunc[client.Object, Request] {
	return func(_ context.Context, obj client.Object) []Request {
		return []Request{{
			GroupVersionKind: gvk,
			NamespacedName:   client.ObjectKeyFromObject(obj),
		}}
	}
}
