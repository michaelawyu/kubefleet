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

package clusterrebalancingrequest

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fleetv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/scheduler/queue"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/controller"
)

// Reconciler reconciles the creation and update of a cluster rebalancing request.
type Reconciler struct {
	// Client is the client the controller uses to access the hub cluster.
	client.Client
	// SchedulerWorkQueue is the workqueue in use by the scheduler.
	SchedulerWorkQueue queue.PlacementSchedulingQueueWriter
}

// Reconcile reconciles the cluster rebalancing request.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	rebalancingReqRef := klog.KRef(req.Namespace, req.Name)
	startTime := time.Now()
	klog.V(2).InfoS("Scheduler source reconciliation starts", "rebalancingRequest", rebalancingReqRef)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Scheduler source reconciliation ends", "rebalancingRequest", rebalancingReqRef, "latency", latency)
	}()

	rebalancingReq := &fleetv1beta1.ClusterRebalancingRequest{}
	if err := r.Get(ctx, req.NamespacedName, rebalancingReq); err != nil {
		if apierrors.IsNotFound(err) {
			// The rebalancing request has been deleted. No further processing is needed.
			klog.V(2).InfoS("The ClusterRebalancingRequest is not found; no further processing needed", "clusterRebalancingRequest", rebalancingReqRef)
			return ctrl.Result{}, nil
		}
		klog.ErrorS(err, "Failed to get ClusterRebalancingRequest", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{}, err
	}

	initCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, fleetv1beta1.ClusterRebalancingRequestConditionTypeInitialized)
	// Ignore observed generation mismatches.
	if initCond == nil || initCond.Status != metav1.ConditionTrue {
		klog.V(2).InfoS("The ClusterRebalancingRequest has not been initialized yet; requeuing for another attempt at waiting for initialization", "clusterRebalancingRequest", rebalancingReqRef)
		return ctrl.Result{RequeueAfter: time.Second * 3}, nil
	}

	migrations := rebalancingReq.Status.Migrations
	for idx := range migrations {
		migration := &migrations[idx]
		placementRef := migration.PlacementReference

		if placementRef.Namespace != "" {
			placementKey := fmt.Sprintf("%s/%s", placementRef.Namespace, placementRef.Name)
			r.SchedulerWorkQueue.Add(queue.PlacementKey(placementKey))
			klog.V(2).InfoS("Enqueued the placement for processing by the scheduler",
				"placement", placementKey, "clusterRebalancingRequest", rebalancingReqRef)
		} else {
			placementKey := placementRef.Name
			r.SchedulerWorkQueue.Add(queue.PlacementKey(placementKey))
			klog.V(2).InfoS("Enqueued the placement for processing by the scheduler",
				"placement", placementKey, "clusterRebalancingRequest", rebalancingReqRef)
		}
	}

	// No action is needed for the scheduler to take in other cases.
	return ctrl.Result{}, nil
}

// buildCustomPredicate creates a predicate that only triggers on important cluster
// rebalancing request events, such as the initialization of a request and the
// marking of a request for deletion.
func buildCustomPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			// Check if the create event is valid.
			if e.Object == nil {
				err := controller.NewUnexpectedBehaviorError(fmt.Errorf("create event is invalid"))
				klog.ErrorS(err, "Failed to process create event")
				return false
			}

			// Cast the object to a ClusterRebalancingRequest.
			rebalancingReq, ok := e.Object.(*fleetv1beta1.ClusterRebalancingRequest)
			if !ok {
				err := controller.NewUnexpectedBehaviorError(fmt.Errorf("failed to cast the object to ClusterRebalancingRequest"))
				klog.ErrorS(err, "Failed to process create event")
				return false
			}

			// Check if the initialized condition has been added to the rebalancing request and is of the true status.
			initCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, fleetv1beta1.ClusterRebalancingRequestConditionTypeInitialized)
			if condition.IsConditionStatusTrue(initCond, rebalancingReq.GetGeneration()) {
				klog.V(2).InfoS("The rebalancing request has been initialized", "rebalancingRequest", klog.KObj(rebalancingReq))
				return true
			}

			// Check if the deletion timestamp has been added to the rebalancing request.
			if e.Object.GetDeletionTimestamp() != nil {
				klog.V(2).InfoS("The rebalancing request has been marked for deletion", "rebalancingRequest", klog.KObj(rebalancingReq))
				return true
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Ignore deletion events (events emitted when the object is actually removed
			// from storage).
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Check if the update event is valid.
			if e.ObjectOld == nil || e.ObjectNew == nil {
				err := controller.NewUnexpectedBehaviorError(fmt.Errorf("update event is invalid"))
				klog.ErrorS(err, "Failed to process update event")
				return false
			}

			// Cast the old and new objects to a ClusterRebalancingRequest.
			oldRebalancingReq, okOld := e.ObjectOld.(*fleetv1beta1.ClusterRebalancingRequest)
			newRebalancingReq, okNew := e.ObjectNew.(*fleetv1beta1.ClusterRebalancingRequest)
			if !okOld || !okNew {
				err := controller.NewUnexpectedBehaviorError(fmt.Errorf("failed to cast the old or new object to ClusterRebalancingRequest"))
				klog.ErrorS(err, "Failed to process update event")
				return false
			}

			// Check if the initialized condition has been added to the rebalancing request and is of the true
			// status.
			oldInitCond := meta.FindStatusCondition(oldRebalancingReq.Status.Conditions, fleetv1beta1.ClusterRebalancingRequestConditionTypeInitialized)
			newInitCond := meta.FindStatusCondition(newRebalancingReq.Status.Conditions, fleetv1beta1.ClusterRebalancingRequestConditionTypeInitialized)
			if !condition.EqualCondition(oldInitCond, newInitCond) && condition.IsConditionStatusTrue(newInitCond, newRebalancingReq.GetGeneration()) {
				klog.V(2).InfoS("The rebalancing request has been initialized", "rebalancingRequest", klog.KObj(newRebalancingReq))
				return true
			}

			// Check if the deletion timestamp has been added to the rebalancing request.
			if e.ObjectOld.GetDeletionTimestamp() == nil && e.ObjectNew.GetDeletionTimestamp() != nil {
				klog.V(2).InfoS("The rebalancing request has been marked for deletion", "rebalancingRequest", klog.KObj(newRebalancingReq))
				return true
			}
			return false
		},
	}
}

// SetupWithManager sets up the controller with the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("clusterrebalancingrequest-scheduler-watcher").
		For(&fleetv1beta1.ClusterRebalancingRequest{}).
		WithEventFilter(buildCustomPredicate()).
		Complete(r)
}
