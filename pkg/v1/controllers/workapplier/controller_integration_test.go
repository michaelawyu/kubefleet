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

package workapplier

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils"
)

const (
	workNameTemplate = "work-%s"
	nsNameTemplate   = "ns-%s"

	deployName = "app"
)

const (
	// Eventually polling interval and timeout.
	eventuallyInterval = time.Millisecond * 500
	eventuallyDuration = time.Second * 10
)

var (
	ignoreFieldObjectMetaAutoGenFields = cmpopts.IgnoreFields(metav1.ObjectMeta{}, "CreationTimestamp", "Generation", "ResourceVersion", "SelfLink", "UID", "ManagedFields")
	ignoreFieldAppliedWorkStatus       = cmpopts.IgnoreFields(placementv1alpha1.AppliedWork{}, "Status")
	ignoreFieldConditionLTTMsg         = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message")

	// ns and deploy are base fixtures; each test case derives its own copies (with a unique namespace
	// and, for ns, a unique name) so that tests can run independently of one another. The envtest
	// package does not support namespace deletion, so a fresh namespace name is used per test case.
	ns = &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
	}
	deploy = &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: deployName,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": "nginx",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "nginx",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nginx",
							Image: "nginx",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 80,
								},
							},
						},
					},
				},
			},
		},
	}
)

// marshalK8sObjJSON marshals a Kubernetes object to JSON bytes for use as a Work manifest.
func marshalK8sObjJSON(obj runtime.Object) []byte {
	data, err := json.Marshal(obj)
	Expect(err).To(BeNil(), "Failed to marshal the k8s object to JSON")
	return data
}

// createWorkObject creates a new, standalone primary Work object with the given manifests.
//
// The work applier only reconciles "primary" work objects, i.e. those carrying the owner placement
// binding label and the linked work count annotation (see controller.go and retrieval.go); since these
// tests do not go through the work generator, the labels/annotations that would normally link a Work
// object to a placement binding and a placement resource snapshot are faked here, simulating a
// cluster-scoped owner placement binding with a single (this) linked work object.
func createWorkObject(workName string, syncStrategy *placementv1alpha1.SyncStrategy, rawManifestJSON ...[]byte) {
	manifests := make([]placementv1alpha1.Manifest, len(rawManifestJSON))
	for idx := range rawManifestJSON {
		manifests[idx] = placementv1alpha1.Manifest{
			RawExtension: runtime.RawExtension{
				Raw: rawManifestJSON[idx],
			},
		}
	}

	work := &placementv1alpha1.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workName,
			Namespace: memberClusterReservedNSName,
			Labels: map[string]string{
				placementv1alpha1.WorkOwnedByPlacementBindingLabelKey: workName,
			},
			Annotations: map[string]string{
				placementv1alpha1.LinkedWorkCountAnnotationKey:                              "1",
				placementv1alpha1.WorkLinkedToPrimaryPlacementResourceSnapshotAnnotationKey: workName,
			},
		},
		Spec: placementv1alpha1.WorkSpec{
			Manifests:    manifests,
			SyncStrategy: syncStrategy,
		},
	}
	Expect(hubClient.Create(ctx, work)).To(Succeed())
}

func updateWorkObject(workName string, syncStrategy *placementv1alpha1.SyncStrategy, rawManifestJSON ...[]byte) {
	manifests := make([]placementv1alpha1.Manifest, len(rawManifestJSON))
	for idx := range rawManifestJSON {
		manifests[idx] = placementv1alpha1.Manifest{
			RawExtension: runtime.RawExtension{
				Raw: rawManifestJSON[idx],
			},
		}
	}

	work := &placementv1alpha1.Work{}
	Expect(hubClient.Get(ctx, client.ObjectKey{Name: workName, Namespace: memberClusterReservedNSName}, work)).To(Succeed())
	work.Spec.Manifests = manifests
	work.Spec.SyncStrategy = syncStrategy
	Expect(hubClient.Update(ctx, work)).To(Succeed())
}

func deleteWorkObject(workName string) {
	work := &placementv1alpha1.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workName,
			Namespace: memberClusterReservedNSName,
		},
	}
	Expect(hubClient.Delete(ctx, work)).To(Succeed(), "Failed to delete the Work object")
}

func workObjectRemovedActual(workName string) func() error {
	return func() error {
		work := &placementv1alpha1.Work{}
		err := hubClient.Get(ctx, client.ObjectKey{Name: workName, Namespace: memberClusterReservedNSName}, work)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("work object still exists")
	}
}

