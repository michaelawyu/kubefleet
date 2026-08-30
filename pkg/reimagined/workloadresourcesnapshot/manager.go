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

package workloadresourcesnapshot

import (
	"context"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/resource"
)

const (
	ManagerName = "AppResourceSnapshotManager"
)

type newSnapshotRequest struct {
	workloadPlacement *experimentalv1beta1.WorkloadPlacement

	resCh chan<- *NewSnapshotRequestResult
}

type NewSnapshotRequestResult struct {
	NewSnapshot *experimentalv1beta1.WorkloadResourceSnapshot
	Err         error
}

type Manager struct {
	client        client.Client
	dynamicClient dynamic.Interface

	q chan newSnapshotRequest

	isShutDown atomic.Bool
}

func NewManager(
	client client.Client,
	dynamicClient dynamic.Interface,
	bufferSize int,
) *Manager {
	return &Manager{
		client:        client,
		dynamicClient: dynamicClient,
		q:             make(chan newSnapshotRequest, bufferSize),
	}
}

func (m *Manager) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			klog.V(2).Info("Stopping the app resource snapshot manager; main context is cancelled")
			m.isShutDown.Store(true)
			return nil
		case req := <-m.q:
			m.processOneNewSnapshotRequest(ctx, &req)
		}
	}
}

func (m *Manager) AddNewSnapshotFor(workloadPlacement *experimentalv1beta1.WorkloadPlacement) (<-chan *NewSnapshotRequestResult, error) {
	if m.isShutDown.Load() {
		return nil, errors.NewTransientError(nil, "failed to add new snapshot request; the manager has shut down", "workloadPlacement", klog.KObj(workloadPlacement))
	}

	resCh := make(chan *NewSnapshotRequestResult, 1)
	req := newSnapshotRequest{
		workloadPlacement: workloadPlacement,
		resCh:             resCh,
	}
	select {
	case m.q <- req:
		klog.V(2).InfoS("Added new snapshot request to the app resource snapshot manager workqueue", "workloadPlacement", klog.KObj(workloadPlacement))
	default:
		return nil, errors.NewTransientError(nil, "failed to add new snapshot request; app resource snapshot manager workqueue is full", "workloadPlacement", klog.KObj(workloadPlacement))
	}
	return resCh, nil
}

func (m *Manager) RequestAndWaitForNewSnapshot(
	ctx context.Context,
	workloadPlacement *experimentalv1beta1.WorkloadPlacement,
	maxWaitTime time.Duration,
) (*experimentalv1beta1.WorkloadResourceSnapshot, error) {
	resCh, err := m.AddNewSnapshotFor(workloadPlacement)
	if err != nil {
		return nil, errors.Wraps(err, "failed to request new snapshot for the workload placement", "workloadPlacement", klog.KObj(workloadPlacement))
	}

	childCtx, childCancel := context.WithTimeout(ctx, maxWaitTime)
	defer childCancel()

	select {
	case res := <-resCh:
		if res == nil {
			return nil, errors.NewUnexpectedError(nil, "snapshot creation returns a nil result")
		}
		if res.Err != nil {
			return nil, errors.Wraps(res.Err, "snapshot creation fails with an error")
		}
		return res.NewSnapshot, nil
	case <-childCtx.Done():
		return nil, errors.NewTransientError(childCtx.Err(),
			"context is cancelled while waiting for snapshot to be created",
			"workloadPlacement", klog.KObj(workloadPlacement), "err", childCtx.Err())
	}
}

func (m *Manager) processOneNewSnapshotRequest(ctx context.Context, req *newSnapshotRequest) {
	workloadPlacement := req.workloadPlacement
	if workloadPlacement == nil {
		wrappedErr := errors.NewUnexpectedError(nil, "cannot create new resource snapshot for a nil workload placement object")
		req.resCh <- &NewSnapshotRequestResult{Err: wrappedErr}
		close(req.resCh)
		return
	}

	startTime := time.Now()
	klog.V(2).InfoS("Started to create new app resource snapshot", "workloadPlacement", klog.KObj(workloadPlacement))
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Finished creating new app resource snapshot",
			"workloadPlacement", klog.KObj(workloadPlacement),
			"latencyInMilliseconds", latency)
	}()

	// Retrieve the manifests of this workload placement.

	// Retrieve the workload manifest.
	workloadManifest, err := m.retrieveWorkloadManifestFrom(ctx, workloadPlacement)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to retrieve workload manifest in use by the workload placement",
			"workloadPlacement", klog.KObj(workloadPlacement))
		req.resCh <- &NewSnapshotRequestResult{Err: wrappedErr}
		close(req.resCh)
		return
	}

	// Retrieve the manifests of the additional resources.
	//
	// Note: for simplicity, ignore the case where the user simply shuffles the order
	// of the additional resources in a placement.
	additionalResManifests, err := m.retrieveAdditionalResourceManifestsFrom(ctx, workloadPlacement)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to retrieve additional resources in use by the workload placement",
			"workloadPlacement", klog.KObj(workloadPlacement))
		req.resCh <- &NewSnapshotRequestResult{Err: wrappedErr}
		close(req.resCh)
		return
	}

	// Create a new snapshot.
	newSnapshot, err := m.createNewResourceSnapshot(ctx, workloadPlacement, workloadManifest, additionalResManifests)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to create new app resource snapshot for the workload placement",
			"workloadPlacement", klog.KObj(workloadPlacement))
		req.resCh <- &NewSnapshotRequestResult{Err: wrappedErr}
		close(req.resCh)
		return
	}

	// All done.
	res := &NewSnapshotRequestResult{
		NewSnapshot: newSnapshot,
		Err:         nil,
	}
	req.resCh <- res
	close(req.resCh)
}

