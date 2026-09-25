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

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
)

func (r *Reconciler) cleanup(ctx context.Context, obj *unstructured.Unstructured) (ctrl.Result, error) {
	objKey := client.ObjectKeyFromObject(obj)

	if !controllerutil.ContainsFinalizer(obj, placementPolicyMakerCleanupFinalizer) {
		klog.V(2).InfoS("The object does not have the cleanup finalizer; skipping",
			"controller", controllerName, "object", objKey)
		return ctrl.Result{}, nil
	}

	// Find the corresponding PlacementPolicy by following the same naming link used when it
	// was created, and remove the resource selector that corresponds to this object from it.
	if ppName, ok := placementPolicyNameFor(obj); !ok {
		klog.V(2).InfoS("The object no longer carries the annotation/label needed to derive the PlacementPolicy name; skipping",
			"controller", controllerName, "object", objKey)
	} else {
		ppKey := types.NamespacedName{Namespace: obj.GetNamespace(), Name: ppName}
		pp := &placementv1alpha1.PlacementPolicy{}
		if err := r.hostClusterClient.Get(ctx, ppKey, pp); err != nil {
			if !apierrors.IsNotFound(err) {
				klog.ErrorS(err, "Failed to retrieve the PlacementPolicy", "controller", controllerName, "placementPolicy", ppKey)
				return ctrl.Result{}, err
			}
			klog.V(2).InfoS("The PlacementPolicy no longer exists; nothing to clean up",
				"controller", controllerName, "placementPolicy", ppKey)
		} else {
			objGVK := obj.GroupVersionKind()
			remaining := pp.Spec.ResourceSelectors[:0]
			for _, sel := range pp.Spec.ResourceSelectors {
				if sel.APIGroup == objGVK.Group && sel.Kind == objGVK.Kind && sel.Name == obj.GetName() {
					continue
				}
				remaining = append(remaining, sel)
			}

			if len(remaining) == 0 {
				// No resource selector is left; delete the PlacementPolicy outright, using
				// the resource version just read as a precondition, so that a concurrently
				// modified (e.g. re-populated) PlacementPolicy is not deleted by mistake.
				if err := r.hostClusterClient.Delete(ctx, pp, &client.DeleteOptions{
					Preconditions: &metav1.Preconditions{ResourceVersion: ptr.To(pp.ResourceVersion)},
				}); err != nil && !apierrors.IsNotFound(err) {
					klog.ErrorS(err, "Failed to delete the PlacementPolicy", "controller", controllerName, "placementPolicy", ppKey)
					return ctrl.Result{}, err
				}
				klog.V(2).InfoS("Deleted the PlacementPolicy as it no longer has any resource selectors left",
					"controller", controllerName, "placementPolicy", ppKey)
			} else {
				pp.Spec.ResourceSelectors = remaining
				if err := r.hostClusterClient.Update(ctx, pp); err != nil {
					klog.ErrorS(err, "Failed to update the PlacementPolicy", "controller", controllerName, "placementPolicy", ppKey)
					return ctrl.Result{}, err
				}
				klog.V(2).InfoS("Removed the resource selector from the PlacementPolicy",
					"controller", controllerName, "placementPolicy", ppKey)
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
