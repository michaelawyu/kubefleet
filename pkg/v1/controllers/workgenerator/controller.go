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

package workgenerator

import (
	"context"
	"time"

	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	parallelizerutil "github.com/kubefleet-dev/kubefleet/pkg/utils/parallelizer"
)

const (
	controllerName = "work-generator"

	workGeneratorCleanupFinalizer = "placement.kubefleet.dev/work-generator-cleanup"
)

type Reconciler struct {
	hubClient client.Client

	parallelizer parallelizerutil.Parallelizer
}

func New(hubClient client.Client, workerCnt int) *Reconciler {
	parallelizer := parallelizerutil.NewParallelizer(workerCnt)

	return &Reconciler{
		hubClient:    hubClient,
		parallelizer: parallelizer,
	}
}

// TO-DO (chenyu1): switch to field-based indexes for better performance when listing objects.

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "placementBinding", req.NamespacedName, "controller", controllerName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends", "placementBinding", req.NamespacedName, "controller", controllerName, "latency", latency)
	}()

	// Retrieve the PlacementBinding object.
	placementBinding, err := r.retrievePlacementBinding(ctx, req.NamespacedName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(2).InfoS("placement binding is not found", "namespacedName", req.NamespacedName, "controller", controllerName)
			return ctrl.Result{}, nil
		}
		klog.ErrorS(err, "", "namespacedName", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, errors.Wraps(err, "", "namespacedName", req.NamespacedName, "controller", controllerName)
	}

	placementBindingSpec := placementBinding.GetSpec()
	// Clean up the work objects for the placement binding if it has been marked for deletion or if it has been
	// suspended.
	if placementBinding.GetDeletionTimestamp() != nil || placementBindingSpec.Suspended {
		if err := r.cleanupWorks(ctx, placementBinding); err != nil {
			wrappedErr := errors.Wraps(err, "failed to clean up work objects for placement binding",
				"placementBinding", klog.KObj(placementBinding), "controller", controllerName)
			klog.ErrorS(wrappedErr, "failed to clean up work objects for placement binding",
				errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
		return ctrl.Result{}, nil
	}
	// Add the cleanup finalizer if it is not already present.
	if err := r.addPlacementBindingCleanupFinalizer(ctx, placementBinding); err != nil {
		wrappedErr := errors.Wraps(err, "failed to add cleanup finalizer to placement binding",
			"placementBinding", klog.KObj(placementBinding), "controller", controllerName)
		klog.ErrorS(wrappedErr, "failed to add cleanup finalizer to placement binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Do a sanity check; verify if a target cluster and a (primary) placement resource snapshot have been assigned.
	if len(placementBindingSpec.ClusterName) == 0 || len(placementBindingSpec.ResourceSnapshotName) == 0 {
		wrappedErr := errors.NewUnexpectedError(nil, "the placement binding does not have a target cluster or a placement resource snapshot assigned",
			"placementBinding", klog.KObj(placementBinding), "controller", controllerName)
		klog.ErrorS(wrappedErr, "failed to process placement binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Retrieve the Work objects owned by the placement binding.
	works, err := r.listWorksByOwnerBinding(ctx, placementBindingSpec.ClusterName, placementBinding.GetNamespace(), placementBinding.GetName())
	if err != nil {
		wrappedErr := errors.Wraps(err, "", "placementBinding", klog.KObj(placementBinding),
			"targetCluster", placementBindingSpec.ClusterName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "failed to list work objects owned by binding", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Check if the Work objects are consistent with the assigned primary and secondary placement resource snapshots.
	// If so, no need to update the spec of the Work objects; just sync the status back to the placement binding
	// instead.
	//
	// Note (chenyu1): this check is intended as a shortcut to avoid constant re-generation and validation of
	// work objects (which can be expensive when there are a large number of manifests to place); once the controller
	// signals that it has completed processing a placement binding given a specific configuration (a specific
	// set of placement resource snapshots) and generated all the needed work objects, the control loop will skip
	// to status reporting. In general we do not try to guard against byzantine faults here, especially
	// considering that work objects are KubeFleet internal API objects that reside in reserved namespaces; if a
	// non-KubeFleet agent decides to tamper with work objects, the system is not guaranteed to auto-recover.
	// The changes, however, will be overwritten upon rollouts.
	if areWorksUpToDate(placementBinding, works) {
		if err := r.refreshPlacementBindingStatus(ctx, placementBinding, works); err != nil {
			wrappedErr := errors.Wraps(err, "failed to refresh placement binding status",
				"placementBinding", klog.KObj(placementBinding), "targetCluster", placementBindingSpec.ClusterName,
				"controller", controllerName)
			klog.ErrorS(wrappedErr, "failed to refresh placement binding status", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
		return ctrl.Result{}, nil
	}

	// The Work objects are absent or not up-to-date. Retrieve the placement resource snapshots and create/update
	// the Work objects accordingly.

	// Retrieve the assigned primary and secondary placement resource snapshots referenced by the placement binding.
	placementResourceSnapshots, err := r.retrievePrimaryAndSecondaryPlacementResourceSnapshots(ctx, placementBinding)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to retrieve placement resource snapshots",
			"placementBinding", klog.KObj(placementBinding), "targetCluster", placementBindingSpec.ClusterName,
			"controller", controllerName)
		klog.ErrorS(wrappedErr, "failed to retrieve placement resource snapshots referenced by binding",
			errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Create or update the work objects.
	createdOrUpdatedWorks, err := r.refreshWorks(ctx, placementBinding, placementResourceSnapshots, works)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to refresh work objects",
			"placementBinding", klog.KObj(placementBinding), "targetCluster", placementBindingSpec.ClusterName,
			"controller", controllerName)
		klog.ErrorS(wrappedErr, "failed to refresh work objects for placement binding",
			errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Report the processing progress via placement binding status.
	if err := r.reportPlacementBindingProcessingProgress(ctx, placementBinding, placementResourceSnapshots[0], createdOrUpdatedWorks); err != nil {
		wrappedErr := errors.Wraps(err, "failed to report placement binding processing progress",
			"placementBinding", klog.KObj(placementBinding), "targetCluster", placementBindingSpec.ClusterName,
			"controller", controllerName)
		klog.ErrorS(wrappedErr, "failed to report placement binding processing progress",
			errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// The work objects have been refreshed; wait for the created/updated events to trigger reconciliation
	// of the placement binding, so that status can be sync'd back. No need to manually requeue here.
	return ctrl.Result{}, nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, maxConcurrentReconciles int) error {
	// enqueueOwnerBindingForWork resolves the owner placement binding from a work object's labels and enqueues it
	// for reconciliation. eventType is used for logging only.
	enqueueOwnerBindingForWork := func(work client.Object, eventType string, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		if work == nil {
			wrappedErr := errors.NewUnexpectedError(nil, "received a nil work object", "eventType", eventType, "controller", controllerName)
			klog.ErrorS(wrappedErr, "received a nil work object", errors.Args(wrappedErr)...)
			return
		}
		labels := work.GetLabels()
		ownerBindingNSName, nsNameFound := labels[placementv1alpha1.WorkOwnerNamespaceLabelKey]
		ownerBindingName, bindingNameFound := labels[placementv1alpha1.WorkOwnedByPlacementBindingLabelKey]
		if !nsNameFound || !bindingNameFound {
			err := errors.NewUnexpectedError(nil, "work object is missing required labels",
				"work", klog.KObj(work), "eventType", eventType, "controller", controllerName)
			klog.ErrorS(err, "work object is missing required labels", errors.Args(err)...)
			return
		}
		ownerBinding := types.NamespacedName{Namespace: ownerBindingNSName, Name: ownerBindingName}
		klog.V(2).InfoS("Enqueue the owner placement binding for reconciliation",
			"work", klog.KObj(work), "eventType", eventType, "placementBinding", ownerBinding)
		q.Add(reconcile.Request{NamespacedName: ownerBinding})
	}

	workObjHandlerFuncs := handler.Funcs{
		// The controller needs to watch for work object create events as the client-side cache might
		// lag under heavy load, i.e., it might learn about a work object only after its status has been updated.
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueueOwnerBindingForWork(e.Object, "create", q)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				wrappedErr := errors.NewUnexpectedError(nil, "received nil work objects in update event", "controller", controllerName)
				klog.ErrorS(wrappedErr, "received nil work objects in update event", errors.Args(wrappedErr)...)
				return
			}

			oldWork, canCastOldWork := e.ObjectOld.(*placementv1alpha1.Work)
			newWork, canCastNewWork := e.ObjectNew.(*placementv1alpha1.Work)
			if !canCastOldWork || !canCastNewWork {
				wrappedErr := errors.NewUnexpectedError(nil, "failed to cast work objects in update event", "controller", controllerName)
				klog.ErrorS(wrappedErr, "failed to cast work objects in update event", errors.Args(wrappedErr)...)
				return
			}

			// Only enqueue when the status has changed, so that status can be synced back to the owner binding.
			if !equality.Semantic.DeepEqual(oldWork.Status, newWork.Status) {
				enqueueOwnerBindingForWork(e.ObjectNew, "update", q)
			}
		},
		// The controller watches for work object delete events just to guard against unexpected deletion of work
		// objects. Normally the work objects are owned by placement bindings and are only deleted by this
		// controller.
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueueOwnerBindingForWork(e.Object, "delete", q)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		// The controller watches placement binding objects (both namespace-scoped and cluster-scoped) for spec
		// changes (generation predicate).
		Watches(&placementv1alpha1.PlacementBinding{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&placementv1alpha1.ClusterPlacementBinding{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// The controller watches work objects for status changes, so that status can be synced back to their
		// owner placement bindings.
		Watches(&placementv1alpha1.Work{}, workObjHandlerFuncs).
		Complete(r)
}
