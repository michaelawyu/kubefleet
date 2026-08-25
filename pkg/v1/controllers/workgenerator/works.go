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

package workgenerator

import (
	"context"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/parallelizer"
)

const (
	derivedFromSnapshotSourceFmt = "placement-resource-snapshot/%s"
)

var (
	workGVK = schema.GroupVersionKind{
		Group:   placementv1alpha1.GroupVersion.Group,
		Version: placementv1alpha1.GroupVersion.Version,
		Kind:    "Work",
	}
)

func areWorksUpToDate(placementBinding placementv1alpha1.PlacementBindingAccessor, works []placementv1alpha1.Work) bool {
	lastProcessedSnapshotName := ""
	if placementBinding.GetStatus().LastProcessedResourceSnapshotName != nil {
		lastProcessedSnapshotName = *placementBinding.GetStatus().LastProcessedResourceSnapshotName
	}
	primarySnapshotName := placementBinding.GetSpec().ResourceSnapshotName

	if lastProcessedSnapshotName != primarySnapshotName {
		return false
	}

	// Check if the sync strategy of the placement binding still matches that on the work objects.
	syncStrategy := placementBinding.GetSpec().SyncStrategy
	for idx := range works {
		work := &works[idx]
		if !reflect.DeepEqual(work.Spec.SyncStrategy, syncStrategy) {
			return false
		}
	}

	return true
}

func (r *Reconciler) refreshWorks(ctx context.Context,
	placementBinding placementv1alpha1.PlacementBindingAccessor,
	sortedPlacementResourceSnapshots []placementv1alpha1.PlacementResourceSnapshotAccessor,
	works []placementv1alpha1.Work,
) ([]*placementv1alpha1.Work, error) {
	worksToDelete := []*placementv1alpha1.Work{}

	// Build an index of work objects by their names.
	existingWorksByName := make(map[string]*placementv1alpha1.Work, len(works))
	for idx := range works {
		work := &works[idx]
		existingWorksByName[work.GetName()] = work
	}

	createdOrUpdatedWorkNames := sets.Set[string]{}
	// First, build a work object for the primary placement resource snapshot.
	//
	// This work object serves as the owner of all other work objects created for this placement binding. KubeFleet
	// leverages this setup to ensure that if a placement binding is deleted, all the work objects created for it
	// will be cleaned up automatically by K8s' built-in GC process.
	//
	// We cannot set the placement binding itself as the owner of the work objects as they might reside in
	// different namespaces, and cross-namespace ownership is not allowed in K8s. The list-then-delete loop has
	// limitations as well, as stale cache might leave some work objects behind.
	primaryPlacementResourceSnapshot := sortedPlacementResourceSnapshots[0]
	workToCreateOrUpdate, err := buildWorkObjectFor(primaryPlacementResourceSnapshot, placementBinding, nil)
	if err != nil {
		return nil, errors.Wraps(err, "failed to build work object for primary placement resource snapshot",
			"primaryPlacementResourceSnapshot", klog.KObj(primaryPlacementResourceSnapshot))
	}
	createdOrUpdatedWorks, err := r.createOrUpdateWorkObjects(ctx, []*placementv1alpha1.Work{workToCreateOrUpdate}, placementBinding)
	if err != nil {
		return nil, errors.Wraps(err, "failed to create or update work object for primary placement resource snapshot",
			"primaryPlacementResourceSnapshot", klog.KObj(primaryPlacementResourceSnapshot))
	}
	createdOrUpdatedWorkNames.Insert(createdOrUpdatedWorks[0].GetName())
	ownerWorkObjRef := metav1.NewControllerRef(createdOrUpdatedWorks[0], workGVK)

	// Then build work objects for any secondary placement resource snapshots.
	var additionalWorksToCreateOrUpdate []*placementv1alpha1.Work
	for idx := 1; idx < len(sortedPlacementResourceSnapshots); idx++ {
		snapshot := sortedPlacementResourceSnapshots[idx]
		work, err := buildWorkObjectFor(snapshot, placementBinding, ownerWorkObjRef)
		if err != nil {
			return nil, errors.Wraps(err, "failed to build work object for placement resource snapshot",
				"placementResourceSnapshot", klog.KObj(snapshot))
		}
		if alreadyCreatedOrUpdated := createdOrUpdatedWorkNames.Has(work.GetName()); alreadyCreatedOrUpdated {
			return nil, errors.NewUnexpectedError(nil, "duplicate work object built for placement resource snapshot",
				"work", klog.KObj(work), "placementResourceSnapshot", klog.KObj(snapshot))
		}
		additionalWorksToCreateOrUpdate = append(additionalWorksToCreateOrUpdate, work)
		createdOrUpdatedWorkNames.Insert(work.GetName())
	}

	// Check for dangling work objects (those that are no longer linked with any source) and add them to the
	// deletion list.
	for _, work := range existingWorksByName {
		if updated := createdOrUpdatedWorkNames.Has(work.GetName()); updated {
			continue
		}
		klog.V(2).InfoS("A work object is no longer needed; mark it for deletion", "work", klog.KObj(work))
		worksToDelete = append(worksToDelete, work)
	}

	// Issue the create or update ops for the secondary work objects in parallel.
	additionalCreatedOrUpdatedWorks, err := r.createOrUpdateWorkObjects(ctx, additionalWorksToCreateOrUpdate, placementBinding)
	if err != nil {
		return nil, errors.Wraps(err, "failed to create or update additional work objects for secondary placement resource snapshots")
	}
	createdOrUpdatedWorks = append(createdOrUpdatedWorks, additionalCreatedOrUpdatedWorks...)

	// Issue the delete ops in parallel.
	if err := r.deleteWorkObjects(ctx, worksToDelete, placementBinding); err != nil {
		return nil, errors.Wraps(err, "failed to delete dangling work objects")
	}

	return createdOrUpdatedWorks, nil
}

