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

package stagedupdate

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	rolloutv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/rollout/v1alpha1"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/parallelizer"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/managers/placementresourcesnapshot"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/utils/bindingmanager"
)

const (
	controllerName = "StagedUpdateController"

	maxAllowedClusterProcessingConcurrency = 5
)

const (
	stagedUpdateRunCleanupFinalizer = "kubefleet.dev/staged-update-run-cleanup"
)

type Reconciler struct {
	HubClient client.Client

	PlacementResourceSnapshotManager *placementresourcesnapshot.Manager

	Parallelizer parallelizer.Parallelizer

	BindingManagerClaimRateLimiter workqueue.TypedRateLimiter[ctrl.Request]

	RefreshPeriod time.Duration
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "stagedUpdateRun", req.NamespacedName, "controller", controllerName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends", "stagedUpdateRun", req.NamespacedName, "latency", latency, "controller", controllerName)
	}()

	// Retrieve the StagedUpdateRun or ClusterStagedUpdateRun resource and return it as a StagedUpdateRunAccessor interface.
	stagedUpdateRunAccessor, err := r.retrieveStagedUpdateRun(ctx, req.NamespacedName)
	if err != nil {
		wrappedErr := errors.Wraps(err, "", "stagedUpdateRun", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to retrieve staged update run object", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Clean things up if the staged update run object has been marked for deletion.
	if !stagedUpdateRunAccessor.GetDeletionTimestamp().IsZero() {
		if err := r.cleanup(ctx, stagedUpdateRunAccessor); err != nil {
			wrappedErr := errors.Wraps(err, "", "stagedUpdateRun", req.NamespacedName, "controller", controllerName)
			klog.ErrorS(wrappedErr, "Failed to cleanup staged update run", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
		return ctrl.Result{}, nil
	}

	// Retrieve the corresponding PlacementPolicy or ClusterPlacementPolicy resource and return it as a PlacementPolicyAccessor interface.
	placementPolicyAccessor, err := r.retrieveLinkedPlacementPolicy(ctx, stagedUpdateRunAccessor)
	if err != nil {
		wrappedErr := errors.Wraps(err, "",
			"stagedUpdateRun", req.NamespacedName,
			"placementPolicyName", stagedUpdateRunAccessor.GetSpec().PlacementPolicyName,
			"controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to retrieve linked placement policy object", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Skip processing if the staged update run is in a terminal state or suspended after being initialized.
	stagedUpdateRunObjRef := placementv1alpha1.ObjectReference{
		APIGroup:   rolloutv1alpha1.GroupVersion.Group,
		APIVersion: rolloutv1alpha1.GroupVersion.Version,
		Kind:       stagedUpdateRunAccessor.GetObjectKind().GroupVersionKind().Kind,
		Namespace:  stagedUpdateRunAccessor.GetNamespace(),
		Name:       stagedUpdateRunAccessor.GetName(),
	}
	if r.shouldSkipProcessing(stagedUpdateRunAccessor) {
		// Check if the controller has acquired the binding manager role. If so, relinquish the role so that other controllers
		// may manage bindings for the placement policy.
		if err := bindingmanager.RelinquishRoleFor(ctx, r.HubClient, placementPolicyAccessor, controllerName, stagedUpdateRunObjRef); err != nil {
			wrappedErr := errors.Wraps(err, "",
				"stagedUpdateRun", req.NamespacedName,
				"placementPolicyName", stagedUpdateRunAccessor.GetSpec().PlacementPolicyName,
				"controller", controllerName)
			klog.ErrorS(wrappedErr, "Failed to relinquish binding manager role", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
		return ctrl.Result{}, nil
	}

	// Add a cleanup finalizer to the staged update run object if it doesn't have one.
	if err := r.ensureCleanupFinalizer(ctx, stagedUpdateRunAccessor); err != nil {
		wrappedErr := errors.Wraps(err, "", "stagedUpdateRun", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to ensure cleanup finalizer on staged update run object", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Claim the staged update run object as the binding manager for the placement policy. This ensures that no other controllers
	// (e.g., the scheduling process, the migrations, etc.) can interfere with the rollout process.
	claimed, err := bindingmanager.ClaimRoleAs(ctx, r.HubClient, placementPolicyAccessor, controllerName, stagedUpdateRunObjRef)
	if err != nil {
		wrappedErr := errors.Wraps(err, "",
			"stagedUpdateRun", req.NamespacedName,
			"placementPolicyName", stagedUpdateRunAccessor.GetSpec().PlacementPolicyName,
			"controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to claim binding manager role", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}
	if !claimed {
		klog.V(2).InfoS("Cannot claim the binding manager role for now; will retry later",
			"stagedUpdateRun", req.NamespacedName,
			"placementPolicyName", stagedUpdateRunAccessor.GetSpec().PlacementPolicyName,
			"controller", controllerName)
		backoffDuration := r.BindingManagerClaimRateLimiter.When(req)
		return ctrl.Result{RequeueAfter: backoffDuration}, nil
	}
	klog.V(2).InfoS("Successfully claimed the binding manager role for the staged update run",
		"stagedUpdateRun", req.NamespacedName,
		"placementPolicyName", stagedUpdateRunAccessor.GetSpec().PlacementPolicyName,
		"controller", controllerName)
	// Reset the rate limiter for the staged update run object.
	r.BindingManagerClaimRateLimiter.Forget(req)

	// Initialize the staged update run (if applicable).
	shouldSkip, err := r.initialize(ctx, placementPolicyAccessor, stagedUpdateRunAccessor)
	if err != nil {
		wrappedErr := errors.Wraps(err, "", "stagedUpdateRun", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to initialize staged update run", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}
	if shouldSkip {
		// Skip further processing.
		return ctrl.Result{}, nil
	}

	// Process the staged update run.
	allStagesProcessingRes, requeueAfter, processingErr := r.process(ctx, stagedUpdateRunAccessor)
	klog.V(2).InfoS("Processed staged update run",
		"stagedUpdateRun", req.NamespacedName,
		"placementPolicyName", stagedUpdateRunAccessor.GetSpec().PlacementPolicyName,
		"controller", controllerName,
		"allStagesProcessingRes", allStagesProcessingRes,
		"err", processingErr)

	// Refresh the staged update run status.
	//
	// Note that this is done regardless of the processing results, so as to keep the users informed of the progress
	// so far.
	if updateErr := r.HubClient.Status().Update(ctx, stagedUpdateRunAccessor); updateErr != nil {
		wrappedErr := errors.NewAPIServerError(updateErr, "", false,
			"stagedUpdateRun", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to update staged update run status", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Handle processing errors and requeues accordingly.
	if processingErr != nil {
		wrappedErr := errors.Wraps(processingErr, "", "stagedUpdateRun", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(processingErr, "Failed to process staged update run", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}
	switch allStagesProcessingRes {
	case allStagesProcessingResultSucceeded, allStagesProcessingResultFailed:
		// The staged update run has been completed. Perform any necessary cleanup.
		if err := r.cleanup(ctx, stagedUpdateRunAccessor); err != nil {
			wrappedErr := errors.Wraps(err, "", "stagedUpdateRun", req.NamespacedName, "controller", controllerName)
			klog.ErrorS(wrappedErr, "Failed to cleanup staged update run", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
	case allStagesProcessingResultInProgress:
		// Requeue after the specified duration.
		if requeueAfter != nil {
			return ctrl.Result{RequeueAfter: *requeueAfter}, nil
		}
		return ctrl.Result{RequeueAfter: time.Second * 3}, nil
	default:
		// An unexpected processing result is yielded; consider this as an unexpected error.
		wrappedErr := errors.NewUnexpectedError(nil, "encountered an unexpected processing result",
			"observedProcessingResult", allStagesProcessingRes,
			"stagedUpdateRun", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the manager.
//
// The controller watches both ClusterStagedUpdateRun (cluster-scoped) and StagedUpdateRun
// (namespace-scoped) objects, funneling events for either kind into the same reconcile queue;
// Reconcile (via retrieveStagedUpdateRun) tells the two apart by whether the incoming request
// carries a namespace.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, maxConcurrentReconciles int) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		Watches(&rolloutv1alpha1.ClusterStagedUpdateRun{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&rolloutv1alpha1.StagedUpdateRun{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func (r *Reconciler) retrieveStagedUpdateRun(ctx context.Context, key types.NamespacedName) (rolloutv1alpha1.StagedUpdateRunAccessor, error) {
	var stagedUpdateRunAccessor rolloutv1alpha1.StagedUpdateRunAccessor
	var gvk schema.GroupVersionKind
	if key.Namespace == "" {
		// The object to retrieve is a ClusterStagedUpdateRun.
		stagedUpdateRunAccessor = &rolloutv1alpha1.ClusterStagedUpdateRun{}
		gvk = rolloutv1alpha1.GroupVersion.WithKind("ClusterStagedUpdateRun")
	} else {
		// The object to retrieve is a StagedUpdateRun.
		stagedUpdateRunAccessor = &rolloutv1alpha1.StagedUpdateRun{}
		gvk = rolloutv1alpha1.GroupVersion.WithKind("StagedUpdateRun")
	}

	if err := r.HubClient.Get(ctx, key, stagedUpdateRunAccessor); err != nil {
		return nil, errors.NewAPIServerError(err, "failed to get staged update run object", true)
	}
	// The controller-runtime client (and the informer cache backing it) does not populate TypeMeta
	// (apiVersion/kind) on typed Get calls, so set the GVK explicitly here. This ensures that any code
	// relying on stagedUpdateRunAccessor.GetObjectKind() afterwards (e.g., building an ObjectReference
	// for the binding manager) sees a correctly populated Kind/APIVersion.
	stagedUpdateRunAccessor.GetObjectKind().SetGroupVersionKind(gvk)
	return stagedUpdateRunAccessor, nil
}

func (r *Reconciler) retrieveLinkedPlacementPolicy(
	ctx context.Context, stagedUpdateRunAccessor rolloutv1alpha1.StagedUpdateRunAccessor) (placementv1alpha1.PlacementPolicyAccessor, error) {
	placementPolicyName := stagedUpdateRunAccessor.GetSpec().PlacementPolicyName
	placementPolicyKey := types.NamespacedName{
		Namespace: stagedUpdateRunAccessor.GetNamespace(),
		Name:      placementPolicyName,
	}

	var placementPolicyAccessor placementv1alpha1.PlacementPolicyAccessor
	if placementPolicyKey.Namespace == "" {
		placementPolicyAccessor = &placementv1alpha1.ClusterPlacementPolicy{}
	} else {
		placementPolicyAccessor = &placementv1alpha1.PlacementPolicy{}
	}

	if err := r.HubClient.Get(ctx, placementPolicyKey, placementPolicyAccessor); err != nil {
		return nil, errors.NewAPIServerError(err, "failed to get placement policy object", true)
	}
	return placementPolicyAccessor, nil
}

func (r *Reconciler) ensureCleanupFinalizer(ctx context.Context, stagedUpdateRunAccessor rolloutv1alpha1.StagedUpdateRunAccessor) error {
	if controllerutil.ContainsFinalizer(stagedUpdateRunAccessor, stagedUpdateRunCleanupFinalizer) {
		return nil
	}

	controllerutil.AddFinalizer(stagedUpdateRunAccessor, stagedUpdateRunCleanupFinalizer)
	if err := r.HubClient.Update(ctx, stagedUpdateRunAccessor); err != nil {
		return errors.NewAPIServerError(err, "failed to update staged update run object", false)
	}
	klog.V(2).InfoS("Added cleanup finalizer to staged update run object", "stagedUpdateRun", klog.KObj(stagedUpdateRunAccessor))
	return nil
}

func (r *Reconciler) shouldSkipProcessing(stagedUpdateRunAccessor rolloutv1alpha1.StagedUpdateRunAccessor) bool {
	// Skip processing if the staged update run is in a terminal state or suspended after being initialized.
	//
	// To retry the staged update, one needs to create a new staged update run object.

	// Skip processing if the initialization has failed.
	initCond := meta.FindStatusCondition(stagedUpdateRunAccessor.GetStatus().Conditions, rolloutv1alpha1.StagedUpdateRunCondTypeInitialized)
	if initCond != nil && initCond.Status == metav1.ConditionFalse {
		// Note that for this branch no observed generation check is needed. Staged update run objects have immutable spec (except
		// for the suspended switch).
		klog.V(2).InfoS("The staged update run has failed to initialize; skip further processing", "stagedUpdateRun", klog.KObj(stagedUpdateRunAccessor), "controller", controllerName)
		return true
	}

	// Skip processing if the staged update run has been completed (successfully or not).
	completedCond := meta.FindStatusCondition(stagedUpdateRunAccessor.GetStatus().Conditions, rolloutv1alpha1.StagedUpdateRunCondTypeCompleted)
	if completedCond != nil {
		// Note that for this branch no observed generation check is needed. Staged update run objects have immutable spec (except
		// for the suspended switch).
		klog.V(2).InfoS("The staged update run has been completed; skip further processing",
			"stagedUpdateRun", klog.KObj(stagedUpdateRunAccessor), "controller", controllerName)
		return true
	}

	// Skip processing if the staged update run has been initialized but is suspended.
	if initCond != nil && initCond.Status == metav1.ConditionTrue && stagedUpdateRunAccessor.GetSpec().Suspended {
		klog.V(2).InfoS("The staged update run is suspended; skip further processing", "stagedUpdateRun", klog.KObj(stagedUpdateRunAccessor), "controller", controllerName)
		return true
	}

	return false
}