func (m *Manager) RetrieveLatestResourceSnapshot(ctx context.Context, workloadPlacement *experimentalv1beta1.WorkloadPlacement) (*experimentalv1beta1.WorkloadResourceSnapshot, error) {
	snapshotList := &experimentalv1beta1.WorkloadResourceSnapshotList{}
	labelSelector := client.MatchingLabels{
		experimentalv1beta1.ResourceSnapshotOwnedByLabelKey: workloadPlacement.Name,
	}
	if err := m.client.List(ctx, snapshotList, labelSelector, client.InNamespace(workloadPlacement.Namespace)); err != nil {
		wrappedErr := errors.NewAPIServerError(err, "failed to list app resource snapshots for the workload placement", false)
		return nil, wrappedErr
	}

	if len(snapshotList.Items) == 0 {
		return nil, nil
	}

	var errs []error
	sort.Slice(snapshotList.Items, func(i, j int) bool {
		iRevisionStr := snapshotList.Items[i].Labels[experimentalv1beta1.ResourceSnapshotRevisionLabelKey]
		jRevisionStr := snapshotList.Items[j].Labels[experimentalv1beta1.ResourceSnapshotRevisionLabelKey]
		iRevision, err := strconv.Atoi(iRevisionStr)
		if err != nil {
			klog.ErrorS(errors.NewUnexpectedError(err, "failed to parse snapshot revision label value to int", "snapshotName", snapshotList.Items[i].Name, "revisionValue", iRevisionStr), "Failed to parse snapshot revision label value to int")
			errs = append(errs, err)
			return false
		}
		jRevision, err := strconv.Atoi(jRevisionStr)
		if err != nil {
			klog.ErrorS(errors.NewUnexpectedError(err, "failed to parse snapshot revision label value to int", "snapshotName", snapshotList.Items[j].Name, "revisionValue", jRevisionStr), "Failed to parse snapshot revision label value to int")
			errs = append(errs, err)
			return false
		}
		return iRevision > jRevision
	})

	if len(errs) > 0 {
		return nil, errors.NewUnexpectedError(nil, "failed to sort app resource snapshots by revision number", "errs", errs)
	}
	return &snapshotList.Items[0], nil
}

// Note: for simplicity, ignore the case where the user simply shuffles the order
// of the additional resources in a placement.
func (m *Manager) IsResourceSnapshotUpToDate(
	ctx context.Context,
	workloadPlacement *experimentalv1beta1.WorkloadPlacement,
	workloadResourceSnapshot *experimentalv1beta1.WorkloadResourceSnapshot,
) (bool, error) {
	// Retrieve the workload manifest.
	workloadManifest, err := m.retrieveWorkloadManifestFrom(ctx, workloadPlacement)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to retrieve workload manifest in use by the workload placement",
			"workloadPlacement", klog.KObj(workloadPlacement))
		return false, wrappedErr
	}
	curWorkloadManifestHash := resource.HashOfBytes(workloadManifest.Manifest.Raw)
	snapshotWorkloadManifestHash := resource.HashOfBytes(workloadResourceSnapshot.Spec.Workload.Manifest.Raw)
	if curWorkloadManifestHash != snapshotWorkloadManifestHash {
		return false, nil
	}

	// Retrieve the manifests of the additional resources.
	additionalResManifests, err := m.retrieveAdditionalResourceManifestsFrom(ctx, workloadPlacement)
	if err != nil {
		wrappedErr := errors.Wraps(err, "failed to retrieve additional resources in use by the workload placement",
			"workloadPlacement", klog.KObj(workloadPlacement))
		return false, wrappedErr
	}
	if len(additionalResManifests) != len(workloadResourceSnapshot.Spec.AdditionalResources) {
		return false, nil
	}
	for i := range additionalResManifests {
		curAdditionalResManifestHash := resource.HashOfBytes(additionalResManifests[i].Manifest.Raw)
		snapshotAdditionalResManifestHash := resource.HashOfBytes(workloadResourceSnapshot.Spec.AdditionalResources[i].Manifest.Raw)

		if curAdditionalResManifestHash != snapshotAdditionalResManifestHash {
			return false, nil
		}
	}
	return true, nil
}