func buildWorkObjectFor(
	placementResourceSnapshot placementv1alpha1.PlacementResourceSnapshotAccessor,
	placementBinding placementv1alpha1.PlacementBindingAccessor,
	ownerWorkObjRef *metav1.OwnerReference,
) (*placementv1alpha1.Work, error) {
	snapshotSubIdx := placementResourceSnapshot.GetLabels()[placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey]
	if len(snapshotSubIdx) == 0 {
		return nil, errors.NewUnexpectedError(nil, "no sub-index label found on the placement resource snapshot")
	}
	derivedFromSnapshotSrc := fmt.Sprintf(derivedFromSnapshotSourceFmt, snapshotSubIdx)
	placementBindingSpec := placementBinding.GetSpec()
	placementResourceSnapshotSpec := placementResourceSnapshot.GetSpec()

	workName, err := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(placementBinding, snapshotSubIdx == "0", snapshotSubIdx)
	if err != nil {
		return nil, errors.Wraps(err, "failed to generate unique name for the work object", "snapshotSubIdx", snapshotSubIdx)
	}

	work := &placementv1alpha1.Work{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: fmt.Sprintf(utils.NamespaceNameFormat, placementBinding.GetSpec().ClusterName),
			Name:      workName,
		},
	}
	updateWorkObjectMetadataAndSpec(
		work,
		ownerWorkObjRef,
		placementBinding.GetNamespace(),
		placementBindingSpec.PlacementPolicyName,
		placementBinding.GetName(),
		derivedFromSnapshotSrc,
		placementResourceSnapshotSpec.Resources,
		placementBindingSpec.SyncStrategy,
	)
	return work, nil
}

