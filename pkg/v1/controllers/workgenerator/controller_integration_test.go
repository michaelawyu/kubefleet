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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	localregistry "github.com/kubefleet-dev/kubefleet/test/oci"
)

const (
	// The member cluster name used across integration tests.
	memberClusterName = "bravelion"

	// The name of the placement policy used across integration tests.
	placementName = "my-placement"

	// Eventually polling interval and timeout.
	eventuallyInterval = 500 * time.Millisecond
	eventuallyDuration = 10 * time.Second
)

// rawJSON returns the JSON encoding of v as a runtime.RawExtension.
// It panics on marshaling failure to keep test helpers concise.
func rawJSON(v any) runtime.RawExtension {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal object to JSON: %v", err))
	}
	return runtime.RawExtension{Raw: data}
}

var _ = Describe("binding operations", func() {
	Context("when a new binding is created (hub cluster manifests)", Ordered, func() {
		bindingName := "test-binding-hub-manifests"
		snapshotName := "test-snapshot-hub-manifests-rev-1"
		snapshotNameRev2 := "test-snapshot-hub-manifests-rev-2"

		var deploy *appsv1.Deployment
		var cm *corev1.ConfigMap

		var deployRawJSON, cmRawJSON, updatedCmRawJSON runtime.RawExtension

		BeforeAll(func() {
			// Create the PlacementResourceSnapshot that the binding will reference.
			// It captures the Deployment and the ConfigMap as its resources.
			deploy = &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app",
					Namespace: workNSName,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "app"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "app"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "app",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
			}
			cm = &corev1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-config",
					Namespace: workNSName,
				},
				Data: map[string]string{
					"key": "value",
				},
			}

			deployRawJSON = rawJSON(deploy)
			cmRawJSON = rawJSON(cm)
			snapshot := &placementv1alpha1.PlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      snapshotName,
					Namespace: workNSName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         placementName,
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "0",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
				Spec: placementv1alpha1.PlacementResourceSnapshotSpec{
					Resources: []placementv1alpha1.SnapshottedResource{
						{
							Identifier: placementv1alpha1.ObjectReference{
								Name:       "app",
								APIGroup:   "apps",
								APIVersion: "v1",
								Kind:       "Deployment",
							},
							Manifest: deployRawJSON,
						},
						{
							Identifier: placementv1alpha1.ObjectReference{
								Name:       "app-config",
								APIGroup:   "",
								APIVersion: "v1",
								Kind:       "ConfigMap",
							},
							Manifest: cmRawJSON,
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, snapshot)).To(Succeed())

			// Create the binding.
			binding := &placementv1alpha1.PlacementBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bindingName,
					Namespace: workNSName,
				},
				Spec: placementv1alpha1.PlacementBindingSpec{
					PlacementPolicyName:  placementName,
					ClusterName:          memberClusterName,
					ResourceSnapshotName: snapshotName,
				},
			}
			Expect(hubClient.Create(ctx, binding)).To(Succeed())
		})

		AfterAll(func() {
			// Issue the delete (idempotent — ignore not-found if already gone), then wait for
			// the controller to drop its finalizer and fully remove the object.
			Eventually(func() error {
				binding := &placementv1alpha1.PlacementBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}

				if binding.DeletionTimestamp.IsZero() {
					if err := hubClient.Delete(ctx, binding); err != nil {
						return err
					}
				}
				return fmt.Errorf("binding still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "binding should be deleted by the controller")

			// Remove both snapshot revisions and wait for them to be fully deleted.
			for _, name := range []string{snapshotName, snapshotNameRev2} {
				Eventually(func() error {
					snapshot := &placementv1alpha1.PlacementResourceSnapshot{}
					if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: name}, snapshot); err != nil {
						if apierrors.IsNotFound(err) {
							return nil
						}
						return err
					}
					if err := hubClient.Delete(ctx, snapshot); err != nil {
						return err
					}
					return fmt.Errorf("snapshot still exists")
				}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "snapshot should be fully deleted")
			}

			// The controller deletes owned Work objects as part of its cleanup before removing
			// the binding finalizer, so by the time the binding is gone the Work objects should
			// already be gone too. Wait to confirm, filtering to only those owned by this binding.
			Eventually(func() (int, error) {
				workList := &placementv1alpha1.WorkList{}
				if err := hubClient.List(ctx, workList,
					client.InNamespace(memberClusterReservedNSName),
					client.MatchingLabels{
						placementv1alpha1.WorkOwnedByPlacementBindingLabelKey: bindingName,
						placementv1alpha1.WorkOwnerNamespaceLabelKey:          workNSName,
					},
				); err != nil {
					return 0, err
				}
				return len(workList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all Work objects owned by the binding should be cleaned up")
		})

		It("should create a Work object in the member cluster's hub namespace", func() {
			// Compute the expected Work name the same way the controller does (it is derived from the owner
			// placement policy's namespaced name, not the binding's), rather than hard-coding the hash suffix.
			namingBinding := &placementv1alpha1.PlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			wantWork := &placementv1alpha1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wantWorkName,
					Namespace: memberClusterReservedNSName,
					Labels: map[string]string{
						placementv1alpha1.WorkOwnedByPlacementBindingLabelKey: workOwnerLabelValue(bindingName),
						placementv1alpha1.WorkOwnerNamespaceLabelKey:          workNSName,
						placementv1alpha1.WorkOwnedByPlacementPolicyLabelKey:  workOwnerLabelValue(placementName),
					},
					Annotations: map[string]string{
						placementv1alpha1.WorkLinkedToPrimaryPlacementResourceSnapshotAnnotationKey: snapshotName,
						placementv1alpha1.WorkDerivedFromSourceAnnotationKey:                        "placement-resource-snapshot/0",
						placementv1alpha1.WorkOwnedByPlacementPolicyAnnotationKey:                   placementName,
						placementv1alpha1.WorkOwnedByPlacementBindingAnnotationKey:                  bindingName,
						placementv1alpha1.LinkedWorkCountAnnotationKey:                              "1",
					},
				},
				Spec: placementv1alpha1.WorkSpec{
					Manifests: []placementv1alpha1.Manifest{
						{RawExtension: deployRawJSON},
						{RawExtension: cmRawJSON},
					},
				},
			}

			By("waiting for the Work object to be created")
			work := &placementv1alpha1.Work{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{
					Namespace: memberClusterReservedNSName,
					Name:      wantWorkName,
				}, work)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Work object should be created in the member namespace")

			By("verifying the Work object matches the expected state")
			if diff := cmp.Diff(work, wantWork,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation"),
				cmpopts.IgnoreFields(placementv1alpha1.WorkSpec{}, "Manifests"),
			); diff != "" {
				Fail(fmt.Sprintf("Work object mismatch (-got, +want):\n%s", diff))
			}

			By("verifying that the cleanup finalizer is added to the binding")
			binding := &placementv1alpha1.PlacementBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding)).To(Succeed())
			Expect(binding.Finalizers).To(ContainElement(workGeneratorCleanupFinalizer))
		})

		It("should reflect a waiting-for-synchronization and waiting-for-availability-check status on the binding", func() {
			// In the test environment there is no work applier, so the Work object will never gain an Applied
			// or Available condition on its own. The controller sets this "waiting" status as soon as it
			// creates the Work object (see reportPlacementBindingProcessingProgress) and, since the Work object
			// never changes here, does not advance past it.
			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionUnknown,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonWaitingForAvailabilityCheck,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionFalse,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonWaitingForSynchronization,
				},
			}

			By("waiting for the binding status conditions to be populated")
			binding := &placementv1alpha1.PlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(HaveLen(2), "binding should have two status conditions")

			By("verifying the binding status conditions match the expected state")
			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should reflect Applied=True and Available=True on the binding after the Work status is updated", func() {
			namingBinding := &placementv1alpha1.PlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			By("fetching the Work object")
			work := &placementv1alpha1.Work{}
			Expect(hubClient.Get(ctx, types.NamespacedName{
				Namespace: memberClusterReservedNSName,
				Name:      wantWorkName,
			}, work)).To(Succeed())

			By("patching the Work status with Applied=True and Available=True")
			updatedWork := work.DeepCopy()
			updatedWork.Status.Conditions = []metav1.Condition{
				{
					Type:               placementv1alpha1.WorkCondTypeApplied,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsApplied",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               placementv1alpha1.WorkCondTypeAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsAvailable",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedWork)).To(Succeed())

			By("waiting for the binding status to reflect Synchronized=True and AllResourcesAvailable=True")
			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonAllResourcesAvailable,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonAllResourcesSynchronized,
				},
			}
			binding := &placementv1alpha1.PlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(SatisfyAll(
				HaveLen(2),
				ContainElement(HaveField("Status", metav1.ConditionTrue)),
			), "binding should have two True status conditions")

			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should update the ConfigMap and build a new PlacementResourceSnapshot for revision 2", func() {
			updatedCM := cm.DeepCopy()
			updatedCM.Data["key"] = "updated-value"
			updatedCmRawJSON = rawJSON(updatedCM)

			snapshot := &placementv1alpha1.PlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      snapshotNameRev2,
					Namespace: workNSName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         placementName,
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "1",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
				Spec: placementv1alpha1.PlacementResourceSnapshotSpec{
					Resources: []placementv1alpha1.SnapshottedResource{
						{
							Identifier: placementv1alpha1.ObjectReference{
								Name:       "app",
								APIGroup:   "apps",
								APIVersion: "v1",
								Kind:       "Deployment",
							},
							Manifest: deployRawJSON,
						},
						{
							Identifier: placementv1alpha1.ObjectReference{
								Name:       "app-config",
								APIGroup:   "",
								APIVersion: "v1",
								Kind:       "ConfigMap",
							},
							Manifest: updatedCmRawJSON,
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, snapshot)).To(Succeed())
		})

		It("should set the binding to reference the new resource snapshot", func() {
			binding := &placementv1alpha1.PlacementBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding)).To(Succeed())
			binding.Spec.ResourceSnapshotName = snapshotNameRev2
			Expect(hubClient.Update(ctx, binding)).To(Succeed())
		})

		It("should update the Work object to reflect the new ConfigMap", func() {
			namingBinding := &placementv1alpha1.PlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			By("waiting for the Work object to be relinked to the new resource snapshot")
			work := &placementv1alpha1.Work{}
			Eventually(func() (string, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{
					Namespace: memberClusterReservedNSName,
					Name:      wantWorkName,
				}, work); err != nil {
					return "", err
				}
				return work.Annotations[placementv1alpha1.WorkLinkedToPrimaryPlacementResourceSnapshotAnnotationKey], nil
			}, eventuallyDuration, eventuallyInterval).Should(Equal(snapshotNameRev2), "Work object should be relinked to the new resource snapshot")

			By("verifying the Work object carries the updated ConfigMap")
			Expect(work.Spec.Manifests).To(HaveLen(2), "Work object should still carry two manifests")
			gotCM := &corev1.ConfigMap{}
			Expect(json.Unmarshal(work.Spec.Manifests[1].Raw, gotCM)).To(Succeed(), "manifest 1 should decode into a ConfigMap")
			Expect(gotCM.Data).To(Equal(map[string]string{"key": "updated-value"}), "ConfigMap data should reflect the update")
		})

		It("should reflect a waiting-for-synchronization and waiting-for-availability-check status on the binding once more", func() {
			// The Work object's spec has changed (new ConfigMap manifest), so its generation has advanced
			// past the one recorded on its stale Applied/Available conditions from the previous revision;
			// the controller reports this transitional "waiting" status as soon as it relinks the Work object.
			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionUnknown,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonWaitingForAvailabilityCheck,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionFalse,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonWaitingForSynchronization,
				},
			}

			By("waiting for the binding status conditions to reflect the waiting state again")
			binding := &placementv1alpha1.PlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(SatisfyAll(
				HaveLen(2),
				ContainElement(HaveField("Reason", placementv1alpha1.PlacementBindingSynchronizedCondReasonWaitingForSynchronization)),
			), "binding should report the waiting-for-synchronization status again")

			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should reflect Applied=True and Available=True on the binding after the Work status is updated again", func() {
			namingBinding := &placementv1alpha1.PlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			By("fetching the Work object")
			work := &placementv1alpha1.Work{}
			Expect(hubClient.Get(ctx, types.NamespacedName{
				Namespace: memberClusterReservedNSName,
				Name:      wantWorkName,
			}, work)).To(Succeed())

			By("patching the Work status with Applied=True and Available=True for the new generation")
			updatedWork := work.DeepCopy()
			updatedWork.Status.Conditions = []metav1.Condition{
				{
					Type:               placementv1alpha1.WorkCondTypeApplied,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsApplied",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               placementv1alpha1.WorkCondTypeAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsAvailable",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedWork)).To(Succeed())

			By("waiting for the binding status to reflect Synchronized=True and AllResourcesAvailable=True once more")
			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonAllResourcesAvailable,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonAllResourcesSynchronized,
				},
			}
			binding := &placementv1alpha1.PlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(SatisfyAll(
				HaveLen(2),
				ContainElement(HaveField("Status", metav1.ConditionTrue)),
			), "binding should have two True status conditions")

			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})
	})

	Context("when a new cluster binding is created (hub cluster manifests)", Ordered, func() {
		bindingName := "test-cluster-binding-hub-manifests"
		snapshotName := "test-cluster-snapshot-hub-manifests-rev-1"

		var deploy *appsv1.Deployment
		var cm *corev1.ConfigMap

		var deployRawJSON, cmRawJSON runtime.RawExtension

		BeforeAll(func() {
			deploy = &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster-app",
					Namespace: workNSName,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "cluster-app"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "cluster-app"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "cluster-app",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
			}
			cm = &corev1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster-app-config",
					Namespace: workNSName,
				},
				Data: map[string]string{
					"key": "value",
				},
			}

			deployRawJSON = rawJSON(deploy)
			cmRawJSON = rawJSON(cm)
			snapshot := &placementv1alpha1.ClusterPlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name: snapshotName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         placementName,
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "0",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
				Spec: placementv1alpha1.PlacementResourceSnapshotSpec{
					Resources: []placementv1alpha1.SnapshottedResource{
						{
							Identifier: placementv1alpha1.ObjectReference{
								Name:       deploy.Name,
								APIGroup:   "apps",
								APIVersion: "v1",
								Kind:       "Deployment",
							},
							Manifest: deployRawJSON,
						},
						{
							Identifier: placementv1alpha1.ObjectReference{
								Name:       cm.Name,
								APIGroup:   "",
								APIVersion: "v1",
								Kind:       "ConfigMap",
							},
							Manifest: cmRawJSON,
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, snapshot)).To(Succeed())

			binding := &placementv1alpha1.ClusterPlacementBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: bindingName,
				},
				Spec: placementv1alpha1.PlacementBindingSpec{
					PlacementPolicyName:  placementName,
					ClusterName:          memberClusterName,
					ResourceSnapshotName: snapshotName,
				},
			}
			Expect(hubClient.Create(ctx, binding)).To(Succeed())
		})

		AfterAll(func() {
			Eventually(func() error {
				binding := &placementv1alpha1.ClusterPlacementBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: bindingName}, binding); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}

				if binding.DeletionTimestamp.IsZero() {
					if err := hubClient.Delete(ctx, binding); err != nil {
						return err
					}
				}
				return fmt.Errorf("cluster binding still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "cluster binding should be deleted by the controller")

			Eventually(func() error {
				snapshot := &placementv1alpha1.ClusterPlacementResourceSnapshot{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: snapshotName}, snapshot); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if err := hubClient.Delete(ctx, snapshot); err != nil {
					return err
				}
				return fmt.Errorf("cluster snapshot still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "cluster snapshot should be fully deleted")

			Eventually(func() (int, error) {
				workList := &placementv1alpha1.WorkList{}
				if err := hubClient.List(ctx, workList,
					client.InNamespace(memberClusterReservedNSName),
					client.MatchingLabels{
						placementv1alpha1.WorkOwnedByPlacementBindingLabelKey: bindingName,
						placementv1alpha1.WorkOwnerNamespaceLabelKey:          "",
					},
				); err != nil {
					return 0, err
				}
				return len(workList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all Work objects owned by the cluster binding should be cleaned up")
		})

		It("should create a Work object in the member cluster's hub namespace", func() {
			namingBinding := &placementv1alpha1.ClusterPlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			wantWork := &placementv1alpha1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wantWorkName,
					Namespace: memberClusterReservedNSName,
					Labels: map[string]string{
						placementv1alpha1.WorkOwnedByPlacementBindingLabelKey: workOwnerLabelValue(bindingName),
						placementv1alpha1.WorkOwnerNamespaceLabelKey:          "",
						placementv1alpha1.WorkOwnedByPlacementPolicyLabelKey:  workOwnerLabelValue(placementName),
					},
					Annotations: map[string]string{
						placementv1alpha1.WorkLinkedToPrimaryPlacementResourceSnapshotAnnotationKey: snapshotName,
						placementv1alpha1.WorkDerivedFromSourceAnnotationKey:                        "placement-resource-snapshot/0",
						placementv1alpha1.WorkOwnedByPlacementPolicyAnnotationKey:                   placementName,
						placementv1alpha1.WorkOwnedByPlacementBindingAnnotationKey:                  bindingName,
						placementv1alpha1.LinkedWorkCountAnnotationKey:                              "1",
					},
				},
				Spec: placementv1alpha1.WorkSpec{
					Manifests: []placementv1alpha1.Manifest{
						{RawExtension: deployRawJSON},
						{RawExtension: cmRawJSON},
					},
				},
			}

			work := &placementv1alpha1.Work{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{
					Namespace: memberClusterReservedNSName,
					Name:      wantWorkName,
				}, work)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Work object should be created in the member namespace")

			if diff := cmp.Diff(work, wantWork,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation"),
				cmpopts.IgnoreFields(placementv1alpha1.WorkSpec{}, "Manifests"),
			); diff != "" {
				Fail(fmt.Sprintf("Work object mismatch (-got, +want):\n%s", diff))
			}

			binding := &placementv1alpha1.ClusterPlacementBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: bindingName}, binding)).To(Succeed())
			Expect(binding.Finalizers).To(ContainElement(workGeneratorCleanupFinalizer))
		})

		It("should reflect a waiting-for-synchronization and waiting-for-availability-check status on the cluster binding", func() {
			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionUnknown,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonWaitingForAvailabilityCheck,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionFalse,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonWaitingForSynchronization,
				},
			}

			binding := &placementv1alpha1.ClusterPlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(HaveLen(2), "cluster binding should have two status conditions")

			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("cluster binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should reflect Applied=True and Available=True on the cluster binding after the Work status is updated", func() {
			namingBinding := &placementv1alpha1.ClusterPlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			work := &placementv1alpha1.Work{}
			Expect(hubClient.Get(ctx, types.NamespacedName{
				Namespace: memberClusterReservedNSName,
				Name:      wantWorkName,
			}, work)).To(Succeed())

			updatedWork := work.DeepCopy()
			updatedWork.Status.Conditions = []metav1.Condition{
				{
					Type:               placementv1alpha1.WorkCondTypeApplied,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsApplied",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               placementv1alpha1.WorkCondTypeAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsAvailable",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedWork)).To(Succeed())

			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonAllResourcesAvailable,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonAllResourcesSynchronized,
				},
			}
			binding := &placementv1alpha1.ClusterPlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(SatisfyAll(
				HaveLen(2),
				ContainElement(HaveField("Status", metav1.ConditionTrue)),
			), "cluster binding should have two True status conditions")

			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("cluster binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})
	})

	Context("when a new binding is created (ORAS manifests)", Ordered, func() {
		bindingName := "test-binding-oras"
		snapshotName := "test-snapshot-oras-rev-1"
		ociSecretName := "local-registry-access"
		orasManifestsName := "web-app"

		BeforeAll(func() {
			By("resolving the OCI artifact digest from the local registry")
			digest, err := localregistry.Resolve(localregistry.RawManifestsArtifactURL, localregistry.RawManifestsArtifactTag)
			Expect(err).ToNot(HaveOccurred())

			By("creating the image pull secret for the OCI registry")
			dockerConfigJSON := fmt.Sprintf(`{"auths":{%q:{"username":%q,"password":%q}}}`,
				localregistry.RegistryURL, localregistry.RegistryUsername, localregistry.RegistryPassword)
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ociSecretName,
					Namespace: workNSName,
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(dockerConfigJSON),
				},
			}
			Expect(hubClient.Create(ctx, secret)).To(Succeed())

			// Build the ORASManifests object that will be embedded into the snapshot. The
			// controller reads it back out of the snapshot to locate and extract the manifests
			// stored in the OCI artifact.
			orasManifests := &placementv1alpha1.ORASManifests{
				TypeMeta: metav1.TypeMeta{
					APIVersion: placementv1alpha1.GroupVersion.String(),
					Kind:       "ORASManifests",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      orasManifestsName,
					Namespace: workNSName,
				},
				Spec: placementv1alpha1.ORASManifestsSpec{
					OCIArtifact: &placementv1alpha1.OCIArtifact{
						URL: localregistry.RawManifestsArtifactURL,
						Ref: &placementv1alpha1.OCIArtifactReference{
							Tag: localregistry.RawManifestsArtifactTag,
						},
						AuthProvider: &placementv1alpha1.OCIArtifactAuthProvider{
							Type: placementv1alpha1.AuthProviderTypeGeneric,
							SecretRef: &placementv1alpha1.ObjectReference{
								APIGroup:   "",
								APIVersion: "v1",
								Kind:       "Secret",
								Namespace:  workNSName,
								Name:       ociSecretName,
							},
						},
					},
					Path: ".",
				},
			}

			// The digest of the OCI artifact to retrieve is carried in the snapshot's additional
			// info; the controller resolves it against the local registry to extract manifests.
			additionalInfo := placementv1alpha1.ORASManifestsAdditionalInfoForSnapshots{
				OCIArtifactDigest: digest,
			}
			additionalInfoBytes, err := json.Marshal(additionalInfo)
			Expect(err).ToNot(HaveOccurred())

			By("creating the PlacementResourceSnapshot that references the ORAS manifests")
			snapshot := &placementv1alpha1.PlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      snapshotName,
					Namespace: workNSName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         placementName,
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "0",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
				Spec: placementv1alpha1.PlacementResourceSnapshotSpec{
					Resources: []placementv1alpha1.SnapshottedResource{
						{
							Identifier: placementv1alpha1.ObjectReference{
								Name:       orasManifestsName,
								APIGroup:   placementv1alpha1.GroupVersion.Group,
								APIVersion: placementv1alpha1.GroupVersion.String(),
								Kind:       "ORASManifests",
							},
							Manifest: rawJSON(orasManifests),
							AdditionalInfo: map[string][]byte{
								placementv1alpha1.ResourceSnapshotAdditionalInfoKeyORASManifests: additionalInfoBytes,
							},
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, snapshot)).To(Succeed())

			By("creating the binding that references the snapshot")
			binding := &placementv1alpha1.PlacementBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bindingName,
					Namespace: workNSName,
				},
				Spec: placementv1alpha1.PlacementBindingSpec{
					PlacementPolicyName:  placementName,
					ClusterName:          memberClusterName,
					ResourceSnapshotName: snapshotName,
				},
			}
			Expect(hubClient.Create(ctx, binding)).To(Succeed())
		})

		AfterAll(func() {
			// Issue the delete (idempotent — ignore not-found if already gone), then wait for
			// the controller to drop its finalizer and fully remove the object.
			Eventually(func() error {
				binding := &placementv1alpha1.PlacementBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}

				if binding.DeletionTimestamp.IsZero() {
					if err := hubClient.Delete(ctx, binding); err != nil {
						return err
					}
				}
				return fmt.Errorf("binding still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "binding should be deleted by the controller")

			// Remove the snapshot and wait for it to be fully deleted.
			Eventually(func() error {
				snapshot := &placementv1alpha1.PlacementResourceSnapshot{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: snapshotName}, snapshot); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if err := hubClient.Delete(ctx, snapshot); err != nil {
					return err
				}
				return fmt.Errorf("snapshot still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "snapshot should be fully deleted")

			// Remove the image pull secret and wait for it to be fully deleted.
			Eventually(func() error {
				secret := &corev1.Secret{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: ociSecretName}, secret); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if err := hubClient.Delete(ctx, secret); err != nil {
					return err
				}
				return fmt.Errorf("secret still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "secret should be fully deleted")

			// The controller deletes owned Work objects as part of its cleanup before removing
			// the binding finalizer, so by the time the binding is gone the Work objects should
			// already be gone too. Wait to confirm, filtering to only those owned by this binding.
			Eventually(func() (int, error) {
				workList := &placementv1alpha1.WorkList{}
				if err := hubClient.List(ctx, workList,
					client.InNamespace(memberClusterReservedNSName),
					client.MatchingLabels{
						placementv1alpha1.WorkOwnedByPlacementBindingLabelKey: bindingName,
						placementv1alpha1.WorkOwnerNamespaceLabelKey:          workNSName,
					},
				); err != nil {
					return 0, err
				}
				return len(workList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all Work objects owned by the binding should be cleaned up")
		})

		It("should create a Work object with the manifests extracted from the OCI artifact", func() {
			namingBinding := &placementv1alpha1.PlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			wantWork := &placementv1alpha1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wantWorkName,
					Namespace: memberClusterReservedNSName,
					Labels: map[string]string{
						placementv1alpha1.WorkOwnedByPlacementBindingLabelKey: workOwnerLabelValue(bindingName),
						placementv1alpha1.WorkOwnerNamespaceLabelKey:          workNSName,
						placementv1alpha1.WorkOwnedByPlacementPolicyLabelKey:  workOwnerLabelValue(placementName),
					},
					Annotations: map[string]string{
						placementv1alpha1.WorkLinkedToPrimaryPlacementResourceSnapshotAnnotationKey: snapshotName,
						placementv1alpha1.WorkDerivedFromSourceAnnotationKey:                        "placement-resource-snapshot/0",
						placementv1alpha1.WorkOwnedByPlacementPolicyAnnotationKey:                   placementName,
						placementv1alpha1.WorkOwnedByPlacementBindingAnnotationKey:                  bindingName,
						placementv1alpha1.LinkedWorkCountAnnotationKey:                              "1",
					},
				},
			}

			By("waiting for the Work object to be created")
			work := &placementv1alpha1.Work{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{
					Namespace: memberClusterReservedNSName,
					Name:      wantWorkName,
				}, work)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Work object should be created in the member namespace")

			By("verifying the Work object's metadata matches the expected state")
			if diff := cmp.Diff(work, wantWork,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation"),
				cmpopts.IgnoreFields(placementv1alpha1.Work{}, "Spec", "Status"),
			); diff != "" {
				Fail(fmt.Sprintf("Work object mismatch (-got, +want):\n%s", diff))
			}

			By("verifying the manifests extracted from the OCI artifact are placed on the Work object")
			// The OCI artifact carries three Kubernetes manifests, extracted in path-based lexical
			// order: a ConfigMap, a Deployment, and a Namespace. Unmarshal each raw manifest into a
			// typed object and diff it against the expected state (the raw bytes are produced from
			// the source YAML, so they are compared after decoding rather than byte-for-byte).
			manifests := work.Spec.Manifests
			Expect(manifests).To(HaveLen(3), "Work object should carry the three extracted manifests")

			cm := &corev1.ConfigMap{}
			deploy := &appsv1.Deployment{}
			ns := &corev1.Namespace{}
			Expect(json.Unmarshal(manifests[0].Raw, cm)).To(Succeed(), "manifest 0 should decode into a ConfigMap")
			Expect(json.Unmarshal(manifests[1].Raw, deploy)).To(Succeed(), "manifest 1 should decode into a Deployment")
			Expect(json.Unmarshal(manifests[2].Raw, ns)).To(Succeed(), "manifest 2 should decode into a Namespace")

			wantCM := &corev1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "web",
					Namespace: "work",
				},
				Data: map[string]string{
					"foo": "bar",
				},
			}
			wantDeploy := &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nginx",
					Namespace: "work",
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
									Image: "nginx:stable",
									Ports: []corev1.ContainerPort{
										{ContainerPort: 80},
									},
								},
							},
						},
					},
				},
			}
			wantNS := &corev1.Namespace{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Namespace",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "work",
				},
			}
			if diff := cmp.Diff(cm, wantCM); diff != "" {
				Fail(fmt.Sprintf("ConfigMap manifest mismatch (-got, +want):\n%s", diff))
			}
			if diff := cmp.Diff(deploy, wantDeploy); diff != "" {
				Fail(fmt.Sprintf("Deployment manifest mismatch (-got, +want):\n%s", diff))
			}
			if diff := cmp.Diff(ns, wantNS); diff != "" {
				Fail(fmt.Sprintf("Namespace manifest mismatch (-got, +want):\n%s", diff))
			}

			By("verifying that the cleanup finalizer is added to the binding")
			binding := &placementv1alpha1.PlacementBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding)).To(Succeed())
			Expect(binding.Finalizers).To(ContainElement(workGeneratorCleanupFinalizer))
		})

		It("should update the binding status based on the Work object's status", func() {
			// In the test environment there is no work applier, so the Work object will have an
			// empty status. The controller should reflect this back on the binding status.
			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionUnknown,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonWaitingForAvailabilityCheck,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionFalse,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonWaitingForSynchronization,
				},
			}

			By("waiting for the binding status conditions to be populated")
			binding := &placementv1alpha1.PlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(HaveLen(2), "binding should have two status conditions")

			By("verifying the binding status conditions match the expected state")
			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should reflect Applied=True and Available=True on the binding after the Work status is updated", func() {
			namingBinding := &placementv1alpha1.PlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			By("fetching the Work object")
			work := &placementv1alpha1.Work{}
			Expect(hubClient.Get(ctx, types.NamespacedName{
				Namespace: memberClusterReservedNSName,
				Name:      wantWorkName,
			}, work)).To(Succeed())

			By("patching the Work status with Applied=True and Available=True")
			updatedWork := work.DeepCopy()
			updatedWork.Status.Conditions = []metav1.Condition{
				{
					Type:               placementv1alpha1.WorkCondTypeApplied,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsApplied",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               placementv1alpha1.WorkCondTypeAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsAvailable",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedWork)).To(Succeed())

			By("waiting for the binding status to reflect Synchronized=True and AllResourcesAvailable=True")
			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonAllResourcesAvailable,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonAllResourcesSynchronized,
				},
			}
			binding := &placementv1alpha1.PlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(SatisfyAll(
				HaveLen(2),
				ContainElement(HaveField("Status", metav1.ConditionTrue)),
			), "binding should have two True status conditions")

			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})
	})

	Context("when a new cluster binding is created (ORAS manifests)", Ordered, func() {
		bindingName := "test-cluster-binding-oras"
		snapshotName := "test-cluster-snapshot-oras-rev-1"
		ociSecretName := "local-registry-access-cluster"
		orasManifestsName := "cluster-web-app"

		BeforeAll(func() {
			digest, err := localregistry.Resolve(localregistry.RawManifestsArtifactURL, localregistry.RawManifestsArtifactTag)
			Expect(err).ToNot(HaveOccurred())

			dockerConfigJSON := fmt.Sprintf(`{"auths":{%q:{"username":%q,"password":%q}}}`,
				localregistry.RegistryURL, localregistry.RegistryUsername, localregistry.RegistryPassword)
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ociSecretName,
					Namespace: workNSName,
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(dockerConfigJSON),
				},
			}
			Expect(hubClient.Create(ctx, secret)).To(Succeed())

			orasManifests := &placementv1alpha1.ORASManifests{
				TypeMeta: metav1.TypeMeta{
					APIVersion: placementv1alpha1.GroupVersion.String(),
					Kind:       "ORASManifests",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      orasManifestsName,
					Namespace: workNSName,
				},
				Spec: placementv1alpha1.ORASManifestsSpec{
					OCIArtifact: &placementv1alpha1.OCIArtifact{
						URL: localregistry.RawManifestsArtifactURL,
						Ref: &placementv1alpha1.OCIArtifactReference{
							Tag: localregistry.RawManifestsArtifactTag,
						},
						AuthProvider: &placementv1alpha1.OCIArtifactAuthProvider{
							Type: placementv1alpha1.AuthProviderTypeGeneric,
							SecretRef: &placementv1alpha1.ObjectReference{
								APIGroup:   "",
								APIVersion: "v1",
								Kind:       "Secret",
								Namespace:  workNSName,
								Name:       ociSecretName,
							},
						},
					},
					Path: ".",
				},
			}

			additionalInfo := placementv1alpha1.ORASManifestsAdditionalInfoForSnapshots{
				OCIArtifactDigest: digest,
			}
			additionalInfoBytes, err := json.Marshal(additionalInfo)
			Expect(err).ToNot(HaveOccurred())

			snapshot := &placementv1alpha1.ClusterPlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name: snapshotName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         placementName,
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "0",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
				Spec: placementv1alpha1.PlacementResourceSnapshotSpec{
					Resources: []placementv1alpha1.SnapshottedResource{
						{
							Identifier: placementv1alpha1.ObjectReference{
								Name:       orasManifestsName,
								APIGroup:   placementv1alpha1.GroupVersion.Group,
								APIVersion: placementv1alpha1.GroupVersion.String(),
								Kind:       "ORASManifests",
							},
							Manifest: rawJSON(orasManifests),
							AdditionalInfo: map[string][]byte{
								placementv1alpha1.ResourceSnapshotAdditionalInfoKeyORASManifests: additionalInfoBytes,
							},
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, snapshot)).To(Succeed())

			binding := &placementv1alpha1.ClusterPlacementBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: bindingName,
				},
				Spec: placementv1alpha1.PlacementBindingSpec{
					PlacementPolicyName:  placementName,
					ClusterName:          memberClusterName,
					ResourceSnapshotName: snapshotName,
				},
			}
			Expect(hubClient.Create(ctx, binding)).To(Succeed())
		})

		AfterAll(func() {
			Eventually(func() error {
				binding := &placementv1alpha1.ClusterPlacementBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: bindingName}, binding); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}

				if binding.DeletionTimestamp.IsZero() {
					if err := hubClient.Delete(ctx, binding); err != nil {
						return err
					}
				}
				return fmt.Errorf("cluster binding still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "cluster binding should be deleted by the controller")

			Eventually(func() error {
				snapshot := &placementv1alpha1.ClusterPlacementResourceSnapshot{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: snapshotName}, snapshot); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if err := hubClient.Delete(ctx, snapshot); err != nil {
					return err
				}
				return fmt.Errorf("cluster snapshot still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "cluster snapshot should be fully deleted")

			Eventually(func() error {
				secret := &corev1.Secret{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: ociSecretName}, secret); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if err := hubClient.Delete(ctx, secret); err != nil {
					return err
				}
				return fmt.Errorf("secret still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "secret should be fully deleted")

			Eventually(func() (int, error) {
				workList := &placementv1alpha1.WorkList{}
				if err := hubClient.List(ctx, workList,
					client.InNamespace(memberClusterReservedNSName),
					client.MatchingLabels{
						placementv1alpha1.WorkOwnedByPlacementBindingLabelKey: bindingName,
						placementv1alpha1.WorkOwnerNamespaceLabelKey:          "",
					},
				); err != nil {
					return 0, err
				}
				return len(workList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all Work objects owned by the cluster binding should be cleaned up")
		})

		It("should create a Work object with the manifests extracted from the OCI artifact", func() {
			namingBinding := &placementv1alpha1.ClusterPlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			wantWork := &placementv1alpha1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wantWorkName,
					Namespace: memberClusterReservedNSName,
					Labels: map[string]string{
						placementv1alpha1.WorkOwnedByPlacementBindingLabelKey: workOwnerLabelValue(bindingName),
						placementv1alpha1.WorkOwnerNamespaceLabelKey:          "",
						placementv1alpha1.WorkOwnedByPlacementPolicyLabelKey:  workOwnerLabelValue(placementName),
					},
					Annotations: map[string]string{
						placementv1alpha1.WorkLinkedToPrimaryPlacementResourceSnapshotAnnotationKey: snapshotName,
						placementv1alpha1.WorkDerivedFromSourceAnnotationKey:                        "placement-resource-snapshot/0",
						placementv1alpha1.WorkOwnedByPlacementPolicyAnnotationKey:                   placementName,
						placementv1alpha1.WorkOwnedByPlacementBindingAnnotationKey:                  bindingName,
						placementv1alpha1.LinkedWorkCountAnnotationKey:                              "1",
					},
				},
			}

			work := &placementv1alpha1.Work{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{
					Namespace: memberClusterReservedNSName,
					Name:      wantWorkName,
				}, work)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Work object should be created in the member namespace")

			if diff := cmp.Diff(work, wantWork,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation"),
				cmpopts.IgnoreFields(placementv1alpha1.Work{}, "Spec", "Status"),
			); diff != "" {
				Fail(fmt.Sprintf("Work object mismatch (-got, +want):\n%s", diff))
			}

			manifests := work.Spec.Manifests
			Expect(manifests).To(HaveLen(3), "Work object should carry the three extracted manifests")

			cm := &corev1.ConfigMap{}
			deploy := &appsv1.Deployment{}
			ns := &corev1.Namespace{}
			Expect(json.Unmarshal(manifests[0].Raw, cm)).To(Succeed(), "manifest 0 should decode into a ConfigMap")
			Expect(json.Unmarshal(manifests[1].Raw, deploy)).To(Succeed(), "manifest 1 should decode into a Deployment")
			Expect(json.Unmarshal(manifests[2].Raw, ns)).To(Succeed(), "manifest 2 should decode into a Namespace")

			wantCM := &corev1.ConfigMap{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "ConfigMap",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "web",
					Namespace: "work",
				},
				Data: map[string]string{
					"foo": "bar",
				},
			}
			wantDeploy := &appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nginx",
					Namespace: "work",
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
									Image: "nginx:stable",
									Ports: []corev1.ContainerPort{
										{ContainerPort: 80},
									},
								},
							},
						},
					},
				},
			}
			wantNS := &corev1.Namespace{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "v1",
					Kind:       "Namespace",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "work",
				},
			}
			if diff := cmp.Diff(cm, wantCM); diff != "" {
				Fail(fmt.Sprintf("ConfigMap manifest mismatch (-got, +want):\n%s", diff))
			}
			if diff := cmp.Diff(deploy, wantDeploy); diff != "" {
				Fail(fmt.Sprintf("Deployment manifest mismatch (-got, +want):\n%s", diff))
			}
			if diff := cmp.Diff(ns, wantNS); diff != "" {
				Fail(fmt.Sprintf("Namespace manifest mismatch (-got, +want):\n%s", diff))
			}

			binding := &placementv1alpha1.ClusterPlacementBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: bindingName}, binding)).To(Succeed())
			Expect(binding.Finalizers).To(ContainElement(workGeneratorCleanupFinalizer))
		})

		It("should update the cluster binding status based on the Work object's status", func() {
			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionUnknown,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonWaitingForAvailabilityCheck,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionFalse,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonWaitingForSynchronization,
				},
			}

			binding := &placementv1alpha1.ClusterPlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(HaveLen(2), "cluster binding should have two status conditions")

			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("cluster binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should reflect Applied=True and Available=True on the cluster binding after the Work status is updated", func() {
			namingBinding := &placementv1alpha1.ClusterPlacementBinding{
				ObjectMeta: metav1.ObjectMeta{Name: bindingName},
				Spec:       placementv1alpha1.PlacementBindingSpec{PlacementPolicyName: placementName},
			}
			wantWorkName := uniqueNameForWorkDerivedFromPlacementResourceSnapshot(
				namingBinding, true, &placementResourceSnapshotDerivedFromSourceFormatter{snapshotSubIdx: "0"})

			work := &placementv1alpha1.Work{}
			Expect(hubClient.Get(ctx, types.NamespacedName{
				Namespace: memberClusterReservedNSName,
				Name:      wantWorkName,
			}, work)).To(Succeed())

			updatedWork := work.DeepCopy()
			updatedWork.Status.Conditions = []metav1.Condition{
				{
					Type:               placementv1alpha1.WorkCondTypeApplied,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsApplied",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               placementv1alpha1.WorkCondTypeAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsAvailable",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedWork)).To(Succeed())

			wantConditions := []metav1.Condition{
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeAvailable,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingAvailableCondReasonAllResourcesAvailable,
				},
				{
					Type:   placementv1alpha1.PlacementBindingCondTypeSynchronized,
					Status: metav1.ConditionTrue,
					Reason: placementv1alpha1.PlacementBindingSynchronizedCondReasonAllResourcesSynchronized,
				},
			}
			binding := &placementv1alpha1.ClusterPlacementBinding{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: bindingName}, binding); err != nil {
					return nil, err
				}
				return binding.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(SatisfyAll(
				HaveLen(2),
				ContainElement(HaveField("Status", metav1.ConditionTrue)),
			), "cluster binding should have two True status conditions")

			if diff := cmp.Diff(binding.Status.Conditions, wantConditions,
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("cluster binding status conditions mismatch (-got, +want):\n%s", diff))
			}
		})
	})
})