func workFinalizerAddedActual(workName string) func() error {
	return func() error {
		work := &placementv1alpha1.Work{}
		if err := hubClient.Get(ctx, client.ObjectKey{Name: workName, Namespace: memberClusterReservedNSName}, work); err != nil {
			return fmt.Errorf("failed to retrieve the Work object: %w", err)
		}

		if !controllerutil.ContainsFinalizer(work, workApplierCleanupFinalizer) {
			return fmt.Errorf("cleanup finalizer has not been added")
		}
		return nil
	}
}

func appliedWorkCreatedActual(workName string) func() error {
	return func() error {
		appliedWork := &placementv1alpha1.AppliedWork{}
		if err := memberClient.Get(ctx, client.ObjectKey{Name: workName}, appliedWork); err != nil {
			return fmt.Errorf("failed to retrieve the AppliedWork object: %w", err)
		}

		wantAppliedWork := &placementv1alpha1.AppliedWork{
			ObjectMeta: metav1.ObjectMeta{
				Name: workName,
			},
			Spec: placementv1alpha1.AppliedWorkSpec{
				WorkName:      workName,
				WorkNamespace: memberClusterReservedNSName,
			},
		}
		if diff := cmp.Diff(
			appliedWork, wantAppliedWork,
			ignoreFieldObjectMetaAutoGenFields,
			ignoreFieldAppliedWorkStatus,
		); diff != "" {
			return fmt.Errorf("appliedWork diff (-got +want):\n%s", diff)
		}
		return nil
	}
}

func prepareAppliedWorkOwnerRef(workName string) *metav1.OwnerReference {
	appliedWork := &placementv1alpha1.AppliedWork{}
	Expect(memberClient.Get(ctx, client.ObjectKey{Name: workName}, appliedWork)).To(Succeed(), "Failed to retrieve the AppliedWork object")

	return &metav1.OwnerReference{
		APIVersion:         placementv1alpha1.GroupVersion.String(),
		Kind:               "AppliedWork",
		Name:               appliedWork.Name,
		UID:                appliedWork.GetUID(),
		BlockOwnerDeletion: ptr.To(true),
	}
}

func regularNSObjectAppliedActual(nsName string, appliedWorkOwnerRef *metav1.OwnerReference) func() error {
	return func() error {
		gotNS := &corev1.Namespace{}
		if err := memberClient.Get(ctx, client.ObjectKey{Name: nsName}, gotNS); err != nil {
			return fmt.Errorf("failed to retrieve the NS object: %w", err)
		}

		// To ignore default values automatically, here the test suite rebuilds the objects.
		wantNS := ns.DeepCopy()
		wantNS.TypeMeta = metav1.TypeMeta{}
		wantNS.Name = nsName
		wantNS.OwnerReferences = []metav1.OwnerReference{
			*appliedWorkOwnerRef,
		}

		rebuiltGotNS := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:            gotNS.Name,
				OwnerReferences: gotNS.OwnerReferences,
			},
		}

		if diff := cmp.Diff(rebuiltGotNS, wantNS); diff != "" {
			return fmt.Errorf("namespace diff (-got +want):\n%s", diff)
		}
		return nil
	}
}

func regularDeploymentObjectAppliedActual(nsName, deployName string, appliedWorkOwnerRef *metav1.OwnerReference) func() error {
	return func() error {
		gotDeploy := &appsv1.Deployment{}
		if err := memberClient.Get(ctx, client.ObjectKey{Namespace: nsName, Name: deployName}, gotDeploy); err != nil {
			return fmt.Errorf("failed to retrieve the Deployment object: %w", err)
		}

		// To ignore default values automatically, here the test suite rebuilds the objects.
		wantDeploy := deploy.DeepCopy()
		wantDeploy.TypeMeta = metav1.TypeMeta{}
		wantDeploy.Namespace = nsName
		wantDeploy.Name = deployName
		wantDeploy.OwnerReferences = []metav1.OwnerReference{
			*appliedWorkOwnerRef,
		}

		if len(gotDeploy.Spec.Template.Spec.Containers) != 1 {
			return fmt.Errorf("number of containers in the Deployment object, got %d, want %d", len(gotDeploy.Spec.Template.Spec.Containers), 1)
		}
		if len(gotDeploy.Spec.Template.Spec.Containers[0].Ports) != 1 {
			return fmt.Errorf("number of ports in the first container, got %d, want %d", len(gotDeploy.Spec.Template.Spec.Containers[0].Ports), 1)
		}
		rebuiltGotDeploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:       gotDeploy.Namespace,
				Name:            gotDeploy.Name,
				OwnerReferences: gotDeploy.OwnerReferences,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: gotDeploy.Spec.Replicas,
				Selector: gotDeploy.Spec.Selector,
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": gotDeploy.Spec.Template.Labels["app"],
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  gotDeploy.Spec.Template.Spec.Containers[0].Name,
								Image: gotDeploy.Spec.Template.Spec.Containers[0].Image,
								Ports: []corev1.ContainerPort{
									{
										ContainerPort: gotDeploy.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort,
									},
								},
							},
						},
					},
				},
			},
		}
		if diff := cmp.Diff(rebuiltGotDeploy, wantDeploy); diff != "" {
			return fmt.Errorf("deployment diff (-got +want):\n%s", diff)
		}
		return nil
	}
}

