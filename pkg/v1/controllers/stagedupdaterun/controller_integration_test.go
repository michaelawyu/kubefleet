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

package stagedupdate

import (
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	rolloutv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/rollout/v1alpha1"
)

// Eventually polling interval and timeout.
const (
	eventuallyInterval = 500 * time.Millisecond
	eventuallyDuration = 30 * time.Second
)

const (
	// The names of the fixtures used across this test.
	placementPolicyName      = "my-placement"
	stagedUpdateStrategyName = "my-staged-update-strategy"
	stagedUpdateRunName      = "my-staged-update-run"
	configMapName            = "app-config"
	stageName                = "stage1"

	// resourceSnapshotName is the name of the resource snapshot the staged update run is set up to roll
	// out; its content does not matter for this test.
	resourceSnapshotName = "my-placement-resource-snapshot-0"

	// legacyResourceSnapshotName is the placeholder resource snapshot name each placement binding is
	// created with; the staged update run is expected to update it to resourceSnapshotName.
	legacyResourceSnapshotName = "my-placement-resource-snapshot-legacy"
)

// clusterNames are the 3 member clusters the placement bindings target; a single, unconstrained stage
// (empty label selector) with a max concurrency of 3 is expected to roll out to all of them at once.
var clusterNames = []string{"bravelion", "jumpingcat", "sleepingwolf"}

func placementBindingName(clusterName string) string {
	return fmt.Sprintf("%s-%s", placementPolicyName, clusterName)
}

