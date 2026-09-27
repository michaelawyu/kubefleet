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

package placementmigrationrequest

import (
	"context"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	controllerName = "PlacementMigrationRequest"

	placementMigrationRequestCleanupFinalizer = "kubefleet.dev/placement-migration-request-cleanup"

	clusterRequestNameFmt   = "%s-%s-replacement"
	toClusterBindingNameFmt = "%s-%s-migrated"

	// placementBindingOwnedByLabelKey is a label key applied to a to binding created during migration; it
	// records the name of the placement policy that owns the binding, mirroring the naming convention used
	// by placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey.
	placementBindingOwnedByLabelKey = "placement.kubefleet.dev/placement-binding-owned-by"
	// placementBindingMigratedFromLabelKey is a label key applied to a to binding created during migration;
	// it records the name of the from binding that the to binding was migrated from, so that the commit
	// process can find and promote/drop the to binding as appropriate.
	placementBindingMigratedFromLabelKey = "placement.kubefleet.dev/placement-binding-migrated-from"

	// clusterSelectorHashAnnotationKey mirrors the (unexported) annotation key that the placementpolicy
	// controller uses to record the hash of the cluster selectors on a placement binding; it is duplicated
	// here (rather than imported) as that constant is package-private.
	clusterSelectorHashAnnotationKey = "placement.kubefleet.dev/placement-cluster-selector-hash"
)

type migrationRunResult string

const (
	migrationRunResultSucceeded  migrationRunResult = "Succeeded"
	migrationRunResultFailed     migrationRunResult = "Failed"
	migrationRunResultInProgress migrationRunResult = "InProgress"
)

type migrationAttemptResult string

const (
	migrationAttemptResultSucceeded  migrationAttemptResult = "Succeeded"
	migrationAttemptResultFailed     migrationAttemptResult = "Failed"
	migrationAttemptResultSkipped    migrationAttemptResult = "Skipped"
	migrationAttemptResultInProgress migrationAttemptResult = "InProgress"
)

type migrationAttemptBundle struct {
	attempt           *placementv1alpha1.PerPlacementMigrationStatus
	assignedWorkerIdx int

	res          migrationAttemptResult
	lastKnownErr error
}

type Reconciler struct {
	HubClient client.Client

	initMutex         sync.Mutex
	MaxWaitTimePerRun time.Duration
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts",
		"placementMigrationRequest", req.NamespacedName, "controller", controllerName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends",
			"placementMigrationRequest", req.NamespacedName, "latencyMilliseconds", latency, "controller", controllerName)
	}()

	// Retrieve the placement migration request.
	migrationReq := &placementv1alpha1.PlacementMigrationRequest{}
	err := r.HubClient.Get(ctx, req.NamespacedName, migrationReq)
	switch {
	case apierrors.IsNotFound(err):
		klog.V(2).InfoS("The placement migration request is not found; it might have been deleted after the reconciliation request was enqueued",
			"placementMigrationRequest", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	case err != nil:
		wrappedErr := errors.NewAPIServerError(err, "", true, "placementMigrationRequest", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to get the placement migration request", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Process the removal of the placement migration request.
	if !migrationReq.ObjectMeta.DeletionTimestamp.IsZero() {
		klog.V(2).InfoS("The placement migration request has been marked for deletion; process its removal",
			"placementMigrationRequest", req.NamespacedName, "controller", controllerName)
		return r.commit(ctx, migrationReq)
	}

	// Add the cleanup finalizer to the request.
	if !controllerutil.ContainsFinalizer(migrationReq, placementMigrationRequestCleanupFinalizer) {
		controllerutil.AddFinalizer(migrationReq, placementMigrationRequestCleanupFinalizer)
		if err := r.HubClient.Update(ctx, migrationReq); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", true, "placementMigrationRequest", req.NamespacedName, "controller", controllerName)
			klog.ErrorS(wrappedErr, "Failed to add finalizer to the placement migration request", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
	}

	// Check if a rollback has been requested.
	if migrationReq.Spec.Rollback {
		klog.V(2).InfoS("Rollback has been requested for the placement migration request; process the rollback",
			"placementMigrationRequest", req.NamespacedName, "controller", controllerName)
		return r.rollback(ctx, migrationReq)
	}

	// Initialize the migration if applicable.

	// Acquire the initialization mutex first.
	r.initMutex.Lock()
	err = r.initialize(ctx, migrationReq)
	r.initMutex.Unlock()
	if err != nil {
		wrappedErr := errors.Wraps(err, "", "placementMigrationRequest", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to initialize the placement migration request", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	// Start the migration.
	res, err := r.migrateWorkloads(ctx, migrationReq)
	if err != nil {
		wrappedErr := errors.Wraps(err, "", "placementMigrationRequest", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to migrate placements for the placement migration request", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	klog.V(2).InfoS("Finished a round of processing for the migration request", "placementMigrationRequest", klog.KObj(migrationReq), "migrationRunResult", res)
	if res == migrationRunResultInProgress {
		// The migration run is still in progress; requeue for the next round of processing.
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	} else {
		// The migration run has completed (either succeeded or failed); no need to requeue.
		return ctrl.Result{}, nil
	}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&placementv1alpha1.PlacementMigrationRequest{}).
		Complete(r)
}
