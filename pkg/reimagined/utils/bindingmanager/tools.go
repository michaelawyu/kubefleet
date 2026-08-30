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

package bindingmanager

import (
	"context"
	"reflect"

	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func ClaimAsBindingManager(
	ctx context.Context,
	hubClient client.Client,
	placement *experimentalv1beta1.WorkloadPlacement,
	wantBindingManager *experimentalv1beta1.PlacementBindingManager,
) (bool, error) {
	if wantBindingManager == nil {
		return false, errors.Wraps(nil, "cannot make a claim with an empty binding manager")
	}

	// Should apply anyway to ensure that the change is always respected.
	bindingManagers := placement.Status.BindingManagers
	var alreadyClaimedByOthers bool
	switch {
	case wantBindingManager.Mode == experimentalv1beta1.BindingManagerModeExclusive && wantBindingManager.ControllerName != nil:
		// The caller would like to claim the binding manager role exclusively with a controller
		// name.
		for idx := range bindingManagers {
			// The list of binding managers should be empty or should contain only the current controller.
			bindingManager := bindingManagers[idx]
			if bindingManager.ControllerName == nil || *bindingManager.ControllerName != *wantBindingManager.ControllerName {
				alreadyClaimedByOthers = true
				break
			}
		}
	case wantBindingManager.Mode == experimentalv1beta1.BindingManagerModeExclusive:
		// The caller would like to claim the binding manager role exclusively with an object reference.
		for idx := range bindingManagers {
			// The list of binding managers should be empty or should contain only the current object reference.
			bindingManager := bindingManagers[idx]
			if bindingManager.ObjectRef == nil || !reflect.DeepEqual(bindingManager.ObjectRef, wantBindingManager.ObjectRef) {
				alreadyClaimedByOthers = true
				break
			}
		}
	default:
		// No need to implement the support for shared mode at this moment. Simply return an error.
		return false, errors.Wraps(nil, "unsupported binding manager claim mode; only exclusive mode with a controller name or an object reference is supported", "mode", wantBindingManager.Mode)
	}

	if alreadyClaimedByOthers {
		// The binding manager role has already been claimed by another controller; in this case, give up the
		// scheduling attempt and requeue after some time.
		return false, nil
	}

	if len(bindingManagers) == 0 {
		// There is no active binding manager; claim the role.
		//
		// The request would fail if the list of binding managers has been updated by another agent.
		bindingManagers = append(bindingManagers, *wantBindingManager)
		placement.Status.BindingManagers = bindingManagers
		if err := hubClient.Status().Update(ctx, placement); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", false, "workloadPlacement", klog.KObj(placement))
			return false, wrappedErr
		}
		return true, nil
	}

	// The current controller has already claimed the binding manager role.
	//
	// The current view might be stale; to avoid this complication, do a dry-run to check if the view is still
	// up-to-date.
	placementToPatch := placement.DeepCopy()
	placementToPatch.Status.BindingManagers = []experimentalv1beta1.PlacementBindingManager{}
	if err := hubClient.Status().Patch(
		ctx,
		placementToPatch,
		client.MergeFromWithOptions(placement, client.MergeFromWithOptimisticLock{}),
		client.DryRunAll,
	); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "", false, "workloadPlacement", klog.KObj(placement))
		klog.ErrorS(wrappedErr, "The current view of binding managers might be stale, or the freshness verification attempt has failed", errors.Args(wrappedErr)...)
		return false, wrappedErr
	}
	return true, nil
}

func RelinquishBindingManagerRoleAnyway(
	ctx context.Context,
	hubClient client.Client,
	placement *experimentalv1beta1.WorkloadPlacement,
	wantBindingManager *experimentalv1beta1.PlacementBindingManager,
) error {
	if wantBindingManager == nil {
		return errors.Wraps(nil, "cannot relinquish with an empty binding manager")
	}

	bindingManagers := placement.Status.BindingManagers
	updatedBindingManagers := make([]experimentalv1beta1.PlacementBindingManager, 0, len(bindingManagers))
	for idx := range bindingManagers {
		bindingManager := bindingManagers[idx]
		if !reflect.DeepEqual(bindingManager, *wantBindingManager) {
			updatedBindingManagers = append(updatedBindingManagers, bindingManager)
		}
	}

	if len(updatedBindingManagers) == len(bindingManagers) {
		// No removal is needed; the current view might be stale. To avoid this complication, do a dry-run to check if the view is still up-to-date.
		placementToPatch := placement.DeepCopy()
		placementToPatch.Status.BindingManagers = []experimentalv1beta1.PlacementBindingManager{
			{
				Mode:           experimentalv1beta1.BindingManagerModeExclusive,
				ControllerName: ptr.To(""),
			},
		}
		if err := hubClient.Status().Patch(
			ctx,
			placementToPatch,
			client.MergeFromWithOptions(placement, client.MergeFromWithOptimisticLock{}),
			client.DryRunAll,
		); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", false, "workloadPlacement", klog.KObj(placement))
			klog.ErrorS(wrappedErr, "The current view of binding managers might be stale, or the freshness verification attempt has failed", errors.Args(wrappedErr)...)
			return wrappedErr
		}
		return nil
	} else {
		// Update the list of binding managers by removing the current controller;
		// the request would fail if the list has been updated by another agent.
		placement.Status.BindingManagers = updatedBindingManagers
		if err := hubClient.Status().Update(ctx, placement); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", false, "workloadPlacement", klog.KObj(placement))
			return wrappedErr
		}
		return nil
	}
}