var _ = Describe("staged update run rollout", func() {
	Context("rolling out to 3 clusters concurrently in a single stage", Ordered, func() {
		BeforeAll(func() {
			By("creating a ConfigMap to be selected by the placement policy")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: workNSName,
				},
				Data: map[string]string{"key": "value"},
			}
			Expect(hubClient.Create(ctx, cm)).To(Succeed())

			By("creating 3 member clusters")
			for _, name := range clusterNames {
				mc := &clusterv1beta1.MemberCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: name,
					},
					Spec: clusterv1beta1.MemberClusterSpec{
						Identity: rbacv1.Subject{
							Kind: rbacv1.ServiceAccountKind,
							Name: "hub-access",
						},
					},
				}
				Expect(hubClient.Create(ctx, mc)).To(Succeed())
			}

			By("creating the placement policy")
			placementPolicy := &placementv1alpha1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      placementPolicyName,
					Namespace: workNSName,
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Name:       configMapName,
							APIGroup:   "",
							APIVersion: "v1",
							Kind:       "ConfigMap",
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, placementPolicy)).To(Succeed())

			By("creating the placement resource snapshot to be rolled out; its content does not matter")
			snapshot := &placementv1alpha1.PlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceSnapshotName,
					Namespace: workNSName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         placementPolicyName,
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "0",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
			}
			Expect(hubClient.Create(ctx, snapshot)).To(Succeed())

			By("creating 3 placement bindings, one per member cluster")
			for _, name := range clusterNames {
				binding := &placementv1alpha1.PlacementBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      placementBindingName(name),
						Namespace: workNSName,
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: placementPolicyName,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName: placementPolicyName,
						ClusterName:         name,
						// A placeholder value; the staged update run is expected to update this to
						// resourceSnapshotName.
						ResourceSnapshotName: legacyResourceSnapshotName,
					},
				}
				Expect(hubClient.Create(ctx, binding)).To(Succeed())
			}

			By("creating the staged update strategy with a single, unconstrained stage")
			strategy := &rolloutv1alpha1.StagedUpdateStrategy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      stagedUpdateStrategyName,
					Namespace: workNSName,
				},
				Spec: rolloutv1alpha1.StagedUpdateStrategySpec{
					Stages: []rolloutv1alpha1.Stage{
						{
							Name: stageName,
							// An empty (non-nil) label selector matches all clusters referenced by the
							// placement bindings.
							LabelSelector:  &metav1.LabelSelector{},
							MaxConcurrency: ptr.To(intstr.FromInt32(3)),
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, strategy)).To(Succeed())

			By("creating the staged update run, targeting the placement policy above")
			run := &rolloutv1alpha1.StagedUpdateRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      stagedUpdateRunName,
					Namespace: workNSName,
				},
				Spec: rolloutv1alpha1.StagedUpdateRunSpec{
					PlacementPolicyName:      placementPolicyName,
					ResourceSnapshotName:     resourceSnapshotName,
					StagedUpdateStrategyName: stagedUpdateStrategyName,
				},
			}
			Expect(hubClient.Create(ctx, run)).To(Succeed())
		})

		It("should update all 3 placement bindings to roll out the new resource snapshot", func() {
			for _, name := range clusterNames {
				bindingName := placementBindingName(name)
				Eventually(func() (string, error) {
					binding := &placementv1alpha1.PlacementBinding{}
					if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding); err != nil {
						return "", err
					}
					return binding.Spec.ResourceSnapshotName, nil
				}, eventuallyDuration, eventuallyInterval).Should(Equal(resourceSnapshotName),
					fmt.Sprintf("placement binding %s should be updated to roll out the new resource snapshot", bindingName))
			}
		})

		It("should mark all 3 placement bindings as synchronized and available", func() {
			for _, name := range clusterNames {
				bindingName := placementBindingName(name)

				By("patching binding " + bindingName)
				binding := &placementv1alpha1.PlacementBinding{}
				Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: bindingName}, binding)).To(Succeed())

				updatedBinding := binding.DeepCopy()
				updatedBinding.Status.Conditions = []metav1.Condition{
					{
						Type:               placementv1alpha1.PlacementBindingCondTypeSynchronized,
						Status:             metav1.ConditionTrue,
						Reason:             placementv1alpha1.PlacementBindingSynchronizedCondReasonAllResourcesSynchronized,
						ObservedGeneration: binding.Generation,
						LastTransitionTime: metav1.Now(),
					},
					{
						Type:               placementv1alpha1.PlacementBindingCondTypeAvailable,
						Status:             metav1.ConditionTrue,
						Reason:             placementv1alpha1.PlacementBindingAvailableCondReasonAllResourcesAvailable,
						ObservedGeneration: binding.Generation,
						LastTransitionTime: metav1.Now(),
					},
				}
				Expect(hubClient.Status().Update(ctx, updatedBinding)).To(Succeed())
			}
		})

		It("should mark the staged update run as completed successfully", func() {
			wantCompletedCond := &metav1.Condition{
				Type:   rolloutv1alpha1.StagedUpdateRunCondTypeCompleted,
				Status: metav1.ConditionTrue,
				Reason: rolloutv1alpha1.StagedUpdateRunCompletedCondReasonSucceeded,
			}

			By("waiting for the staged update run to report completion")
			run := &rolloutv1alpha1.StagedUpdateRun{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: stagedUpdateRunName}, run); err != nil {
					return err.Error()
				}
				completedCond := meta.FindStatusCondition(run.Status.Conditions, rolloutv1alpha1.StagedUpdateRunCondTypeCompleted)
				if completedCond == nil {
					return "the Completed condition has not been reported yet"
				}
				return cmp.Diff(completedCond, wantCompletedCond,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"))
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"the staged update run should report Completed=True/Succeeded")

			By("verifying the stage completed successfully with the configured concurrency")
			Expect(run.Status.Stages).To(HaveLen(1), "the staged update run should have a single stage")
			stage := run.Status.Stages[0]
			Expect(stage.StageName).To(Equal(stageName))
			Expect(stage.MaxConcurrency).To(HaveValue(Equal(int32(3))), "the stage should have resolved a max concurrency of 3")

			stageCompletedCond := meta.FindStatusCondition(stage.Conditions, rolloutv1alpha1.StagedUpdateRunPerStageCondTypeCompleted)
			Expect(stageCompletedCond).NotTo(BeNil(), "the stage should have a Completed condition")
			Expect(stageCompletedCond.Status).To(Equal(metav1.ConditionTrue))
			Expect(stageCompletedCond.Reason).To(Equal(rolloutv1alpha1.StagedUpdateRunPerStageCompletedCondReasonSucceeded))

			By("verifying all 3 clusters in the stage completed successfully")
			Expect(stage.Clusters).To(HaveLen(3), "the stage should feature all 3 clusters")
			for _, clusterStatus := range stage.Clusters {
				clusterCompletedCond := meta.FindStatusCondition(clusterStatus.Conditions, rolloutv1alpha1.StagedUpdateRunPerClusterCondTypeCompleted)
				Expect(clusterCompletedCond).NotTo(BeNil(), "cluster %s should have a Completed condition", clusterStatus.ClusterName)
				Expect(clusterCompletedCond.Status).To(Equal(metav1.ConditionTrue))
				Expect(clusterCompletedCond.Reason).To(Equal(rolloutv1alpha1.StagedUpdateRunPerClusterCompletedCondReasonSucceeded))
			}
		})

		AfterAll(func() {
			By("deleting the staged update run and waiting for it to be fully removed")
			run := &rolloutv1alpha1.StagedUpdateRun{ObjectMeta: metav1.ObjectMeta{Name: stagedUpdateRunName, Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, run))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: stagedUpdateRunName}, run)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("staged update run still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "the staged update run should be fully removed")

			By("deleting the staged update strategy")
			strategy := &rolloutv1alpha1.StagedUpdateStrategy{ObjectMeta: metav1.ObjectMeta{Name: stagedUpdateStrategyName, Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, strategy))).To(Succeed())

			By("deleting the 3 placement bindings")
			for _, name := range clusterNames {
				binding := &placementv1alpha1.PlacementBinding{ObjectMeta: metav1.ObjectMeta{Name: placementBindingName(name), Namespace: workNSName}}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, binding))).To(Succeed())
			}

			By("deleting the placement resource snapshot")
			snapshot := &placementv1alpha1.PlacementResourceSnapshot{ObjectMeta: metav1.ObjectMeta{Name: resourceSnapshotName, Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, snapshot))).To(Succeed())

			By("deleting the placement policy")
			placementPolicy := &placementv1alpha1.PlacementPolicy{ObjectMeta: metav1.ObjectMeta{Name: placementPolicyName, Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, placementPolicy))).To(Succeed())

			By("deleting the ConfigMap")
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, cm))).To(Succeed())

			By("deleting the 3 member clusters")
			for _, name := range clusterNames {
				mc := &clusterv1beta1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Name: name}}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, mc))).To(Succeed())
			}
		})
	})
})
