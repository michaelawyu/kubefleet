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

package controllers

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubefleet-dev/kubefleet/pkg/propertyprovider/azure/trackers"
)

// NodeMetricsReconciler reconciles NodeMetrics objects.
type NodeMetricsReconciler struct {
	NMT    *trackers.NodeMetricsTracker
	Client client.Client
}

// Reconcile reconciles a NodeMetrics object.
func (nm *NodeMetricsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	nodeMetricsRef := klog.KRef(req.Namespace, req.Name)
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts for node metrics objects in the Azure property provider", "nodeMetrics", nodeMetricsRef)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends for node metrics objects in the Azure property provider", "nodeMetrics", nodeMetricsRef, "latency", latency)
	}()

	// Retrieve the NodeMetrics object.
	nodeMetrics := &metricsv1beta1.NodeMetrics{}
	if err := nm.Client.Get(ctx, req.NamespacedName, nodeMetrics); err != nil {
		// Failed to get the NodeMetrics object; this signals that the metrics should be untracked.
		if errors.IsNotFound(err) {
			// This branch essentially processes the deletion event of the NodeMetrics object.
			// At this point the metrics may have not been tracked by the tracker at all; if that's
			// the case, the removal (untracking) operation is a no-op.
			nm.NMT.Remove(req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Note that this controller will not untrack a NodeMetrics object when it is first marked
	// for deletion; instead, it performs the untracking when the NodeMetrics object is actually
	// gone from the etcd store. This is intentional, as the NodeMetrics object's lifecycle might
	// not align with its corresponding Node object's lifecycle, and untracking too early might
	// lead to a case of temporary inconsistency.

	// Track the NodeMetrics object.
	klog.V(2).InfoS("Attempt to track node metrics", "nodeMetrics", klog.KObj(nodeMetrics))
	nm.NMT.AddOrUpdate(nodeMetrics)

	// Requeue periodically; this helps ensure that the property provider always have up-to-date
	// metrics about nodes (or more specifically, ensure that stale metrics will be detected and
	// untracked timely).
	return ctrl.Result{RequeueAfter: trackers.NodeMetricsShelfLife}, nil
}

// SetupWithManager sets up the controller with the controller runtime manager.
func (nm *NodeMetricsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Set up the controller for NodeMetrics objects.
	return ctrl.NewControllerManagedBy(mgr).
		For(&metricsv1beta1.NodeMetrics{}).
		Complete(nm)
}
