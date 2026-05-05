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
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
)

func (r *Reconciler) waitForSchedulerAcknowledgement(
	ctx context.Context,
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest,
) (bool, error) {
	// Check if the rebalancing has started.
	startedCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, string(placementv1beta1.ClusterRebalancingRequestConditionTypeStarted))
	if condition.IsConditionStatusTrue(startedCond, rebalancingReq.Generation) {
		return true, nil
	}

	fromCluster := rebalancingReq.Spec.From
	toCluster := rebalancingReq.Spec.To

	// Retrieve the list of placement references from the request status.
	migrations := rebalancingReq.Status.Migrations
	for idx := range migrations {
		migration := &migrations[idx]
		placementRef := &migration.PlacementReference

		// Retrieve the placement and check if the scheduler has acknowledged the rebalancing request.
		//
		// The scheduler reports its acknowledgement by setting a "rebalancing-managed-by" annotation on the placement, with the
		// value being the UID of the ClusterRebalancingRequest.
		var placementObj placementv1beta1.PlacementObj
		if placementRef.Namespace == "" {
			// The placement is cluster-scoped (CRP).
			placementObj = &placementv1beta1.ClusterResourcePlacement{}
			if err := r.Client.Get(ctx, client.ObjectKey{Name: placementRef.Name}, placementObj); err != nil {
				return false, fmt.Errorf("failed to retrieve CRP: %w", err)
			}
		} else {
			// The placement is namespace-scoped (RP).
			placementObj = &placementv1beta1.ResourcePlacement{}
			if err := r.Client.Get(ctx, client.ObjectKey{Namespace: placementRef.Namespace, Name: placementRef.Name}, placementObj); err != nil {
				return false, fmt.Errorf("failed to retrieve RP: %w", err)
			}
		}

		reqUID, found := placementObj.GetAnnotations()[placementv1beta1.PlacementRebalancingManagedByAnnotationKey]
		if !found || reqUID != string(rebalancingReq.GetUID()) {
			// The scheduler has not acknowledged the request yet.
			klog.V(2).InfoS(
				"The scheduler has not acknowledged the rebalancing request yet; wait and retry",
				"placement", klog.KObj(placementObj))
			return false, nil
		}

		// The scheduler has acknowledged the request. List the bindings associated with the placement,
		// and populate binding references.
		var fromBinding placementv1beta1.BindingObj
		var fromBindingCnt int
		var toBinding placementv1beta1.BindingObj
		var toBindingCnt int
		if placementRef.Namespace == "" {
			// The placement is cluster-scoped (CRP). List ClusterResourceBindings.
			clusterResBindings := &placementv1beta1.ClusterResourceBindingList{}
			if err := r.Client.List(ctx, clusterResBindings); err != nil {
				return false, fmt.Errorf("failed to list ClusterResourceBindings: %w", err)
			}
			for idx := range clusterResBindings.Items {
				binding := &clusterResBindings.Items[idx]
				if binding.GetBindingSpec().TargetCluster == fromCluster {
					fromBinding = binding
					fromBindingCnt++
				}
				if binding.GetBindingSpec().TargetCluster == toCluster {
					toBinding = binding
					toBindingCnt++
				}
			}
		} else {
			// The placement is namespace-scoped (RP). List ResourceBindings in the placement namespace.
			resBindings := &placementv1beta1.ResourceBindingList{}
			if err := r.Client.List(ctx, resBindings, client.InNamespace(placementRef.Namespace)); err != nil {
				return false, fmt.Errorf("failed to list ResourceBindings: %w", err)
			}
			for idx := range resBindings.Items {
				binding := &resBindings.Items[idx]
				if binding.GetBindingSpec().TargetCluster == fromCluster {
					fromBinding = binding
					fromBindingCnt++
				}
				if binding.GetBindingSpec().TargetCluster == toCluster {
					toBinding = binding
					toBindingCnt++
				}
			}
		}

		// Do some sanity checks.
		switch {
		case fromBindingCnt > 1:
			klog.InfoS(
				"There are multiple bindings in presence for the from cluster; wait and retry until the extra binding disappears",
				"placement", klog.KObj(placementObj), "fromCluster", fromCluster, "bindingCount", fromBindingCnt)
			return false, nil
		case toBindingCnt > 1:
			klog.InfoS(
				"There are multiple bindings in presence for the to cluster; wait and retry until the extra binding disappears",
				"placement", klog.KObj(placementObj), "toCluster", toCluster, "bindingCount", toBindingCnt)
			return false, nil
		case fromBinding == nil:
			// The binding for the from cluster is absent. This can only happen in a corner case where
			// a binding was present when the rebalancing request was being initialized, but disappears
			// before the scheduler acknowledges the request. No action is needed; the rebalancer will
			// skip this placement.
			klog.V(2).InfoS(
				"No binding is found for the from cluster; the request might have observed an inconsistent state during initialization; skip this placement",
				"placement", klog.KObj(placementObj), "fromCluster", fromCluster)
			continue
		case fromBinding.GetAnnotations()[placementv1beta1.ReservedBindingAnnotationKey] != strconv.FormatBool(true):
			// The binding for the from cluster does not have the expected reserved binding annotation;
			// this might be a sign of cache inconsistency. Wait and retry.
			klog.V(2).InfoS(
				"The binding for the from cluster does not have the expected reserved binding annotation; the cache might be out of sync; wait and retry",
				"placement", klog.KObj(placementObj),
				"binding", klog.KObj(fromBinding),
				"fromCluster", fromCluster,
				"toCluster", toCluster,
				"rebalancingRequest", klog.KObj(rebalancingReq))
			return false, nil
		case toBinding != nil && toBinding.GetAnnotations()[placementv1beta1.ReservedBindingAnnotationKey] != strconv.FormatBool(true):
			// The binding for the to cluster does not have the expected reserved binding annotation;
			// this might be a sign of cache inconsistency. Wait and retry.
			klog.V(2).InfoS(
				"The binding for the to cluster does not have the expected reserved binding annotation; the cache might be out of sync; wait and retry",
				"placement", klog.KObj(placementObj),
				"binding", klog.KObj(toBinding),
				"fromCluster", fromCluster,
				"toCluster", toCluster,
				"rebalancingRequest", klog.KObj(rebalancingReq))
			return false, nil
		}

		// It is certain now that the scheduler has acknowledged the request, and there is exactly one
		// binding for the from cluster, and one or zero binding for the to cluster. Populate the binding
		// references.

		// Populate the from binding reference in the request status.
		migration.FromClusterBindingReference = &corev1.ObjectReference{
			Kind:      fromBinding.GetObjectKind().GroupVersionKind().Kind,
			Name:      fromBinding.GetName(),
			Namespace: fromBinding.GetNamespace(),
			UID:       fromBinding.GetUID(),
		}
		if toBinding != nil {
			// Populate the to binding reference in the request status.
			migration.ToClusterBindingReference = &corev1.ObjectReference{
				Kind:      toBinding.GetObjectKind().GroupVersionKind().Kind,
				Name:      toBinding.GetName(),
				Namespace: toBinding.GetNamespace(),
				UID:       toBinding.GetUID(),
			}
		}
	}

	rebalancingReq.Status.Migrations = migrations
	// Add a started condition.
	startedCond = &metav1.Condition{
		Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeStarted,
		Status:             metav1.ConditionTrue,
		Reason:             "SchedulerAcknowledgedRebalancingReq",
		Message:            "The KubeFleet scheduler has acknowledged the rebalancing request; start to process the request",
		ObservedGeneration: rebalancingReq.Generation,
	}
	meta.SetStatusCondition(&rebalancingReq.Status.Conditions, *startedCond)

	// Refresh the status.
	if err := r.Client.Status().Update(ctx, rebalancingReq); err != nil {
		return false, fmt.Errorf("failed to update ClusterRebalancingRequest status: %w", err)
	}

	return true, nil
}