func markDeploymentAsAvailable(nsName, deployName string) {
	gotDeploy := &appsv1.Deployment{}
	Expect(memberClient.Get(ctx, client.ObjectKey{Namespace: nsName, Name: deployName}, gotDeploy)).To(Succeed(), "Failed to retrieve the Deployment object")

	now := metav1.Now()
	requiredReplicas := int32(1)
	if gotDeploy.Spec.Replicas != nil {
		requiredReplicas = *gotDeploy.Spec.Replicas
	}
	gotDeploy.Status = appsv1.DeploymentStatus{
		ObservedGeneration:  gotDeploy.Generation,
		Replicas:            requiredReplicas,
		UpdatedReplicas:     requiredReplicas,
		ReadyReplicas:       requiredReplicas,
		AvailableReplicas:   requiredReplicas,
		UnavailableReplicas: 0,
		Conditions: []appsv1.DeploymentCondition{
			{
				Type:               appsv1.DeploymentAvailable,
				Status:             corev1.ConditionTrue,
				Reason:             "MarkedAsAvailable",
				Message:            "Deployment has been marked as available",
				LastUpdateTime:     now,
				LastTransitionTime: now,
			},
		},
	}
	Expect(memberClient.Status().Update(ctx, gotDeploy)).To(Succeed(), "Failed to mark the Deployment object as available")
}

func workStatusUpdated(
	workName string,
	workConds []metav1.Condition,
	manifestStatuses []placementv1alpha1.PerManifestStatus,
) func() error {
	return func() error {
		work := &placementv1alpha1.Work{}
		if err := hubClient.Get(ctx, client.ObjectKey{Name: workName, Namespace: memberClusterReservedNSName}, work); err != nil {
			return fmt.Errorf("failed to retrieve the Work object: %w", err)
		}

		// Update the conditions with the observed generation; note that this is the generation of the
		// Work object itself, as opposed to the observed generation of a manifest condition, which is
		// that of the applied resource in the member cluster.
		for idx := range workConds {
			workConds[idx].ObservedGeneration = work.Generation
		}
		wantWorkStatus := placementv1alpha1.WorkStatus{
			Conditions: workConds,
			Manifests:  manifestStatuses,
		}

		if diff := cmp.Diff(
			work.Status, wantWorkStatus,
			ignoreFieldConditionLTTMsg,
		); diff != "" {
			return fmt.Errorf("work status diff (-got, +want):\n%s", diff)
		}
		return nil
	}
}

func appliedWorkStatusUpdated(workName string, appliedResources []placementv1alpha1.AppliedResource) func() error {
	return func() error {
		appliedWork := &placementv1alpha1.AppliedWork{}
		if err := memberClient.Get(ctx, client.ObjectKey{Name: workName}, appliedWork); err != nil {
			return fmt.Errorf("failed to retrieve the AppliedWork object: %w", err)
		}

		wantAppliedWorkStatus := placementv1alpha1.AppliedWorkStatus{
			AppliedResources: appliedResources,
		}
		if diff := cmp.Diff(appliedWork.Status, wantAppliedWorkStatus); diff != "" {
			return fmt.Errorf("appliedWork status diff (-got, +want):\n%s", diff)
		}
		return nil
	}
}

