/*
Copyright 2025 The KubeFleet Authors.

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

package placementpolicymaker

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
)

const (
	controllerName = "placementpolicymaker"

	// clusterSelectorsAnnotationKey is the annotation expected on the root object of an
	// ownership chain that marks it (and everything transitively owned by it) for placement
	// across multiple clusters.
	clusterSelectorsAnnotationKey                     = "kubefleet.dev/cluster-selectors-from-tenant"
	clusterSelectorsLastFetchedTimestampAnnotationKey = "kubefleet.dev/cluster-selectors-from-tenant-last-fetched-timestamp"

	sourceObjectAnnotationKey = "kubefleet.dev/source-object"
	sourceHashLabelKey        = "kubefleet.dev/source-hash"

	placementPolicyClusterSelectorsLastUpdatedTimestampAnnotationKey = "kubefleet.dev/placement-policy-cluster-selectors-last-updated-timestamp"

	// regionLabelKey is the well-known node/cluster topology label used as the region label
	// selector on the generated placement policy.
	regionLabelKey = "topology.kubernetes.io/region"

	placementPolicyMakerCleanupFinalizer = "kubefleet.dev/placement-policy-maker-cleanup"
)

// podGVK is the GroupVersionKind for the core Pod type; it is used to build Requests out of the
// Pod events this controller watches for.
var podGVK = corev1.SchemeGroupVersion.WithKind("Pod")

type Request struct {
	gvk            schema.GroupVersionKind
	namespacedName types.NamespacedName
}

type Reconciler struct {
	hostClusterClient client.Client
}

// resourcePlacementNameFor derives the name of the ResourcePlacement corresponding to obj, using
// the same recipe used when the ResourcePlacement is first created: the source object reference
// (with slashes and dots stripped) plus the first 12 characters of the source hash. It reports
// ok as false if obj does not (or no longer) carries the annotation/label needed to derive the
// name, in which case name is meaningless.
func resourcePlacementNameFor(obj *unstructured.Unstructured) (name string, ok bool) {
	sourceObjectRef, hasSourceObjectRef := obj.GetAnnotations()[sourceObjectAnnotationKey]
	sourceHash, hasSourceHash := obj.GetLabels()[sourceHashLabelKey]
	if !hasSourceObjectRef || !hasSourceHash || len(sourceHash) < 12 {
		return "", false
	}

	strippedSourceObjectRef := strings.NewReplacer("/", "", ".", "").Replace(sourceObjectRef)
	return fmt.Sprintf("%s-%s", strippedSourceObjectRef, sourceHash[:12]), true
}

func (r *Reconciler) Reconcile(ctx context.Context, req Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "request", req, "controller", controllerName)
	defer func() {
		klog.V(2).InfoS("Reconciliation ends", "request", req, "controller", controllerName, "latency", time.Since(startTime).Milliseconds())
	}()

	// Retrieve the object.
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(req.gvk)
	if err := r.hostClusterClient.Get(ctx, req.namespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(2).InfoS("Object not found in the host cluster; skipping", "request", req, "controller", controllerName)
			return ctrl.Result{}, nil
		}
		klog.ErrorS(err, "Failed to retrieve the object from the host cluster", "request", req, "controller", controllerName)
		return ctrl.Result{}, err
	}
	klog.V(2).InfoS("Retrieved the object from the host cluster", "request", req, "controller", controllerName, "resourceVersion", obj.GetResourceVersion())

	if obj.GetDeletionTimestamp() != nil {
		// The object is marked for deletion; clean things up.
		return r.cleanup(ctx, obj)
	}

	// Add a finalizer to the object if it doesn't already have one.
	if controllerutil.AddFinalizer(obj, placementPolicyMakerCleanupFinalizer) {
		if err := r.hostClusterClient.Update(ctx, obj); err != nil {
			klog.ErrorS(err, "Failed to add the cleanup finalizer to the object",
				"request", req, "controller", controllerName)
			return ctrl.Result{}, err
		}
		klog.V(2).InfoS("Added the cleanup finalizer to the object", "request", req, "controller", controllerName)
	}

	// Check if the required annotations and labels are present on the object.
	annotations := obj.GetAnnotations()
	requiredAnnotationKeys := []string{sourceObjectAnnotationKey, clusterSelectorsAnnotationKey, clusterSelectorsLastFetchedTimestampAnnotationKey}
	for _, k := range requiredAnnotationKeys {
		if _, ok := annotations[k]; !ok {
			klog.V(2).InfoS("Required annotation is absent from the object; skipping",
				"request", req, "controller", controllerName, "annotation", k)
			return ctrl.Result{}, nil
		}
	}
	if _, ok := obj.GetLabels()[sourceHashLabelKey]; !ok {
		klog.V(2).InfoS("Required label is absent from the object; skipping",
			"request", req, "controller", controllerName, "label", sourceHashLabelKey)
		return ctrl.Result{}, nil
	}

	// Parse the cluster selectors from the annotation.
	clusterSelectors := annotations[clusterSelectorsAnnotationKey]
	// For this demo, the cluster selectors annotation is always in the "region=X" format; parse
	// out the region (X) value.
	region := strings.TrimPrefix(clusterSelectors, "region=")
	klog.V(2).InfoS("Parsed the cluster selectors annotation",
		"request", req, "controller", controllerName, "clusterSelectors", clusterSelectors, "region", region)

	// Create or update the corresponding placement policy.
	rpName, _ := resourcePlacementNameFor(obj)

	rp := &placementv1beta1.ResourcePlacement{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: obj.GetNamespace(),
			Name:      rpName,
		},
	}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.hostClusterClient, rp, func() error {
		// Ensure a resource selector exists for the object's Group/Kind/Name (Version is
		// ignored: the same logical resource type observed at a different served version
		// should still map to the same selector). The object is selected directly by name
		// rather than via a label selector.
		found := false
		for i := range rp.Spec.ResourceSelectors {
			sel := rp.Spec.ResourceSelectors[i]
			if sel.Group == req.gvk.Group && sel.Kind == req.gvk.Kind && sel.Name == obj.GetName() {
				found = true
				break
			}
		}
		if !found {
			rp.Spec.ResourceSelectors = append(rp.Spec.ResourceSelectors, placementv1beta1.ResourceSelectorTerm{
				Group:   req.gvk.Group,
				Version: req.gvk.Version,
				Kind:    req.gvk.Kind,
				Name:    obj.GetName(),
			})
		}

		sourceFetchedStr := annotations[clusterSelectorsLastFetchedTimestampAnnotationKey]
		sourceFetched, err := time.Parse(time.RFC3339, sourceFetchedStr)
		if err != nil {
			return fmt.Errorf("failed to parse the source object's cluster selectors last fetched timestamp %q: %w", sourceFetchedStr, err)
		}

		// Determine whether the placement policy is already up to date: it is only stale (and
		// needs updating) if it has never been updated before, or if it was last updated using
		// an older cluster selectors value than the one just fetched from the source object.
		needsUpdate := true
		if lastUpdatedStr, ok := rp.GetAnnotations()[placementPolicyClusterSelectorsLastUpdatedTimestampAnnotationKey]; ok {
			lastUpdated, err := time.Parse(time.RFC3339, lastUpdatedStr)
			if err != nil {
				return fmt.Errorf("failed to parse the placement policy's cluster selectors last updated timestamp %q: %w", lastUpdatedStr, err)
			}
			needsUpdate = lastUpdated.Before(sourceFetched)
		}
		if !needsUpdate {
			klog.V(2).InfoS("The placement policy's cluster selectors are already up to date; skipping the update",
				"request", req, "controller", controllerName, "resourcePlacement", client.ObjectKeyFromObject(rp))
			return nil
		}

		rp.Spec.Policy = &placementv1beta1.PlacementPolicy{
			PlacementType:    placementv1beta1.PickNPlacementType,
			NumberOfClusters: ptr.To(int32(1)),
			Affinity: &placementv1beta1.Affinity{
				ClusterAffinity: &placementv1beta1.ClusterAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &placementv1beta1.ClusterSelector{
						ClusterSelectorTerms: []placementv1beta1.ClusterSelectorTerm{
							{
								LabelSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										regionLabelKey: region,
									},
								},
							},
						},
					},
				},
			},
		}

		// Record the source timestamp that this update was based on (rather than the current
		// wall-clock time), so that the comparison above remains a proper high-water mark even
		// if this reconcile runs well after the source object was actually last fetched.
		rpAnnotations := rp.GetAnnotations()
		if rpAnnotations == nil {
			rpAnnotations = map[string]string{}
		}
		rpAnnotations[placementPolicyClusterSelectorsLastUpdatedTimestampAnnotationKey] = sourceFetchedStr
		rp.SetAnnotations(rpAnnotations)

		return nil
	}); err != nil {
		klog.ErrorS(err, "Failed to create or update the ResourcePlacement",
			"request", req, "controller", controllerName, "resourcePlacement", client.ObjectKeyFromObject(rp))
		return ctrl.Result{}, err
	}
	klog.V(2).InfoS("Created or updated the ResourcePlacement",
		"request", req, "controller", controllerName, "resourcePlacement", client.ObjectKeyFromObject(rp))

	return ctrl.Result{}, nil
}

// podEventHandler is a custom handler.TypedEventHandler that, for every Pod create, update,
// delete, or generic event, enqueues a Request carrying the Pod GVK and the Pod's namespaced
// name (rather than a plain reconcile.Request, which Request is not).
type podEventHandler struct{}

func (h *podEventHandler) Create(_ context.Context, evt event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Request]) {
	enqueuePodRequest(evt.Object, q)
}

func (h *podEventHandler) Update(_ context.Context, evt event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[Request]) {
	enqueuePodRequest(evt.ObjectNew, q)
}

func (h *podEventHandler) Delete(_ context.Context, evt event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[Request]) {
	enqueuePodRequest(evt.Object, q)
}

func (h *podEventHandler) Generic(_ context.Context, evt event.TypedGenericEvent[client.Object], q workqueue.TypedRateLimitingInterface[Request]) {
	enqueuePodRequest(evt.Object, q)
}

// enqueuePodRequest builds a Request for the given Pod object and adds it to the workqueue.
func enqueuePodRequest(obj client.Object, q workqueue.TypedRateLimitingInterface[Request]) {
	q.Add(Request{
		gvk: podGVK,
		namespacedName: types.NamespacedName{
			Namespace: obj.GetNamespace(),
			Name:      obj.GetName(),
		},
	})
}

// SetupWithManager sets up the controller with the given manager. For now, it watches only Pod
// objects, using the custom podEventHandler above to enqueue Requests rather than the default
// reconcile.Request; a plain For(...) call cannot be used here, as its enqueue behavior is
// hardwired to reconcile.Request and does not support a custom request type.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[Request](mgr).
		Named(controllerName).
		Watches(&corev1.Pod{}, &podEventHandler{}).
		Complete(r)
}
