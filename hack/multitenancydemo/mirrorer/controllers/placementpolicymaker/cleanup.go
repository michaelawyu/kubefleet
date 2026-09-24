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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
)

func (r *Reconciler) cleanup(ctx context.Context, obj *unstructured.Unstructured) (ctrl.Result, error) {
	objKey := client.ObjectKeyFromObject(obj)

	if !controllerutil.ContainsFinalizer(obj, placementPolicyMakerCleanupFinalizer) {
		klog.V(2).InfoS("The object does not have the cleanup finalizer; skipping",
			"controller", controllerName, "object", objKey)
		return ctrl.Result{}, nil
	}

	// Find the corresponding ResourcePlacement by following the same naming link used when it
	// was created, and remove the resource selector that corresponds to this object from it.
	if rpName, ok := resourcePlacementNameFor(obj); !ok {
		klog.V(2).InfoS("The object no longer carries the annotation/label needed to derive the ResourcePlacement name; skipping",
			"controller", controllerName, "object", objKey)
	} else {
		rpKey := types.NamespacedName{Namespace: obj.GetNamespace(), Name: rpName}
		rp := &placementv1beta1.ResourcePlacement{}
		if err := r.hostClusterClient.Get(ctx, rpKey, rp); err != nil {
			if !apierrors.IsNotFound(err) {
				klog.ErrorS(err, "Failed to retrieve the ResourcePlacement", "controller", controllerName, "resourcePlacement", rpKey)
				return ctrl.Result{}, err
			}
			klog.V(2).InfoS("The ResourcePlacement no longer exists; nothing to clean up",
				"controller", controllerName, "resourcePlacement", rpKey)
		} else {
			objGVK := obj.GroupVersionKind()
			remaining := rp.Spec.ResourceSelectors[:0]
			for _, sel := range rp.Spec.ResourceSelectors {
				if sel.Group == objGVK.Group && sel.Kind == objGVK.Kind && sel.Name == obj.GetName() {
					continue
				}
				remaining = append(remaining, sel)
			}

			if len(remaining) == 0 {
				// No resource selector is left; delete the ResourcePlacement outright, using
				// the resource version just read as a precondition, so that a concurrently
				// modified (e.g. re-populated) ResourcePlacement is not deleted by mistake.
				if err := r.hostClusterClient.Delete(ctx, rp, &client.DeleteOptions{
					Preconditions: &metav1.Preconditions{ResourceVersion: ptr.To(rp.ResourceVersion)},
				}); err != nil && !apierrors.IsNotFound(err) {
					klog.ErrorS(err, "Failed to delete the ResourcePlacement", "controller", controllerName, "resourcePlacement", rpKey)
					return ctrl.Result{}, err
				}
				klog.V(2).InfoS("Deleted the ResourcePlacement as it no longer has any resource selectors left",
					"controller", controllerName, "resourcePlacement", rpKey)
			} else {
				rp.Spec.ResourceSelectors = remaining
				if err := r.hostClusterClient.Update(ctx, rp); err != nil {
					klog.ErrorS(err, "Failed to update the ResourcePlacement", "controller", controllerName, "resourcePlacement", rpKey)
					return ctrl.Result{}, err
				}
				klog.V(2).InfoS("Removed the resource selector from the ResourcePlacement",
					"controller", controllerName, "resourcePlacement", rpKey)
			}
		}
	}

	// Remove the finalizer so that the object can actually be deleted.
	if controllerutil.RemoveFinalizer(obj, placementPolicyMakerCleanupFinalizer) {
		if err := r.hostClusterClient.Update(ctx, obj); err != nil {
			klog.ErrorS(err, "Failed to remove the cleanup finalizer from the object", "controller", controllerName, "object", objKey)
			return ctrl.Result{}, err
		}
		klog.V(2).InfoS("Removed the cleanup finalizer from the object", "controller", controllerName, "object", objKey)
	}

	return ctrl.Result{}, nil
}
