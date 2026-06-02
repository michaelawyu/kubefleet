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

package workloadplacement

import (
	"context"
	"math/rand/v2"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	controllerName = "WorkloadPlacement"
)

type Reconciler struct {
	HubClient client.Client
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "workloadPlacement", req.NamespacedName, "controller", controllerName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends", "workloadPlacement", req.NamespacedName, "controller", controllerName, "latency", latency)
	}()

	// Retrieve the WorkloadPlacement object.
	placement := &experimentalv1beta1.WorkloadPlacement{}
	err := r.HubClient.Get(ctx, req.NamespacedName, placement)
	switch {
	case apierrors.IsNotFound(err):
		// The placement cannot be found; it may have been deleted already. No need for
		// further reconciliation.
		klog.V(2).InfoS("The placement object cannot be found", "workloadPlacement", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	case err != nil:
		// An error occurred when trying to retrieve the placement; retry later.
		wrapperErr := errors.NewAPIServerError(err, "", false, "workloadPlacement", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrapperErr, "Failed to get the placement object", errors.Args(wrapperErr)...)
		return ctrl.Result{}, wrapperErr
	}

	if !placement.DeletionTimestamp.IsZero() {
		// Not yet implemented.
	}

	scheduledCond := meta.FindStatusCondition(placement.Status.Conditions, experimentalv1beta1.WorkloadPlacementCondTypeScheduled)

	memberClusterList := &clusterv1beta1.MemberClusterList{}
	if err := r.HubClient.List(ctx, memberClusterList); err != nil {
		wrapperErr := errors.NewAPIServerError(err, "", false, "workloadPlacement", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrapperErr, "Failed to list member clusters", errors.Args(wrapperErr)...)
		return ctrl.Result{}, wrapperErr
	}
	memberClusters := memberClusterList.Items

	unmatchedClusterSelectors := make([]map[string]string, 0)
	selectedClusters := make([]clusterv1beta1.MemberCluster, 0)
	for sidx := range placement.Spec.ClusterSelectors {
		selector := placement.Spec.ClusterSelectors[sidx]
		selectedClustersPerSelector := make([]clusterv1beta1.MemberCluster, 0)

		for cidx := range memberClusters {
			cluster := memberClusters[cidx]

			if clusterMatchesSelector(cluster, selector) {
				selectedClustersPerSelector = append(selectedClustersPerSelector, cluster)
			}

			// Just shuffle the selected clusters.
			rand.Shuffle(len(selectedClustersPerSelector), func(i, j int) {
				selectedClustersPerSelector[i], selectedClustersPerSelector[j] = selectedClustersPerSelector[j], selectedClustersPerSelector[i]
			})
		}

		if len(selectedClustersPerSelector) == 0 {
			unmatchedClusterSelectors = append(unmatchedClusterSelectors, selector)
		} else {
			selectedClusters = append(selectedClusters, selectedClustersPerSelector[0])
		}
	}

	return ctrl.Result{}, nil
}

func clusterMatchesSelector(cluster clusterv1beta1.MemberCluster, selector map[string]string) bool {
	for k, v := range selector {
		if gotV, found := cluster.Labels[k]; !found || gotV != v {
			return false
		}
	}
	return true
}
