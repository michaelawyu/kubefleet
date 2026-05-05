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

package rebalancer

import (
	"context"
	"fmt"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
)

const (
	clusterRebalancingRequestCleanUpFinalizer = "cluster-rebalancing-request-cleanup"
)

type RebalacingAttemptResult string

const (
	RebalancingAttemptResultCompleted  RebalacingAttemptResult = "Completed"
	RebalancingAttemptResultInProgress RebalacingAttemptResult = "InProgress"
	RebalancingAttemptResultFailed     RebalacingAttemptResult = "Failed"
)

type Reconciler struct {
	Client client.Client

	initMutx sync.Mutex
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Rebalancer reconciliation starts", "clusterRebalancingRequest", req.NamespacedName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Rebalancer reconciliation ends", "clusterRebalancingRequest", req.NamespacedName, "duration", latency)
	}()

	// Retrieve the ClusterRebalancingRequest object.
	rebalancingReq := &placementv1beta1.ClusterRebalancingRequest{}
	err := r.Client.Get(ctx, req.NamespacedName, rebalancingReq)
	switch {
	case apierrors.IsNotFound(err):
		klog.V(2).InfoS(
			"The rebalancing request is not found; it might have been deleted after the reconciliation request was enqueued", "clusterRebalancingRequest", req.NamespacedName)
		return ctrl.Result{}, nil
	case err != nil:
		klog.ErrorS(err, "Failed to retrieve rebalancing request", "clusterRebalancingRequest", req.NamespacedName)
		return ctrl.Result{}, err
	}

	rebalancingReqRef := klog.KObj(rebalancingReq)

	// Process the removal of the rebalancing request.
	if !rebalancingReq.DeletionTimestamp.IsZero() {
		klog.V(2).InfoS("The rebalancing request has been marked for deletion; processing its removal", "clusterRebalancingRequest", rebalancingReqRef)
		// Not yet implemented.
		return r.commitChanges(ctx, rebalancingReq)
	}

	// Add finalizer to the request.
	if err := r.addFinalizerTo(ctx, rebalancingReq); err != nil {
		klog.ErrorS(err, "Failed to add finalizer to rebalancing request", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{}, fmt.Errorf("failed to add finalizer to the rebalancing request")
	}

	// Check if a roll back has been requested for the request.
	if rebalancingReq.Spec.Rollback {
		klog.V(2).InfoS("Start to roll back the rebalancing request", "clusterRebalancingRequest", rebalancingReqRef)
		return r.rollbackChanges(ctx, rebalancingReq)
	}

	// Set the defaults.
	failurePolicy := rebalancingReq.Spec.FailurePolicy
	if failurePolicy == nil {
		failurePolicy = &placementv1beta1.ClusterRebalancingRequestFailurePolicy{
			OnFailureCount: ptr.To(int32(1)),
			MaximumWaitDurationPerMigrationAttemptSeconds: ptr.To(int32(300)),
		}
		rebalancingReq.Spec.FailurePolicy = failurePolicy
	}
	if rebalancingReq.Spec.MaxConcurrency == nil {
		rebalancingReq.Spec.MaxConcurrency = ptr.To[int32](1)
	}

	// Initialize the ClusterRebalancingRequest (if applicable).

	// Acquire the initialization mutex first.
	r.initMutx.Lock()
	initialized, err := r.initialize(ctx, rebalancingReq)
	r.initMutx.Unlock()
	if err != nil {
		klog.ErrorS(err, "Failed to initialize ClusterRebalancingRequest", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{}, err
	}
	if !initialized {
		klog.V(2).InfoS("ClusterRebalancingRequest is not initialized yet; requeuing for another attempt at initialization", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{RequeueAfter: time.Second * 15}, nil
	}

	// Wait until the scheduler acknowledges the presence of the ClusterRebalancingRequest.
	started, err := r.waitForSchedulerAcknowledgement(ctx, rebalancingReq)
	if err != nil {
		klog.ErrorS(err, "Failed while waiting for scheduler acknowledgement of ClusterRebalancingRequest", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{}, err
	}
	if !started {
		klog.V(2).InfoS("Scheduler has not acknowledged the ClusterRebalancingRequest yet; requeuing for another attempt at waiting for acknowledgement", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{RequeueAfter: time.Second * 15}, nil
	}

	// Start the rebalancing process.
	res, err := r.rebalancePlacements(ctx, rebalancingReq)
	if err != nil {
		klog.ErrorS(err, "Failed to rebalance placements for ClusterRebalancingRequest", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{}, err
	}
	switch res {
	case RebalancingAttemptResultCompleted:
		klog.V(2).InfoS("Completed the rebalancing request", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{}, nil
	case RebalancingAttemptResultInProgress:
		klog.V(2).InfoS("The rebalancing request is still in progress; requeuing for another attempt at checking the progress", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{RequeueAfter: time.Second * 30}, nil
	case RebalancingAttemptResultFailed:
		klog.V(2).InfoS("The rebalancing request has failed; delete the rebalancing request to accept the current state, or roll back the request to revert to the original state", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{}, nil
	default:
		klog.ErrorS(fmt.Errorf("unexpected rebalancing attempt result: %s", res), "Unexpected rebalancing attempt result", "rebalancingAttemptResult", res, "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{}, fmt.Errorf("unexpected rebalancing attempt result: %s", res)
	}
}

func (r *Reconciler) addFinalizerTo(ctx context.Context, rebalancingReq *placementv1beta1.ClusterRebalancingRequest) error {
	if !controllerutil.ContainsFinalizer(rebalancingReq, clusterRebalancingRequestCleanUpFinalizer) {
		rebalancingReq.Finalizers = append(rebalancingReq.Finalizers, clusterRebalancingRequestCleanUpFinalizer)

		if err := r.Client.Update(ctx, rebalancingReq); err != nil {
			klog.ErrorS(err, "Failed to add cleanup finalizer to ClusterRebalancingRequest", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
			return err
		}
		klog.V(2).InfoS("Added cleanup finalizer to ClusterRebalancingRequest", "clusterRebalancingRequest", klog.KObj(rebalancingReq))
	}
	return nil
}
