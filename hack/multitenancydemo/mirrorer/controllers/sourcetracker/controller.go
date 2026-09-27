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

package sourcetracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

const (
	controllerName = "sourcetracker"

	// clusterSelectorsAnnotationKey is the annotation expected on the root object of an
	// ownership chain that marks it (and everything transitively owned by it) for placement
	// across multiple clusters.
	clusterSelectorsAnnotationKey                     = "kubefleet.dev/cluster-selectors-from-tenant"
	clusterSelectorsLastFetchedTimestampAnnotationKey = "kubefleet.dev/cluster-selectors-from-tenant-last-fetched-timestamp"

	sourceObjectAnnotationKey = "kubefleet.dev/source-object"
	sourceHashLabelKey        = "kubefleet.dev/source-hash"

	// requeueInterval is the fixed interval at which a successful reconcile is requeued. This
	// controller only watches Pods, so a change made solely to a Pod's owner (e.g. the
	// cluster-selectors annotation being added, removed, or updated on the root of the
	// ownership chain) would otherwise never trigger a new reconcile; periodic requeuing lets
	// such changes still get picked up, at the cost of some steady-state polling. This is
	// acceptable for demo purposes.
	requeueInterval = 5 * time.Second
)

// podGVK is the GroupVersionKind for the core Pod type; it is used to build Requests out of the
// Pod events the syncer controller watches for.
var podGVK = corev1.SchemeGroupVersion.WithKind("Pod")

type Request struct {
	gvk            schema.GroupVersionKind
	namespacedName types.NamespacedName
}

type Reconciler struct {
	vclusterClient    client.Client
	hostClusterClient client.Client
}