// checkNSOwnerReferences verifies that the Namespace object carries the AppliedWork object as an owner
// reference.
//
// Kubebuilder suggests that in a testing environment like this, to check for the existence of the
// AppliedWork object OwnerReference in the Namespace object rather than the Namespace object's actual
// removal (https://book.kubebuilder.io/reference/envtest.html#testing-considerations), as the envtest
// package does not support namespace deletion.
func checkNSOwnerReferences(workName, nsName string) {
	appliedWork := &placementv1alpha1.AppliedWork{}
	Expect(memberClient.Get(ctx, client.ObjectKey{Name: workName}, appliedWork)).To(Succeed(), "Failed to retrieve the AppliedWork object")

	gotNS := &corev1.Namespace{}
	Expect(memberClient.Get(ctx, client.ObjectKey{Name: nsName}, gotNS)).To(Succeed(), "Failed to retrieve the Namespace object")
	Expect(gotNS.OwnerReferences).To(ContainElement(metav1.OwnerReference{
		APIVersion:         placementv1alpha1.GroupVersion.String(),
		Kind:               "AppliedWork",
		Name:               appliedWork.Name,
		UID:                appliedWork.GetUID(),
		BlockOwnerDeletion: ptr.To(true),
	}), "AppliedWork owner reference not found in the Namespace object")
}

func appliedWorkRemovedActual(workName string) func() error {
	return func() error {
		appliedWork := &placementv1alpha1.AppliedWork{}
		if err := memberClient.Get(ctx, client.ObjectKey{Name: workName}, appliedWork); err != nil {
			if apierrors.IsNotFound(err) {
				// The AppliedWork object has been deleted, which is expected.
				return nil
			}
			return fmt.Errorf("failed to retrieve the AppliedWork object: %w", err)
		}
		if !appliedWork.DeletionTimestamp.IsZero() && controllerutil.ContainsFinalizer(appliedWork, metav1.FinalizerDeleteDependents) {
			// The AppliedWork object is being deleted, but the finalizer is still present. Remove the
			// finalizer, as there is no real garbage collector controller running in this test
			// environment to do so once the (manually deleted) dependents are gone.
			controllerutil.RemoveFinalizer(appliedWork, metav1.FinalizerDeleteDependents)
			Expect(memberClient.Update(ctx, appliedWork)).To(Succeed(), "Failed to remove the finalizer from the AppliedWork object")
		}
		return fmt.Errorf("appliedWork object still exists")
	}
}

func regularDeployRemovedActual(nsName, deployName string) func() error {
	return func() error {
		gotDeploy := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: nsName,
				Name:      deployName,
			},
		}
		// There is no real garbage collector controller running in this test environment, so the
		// dependent (the Deployment object) is deleted here directly; this is a no-op if the work
		// applier itself has already removed it (e.g. as part of garbage collecting a manifest that
		// has been dropped from the Work object).
		if err := memberClient.Delete(ctx, gotDeploy); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete the Deployment object: %w", err)
		}

		if err := memberClient.Get(ctx, client.ObjectKey{Namespace: nsName, Name: deployName}, gotDeploy); !apierrors.IsNotFound(err) {
			return fmt.Errorf("deployment object still exists or an unexpected error occurred: %w", err)
		}
		return nil
	}
}

