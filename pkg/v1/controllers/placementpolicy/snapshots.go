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

package placementpolicy

import (
	"context"

	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	"k8s.io/klog/v2"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
)

// retrieveLatestResourceSnapshot returns the primary placement resource snapshot for the placement policy,
// creating one if none exists yet, and reports whether it is up to date with the currently selected resources.
//
// The current resource snapshot manager creates a snapshot synchronously (rather than requiring a caller to
// separately request one and wait for it to appear), and it may return multiple snapshots when the selected
// resources do not fit in a single one; the first entry is always the primary snapshot.
func (r *Reconciler) retrieveLatestResourceSnapshot(
	ctx context.Context,
	placementPolicy *placementv1alpha1.PlacementPolicy,
) (placementv1alpha1.PlacementResourceSnapshotAccessor, bool, error) {
	snapshots, isUpToDate, err := r.PlacementResourceSnapshotManager.SnapshotResourcesIfNoSnapshotExists(ctx, placementPolicy)
	if err != nil {
		klog.ErrorS(err, "Failed to retrieve (or create) the latest resource snapshot", errors.Args(err)...)
		return nil, false, err
	}
	if len(snapshots) == 0 {
		wrappedErr := errors.NewUnexpectedError(nil, "no resource snapshot was returned for the placement policy", "placementPolicy", klog.KObj(placementPolicy))
		klog.ErrorS(wrappedErr, "Failed to retrieve the latest resource snapshot", errors.Args(wrappedErr)...)
		return nil, false, wrappedErr
	}

	return snapshots[0], isUpToDate, nil
}