// New creates a new Reconciler that talks to the vcluster via vclusterClient and to
// the host cluster via hostClusterClient.
func New(vclusterClient, hostClusterClient client.Client) *Reconciler {
	return &Reconciler{
		vclusterClient:    vclusterClient,
		hostClusterClient: hostClusterClient,
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, req Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "request", req, "controller", controllerName)
	defer func() {
		klog.V(2).InfoS("Reconciliation ends", "request", req, "controller", controllerName, "latency", time.Since(startTime).Milliseconds())
	}()

	// Retrieve the object from the vcluster.
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(req.gvk)
	if err := r.vclusterClient.Get(ctx, req.namespacedName, obj); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(2).InfoS("Object not found in the vcluster; skipping", "request", req, "controller", controllerName)
			return ctrl.Result{RequeueAfter: requeueInterval}, nil
		}
		klog.ErrorS(err, "Failed to retrieve the object from the vcluster", "request", req, "controller", controllerName)
		return ctrl.Result{}, err
	}
	klog.V(2).InfoS("Retrieved the object from the vcluster", "request", req, "controller", controllerName, "resourceVersion", obj.GetResourceVersion())

	// Look up the owner references recursively; retrieve the owner object, and check if it has been set for
	// placement across multiple clusters (via the kubefleet.dev/cluster-selectors annotation).
	rootObj := obj
	for {
		ownerRefs := rootObj.GetOwnerReferences()
		if len(ownerRefs) == 0 {
			// rootObj has no owner of its own; it is the root of the ownership chain.
			break
		}
		if len(ownerRefs) > 1 {
			err := fmt.Errorf("object %s/%s (%s) has %d owner references; only a single owner reference is supported",
				rootObj.GetNamespace(), rootObj.GetName(), rootObj.GroupVersionKind(), len(ownerRefs))
			klog.ErrorS(err, "Rejecting an object with multiple owner references", "request", req, "controller", controllerName)
			// This is considered a user error rather than a transient failure; still requeue
			// after the usual interval (rather than not at all) so that the object is picked up
			// again automatically once the user fixes the owner references.
			return ctrl.Result{RequeueAfter: requeueInterval}, nil
		}

		ownerRef := ownerRefs[0]
		ownerGVK := schema.FromAPIVersionAndKind(ownerRef.APIVersion, ownerRef.Kind)
		// Owner references never carry a namespace: an owner is either cluster-scoped, or
		// namespaced and, by API convention, in the same namespace as the dependent object. The
		// client resolves whether the namespace segment actually applies based on the owner's
		// GVK, so it is safe to pass it along unconditionally here.
		ownerKey := types.NamespacedName{Namespace: rootObj.GetNamespace(), Name: ownerRef.Name}
		ownerObj := &unstructured.Unstructured{}
		ownerObj.SetGroupVersionKind(ownerGVK)
		if err := r.vclusterClient.Get(ctx, ownerKey, ownerObj); err != nil {
			klog.ErrorS(err, "Failed to retrieve the owner object from the vcluster",
				"request", req, "owner", ownerKey, "ownerGVK", ownerGVK, "controller", controllerName)
			return ctrl.Result{}, err
		}
		rootObj = ownerObj
	}

	_, hasClusterSelectorsAnnotation := rootObj.GetAnnotations()[clusterSelectorsAnnotationKey]
	klog.V(2).InfoS("Determined the root of the ownership chain",
		"request", req, "controller", controllerName,
		"root", types.NamespacedName{Namespace: rootObj.GetNamespace(), Name: rootObj.GetName()},
		"rootGVK", rootObj.GroupVersionKind(), "hasClusterSelectorsAnnotation", hasClusterSelectorsAnnotation)
	if !hasClusterSelectorsAnnotation {
		klog.V(2).InfoS("The root of the ownership chain does not have the cluster-selectors annotation; skipping",
			"request", req, "controller", controllerName)

		// The object may still carry the source object, cluster selectors, and source hash
		// annotations/label from an earlier reconcile (e.g. the cluster-selectors annotation
		// was since removed from the root); clean those up so the object no longer looks like
		// it is opted into multi-cluster placement.
		annotations := obj.GetAnnotations()
		labels := obj.GetLabels()
		_, hasSourceObjectAnnotation := annotations[sourceObjectAnnotationKey]
		_, hasClusterSelectorsAnnotationOnObj := annotations[clusterSelectorsAnnotationKey]
		_, hasClusterSelectorsLastFetchedTimestampAnnotation := annotations[clusterSelectorsLastFetchedTimestampAnnotationKey]
		_, hasSourceHashLabel := labels[sourceHashLabelKey]
		if hasSourceObjectAnnotation || hasClusterSelectorsAnnotationOnObj || hasClusterSelectorsLastFetchedTimestampAnnotation || hasSourceHashLabel {
			delete(annotations, sourceObjectAnnotationKey)
			delete(annotations, clusterSelectorsAnnotationKey)
			delete(annotations, clusterSelectorsLastFetchedTimestampAnnotationKey)
			obj.SetAnnotations(annotations)
			delete(labels, sourceHashLabelKey)
			obj.SetLabels(labels)

			if err := r.vclusterClient.Update(ctx, obj); err != nil {
				klog.ErrorS(err, "Failed to remove the stale source object, cluster selectors, and source hash annotations/label from the object",
					"request", req, "controller", controllerName)
				return ctrl.Result{}, err
			}
			klog.V(2).InfoS("Removed the stale source object, cluster selectors, and source hash annotations/label from the object",
				"request", req, "controller", controllerName)
		}

		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	// Add the source object, cluster selectors, and cluster selectors last fetched annotation to the leaf object.
	sourceObjectRef := fmt.Sprintf("%s/%s/%s/%s/%s", req.gvk.Group, req.gvk.Version, req.gvk.Kind, obj.GetNamespace(), obj.GetName())
	newClusterSelectors := rootObj.GetAnnotations()[clusterSelectorsAnnotationKey]

	// Track whether anything actually needs to change on the object; the annotations/labels
	// below are already at their intended values on most reconciles (e.g. ones triggered by an
	// unrelated field update), and issuing an Update call in that case would just re-trigger
	// this very reconcile loop via the resulting Pod update event, forever, without ever making
	// progress.
	changed := false

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	if annotations[sourceObjectAnnotationKey] != sourceObjectRef {
		annotations[sourceObjectAnnotationKey] = sourceObjectRef
		changed = true
	}
	// Only refresh the cluster selectors (and the last-fetched timestamp) when the cluster
	// selectors value actually changes; otherwise, an unrelated reconcile (e.g. one triggered by
	// an unrelated field update) would keep bumping the timestamp even though nothing about the
	// placement policy changed.
	if oldClusterSelectors := annotations[clusterSelectorsAnnotationKey]; oldClusterSelectors != newClusterSelectors {
		annotations[clusterSelectorsAnnotationKey] = newClusterSelectors
		annotations[clusterSelectorsLastFetchedTimestampAnnotationKey] = time.Now().UTC().Format(time.RFC3339)
		changed = true
	}
	obj.SetAnnotations(annotations)

	// Add the source hash label to the object.
	sourceHash := sha256.Sum256([]byte(sourceObjectRef))
	// Kubernetes label values are capped at 63 characters; a full sha256 hex digest is 64
	// characters (one over the limit), so it is truncated to 32 hex characters (128 bits),
	// which remains collision-resistant enough for this purpose.
	sourceHashLabelValue := hex.EncodeToString(sourceHash[:])[:32]

	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	if labels[sourceHashLabelKey] != sourceHashLabelValue {
		labels[sourceHashLabelKey] = sourceHashLabelValue
		changed = true
	}
	obj.SetLabels(labels)

	if !changed {
		klog.V(2).InfoS("The object already carries up-to-date source object, cluster selectors, and source hash annotations/label; skipping update",
			"request", req, "controller", controllerName)
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	if err := r.vclusterClient.Update(ctx, obj); err != nil {
		klog.ErrorS(err, "Failed to update the object with the source object, cluster selectors, and cluster selectors last fetched annotations",
			"request", req, "controller", controllerName)
		return ctrl.Result{}, err
	}
	klog.V(2).InfoS("Updated the object with the source object, cluster selectors, and cluster selectors last fetched annotations",
		"request", req, "controller", controllerName)

	return ctrl.Result{RequeueAfter: requeueInterval}, nil
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

// SetupWithManager sets up the controller with the given manager. For now, it watches only
// Pods (in the vcluster), using the custom podEventHandler above to enqueue Requests rather than
// the default reconcile.Request; a plain For(...) call cannot be used here, as its enqueue
// behavior is hardwired to reconcile.Request and does not support a custom request type.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[Request](mgr).
		Named(controllerName).
		Watches(&corev1.Pod{}, &podEventHandler{}).
		Complete(r)
}
