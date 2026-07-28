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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func (r *Reconciler) cleanupWorks(ctx context.Context, placementBinding *experimentalv1beta1.PlacementBinding) error {
	workNSName := fmt.Sprintf(memberClusterReservedNSFmt, placementBinding.Spec.ClusterName)

	labelSelector := client.MatchingLabels{
		experimentalv1beta1.WorkOwnedByPlacementBindingLabelKey: placementBinding.Name,
		experimentalv1beta1.WorkOwnerNamespaceLabelKey:          placementBinding.Namespace,
	}
	workList := &placementv1beta1.WorkList{}
	if err := r.HubClient.List(ctx, workList, labelSelector, client.InNamespace(workNSName)); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "failed to list work objects associated with the binding", true,
			"work", "namespace", workNSName)
		return wrappedErr
	}

	for idx := range workList.Items {
		work := &workList.Items[idx]

		if err := r.HubClient.Delete(ctx, work); err != nil && !apierrors.IsNotFound(err) {
			wrappedErr := errors.NewAPIServerError(err, "failed to delete work object associated with the binding", true, "work", klog.KObj(work), "controllerName", controllerName)
			return wrappedErr
		}
		klog.V(2).InfoS("Deleted work object associated with the binding", "work", klog.KObj(work), "controller", controllerName)
	}
	return nil
}

func (r *Reconciler) removeFinalizer(ctx context.Context, placementBinding *experimentalv1beta1.PlacementBinding) error {
	if controllerutil.ContainsFinalizer(placementBinding, placementBindingCleanupFinalizer) {
		controllerutil.RemoveFinalizer(placementBinding, placementBindingCleanupFinalizer)
		if err := r.HubClient.Update(ctx, placementBinding); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", true, "placementBinding", placementBinding.Name, "controllerName", controllerName)
			return wrappedErr
		}
	}
	return nil
}