var _ = Describe("applying manifests", func() {
	Context("apply new manifests (regular)", Ordered, func() {
		workName := fmt.Sprintf(workNameTemplate, utils.RandStr())
		// The environment prepared by the envtest package does not support namespace
		// deletion; each test case would use a new namespace.
		nsName := fmt.Sprintf(nsNameTemplate, utils.RandStr())

		var appliedWorkOwnerRef *metav1.OwnerReference
		var regularNS *corev1.Namespace
		var regularDeploy *appsv1.Deployment

		BeforeAll(func() {
			// Prepare a NS object.
			regularNS = ns.DeepCopy()
			regularNS.Name = nsName
			regularNSJSON := marshalK8sObjJSON(regularNS)

			// Prepare a Deployment object.
			regularDeploy = deploy.DeepCopy()
			regularDeploy.Namespace = nsName
			regularDeploy.Name = deployName
			regularDeployJSON := marshalK8sObjJSON(regularDeploy)

			// Create a new Work object with all the manifest JSONs.
			createWorkObject(workName, nil, regularNSJSON, regularDeployJSON)
		})

		It("should add cleanup finalizer to the Work object", func() {
			Eventually(workFinalizerAddedActual(workName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to add cleanup finalizer to the Work object")
		})

		It("should prepare an AppliedWork object", func() {
			Eventually(appliedWorkCreatedActual(workName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to prepare an AppliedWork object")

			appliedWorkOwnerRef = prepareAppliedWorkOwnerRef(workName)
		})

		It("should apply the manifests", func() {
			// Ensure that the NS object has been applied as expected.
			Eventually(regularNSObjectAppliedActual(nsName, appliedWorkOwnerRef), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to apply the namespace object")

			Expect(memberClient.Get(ctx, client.ObjectKey{Name: nsName}, regularNS)).To(Succeed(), "Failed to retrieve the NS object")

			// Ensure that the Deployment object has been applied as expected.
			Eventually(regularDeploymentObjectAppliedActual(nsName, deployName, appliedWorkOwnerRef), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to apply the deployment object")

			Expect(memberClient.Get(ctx, client.ObjectKey{Namespace: nsName, Name: deployName}, regularDeploy)).To(Succeed(), "Failed to retrieve the Deployment object")
		})

		It("can mark the deployment as available", func() {
			markDeploymentAsAvailable(nsName, deployName)
		})

		It("should update the Work object status", func() {
			// Prepare the status information.
			workConds := []metav1.Condition{
				{
					Type:   placementv1alpha1.WorkCondTypeApplied,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.WorkAppliedCondAllManifestsAppliedReason,
				},
				{
					Type:   placementv1alpha1.WorkCondTypeAvailable,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.WorkAvailableCondAllManifestsAvailableReason,
				},
			}
			// The observed generation of a manifest condition is that of the applied resource on the
			// member cluster side; the exact values are not worth hard-coding, so they are read back
			// from the objects retrieved earlier instead.
			manifestStatuses := []placementv1alpha1.PerManifestStatus{
				{
					Identifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    0,
						APIGroup:   "",
						APIVersion: "v1",
						Kind:       "Namespace",
						Resource:   "namespaces",
						Name:       nsName,
					},
					Conditions: []metav1.Condition{
						{
							Type:               placementv1alpha1.ManifestCondTypeApplied,
							Status:             metav1.ConditionTrue,
							Reason:             string(ApplyResTypeApplied),
							ObservedGeneration: regularNS.Generation,
						},
						{
							Type:               placementv1alpha1.ManifestCondTypeAvailable,
							Status:             metav1.ConditionTrue,
							Reason:             string(AvailabilityResultTypeAvailable),
							ObservedGeneration: regularNS.Generation,
						},
					},
				},
				{
					Identifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    1,
						APIGroup:   "apps",
						APIVersion: "v1",
						Kind:       "Deployment",
						Resource:   "deployments",
						Name:       deployName,
						Namespace:  nsName,
					},
					Conditions: []metav1.Condition{
						{
							Type:               placementv1alpha1.ManifestCondTypeApplied,
							Status:             metav1.ConditionTrue,
							Reason:             string(ApplyResTypeApplied),
							ObservedGeneration: regularDeploy.Generation,
						},
						{
							Type:               placementv1alpha1.ManifestCondTypeAvailable,
							Status:             metav1.ConditionTrue,
							Reason:             string(AvailabilityResultTypeAvailable),
							ObservedGeneration: regularDeploy.Generation,
						},
					},
				},
			}

			Eventually(workStatusUpdated(workName, workConds, manifestStatuses), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to update work status")
		})

		It("should update the AppliedWork object status", func() {
			// Prepare the status information.
			appliedResources := []placementv1alpha1.AppliedResource{
				{
					ManifestIdentifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    0,
						APIGroup:   "",
						APIVersion: "v1",
						Kind:       "Namespace",
						Resource:   "namespaces",
						Name:       nsName,
					},
					UID: regularNS.UID,
				},
				{
					ManifestIdentifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    1,
						APIGroup:   "apps",
						APIVersion: "v1",
						Kind:       "Deployment",
						Resource:   "deployments",
						Name:       deployName,
						Namespace:  nsName,
					},
					UID: regularDeploy.UID,
				},
			}

			Eventually(appliedWorkStatusUpdated(workName, appliedResources), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to update appliedWork status")
		})

		AfterAll(func() {
			// Delete the Work object and related resources.
			deleteWorkObject(workName)

			// Ensure applied manifest has been removed.
			Eventually(regularDeployRemovedActual(nsName, deployName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to remove the deployment object")

			// Kubebuilder suggests that in a testing environment like this, to check for the existence of the AppliedWork object
			// OwnerReference in the Namespace object (https://book.kubebuilder.io/reference/envtest.html#testing-considerations).
			checkNSOwnerReferences(workName, nsName)

			// Ensure that the AppliedWork object has been removed.
			Eventually(appliedWorkRemovedActual(workName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to remove the AppliedWork object")

			Eventually(workObjectRemovedActual(workName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to remove the Work object")

			// The environment prepared by the envtest package does not support namespace
			// deletion; consequently this test suite would not attempt to verify its deletion.
		})
	})

	Context("garbage collect removed manifests", Ordered, func() {
		workName := fmt.Sprintf(workNameTemplate, utils.RandStr())
		// The environment prepared by the envtest package does not support namespace
		// deletion; each test case would use a new namespace.
		nsName := fmt.Sprintf(nsNameTemplate, utils.RandStr())

		var appliedWorkOwnerRef *metav1.OwnerReference
		var regularNS *corev1.Namespace
		var regularDeploy *appsv1.Deployment

		BeforeAll(func() {
			// Prepare a NS object.
			regularNS = ns.DeepCopy()
			regularNS.Name = nsName
			regularNSJSON := marshalK8sObjJSON(regularNS)

			// Prepare a Deployment object.
			regularDeploy = deploy.DeepCopy()
			regularDeploy.Namespace = nsName
			regularDeploy.Name = deployName
			regularDeployJSON := marshalK8sObjJSON(regularDeploy)

			// Create a new Work object with all the manifest JSONs.
			createWorkObject(workName, nil, regularNSJSON, regularDeployJSON)
		})

		It("should add cleanup finalizer to the Work object", func() {
			Eventually(workFinalizerAddedActual(workName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to add cleanup finalizer to the Work object")
		})

		It("should prepare an AppliedWork object", func() {
			Eventually(appliedWorkCreatedActual(workName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to prepare an AppliedWork object")

			appliedWorkOwnerRef = prepareAppliedWorkOwnerRef(workName)
		})

		It("should apply the manifests", func() {
			// Ensure that the NS object has been applied as expected.
			Eventually(regularNSObjectAppliedActual(nsName, appliedWorkOwnerRef), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to apply the namespace object")

			Expect(memberClient.Get(ctx, client.ObjectKey{Name: nsName}, regularNS)).To(Succeed(), "Failed to retrieve the NS object")

			// Ensure that the Deployment object has been applied as expected.
			Eventually(regularDeploymentObjectAppliedActual(nsName, deployName, appliedWorkOwnerRef), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to apply the deployment object")

			Expect(memberClient.Get(ctx, client.ObjectKey{Namespace: nsName, Name: deployName}, regularDeploy)).To(Succeed(), "Failed to retrieve the Deployment object")
		})

		It("can mark the deployment as available", func() {
			markDeploymentAsAvailable(nsName, deployName)
		})

		It("should update the Work object status", func() {
			// Prepare the status information.
			workConds := []metav1.Condition{
				{
					Type:   placementv1alpha1.WorkCondTypeApplied,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.WorkAppliedCondAllManifestsAppliedReason,
				},
				{
					Type:   placementv1alpha1.WorkCondTypeAvailable,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.WorkAvailableCondAllManifestsAvailableReason,
				},
			}
			manifestStatuses := []placementv1alpha1.PerManifestStatus{
				{
					Identifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    0,
						APIGroup:   "",
						APIVersion: "v1",
						Kind:       "Namespace",
						Resource:   "namespaces",
						Name:       nsName,
					},
					Conditions: []metav1.Condition{
						{
							Type:               placementv1alpha1.ManifestCondTypeApplied,
							Status:             metav1.ConditionTrue,
							Reason:             string(ApplyResTypeApplied),
							ObservedGeneration: regularNS.Generation,
						},
						{
							Type:               placementv1alpha1.ManifestCondTypeAvailable,
							Status:             metav1.ConditionTrue,
							Reason:             string(AvailabilityResultTypeAvailable),
							ObservedGeneration: regularNS.Generation,
						},
					},
				},
				{
					Identifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    1,
						APIGroup:   "apps",
						APIVersion: "v1",
						Kind:       "Deployment",
						Resource:   "deployments",
						Name:       deployName,
						Namespace:  nsName,
					},
					Conditions: []metav1.Condition{
						{
							Type:               placementv1alpha1.ManifestCondTypeApplied,
							Status:             metav1.ConditionTrue,
							Reason:             string(ApplyResTypeApplied),
							ObservedGeneration: regularDeploy.Generation,
						},
						{
							Type:               placementv1alpha1.ManifestCondTypeAvailable,
							Status:             metav1.ConditionTrue,
							Reason:             string(AvailabilityResultTypeAvailable),
							ObservedGeneration: regularDeploy.Generation,
						},
					},
				},
			}

			Eventually(workStatusUpdated(workName, workConds, manifestStatuses), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to update work status")
		})

		It("should update the AppliedWork object status", func() {
			// Prepare the status information.
			appliedResources := []placementv1alpha1.AppliedResource{
				{
					ManifestIdentifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    0,
						APIGroup:   "",
						APIVersion: "v1",
						Kind:       "Namespace",
						Resource:   "namespaces",
						Name:       nsName,
					},
					UID: regularNS.UID,
				},
				{
					ManifestIdentifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    1,
						APIGroup:   "apps",
						APIVersion: "v1",
						Kind:       "Deployment",
						Resource:   "deployments",
						Name:       deployName,
						Namespace:  nsName,
					},
					UID: regularDeploy.UID,
				},
			}

			Eventually(appliedWorkStatusUpdated(workName, appliedResources), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to update appliedWork status")
		})

		It("can delete some manifests", func() {
			// Update the work object and remove the Deployment manifest.

			// Re-prepare the JSON to make sure that type meta info. is included correctly.
			regularNS := ns.DeepCopy()
			regularNS.Name = nsName
			regularNSJSON := marshalK8sObjJSON(regularNS)

			updateWorkObject(workName, nil, regularNSJSON)
		})

		It("should garbage collect removed manifests", func() {
			Eventually(regularDeployRemovedActual(nsName, deployName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to remove the deployment object")
		})

		It("should update the Work object status", func() {
			// Prepare the status information.
			workConds := []metav1.Condition{
				{
					Type:   placementv1alpha1.WorkCondTypeApplied,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.WorkAppliedCondAllManifestsAppliedReason,
				},
				{
					Type:   placementv1alpha1.WorkCondTypeAvailable,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.WorkAvailableCondAllManifestsAvailableReason,
				},
			}
			manifestStatuses := []placementv1alpha1.PerManifestStatus{
				{
					Identifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    0,
						APIGroup:   "",
						APIVersion: "v1",
						Kind:       "Namespace",
						Resource:   "namespaces",
						Name:       nsName,
					},
					Conditions: []metav1.Condition{
						{
							Type:               placementv1alpha1.ManifestCondTypeApplied,
							Status:             metav1.ConditionTrue,
							Reason:             string(ApplyResTypeApplied),
							ObservedGeneration: regularNS.Generation,
						},
						{
							Type:               placementv1alpha1.ManifestCondTypeAvailable,
							Status:             metav1.ConditionTrue,
							Reason:             string(AvailabilityResultTypeAvailable),
							ObservedGeneration: regularNS.Generation,
						},
					},
				},
			}

			Eventually(workStatusUpdated(workName, workConds, manifestStatuses), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to update work status")
		})

		It("should update the AppliedWork object status", func() {
			// Prepare the status information.
			appliedResources := []placementv1alpha1.AppliedResource{
				{
					ManifestIdentifier: placementv1alpha1.ManifestIdentifier{
						Ordinal:    0,
						APIGroup:   "",
						APIVersion: "v1",
						Kind:       "Namespace",
						Resource:   "namespaces",
						Name:       nsName,
					},
					UID: regularNS.UID,
				},
			}

			Eventually(appliedWorkStatusUpdated(workName, appliedResources), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to update appliedWork status")
		})

		AfterAll(func() {
			// Delete the Work object and related resources.
			deleteWorkObject(workName)

			// Kubebuilder suggests that in a testing environment like this, to check for the existence of the AppliedWork object
			// OwnerReference in the Namespace object (https://book.kubebuilder.io/reference/envtest.html#testing-considerations).
			checkNSOwnerReferences(workName, nsName)

			// Ensure that the AppliedWork object has been removed.
			Eventually(appliedWorkRemovedActual(workName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to remove the AppliedWork object")

			Eventually(workObjectRemovedActual(workName), eventuallyDuration, eventuallyInterval).Should(Succeed(), "Failed to remove the Work object")
			// The environment prepared by the envtest package does not support namespace
			// deletion; consequently this test suite would not attempt to verify its deletion.
		})
	})
})
