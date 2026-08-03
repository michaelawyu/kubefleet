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

package placementbinding

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/reimagined/ociartifactcachedlocalfsstore"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	controllerName = "PlacementBindingController"

	placementBindingCleanupFinalizer = "experimental.kubefleet.dev/placement-binding-cleanup"

	workNameFmt                = "%s-0"
	memberClusterReservedNSFmt = "fleet-member-%s"
)

type Reconciler struct {
	HubClient client.Client

	OCIArtifactCachedStoreManager *ociartifactcachedlocalfsstore.Manager

	UseHTTPToConnectToOCIRegistry bool
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "placementBinding", req.NamespacedName, "controller", controllerName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends", "placementBinding", req.NamespacedName, "controller", controllerName, "latency", latency)
	}()

	// Retrieve the PlacementBinding object.
	placementBinding := &experimentalv1beta1.PlacementBinding{}
	err := r.HubClient.Get(ctx, req.NamespacedName, placementBinding)
	switch {
	case apierrors.IsNotFound(err):
		// The binding cannot be found; it may have been deleted already. No need for
		// further reconciliation.
		klog.V(2).InfoS("The binding object cannot be found", "placementBinding", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	case err != nil:
		// An error occurred when trying to retrieve the binding object; retry later.
		wrappedErr := errors.NewAPIServerError(err, "", true, "placementBinding", req.NamespacedName, "controllerName", controllerName)
		klog.ErrorS(wrappedErr, "Failed to get placement binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	if !placementBinding.DeletionTimestamp.IsZero() || placementBinding.Spec.Suspended {
		// Perform some cleanup.
		if err := r.cleanupWorks(ctx, placementBinding); err != nil {
			klog.ErrorS(err, "Failed to clean up work objects", append(errors.Args(err),
				"placementBinding", req.NamespacedName, "controllerName", controllerName)...)
			return ctrl.Result{}, err
		}

		// The binding has been marked for deletion; drop its cleanup finalizer.
		if err := r.removeFinalizer(ctx, placementBinding); err != nil {
			klog.ErrorS(err, "Failed to remove cleanup finalizer", append(errors.Args(err),
				"placementBinding", req.NamespacedName, "controllerName", controllerName)...)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Add the cleanup finalizer if not already added.
	if !controllerutil.ContainsFinalizer(placementBinding, placementBindingCleanupFinalizer) {
		controllerutil.AddFinalizer(placementBinding, placementBindingCleanupFinalizer)
		if err := r.HubClient.Update(ctx, placementBinding); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", true, "placementBinding", req.NamespacedName, "controllerName", controllerName)
			klog.ErrorS(wrappedErr, "Failed to add finalizer to placement binding")
			return ctrl.Result{}, wrappedErr
		}
	}

	// Check if a member cluster has been selected.
	if len(placementBinding.Spec.ClusterName) == 0 {
		klog.V(2).InfoS("No member cluster has been selected yet; should wait for the scheduling to conclude",
			"placementBinding", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	}

	// Check if a resource snapshot has been associated.
	if placementBinding.Spec.ResourceSnapshotName == nil || len(*placementBinding.Spec.ResourceSnapshotName) == 0 {
		klog.V(2).InfoS("No resource snapshot has been associated with the binding yet; a rollout might be needed",
			"placementBinding", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	}

	// Retrieve the corresponding Work object.
	work := &placementv1beta1.Work{}
	workName := fmt.Sprintf(workNameFmt, placementBinding.Name)
	workNSName := fmt.Sprintf(memberClusterReservedNSFmt, placementBinding.Spec.ClusterName)
	workFound := true
	if err := r.HubClient.Get(ctx, client.ObjectKey{Namespace: workNSName, Name: workName}, work); err != nil {
		if apierrors.IsNotFound(err) {
			workFound = false
		} else {
			wrappedErr := errors.NewAPIServerError(err, "", true, "work", client.ObjectKey{Namespace: workNSName, Name: workName}, "controllerName", controllerName)
			klog.ErrorS(wrappedErr, "Failed to get the corresponding Work object for the binding", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
	}

	if workFound {
		curDerivedFromSnapshotRevision := work.Annotations[experimentalv1beta1.WorkDerivedFromResourceSnapshotAnnotationKey]
		if curDerivedFromSnapshotRevision == *placementBinding.Spec.ResourceSnapshotName {
			// The work object is found and up-to-date; no update is needed.
			klog.V(2).InfoS("The corresponding Work object for the binding is up-to-date; no update is needed", "work", klog.KObj(work), "controller", controllerName)

			// Refresh the status of the binding.
			updatedBinding := placementBinding.DeepCopy()

			prepareSynchronizedCondition(work, updatedBinding)
			prepareAllResourcesAvailableCondition(work, updatedBinding)

			// Write the status.
			if !equality.Semantic.DeepEqual(placementBinding.Status, updatedBinding.Status) {
				if err := r.HubClient.Status().Update(ctx, updatedBinding); err != nil {
					wrappedErr := errors.NewAPIServerError(err, "", true, "placementBinding", req.NamespacedName, "controllerName", controllerName)
					klog.ErrorS(wrappedErr, "Failed to update the status of the binding", errors.Args(wrappedErr)...)
					return ctrl.Result{}, wrappedErr
				}
				klog.V(2).InfoS("Updated the status of the binding", "placementBinding", req.NamespacedName, "controller", controllerName)
			} else {
				klog.V(2).InfoS("The status of the binding is up-to-date; no update is needed", "placementBinding", req.NamespacedName, "controller", controllerName)
			}

			return ctrl.Result{}, nil
		}
	}

	// Create or update the corresponding Work object for the binding.

	// Retrieve the associated resource snapshot.
	resourceSnapshot := &experimentalv1beta1.PlacementResourceSnapshot{}
	if err := r.HubClient.Get(ctx, client.ObjectKey{Namespace: placementBinding.Namespace, Name: *placementBinding.Spec.ResourceSnapshotName}, resourceSnapshot); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "", true,
			"placementResourceSnapshot", req.NamespacedName, "controllerName", controllerName)
		klog.ErrorS(wrappedErr, "Failed to get the associated resource snapshot for the binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	manifests, err := r.extractManifestsFrom(ctx, resourceSnapshot)
	if err != nil {
		wrappedErr := errors.Wraps(err, "",
			"placementResourceSnapshot", req.NamespacedName, "controllerName", controllerName)
		klog.ErrorS(wrappedErr, "Failed to extract manifests from the associated resource snapshot for the binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	workToCreateOrUpdate := &placementv1beta1.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workName,
			Namespace: workNSName,
		},
	}
	resOp, err := controllerutil.CreateOrUpdate(ctx, r.HubClient, workToCreateOrUpdate, func() error {
		if workToCreateOrUpdate.Labels == nil {
			workToCreateOrUpdate.Labels = make(map[string]string)
		}
		workToCreateOrUpdate.Labels[experimentalv1beta1.WorkOwnedByPlacementBindingLabelKey] = placementBinding.Name
		workToCreateOrUpdate.Labels[experimentalv1beta1.WorkOwnerNamespaceLabelKey] = placementBinding.Namespace
		workToCreateOrUpdate.Labels[experimentalv1beta1.WorkOwnedByPlacementPolicyLabelKey] = placementBinding.Spec.PlacementPolicyName

		if workToCreateOrUpdate.Annotations == nil {
			workToCreateOrUpdate.Annotations = make(map[string]string)
		}
		workToCreateOrUpdate.Annotations[experimentalv1beta1.WorkDerivedFromResourceSnapshotAnnotationKey] = *placementBinding.Spec.ResourceSnapshotName

		workToCreateOrUpdate.Spec = placementv1beta1.WorkSpec{
			Workload: placementv1beta1.WorkloadTemplate{
				Manifests: manifests,
			},
		}
		return nil
	})
	if err != nil {
		wrappedErr := errors.NewAPIServerError(err, "", true, "work", klog.KObj(workToCreateOrUpdate), "controllerName", controllerName, "op", resOp)
		klog.ErrorS(wrappedErr, "Failed to create or update the corresponding Work object for the binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}
	klog.V(2).InfoS("Created or updated the corresponding Work object for the binding",
		"work", klog.KObj(workToCreateOrUpdate), "controller", controllerName, "op", resOp)
	return ctrl.Result{}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&experimentalv1beta1.PlacementBinding{}).
		Watches(&placementv1beta1.Work{}, handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
			work, ok := obj.(*placementv1beta1.Work)
			if !ok {
				// The object is not a Work object; report an unexpected error and ignore it.
				err := errors.NewUnexpectedError(nil, "The object to enqueue is not a Work object; ignoring it", "object", klog.KObj(obj), "controller", controllerName)
				klog.ErrorS(err, "Failed to enqueue an object for processing", errors.Args(err)...)
				return nil
			}

			// Enqueue the corresponding PlacementBinding object for reconciliation.
			placementBindingName, ok := work.Labels[experimentalv1beta1.WorkOwnedByPlacementBindingLabelKey]
			if !ok {
				// The Work object is not associated with any PlacementBinding; report an unexpected error and ignore it.
				err := errors.NewUnexpectedError(nil, "The Work object is not associated with any PlacementBinding as the label is missing; ignoring it", "work", klog.KObj(work), "controller", controllerName)
				klog.V(4).ErrorS(err, "Failed to enqueue a Work object for processing", errors.Args(err)...)
				return nil
			}
			placementBindingNSName, ok := work.Labels[experimentalv1beta1.WorkOwnerNamespaceLabelKey]
			if !ok {
				// The Work object is not associated with any PlacementBinding; report an unexpected error and ignore it.
				err := errors.NewUnexpectedError(nil, "The Work object is not associated with any PlacementBinding as the namespace label is missing; ignoring it", "work", klog.KObj(work), "controller", controllerName)
				klog.V(4).ErrorS(err, "Failed to enqueue a Work object for processing", errors.Args(err)...)
				return nil
			}
			req := ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name:      placementBindingName,
					Namespace: placementBindingNSName,
				},
			}
			klog.V(2).InfoS("Found an updated work object; enqueue its owner binding for further processing", "work", klog.KObj(work), "placementBinding", req.NamespacedName, "controller", controllerName)
			return []ctrl.Request{req}
		})).
		Complete(r)
}
