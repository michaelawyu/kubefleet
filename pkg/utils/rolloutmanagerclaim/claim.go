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

package rolloutmanagerclaim

import (
	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"k8s.io/utils/set"
)

func ClaimAsRolloutManager(
	placement placementv1beta1.PlacementObj,
	rolloutManagerRefToAdd *placementv1beta1.RolloutManagerReference,
) (isClaimed, isRefreshNeeded bool) {
	if rolloutManagerRefToAdd == nil {
		return true, false
	}

	rolloutManagers := placement.GetPlacementStatus().RolloutManagedBy
	if rolloutManagers == nil {
		rolloutManagers = []placementv1beta1.RolloutManagerReference{}
	}
	countByManagerKind := make(map[string]int)
	managerUIDs := make(set.Set[string])
	for _, manager := range rolloutManagers {
		kindCnt := countByManagerKind[manager.Kind]
		countByManagerKind[manager.Kind] = kindCnt + 1
		managerUIDs.Insert(string(manager.UID))
	}

	alreadyClaimed := managerUIDs.Has(string(rolloutManagerRefToAdd.UID))
	isClaimExclusive := rolloutManagerRefToAdd.Mode == placementv1beta1.RolloutManagerModeExclusive
	_, hasSameKindManager := countByManagerKind[rolloutManagerRefToAdd.Kind]

	switch {
	case alreadyClaimed:
		// The claim has already been made. No need to add it again.
		return true, false
	case len(countByManagerKind) > 1:
		// There are multiple managers of different kinds. This should not happen
		// in normal operations.
		return false, false
	case isClaimExclusive && len(rolloutManagers) == 0:
		// The claim is exclusive and there are no existing claims. Add the claim.
		rolloutManagers = append(rolloutManagers, *rolloutManagerRefToAdd)
		placement.GetPlacementStatus().RolloutManagedBy = rolloutManagers
		return true, true
	case isClaimExclusive:
		// The claim is exclusive but there are already other rollout managers. Reject the claim.
		return false, false
	case !hasSameKindManager && len(countByManagerKind) > 0:
		// There are already rollout managers of a different kind. Reject the claim.
		return false, false
	default:
		// The claim is of shared mode and there are no conflicting claims. Add the claim.
		rolloutManagers = append(rolloutManagers, *rolloutManagerRefToAdd)
		placement.GetPlacementStatus().RolloutManagedBy = rolloutManagers
		return true, true
	}
}

func RelinquishRolloutManagerRole(
	placement placementv1beta1.PlacementObj,
	rolloutManagerRefToRemove *placementv1beta1.RolloutManagerReference,
) (isRefreshNeeded bool) {
	if rolloutManagerRefToRemove == nil {
		return false
	}

	rolloutManagers := placement.GetPlacementStatus().RolloutManagedBy
	if rolloutManagers == nil {
		return false
	}

	newRolloutManagers := make([]placementv1beta1.RolloutManagerReference, 0, len(rolloutManagers))
	for _, manager := range rolloutManagers {
		if manager.UID == rolloutManagerRefToRemove.UID {
			continue
		}
		newRolloutManagers = append(newRolloutManagers, manager)
	}

	placement.GetPlacementStatus().RolloutManagedBy = newRolloutManagers
	return true
}