func updateWorkObjectMetadataAndSpec(
	work *placementv1alpha1.Work,
	ownerWorkObjRef *metav1.OwnerReference,
	ownerNamespace, ownerPlacementPolicy, ownerPlacementBinding, derivedFromSource string,
	resources []placementv1alpha1.SnapshottedResource,
	syncStrategy *placementv1alpha1.SyncStrategy,
) {
	// Add owner reference to the work object.
	if ownerWorkObjRef != nil {
		work.SetOwnerReferences([]metav1.OwnerReference{*ownerWorkObjRef})
	}

	// Set the derived from source annotation on the work object.
	//
	// For work objects derived from placement resource snapshots, the annotation is set with the value
	// `placement-resource-snapshot/[SUB-INDEX]`, where `[SUB-INDEX]` is the sub-index of the placement
	// resource snapshot that the work object is derived from.
	//
	// Sub-indices are used here instead of indices to avoid any fluctuations caused by the progression
	// of placement resource snapshots over rollouts.
	annotations := work.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[placementv1alpha1.WorkDerivedFromSourceAnnotationKey] = derivedFromSource
	work.SetAnnotations(annotations)

	// Set the owner labels on the work object.
	labels := work.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[placementv1alpha1.WorkOwnerNamespaceLabelKey] = ownerNamespace
	labels[placementv1alpha1.WorkOwnedByPlacementPolicyLabelKey] = ownerPlacementPolicy
	labels[placementv1alpha1.WorkOwnedByPlacementBindingLabelKey] = ownerPlacementBinding
	work.SetLabels(labels)

	// Set the snapshotted resources on the work object.
	manifests := make([]placementv1alpha1.Manifest, len(resources))
	for i := range resources {
		manifests[i] = placementv1alpha1.Manifest{RawExtension: resources[i].Manifest}
	}
	work.Spec.Manifests = manifests

	// Set the sync strategy on the work object.
	work.Spec.SyncStrategy = syncStrategy
}

func (r *Reconciler) createOrUpdateWorkObjects(
	ctx context.Context,
	worksToCreateOrUpdate []*placementv1alpha1.Work,
	placementBinding placementv1alpha1.PlacementBindingAccessor,
) ([]*placementv1alpha1.Work, error) {
	childCtx, childCancel := context.WithCancel(ctx)
	defer childCancel()

	createdOrUpdatedWorks := make([]*placementv1alpha1.Work, len(worksToCreateOrUpdate))
	errFlag := parallelizer.NewErrorFlag()
	r.parallelizer.ParallelizeUntil(childCtx, len(worksToCreateOrUpdate), func(idx int) {
		work := worksToCreateOrUpdate[idx]

		createdOrUpdatedWork := &placementv1alpha1.Work{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: work.GetNamespace(),
				Name:      work.GetName(),
			},
		}
		resOp, err := controllerutil.CreateOrUpdate(childCtx, r.hubClient, createdOrUpdatedWork, func() error {
			// Work objects are considered to be fully internal KubeFleet resources; for this reason
			// here the control loop chooses to overwrite the spec, labels, annotations, and owner references of
			// the work object with the latest values instead of attempting to do a merge.
			createdOrUpdatedWork.Spec = work.Spec
			createdOrUpdatedWork.SetLabels(work.GetLabels())
			createdOrUpdatedWork.SetAnnotations(work.GetAnnotations())
			createdOrUpdatedWork.SetOwnerReferences(work.GetOwnerReferences())
			return nil
		})
		if err != nil {
			wrappedErr := errors.Wraps(err, "failed to create or update work object",
				"work", klog.KObj(work), "resOp", resOp)
			errFlag.Raise(wrappedErr)
			childCancel()
			return
		}

		createdOrUpdatedWorks[idx] = createdOrUpdatedWork
		klog.V(2).InfoS("Successfully created or updated work object",
			"work", klog.KObj(createdOrUpdatedWork), "resOp", resOp,
			"placementBinding", klog.KObj(placementBinding))
	}, "createOrUpdateWorkObjects")
	if err := errFlag.Lower(); err != nil {
		return nil, err
	}
	return createdOrUpdatedWorks, nil
}

func (r *Reconciler) deleteWorkObjects(
	ctx context.Context,
	worksToDelete []*placementv1alpha1.Work,
	placementBinding placementv1alpha1.PlacementBindingAccessor,
) error {
	childCtx, childCancel := context.WithCancel(ctx)
	defer childCancel()

	errFlag := parallelizer.NewErrorFlag()
	r.parallelizer.ParallelizeUntil(childCtx, len(worksToDelete), func(idx int) {
		work := worksToDelete[idx]

		if err := r.hubClient.Delete(childCtx, work); err != nil && !apierrors.IsNotFound(err) {
			wrappedErr := errors.Wraps(err, "failed to delete work object", "work", klog.KObj(work))
			errFlag.Raise(wrappedErr)
			childCancel()
			return
		}
		klog.V(2).InfoS("Successfully deleted work object",
			"work", klog.KObj(work),
			"placementBinding", klog.KObj(placementBinding))
	}, "deleteWorkObjects")
	return errFlag.Lower()
}
