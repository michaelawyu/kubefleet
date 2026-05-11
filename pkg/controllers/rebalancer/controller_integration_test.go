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
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
)

const (
	eventuallyDuration   = time.Second * 10
	eventuallyInterval   = time.Second * 1
	consistentlyDuration = time.Second * 5
	consistentlyInterval = time.Second * 1
)

const (
	workNSNameTmpl    = "work-%d-%s"
	placementNameTmpl = "placement-%d"
	// formatted as [PLACEMENT_NAME]-[CLUSTER-NAME], e.g., placement-1-cluster-1.
	bindingNameTmpl        = "%s-%s"
	rebalancingReqNameTmpl = "rebalancing-%d"

	resourceSnapshotName         = "resource-snapshot-1"
	schedulingPolicySnapshotName = "scheduling-policy-snapshot-1"

	appNamespace  = "app"
	configMapName = "app"

	cluster1Name = "cluster-1"
	cluster2Name = "cluster-2"
)

var (
	ignoreFieldConditionLTTMsg = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message")
	ignoreObjRefFields         = cmpopts.IgnoreFields(corev1.ObjectReference{}, "Kind", "APIVersion", "ResourceVersion", "FieldPath")
)

type bindingPairState struct {
	fromWithdrawn      bool
	fromRolloutStarted bool
	toProvisional      bool
	toWithdrawn        bool
	toRolloutStarted   bool
}

func snapshotTargetedBindingStateAndMigrationConds(
	rebalancingReq *placementv1beta1.ClusterRebalancingRequest) ([]bindingPairState, []metav1.Condition, []metav1.Condition, error) {
	states := make([]bindingPairState, 0, len(rebalancingReq.Status.Migrations))
	completedConds := make([]metav1.Condition, 0, len(rebalancingReq.Status.Migrations))
	rolledBackConds := make([]metav1.Condition, 0, len(rebalancingReq.Status.Migrations))

	for idx := range rebalancingReq.Status.Migrations {
		migration := &rebalancingReq.Status.Migrations[idx]

		state := bindingPairState{}

		// Check the from binding state.
		if migration.FromClusterBindingReference != nil {
			var fromBinding placementv1beta1.BindingObj
			if migration.FromClusterBindingReference.Namespace == "" {
				fromBinding = &placementv1beta1.ClusterResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: migration.FromClusterBindingReference.Name}, fromBinding); err != nil {
					return nil, nil, nil, fmt.Errorf("failed to get from ClusterResourceBinding %s: %w", migration.FromClusterBindingReference.Name, err)
				}
			} else {
				fromBinding = &placementv1beta1.ResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: migration.FromClusterBindingReference.Namespace, Name: migration.FromClusterBindingReference.Name}, fromBinding); err != nil {
					return nil, nil, nil, fmt.Errorf("failed to get from ResourceBinding %s/%s: %w", migration.FromClusterBindingReference.Namespace, migration.FromClusterBindingReference.Name, err)
				}
			}
			state.fromWithdrawn = fromBinding.GetAnnotations()[placementv1beta1.WithdrawnBindingAnnotationKey] == "true"
			rolloutStartedCond := meta.FindStatusCondition(fromBinding.GetBindingStatus().Conditions, string(placementv1beta1.ResourceBindingRolloutStarted))
			state.fromRolloutStarted = condition.IsConditionStatusTrue(rolloutStartedCond, fromBinding.GetGeneration())
		}

		// Check the to binding state.
		if migration.ToClusterBindingReference != nil {
			var toBinding placementv1beta1.BindingObj
			if migration.ToClusterBindingReference.Namespace == "" {
				toBinding = &placementv1beta1.ClusterResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: migration.ToClusterBindingReference.Name}, toBinding); err != nil {
					return nil, nil, nil, fmt.Errorf("failed to get to ClusterResourceBinding %s: %w", migration.ToClusterBindingReference.Name, err)
				}
			} else {
				toBinding = &placementv1beta1.ResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: migration.ToClusterBindingReference.Namespace, Name: migration.ToClusterBindingReference.Name}, toBinding); err != nil {
					return nil, nil, nil, fmt.Errorf("failed to get to ResourceBinding %s/%s: %w", migration.ToClusterBindingReference.Namespace, migration.ToClusterBindingReference.Name, err)
				}
			}
			annotations := toBinding.GetAnnotations()
			state.toProvisional = annotations[placementv1beta1.ProvisionalBindingAnnotationKey] == "true"
			if state.toProvisional {
				state.toWithdrawn = annotations[placementv1beta1.WithdrawnBindingAnnotationKey] == "true"
				rolloutStartedCond := meta.FindStatusCondition(toBinding.GetBindingStatus().Conditions, string(placementv1beta1.ResourceBindingRolloutStarted))
				state.toRolloutStarted = condition.IsConditionStatusTrue(rolloutStartedCond, toBinding.GetGeneration())
			}
		}

		states = append(states, state)

		// Collect the per-migration completed condition.
		completedCond := meta.FindStatusCondition(migration.Conditions, placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted)
		if completedCond != nil {
			completedConds = append(completedConds, *completedCond)
		} else {
			completedConds = append(completedConds, metav1.Condition{})
		}

		// Collect the per-migration rolled back condition.
		rolledBackCond := meta.FindStatusCondition(migration.Conditions, placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack)
		if rolledBackCond != nil {
			rolledBackConds = append(rolledBackConds, *rolledBackCond)
		} else {
			rolledBackConds = append(rolledBackConds, metav1.Condition{})
		}
	}

	return states, completedConds, rolledBackConds, nil
}

var _ = Describe("rebalancing happy path", func() {
	Context("rebalancing placements (completed, committed)", Ordered, func() {
		crpName := fmt.Sprintf(placementNameTmpl, GinkgoParallelProcess())
		rpName := fmt.Sprintf(placementNameTmpl, GinkgoParallelProcess())
		workNSName := fmt.Sprintf(workNSNameTmpl, GinkgoParallelProcess(), utils.RandStr())
		rebalancingReqName := fmt.Sprintf(rebalancingReqNameTmpl, GinkgoParallelProcess())

		var crp *placementv1beta1.ClusterResourcePlacement
		var rp *placementv1beta1.ResourcePlacement
		var crb *placementv1beta1.ClusterResourceBinding
		var rb *placementv1beta1.ResourceBinding
		var provisionalCRB *placementv1beta1.ClusterResourceBinding
		var provisionalRB *placementv1beta1.ResourceBinding
		var rebalancingReq *placementv1beta1.ClusterRebalancingRequest

		BeforeAll(func() {
			// Create the CRP.
			crp = &placementv1beta1.ClusterResourcePlacement{
				ObjectMeta: metav1.ObjectMeta{
					Name: crpName,
				},
				Spec: placementv1beta1.PlacementSpec{
					ResourceSelectors: []placementv1beta1.ResourceSelectorTerm{
						{
							Group:   "",
							Version: "v1",
							Kind:    "Namespace",
							Name:    appNamespace,
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, crp)).To(Succeed())

			// Create a binding for the CRP.
			crb = &placementv1beta1.ClusterResourceBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:       fmt.Sprintf(bindingNameTmpl, crpName, cluster1Name),
					Finalizers: []string{placementv1beta1.WorkFinalizer},
					Labels: map[string]string{
						placementv1beta1.PlacementTrackingLabel: crpName,
					},
				},
				Spec: placementv1beta1.ResourceBindingSpec{
					State:                placementv1beta1.BindingStateBound,
					TargetCluster:        cluster1Name,
					ResourceSnapshotName: resourceSnapshotName,
					ClusterDecision: placementv1beta1.ClusterDecision{
						ClusterName: cluster1Name,
						Selected:    true,
						Reason:      "manually created",
					},
				},
			}
			Expect(hubClient.Create(ctx, crb)).To(Succeed())

			// Create a namespace for the RP.
			workNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: workNSName,
				},
			}
			Expect(hubClient.Create(ctx, workNamespace)).To(Succeed())

			// Create the RP.
			rp = &placementv1beta1.ResourcePlacement{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rpName,
					Namespace: workNSName,
				},
				Spec: placementv1beta1.PlacementSpec{
					ResourceSelectors: []placementv1beta1.ResourceSelectorTerm{
						{
							Group:   "",
							Version: "v1",
							Kind:    "ConfigMap",
							Name:    configMapName,
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, rp)).To(Succeed())

			// Create a binding for the RP.
			rb = &placementv1beta1.ResourceBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:       fmt.Sprintf(bindingNameTmpl, rpName, cluster1Name),
					Namespace:  workNSName,
					Finalizers: []string{placementv1beta1.WorkFinalizer},
					Labels: map[string]string{
						placementv1beta1.PlacementTrackingLabel: rpName,
					},
				},
				Spec: placementv1beta1.ResourceBindingSpec{
					State:                        placementv1beta1.BindingStateBound,
					TargetCluster:                cluster1Name,
					ResourceSnapshotName:         resourceSnapshotName,
					SchedulingPolicySnapshotName: schedulingPolicySnapshotName,
					ClusterDecision: placementv1beta1.ClusterDecision{
						ClusterName: cluster1Name,
						Selected:    true,
						Reason:      "manually created",
					},
				},
			}
			Expect(hubClient.Create(ctx, rb)).To(Succeed())

			// Create the ClusterRebalancingRequest.
			rebalancingReq = &placementv1beta1.ClusterRebalancingRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: rebalancingReqName,
				},
				Spec: placementv1beta1.ClusterRebalancingRequestSpec{
					From: cluster1Name,
					To:   cluster2Name,
					FailurePolicy: &placementv1beta1.ClusterRebalancingRequestFailurePolicy{
						MaxFailureCount: ptr.To(int32(1)),
						MaximumWaitDurationPerMigrationAttemptSeconds: ptr.To(int32(300)),
					},
				},
			}
			Expect(hubClient.Create(ctx, rebalancingReq)).To(Succeed())
		})

		It("should initialize the ClusterRebalancingRequest", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				initCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized)
				wantInitCond := metav1.Condition{
					Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized,
					Status:             metav1.ConditionTrue,
					Reason:             "IdentifiedAndClaimedAllPlacements",
					ObservedGeneration: rebalancingReq.Generation,
				}

				if diff := cmp.Diff(initCond, &wantInitCond, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("unexpected initialization condition (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should populate the ClusterRebalancingRequest status with migration entries", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				wantMigrations := []placementv1beta1.ClusterRebalancingRequestPerMigrationStatus{
					{
						PlacementReference: corev1.ObjectReference{
							Kind: "ClusterResourcePlacement",
							Name: crpName,
							UID:  crp.UID,
						},
					},
					{
						PlacementReference: corev1.ObjectReference{
							Kind:      "ResourcePlacement",
							Name:      rpName,
							Namespace: workNSName,
							UID:       rp.UID,
						},
					},
				}

				if diff := cmp.Diff(
					rebalancingReq.Status.Migrations,
					wantMigrations,
				); diff != "" {
					return fmt.Errorf("ClusterRebalancingRequest migrations mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should claim rollout management on both placements", func() {
			wantRolloutManagedBy := []placementv1beta1.RolloutManagerReference{
				{
					ObjectReference: corev1.ObjectReference{
						Kind: "ClusterRebalancingRequest",
						Name: rebalancingReq.Name,
						UID:  rebalancingReq.UID,
					},
					Mode: placementv1beta1.RolloutManagerModeExclusive,
				},
			}

			Eventually(func() error {
				gotCRP := &placementv1beta1.ClusterResourcePlacement{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crpName}, gotCRP); err != nil {
					return fmt.Errorf("failed to get CRP: %w", err)
				}
				if diff := cmp.Diff(gotCRP.Status.RolloutManagedBy, wantRolloutManagedBy); diff != "" {
					return fmt.Errorf("CRP RolloutManagedBy mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			Eventually(func() error {
				gotRP := &placementv1beta1.ResourcePlacement{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: rpName}, gotRP); err != nil {
					return fmt.Errorf("failed to get RP: %w", err)
				}
				if diff := cmp.Diff(gotRP.Status.RolloutManagedBy, wantRolloutManagedBy); diff != "" {
					return fmt.Errorf("RP RolloutManagedBy mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("can acknowledge the rebalancing request", func() {
			// Add the rebalancing-managed-by annotation to the CRP.
			Eventually(func() error {
				gotCRP := &placementv1beta1.ClusterResourcePlacement{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crpName}, gotCRP); err != nil {
					return fmt.Errorf("failed to get CRP: %w", err)
				}
				if gotCRP.Annotations == nil {
					gotCRP.Annotations = make(map[string]string)
				}
				gotCRP.Annotations[placementv1beta1.PlacementRebalancingManagedByAnnotationKey] = string(rebalancingReq.UID)
				if err := hubClient.Update(ctx, gotCRP); err != nil {
					return fmt.Errorf("failed to update CRP: %w", err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Add the rebalancing-managed-by annotation to the RP.
			Eventually(func() error {
				gotRP := &placementv1beta1.ResourcePlacement{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: rpName}, gotRP); err != nil {
					return fmt.Errorf("failed to get RP: %w", err)
				}
				if gotRP.Annotations == nil {
					gotRP.Annotations = make(map[string]string)
				}
				gotRP.Annotations[placementv1beta1.PlacementRebalancingManagedByAnnotationKey] = string(rebalancingReq.UID)
				if err := hubClient.Update(ctx, gotRP); err != nil {
					return fmt.Errorf("failed to update RP: %w", err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Create a provisional CRB targeting cluster-2 for the CRP.
			provisionalCRB = &placementv1beta1.ClusterResourceBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf(bindingNameTmpl, crpName, cluster2Name),
					Labels: map[string]string{
						placementv1beta1.PlacementTrackingLabel: crpName,
					},
					Annotations: map[string]string{
						placementv1beta1.ProvisionalBindingAnnotationKey: "true",
						placementv1beta1.ReservedBindingAnnotationKey:    "true",
					},
				},
				Spec: placementv1beta1.ResourceBindingSpec{
					State:                placementv1beta1.BindingStateBound,
					TargetCluster:        cluster2Name,
					ResourceSnapshotName: resourceSnapshotName,
					ClusterDecision: placementv1beta1.ClusterDecision{
						ClusterName: cluster2Name,
						Selected:    true,
						Reason:      "picked by rebalancing request",
					},
				},
			}
			Expect(hubClient.Create(ctx, provisionalCRB)).To(Succeed())

			// Create a provisional RB targeting cluster-2 for the RP.
			provisionalRB = &placementv1beta1.ResourceBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf(bindingNameTmpl, rpName, cluster2Name),
					Namespace: workNSName,
					Labels: map[string]string{
						placementv1beta1.PlacementTrackingLabel: rpName,
					},
					Annotations: map[string]string{
						placementv1beta1.ProvisionalBindingAnnotationKey: "true",
						placementv1beta1.ReservedBindingAnnotationKey:    "true",
					},
				},
				Spec: placementv1beta1.ResourceBindingSpec{
					State:                        placementv1beta1.BindingStateBound,
					TargetCluster:                cluster2Name,
					ResourceSnapshotName:         resourceSnapshotName,
					SchedulingPolicySnapshotName: schedulingPolicySnapshotName,
					ClusterDecision: placementv1beta1.ClusterDecision{
						ClusterName: cluster2Name,
						Selected:    true,
						Reason:      "picked by rebalancing request",
					},
				},
			}
			Expect(hubClient.Create(ctx, provisionalRB)).To(Succeed())

			// Mark the existing CRB (from cluster) as a reserved binding.
			Eventually(func() error {
				gotCRB := &placementv1beta1.ClusterResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crb.Name}, gotCRB); err != nil {
					return fmt.Errorf("failed to get CRB: %w", err)
				}
				if gotCRB.Annotations == nil {
					gotCRB.Annotations = make(map[string]string)
				}
				gotCRB.Annotations[placementv1beta1.ReservedBindingAnnotationKey] = "true"
				if err := hubClient.Update(ctx, gotCRB); err != nil {
					return fmt.Errorf("failed to update CRB: %w", err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Mark the existing RB (from cluster) as a reserved binding.
			Eventually(func() error {
				gotRB := &placementv1beta1.ResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: rb.Name}, gotRB); err != nil {
					return fmt.Errorf("failed to get RB: %w", err)
				}
				if gotRB.Annotations == nil {
					gotRB.Annotations = make(map[string]string)
				}
				gotRB.Annotations[placementv1beta1.ReservedBindingAnnotationKey] = "true"
				if err := hubClient.Update(ctx, gotRB); err != nil {
					return fmt.Errorf("failed to update RB: %w", err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should populate the started condition on the ClusterRebalancingRequest", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				startedCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeStarted)
				wantStartedCond := metav1.Condition{
					Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeStarted,
					Status:             metav1.ConditionTrue,
					Reason:             "SchedulerAcknowledgedRebalancingReq",
					ObservedGeneration: rebalancingReq.Generation,
				}
				if diff := cmp.Diff(startedCond, &wantStartedCond, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("unexpected started condition (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should populate the binding references in the ClusterRebalancingRequest status", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				wantMigrations := []placementv1beta1.ClusterRebalancingRequestPerMigrationStatus{
					{
						PlacementReference: corev1.ObjectReference{
							Name: crpName,
							UID:  crp.UID,
						},
						FromClusterBindingReference: &corev1.ObjectReference{
							Name: crb.Name,
							UID:  crb.UID,
						},
						ToClusterBindingReference: &corev1.ObjectReference{
							Name: provisionalCRB.Name,
							UID:  provisionalCRB.UID,
						},
					},
					{
						PlacementReference: corev1.ObjectReference{
							Name:      rpName,
							Namespace: workNSName,
							UID:       rp.UID,
						},
						FromClusterBindingReference: &corev1.ObjectReference{
							Name:      rb.Name,
							Namespace: workNSName,
							UID:       rb.UID,
						},
						ToClusterBindingReference: &corev1.ObjectReference{
							Name:      provisionalRB.Name,
							Namespace: workNSName,
							UID:       provisionalRB.UID,
						},
					},
				}

				if diff := cmp.Diff(
					rebalancingReq.Status.Migrations,
					wantMigrations,
					ignoreObjRefFields,
					cmpopts.IgnoreFields(placementv1beta1.ClusterRebalancingRequestPerMigrationStatus{}, "Conditions", "Stage"),
				); diff != "" {
					return fmt.Errorf("ClusterRebalancingRequest migrations binding references mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should add the withdrawn annotation to the from-cluster binding of the first targeted placement", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true},
				{fromWithdrawn: false, toProvisional: true},
			}
			wantCompletedConds := []metav1.Condition{{}, {}}

			stateUpdatedActual := func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}

			Eventually(stateUpdatedActual, eventuallyDuration, eventuallyInterval).Should(Succeed())
			Consistently(stateUpdatedActual, consistentlyDuration, consistentlyInterval).Should(Succeed())
		})

		It("can drop the work cleanup finalizer from the from-cluster binding of the CRP", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crb.Name}, crb); err != nil {
					return fmt.Errorf("failed to get CRB %s: %w", crb.Name, err)
				}
				crb.Finalizers = nil
				if err := hubClient.Update(ctx, crb); err != nil {
					return fmt.Errorf("failed to update CRB %s to drop the work finalizer: %w", crb.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should surge the to-cluster binding of the first placement", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
				{fromWithdrawn: false, toProvisional: true},
			}
			wantCompletedConds := []metav1.Condition{{}, {}}

			stateUpdatedActual := func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}

			Eventually(stateUpdatedActual, eventuallyDuration, eventuallyInterval).Should(Succeed())
			Consistently(stateUpdatedActual, consistentlyDuration, consistentlyInterval).Should(Succeed())
		})

		It("can mark the first provisional to-cluster binding as Applied and Available", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: provisionalCRB.Name}, provisionalCRB); err != nil {
					return fmt.Errorf("failed to get provisional CRB %s: %w", provisionalCRB.Name, err)
				}
				appliedCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingApplied),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AllWorkAppliedReason,
					ObservedGeneration: provisionalCRB.Generation,
				}
				availableCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingAvailable),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AvailableReason,
					ObservedGeneration: provisionalCRB.Generation,
				}
				meta.SetStatusCondition(&provisionalCRB.Status.Conditions, appliedCond)
				meta.SetStatusCondition(&provisionalCRB.Status.Conditions, availableCond)
				if err := hubClient.Status().Update(ctx, provisionalCRB); err != nil {
					return fmt.Errorf("failed to update status of provisional CRB %s: %w", provisionalCRB.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should complete the first migration and start draining the second", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
				{fromWithdrawn: true, toProvisional: true},
			}
			wantCompletedConds := []metav1.Condition{
				{},
				{},
			}

			stateUpdatedActual := func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}

			Eventually(stateUpdatedActual, eventuallyDuration, eventuallyInterval).Should(Succeed())
			Consistently(stateUpdatedActual, consistentlyDuration, consistentlyInterval).Should(Succeed())
		})

		It("can drop the work cleanup finalizer from the from-cluster binding of the RP", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: rb.Name}, rb); err != nil {
					return fmt.Errorf("failed to get RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				rb.Finalizers = nil
				if err := hubClient.Update(ctx, rb); err != nil {
					return fmt.Errorf("failed to update RB %s/%s to drop the work finalizer: %w", rb.Namespace, rb.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should surge the to-cluster binding of the second placement (RolloutStarted)", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
			}
			wantCompletedConds := []metav1.Condition{
				{},
				{},
			}

			stateUpdatedActual := func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}

			Eventually(stateUpdatedActual, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("can mark the second provisional to-cluster binding as Applied and Available", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: provisionalRB.Namespace, Name: provisionalRB.Name}, provisionalRB); err != nil {
					return fmt.Errorf("failed to get provisional RB %s/%s: %w", provisionalRB.Namespace, provisionalRB.Name, err)
				}
				appliedCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingApplied),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AllWorkAppliedReason,
					ObservedGeneration: provisionalRB.Generation,
				}
				availableCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingAvailable),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AvailableReason,
					ObservedGeneration: provisionalRB.Generation,
				}
				meta.SetStatusCondition(&provisionalRB.Status.Conditions, appliedCond)
				meta.SetStatusCondition(&provisionalRB.Status.Conditions, availableCond)
				if err := hubClient.Status().Update(ctx, provisionalRB); err != nil {
					return fmt.Errorf("failed to update status of provisional RB %s/%s: %w", provisionalRB.Namespace, provisionalRB.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should complete all migrations and mark the rebalancing request as completed", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
			}
			wantCompletedConds := []metav1.Condition{
				{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
					ObservedGeneration: rebalancingReq.Generation,
				},
				{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
					ObservedGeneration: rebalancingReq.Generation,
				},
			}
			wantReqCompletedCond := metav1.Condition{
				Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted,
				Status:             metav1.ConditionTrue,
				Reason:             placementv1beta1.ClusterRebalancingReqCompletedCondReasonSucceeded,
				ObservedGeneration: rebalancingReq.Generation,
			}

			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s\n%+v", diff, rebalancingReq.Status.Migrations)
				}
				gotReqCompletedCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted)
				if diff := cmp.Diff(gotReqCompletedCond, &wantReqCompletedCond, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("ClusterRebalancingRequest completed condition mismatch (-got, +want):\n%s", diff)
				}
				return nil
				// This step can take longer than the default duration to complete.
			}, eventuallyDuration*2, eventuallyInterval).Should(Succeed())
		})

		It("can delete the rebalancing request", func() {
			Eventually(func() error {
				if err := hubClient.Delete(ctx, rebalancingReq); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to delete ClusterRebalancingRequest: %w", err)
				}

				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get ClusterRebalancingRequest after deletion: %w", err)
				}

				return fmt.Errorf("ClusterRebalancingRequest %s still exists", rebalancingReq.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should commit the migration (provisional bindings activated, from bindings deleted)", func() {
			Eventually(func() error {
				// Check that the original from-cluster CRB is gone.
				gotCRB := &placementv1beta1.ClusterResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crb.Name}, gotCRB); err != nil {
					if !apierrors.IsNotFound(err) {
						return fmt.Errorf("failed to get CRB %s: %w", crb.Name, err)
					}
				} else {
					return fmt.Errorf("from-cluster CRB %s still exists", crb.Name)
				}

				// Check that the original from-cluster RB is gone.
				gotRB := &placementv1beta1.ResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: rb.Name}, gotRB); err != nil {
					if !apierrors.IsNotFound(err) {
						return fmt.Errorf("failed to get RB %s/%s: %w", rb.Namespace, rb.Name, err)
					}
				} else {
					return fmt.Errorf("from-cluster RB %s/%s still exists", rb.Namespace, rb.Name)
				}

				// Check that the provisional CRB is present, no longer provisional, no longer reserved,
				// and has the RolloutStarted condition.
				gotProvisionalCRB := &placementv1beta1.ClusterResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: provisionalCRB.Name}, gotProvisionalCRB); err != nil {
					return fmt.Errorf("failed to get provisional CRB %s: %w", provisionalCRB.Name, err)
				}
				if _, found := gotProvisionalCRB.Annotations[placementv1beta1.ProvisionalBindingAnnotationKey]; found {
					return fmt.Errorf("provisional CRB %s still has provisional annotation", provisionalCRB.Name)
				}
				if _, found := gotProvisionalCRB.Annotations[placementv1beta1.ReservedBindingAnnotationKey]; found {
					return fmt.Errorf("provisional CRB %s still has reserved annotation", provisionalCRB.Name)
				}
				rolloutStartedCond := meta.FindStatusCondition(gotProvisionalCRB.Status.Conditions, string(placementv1beta1.ResourceBindingRolloutStarted))
				if !condition.IsConditionStatusTrue(rolloutStartedCond, gotProvisionalCRB.Generation) {
					return fmt.Errorf("provisional CRB %s does not have RolloutStarted condition set to true", provisionalCRB.Name)
				}

				// Check that the provisional RB is present, no longer provisional, no longer reserved,
				// and has the RolloutStarted condition.
				gotProvisionalRB := &placementv1beta1.ResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: provisionalRB.Namespace, Name: provisionalRB.Name}, gotProvisionalRB); err != nil {
					return fmt.Errorf("failed to get provisional RB %s/%s: %w", provisionalRB.Namespace, provisionalRB.Name, err)
				}
				if _, found := gotProvisionalRB.Annotations[placementv1beta1.ProvisionalBindingAnnotationKey]; found {
					return fmt.Errorf("provisional RB %s/%s still has provisional annotation", provisionalRB.Namespace, provisionalRB.Name)
				}
				if _, found := gotProvisionalRB.Annotations[placementv1beta1.ReservedBindingAnnotationKey]; found {
					return fmt.Errorf("provisional RB %s/%s still has reserved annotation", provisionalRB.Namespace, provisionalRB.Name)
				}
				rolloutStartedCond = meta.FindStatusCondition(gotProvisionalRB.Status.Conditions, string(placementv1beta1.ResourceBindingRolloutStarted))
				if !condition.IsConditionStatusTrue(rolloutStartedCond, gotProvisionalRB.Generation) {
					return fmt.Errorf("provisional RB %s/%s does not have RolloutStarted condition set to true", provisionalRB.Namespace, provisionalRB.Name)
				}

				return nil
			}, eventuallyDuration*2, eventuallyInterval).Should(Succeed())
		})

		AfterAll(func() {
			// Delete the ClusterRebalancingRequest.
			Eventually(func() error {
				if err := hubClient.Delete(ctx, rebalancingReq); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete ClusterRebalancingRequest: %w", err)
				}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get ClusterRebalancingRequest after deletion: %w", err)
				}
				return fmt.Errorf("ClusterRebalancingRequest %s still exists", rebalancingReq.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Delete the CRB (clear finalizers first if still present).
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crb.Name}, crb); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get CRB %s: %w", crb.Name, err)
				}
				crb.Finalizers = nil
				if err := hubClient.Update(ctx, crb); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to update CRB %s: %w", crb.Name, err)
				}
				if err := hubClient.Delete(ctx, crb); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete CRB %s: %w", crb.Name, err)
				}
				return fmt.Errorf("CRB %s still exists", crb.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Delete the RB (clear finalizers first if still present).
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: rb.Name}, rb); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				rb.Finalizers = nil
				if err := hubClient.Update(ctx, rb); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to update RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				if err := hubClient.Delete(ctx, rb); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				return fmt.Errorf("RB %s/%s still exists", rb.Namespace, rb.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Delete the provisional CRB if it was created.
			if provisionalCRB != nil {
				Eventually(func() error {
					if err := hubClient.Delete(ctx, provisionalCRB); err != nil && !apierrors.IsNotFound(err) {
						return fmt.Errorf("failed to delete provisional CRB %s: %w", provisionalCRB.Name, err)
					}
					if err := hubClient.Get(ctx, types.NamespacedName{Name: provisionalCRB.Name}, provisionalCRB); err != nil {
						if apierrors.IsNotFound(err) {
							return nil
						}
						return fmt.Errorf("failed to get provisional CRB %s after deletion: %w", provisionalCRB.Name, err)
					}
					return fmt.Errorf("provisional CRB %s still exists", provisionalCRB.Name)
				}, eventuallyDuration, eventuallyInterval).Should(Succeed())
			}

			// Delete the provisional RB if it was created.
			if provisionalRB != nil {
				Eventually(func() error {
					if err := hubClient.Delete(ctx, provisionalRB); err != nil && !apierrors.IsNotFound(err) {
						return fmt.Errorf("failed to delete provisional RB %s/%s: %w", provisionalRB.Namespace, provisionalRB.Name, err)
					}
					if err := hubClient.Get(ctx, types.NamespacedName{Namespace: provisionalRB.Namespace, Name: provisionalRB.Name}, provisionalRB); err != nil {
						if apierrors.IsNotFound(err) {
							return nil
						}
						return fmt.Errorf("failed to get provisional RB %s/%s after deletion: %w", provisionalRB.Namespace, provisionalRB.Name, err)
					}
					return fmt.Errorf("provisional RB %s/%s still exists", provisionalRB.Namespace, provisionalRB.Name)
				}, eventuallyDuration, eventuallyInterval).Should(Succeed())
			}

			// Delete the CRP.
			Eventually(func() error {
				if err := hubClient.Delete(ctx, crp); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete CRP %s: %w", crp.Name, err)
				}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crp.Name}, crp); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get CRP %s after deletion: %w", crp.Name, err)
				}
				return fmt.Errorf("CRP %s still exists", crp.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Delete the RP.
			Eventually(func() error {
				if err := hubClient.Delete(ctx, rp); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete RP %s/%s: %w", rp.Namespace, rp.Name, err)
				}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: rp.Namespace, Name: rp.Name}, rp); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get RP %s/%s after deletion: %w", rp.Namespace, rp.Name, err)
				}
				return fmt.Errorf("RP %s/%s still exists", rp.Namespace, rp.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
			// The environment prepared by the envtest package does not support namespace
			// deletion; consequently this test suite would not attempt to verify its deletion.
		})

	})

	Context("rebalancing placements (completed, rolled back, committed)", Ordered, func() {
		crpName := fmt.Sprintf(placementNameTmpl, GinkgoParallelProcess())
		rpName := fmt.Sprintf(placementNameTmpl, GinkgoParallelProcess())
		workNSName := fmt.Sprintf(workNSNameTmpl, GinkgoParallelProcess(), utils.RandStr())
		rebalancingReqName := fmt.Sprintf(rebalancingReqNameTmpl, GinkgoParallelProcess())

		var crp *placementv1beta1.ClusterResourcePlacement
		var rp *placementv1beta1.ResourcePlacement
		var crb *placementv1beta1.ClusterResourceBinding
		var rb *placementv1beta1.ResourceBinding
		var provisionalCRB *placementv1beta1.ClusterResourceBinding
		var provisionalRB *placementv1beta1.ResourceBinding
		var rebalancingReq *placementv1beta1.ClusterRebalancingRequest

		BeforeAll(func() {
			// Create the CRP.
			crp = &placementv1beta1.ClusterResourcePlacement{
				ObjectMeta: metav1.ObjectMeta{
					Name: crpName,
				},
				Spec: placementv1beta1.PlacementSpec{
					ResourceSelectors: []placementv1beta1.ResourceSelectorTerm{
						{
							Group:   "",
							Version: "v1",
							Kind:    "Namespace",
							Name:    appNamespace,
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, crp)).To(Succeed())

			// Create a binding for the CRP.
			crb = &placementv1beta1.ClusterResourceBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:       fmt.Sprintf(bindingNameTmpl, crpName, cluster1Name),
					Finalizers: []string{placementv1beta1.WorkFinalizer},
					Labels: map[string]string{
						placementv1beta1.PlacementTrackingLabel: crpName,
					},
				},
				Spec: placementv1beta1.ResourceBindingSpec{
					State:                placementv1beta1.BindingStateBound,
					TargetCluster:        cluster1Name,
					ResourceSnapshotName: resourceSnapshotName,
					ClusterDecision: placementv1beta1.ClusterDecision{
						ClusterName: cluster1Name,
						Selected:    true,
						Reason:      "manually created",
					},
				},
			}
			Expect(hubClient.Create(ctx, crb)).To(Succeed())

			// Create a namespace for the RP.
			workNamespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: workNSName,
				},
			}
			Expect(hubClient.Create(ctx, workNamespace)).To(Succeed())

			// Create the RP.
			rp = &placementv1beta1.ResourcePlacement{
				ObjectMeta: metav1.ObjectMeta{
					Name:      rpName,
					Namespace: workNSName,
				},
				Spec: placementv1beta1.PlacementSpec{
					ResourceSelectors: []placementv1beta1.ResourceSelectorTerm{
						{
							Group:   "",
							Version: "v1",
							Kind:    "ConfigMap",
							Name:    configMapName,
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, rp)).To(Succeed())

			// Create a binding for the RP.
			rb = &placementv1beta1.ResourceBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:       fmt.Sprintf(bindingNameTmpl, rpName, cluster1Name),
					Namespace:  workNSName,
					Finalizers: []string{placementv1beta1.WorkFinalizer},
					Labels: map[string]string{
						placementv1beta1.PlacementTrackingLabel: rpName,
					},
				},
				Spec: placementv1beta1.ResourceBindingSpec{
					State:                        placementv1beta1.BindingStateBound,
					TargetCluster:                cluster1Name,
					ResourceSnapshotName:         resourceSnapshotName,
					SchedulingPolicySnapshotName: schedulingPolicySnapshotName,
					ClusterDecision: placementv1beta1.ClusterDecision{
						ClusterName: cluster1Name,
						Selected:    true,
						Reason:      "manually created",
					},
				},
			}
			Expect(hubClient.Create(ctx, rb)).To(Succeed())

			// Create the ClusterRebalancingRequest.
			rebalancingReq = &placementv1beta1.ClusterRebalancingRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: rebalancingReqName,
				},
				Spec: placementv1beta1.ClusterRebalancingRequestSpec{
					From: cluster1Name,
					To:   cluster2Name,
					FailurePolicy: &placementv1beta1.ClusterRebalancingRequestFailurePolicy{
						MaxFailureCount: ptr.To(int32(1)),
						MaximumWaitDurationPerMigrationAttemptSeconds: ptr.To(int32(300)),
					},
				},
			}
			Expect(hubClient.Create(ctx, rebalancingReq)).To(Succeed())
		})

		It("should initialize the ClusterRebalancingRequest", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				initCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized)
				wantInitCond := metav1.Condition{
					Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeInitialized,
					Status:             metav1.ConditionTrue,
					Reason:             "IdentifiedAndClaimedAllPlacements",
					ObservedGeneration: rebalancingReq.Generation,
				}

				if diff := cmp.Diff(initCond, &wantInitCond, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("unexpected initialization condition (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should populate the ClusterRebalancingRequest status with migration entries", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				wantMigrations := []placementv1beta1.ClusterRebalancingRequestPerMigrationStatus{
					{
						PlacementReference: corev1.ObjectReference{
							Kind: "ClusterResourcePlacement",
							Name: crpName,
							UID:  crp.UID,
						},
					},
					{
						PlacementReference: corev1.ObjectReference{
							Kind:      "ResourcePlacement",
							Name:      rpName,
							Namespace: workNSName,
							UID:       rp.UID,
						},
					},
				}

				if diff := cmp.Diff(
					rebalancingReq.Status.Migrations,
					wantMigrations,
				); diff != "" {
					return fmt.Errorf("ClusterRebalancingRequest migrations mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should claim rollout management on both placements", func() {
			wantRolloutManagedBy := []placementv1beta1.RolloutManagerReference{
				{
					ObjectReference: corev1.ObjectReference{
						Kind: "ClusterRebalancingRequest",
						Name: rebalancingReq.Name,
						UID:  rebalancingReq.UID,
					},
					Mode: placementv1beta1.RolloutManagerModeExclusive,
				},
			}

			Eventually(func() error {
				gotCRP := &placementv1beta1.ClusterResourcePlacement{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crpName}, gotCRP); err != nil {
					return fmt.Errorf("failed to get CRP: %w", err)
				}
				if diff := cmp.Diff(gotCRP.Status.RolloutManagedBy, wantRolloutManagedBy); diff != "" {
					return fmt.Errorf("CRP RolloutManagedBy mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			Eventually(func() error {
				gotRP := &placementv1beta1.ResourcePlacement{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: rpName}, gotRP); err != nil {
					return fmt.Errorf("failed to get RP: %w", err)
				}
				if diff := cmp.Diff(gotRP.Status.RolloutManagedBy, wantRolloutManagedBy); diff != "" {
					return fmt.Errorf("RP RolloutManagedBy mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("can acknowledge the rebalancing request", func() {
			// Add the rebalancing-managed-by annotation to the CRP.
			Eventually(func() error {
				gotCRP := &placementv1beta1.ClusterResourcePlacement{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crpName}, gotCRP); err != nil {
					return fmt.Errorf("failed to get CRP: %w", err)
				}
				if gotCRP.Annotations == nil {
					gotCRP.Annotations = make(map[string]string)
				}
				gotCRP.Annotations[placementv1beta1.PlacementRebalancingManagedByAnnotationKey] = string(rebalancingReq.UID)
				if err := hubClient.Update(ctx, gotCRP); err != nil {
					return fmt.Errorf("failed to update CRP: %w", err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Add the rebalancing-managed-by annotation to the RP.
			Eventually(func() error {
				gotRP := &placementv1beta1.ResourcePlacement{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: rpName}, gotRP); err != nil {
					return fmt.Errorf("failed to get RP: %w", err)
				}
				if gotRP.Annotations == nil {
					gotRP.Annotations = make(map[string]string)
				}
				gotRP.Annotations[placementv1beta1.PlacementRebalancingManagedByAnnotationKey] = string(rebalancingReq.UID)
				if err := hubClient.Update(ctx, gotRP); err != nil {
					return fmt.Errorf("failed to update RP: %w", err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Create a provisional CRB targeting cluster-2 for the CRP.
			provisionalCRB = &placementv1beta1.ClusterResourceBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf(bindingNameTmpl, crpName, cluster2Name),
					Labels: map[string]string{
						placementv1beta1.PlacementTrackingLabel: crpName,
					},
					Annotations: map[string]string{
						placementv1beta1.ProvisionalBindingAnnotationKey: "true",
						placementv1beta1.ReservedBindingAnnotationKey:    "true",
					},
				},
				Spec: placementv1beta1.ResourceBindingSpec{
					State:                placementv1beta1.BindingStateBound,
					TargetCluster:        cluster2Name,
					ResourceSnapshotName: resourceSnapshotName,
					ClusterDecision: placementv1beta1.ClusterDecision{
						ClusterName: cluster2Name,
						Selected:    true,
						Reason:      "picked by rebalancing request",
					},
				},
			}
			Expect(hubClient.Create(ctx, provisionalCRB)).To(Succeed())

			// Create a provisional RB targeting cluster-2 for the RP.
			provisionalRB = &placementv1beta1.ResourceBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf(bindingNameTmpl, rpName, cluster2Name),
					Namespace: workNSName,
					Labels: map[string]string{
						placementv1beta1.PlacementTrackingLabel: rpName,
					},
					Annotations: map[string]string{
						placementv1beta1.ProvisionalBindingAnnotationKey: "true",
						placementv1beta1.ReservedBindingAnnotationKey:    "true",
					},
				},
				Spec: placementv1beta1.ResourceBindingSpec{
					State:                        placementv1beta1.BindingStateBound,
					TargetCluster:                cluster2Name,
					ResourceSnapshotName:         resourceSnapshotName,
					SchedulingPolicySnapshotName: schedulingPolicySnapshotName,
					ClusterDecision: placementv1beta1.ClusterDecision{
						ClusterName: cluster2Name,
						Selected:    true,
						Reason:      "picked by rebalancing request",
					},
				},
			}
			Expect(hubClient.Create(ctx, provisionalRB)).To(Succeed())

			// Mark the existing CRB (from cluster) as a reserved binding.
			Eventually(func() error {
				gotCRB := &placementv1beta1.ClusterResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crb.Name}, gotCRB); err != nil {
					return fmt.Errorf("failed to get CRB: %w", err)
				}
				if gotCRB.Annotations == nil {
					gotCRB.Annotations = make(map[string]string)
				}
				gotCRB.Annotations[placementv1beta1.ReservedBindingAnnotationKey] = "true"
				if err := hubClient.Update(ctx, gotCRB); err != nil {
					return fmt.Errorf("failed to update CRB: %w", err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Mark the existing RB (from cluster) as a reserved binding.
			Eventually(func() error {
				gotRB := &placementv1beta1.ResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: rb.Name}, gotRB); err != nil {
					return fmt.Errorf("failed to get RB: %w", err)
				}
				if gotRB.Annotations == nil {
					gotRB.Annotations = make(map[string]string)
				}
				gotRB.Annotations[placementv1beta1.ReservedBindingAnnotationKey] = "true"
				if err := hubClient.Update(ctx, gotRB); err != nil {
					return fmt.Errorf("failed to update RB: %w", err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should populate the started condition on the ClusterRebalancingRequest", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				startedCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeStarted)
				wantStartedCond := metav1.Condition{
					Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeStarted,
					Status:             metav1.ConditionTrue,
					Reason:             "SchedulerAcknowledgedRebalancingReq",
					ObservedGeneration: rebalancingReq.Generation,
				}
				if diff := cmp.Diff(startedCond, &wantStartedCond, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("unexpected started condition (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should populate the binding references in the ClusterRebalancingRequest status", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				wantMigrations := []placementv1beta1.ClusterRebalancingRequestPerMigrationStatus{
					{
						PlacementReference: corev1.ObjectReference{
							Name: crpName,
							UID:  crp.UID,
						},
						FromClusterBindingReference: &corev1.ObjectReference{
							Name: crb.Name,
							UID:  crb.UID,
						},
						ToClusterBindingReference: &corev1.ObjectReference{
							Name: provisionalCRB.Name,
							UID:  provisionalCRB.UID,
						},
					},
					{
						PlacementReference: corev1.ObjectReference{
							Name:      rpName,
							Namespace: workNSName,
							UID:       rp.UID,
						},
						FromClusterBindingReference: &corev1.ObjectReference{
							Name:      rb.Name,
							Namespace: workNSName,
							UID:       rb.UID,
						},
						ToClusterBindingReference: &corev1.ObjectReference{
							Name:      provisionalRB.Name,
							Namespace: workNSName,
							UID:       provisionalRB.UID,
						},
					},
				}

				if diff := cmp.Diff(
					rebalancingReq.Status.Migrations,
					wantMigrations,
					ignoreObjRefFields,
					cmpopts.IgnoreFields(placementv1beta1.ClusterRebalancingRequestPerMigrationStatus{}, "Conditions", "Stage"),
				); diff != "" {
					return fmt.Errorf("ClusterRebalancingRequest migrations binding references mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should add the withdrawn annotation to the from-cluster binding of the first targeted placement", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true},
				{fromWithdrawn: false, toProvisional: true},
			}
			wantCompletedConds := []metav1.Condition{{}, {}}

			stateUpdatedActual := func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}

			Eventually(stateUpdatedActual, eventuallyDuration, eventuallyInterval).Should(Succeed())
			Consistently(stateUpdatedActual, consistentlyDuration, consistentlyInterval).Should(Succeed())
		})

		It("can drop the work cleanup finalizer from the from-cluster binding of the CRP", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crb.Name}, crb); err != nil {
					return fmt.Errorf("failed to get CRB %s: %w", crb.Name, err)
				}
				crb.Finalizers = nil
				if err := hubClient.Update(ctx, crb); err != nil {
					return fmt.Errorf("failed to update CRB %s to drop the work finalizer: %w", crb.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should surge the to-cluster binding of the first placement", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
				{fromWithdrawn: false, toProvisional: true},
			}
			wantCompletedConds := []metav1.Condition{{}, {}}

			stateUpdatedActual := func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}

			Eventually(stateUpdatedActual, eventuallyDuration, eventuallyInterval).Should(Succeed())
			Consistently(stateUpdatedActual, consistentlyDuration, consistentlyInterval).Should(Succeed())
		})

		It("can mark the first provisional to-cluster binding as Applied and Available", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: provisionalCRB.Name}, provisionalCRB); err != nil {
					return fmt.Errorf("failed to get provisional CRB %s: %w", provisionalCRB.Name, err)
				}
				appliedCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingApplied),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AllWorkAppliedReason,
					ObservedGeneration: provisionalCRB.Generation,
				}
				availableCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingAvailable),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AvailableReason,
					ObservedGeneration: provisionalCRB.Generation,
				}
				meta.SetStatusCondition(&provisionalCRB.Status.Conditions, appliedCond)
				meta.SetStatusCondition(&provisionalCRB.Status.Conditions, availableCond)
				if err := hubClient.Status().Update(ctx, provisionalCRB); err != nil {
					return fmt.Errorf("failed to update status of provisional CRB %s: %w", provisionalCRB.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should complete the first migration and start draining the second", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
				{fromWithdrawn: true, toProvisional: true},
			}
			wantCompletedConds := []metav1.Condition{
				{},
				{},
			}

			stateUpdatedActual := func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}

			Eventually(stateUpdatedActual, eventuallyDuration, eventuallyInterval).Should(Succeed())
			Consistently(stateUpdatedActual, consistentlyDuration, consistentlyInterval).Should(Succeed())
		})

		It("can drop the work cleanup finalizer from the from-cluster binding of the RP", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: rb.Name}, rb); err != nil {
					return fmt.Errorf("failed to get RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				rb.Finalizers = nil
				if err := hubClient.Update(ctx, rb); err != nil {
					return fmt.Errorf("failed to update RB %s/%s to drop the work finalizer: %w", rb.Namespace, rb.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should surge the to-cluster binding of the second placement (RolloutStarted)", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
			}
			wantCompletedConds := []metav1.Condition{
				{},
				{},
			}

			stateUpdatedActual := func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s", diff)
				}
				return nil
			}

			Eventually(stateUpdatedActual, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("can mark the second provisional to-cluster binding as Applied and Available", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: provisionalRB.Namespace, Name: provisionalRB.Name}, provisionalRB); err != nil {
					return fmt.Errorf("failed to get provisional RB %s/%s: %w", provisionalRB.Namespace, provisionalRB.Name, err)
				}
				appliedCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingApplied),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AllWorkAppliedReason,
					ObservedGeneration: provisionalRB.Generation,
				}
				availableCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingAvailable),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AvailableReason,
					ObservedGeneration: provisionalRB.Generation,
				}
				meta.SetStatusCondition(&provisionalRB.Status.Conditions, appliedCond)
				meta.SetStatusCondition(&provisionalRB.Status.Conditions, availableCond)
				if err := hubClient.Status().Update(ctx, provisionalRB); err != nil {
					return fmt.Errorf("failed to update status of provisional RB %s/%s: %w", provisionalRB.Namespace, provisionalRB.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should complete all migrations and mark the rebalancing request as completed", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
			}
			wantCompletedConds := []metav1.Condition{
				{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
					ObservedGeneration: rebalancingReq.Generation,
				},
				{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
					ObservedGeneration: rebalancingReq.Generation,
				},
			}
			wantReqCompletedCond := metav1.Condition{
				Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted,
				Status:             metav1.ConditionTrue,
				Reason:             placementv1beta1.ClusterRebalancingReqCompletedCondReasonSucceeded,
				ObservedGeneration: rebalancingReq.Generation,
			}

			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s\n%+v", diff, rebalancingReq.Status.Migrations)
				}
				gotReqCompletedCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted)
				if diff := cmp.Diff(gotReqCompletedCond, &wantReqCompletedCond, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("ClusterRebalancingRequest completed condition mismatch (-got, +want):\n%s", diff)
				}
				return nil
				// This step can take longer than the default duration to complete.
			}, eventuallyDuration*2, eventuallyInterval).Should(Succeed())
		})

		It("can set the rebalancing request to roll back", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				rebalancingReq.Spec.Rollback = true
				if err := hubClient.Update(ctx, rebalancingReq); err != nil {
					return fmt.Errorf("failed to update ClusterRebalancingRequest to enable rollback: %w", err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should start rolling back the first migration attempt", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: false, fromRolloutStarted: true, toProvisional: true, toWithdrawn: true, toRolloutStarted: true},
				{fromWithdrawn: true, toProvisional: true, toRolloutStarted: true},
			}
			wantCompletedConds := []metav1.Condition{
				{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
					ObservedGeneration: rebalancingReq.Generation - 1,
				},
				{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
					ObservedGeneration: rebalancingReq.Generation - 1,
				},
			}
			wantReqCompletedCond := metav1.Condition{
				Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted,
				Status:             metav1.ConditionTrue,
				Reason:             placementv1beta1.ClusterRebalancingReqCompletedCondReasonSucceeded,
				ObservedGeneration: rebalancingReq.Generation - 1,
			}

			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, _, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s\n%+v", diff, rebalancingReq.Status.Migrations)
				}
				gotReqCompletedCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeCompleted)
				if diff := cmp.Diff(gotReqCompletedCond, &wantReqCompletedCond, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("ClusterRebalancingRequest completed condition mismatch (-got, +want):\n%s", diff)
				}
				return nil
				// This step can take longer than the default duration to complete.
			}, eventuallyDuration*2, eventuallyInterval).Should(Succeed())
		})

		It("can add the Applied and Available condition of the true status to the from-cluster binding of the first placement", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crb.Name}, crb); err != nil {
					return fmt.Errorf("failed to get CRB %s: %w", crb.Name, err)
				}
				appliedCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingApplied),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AllWorkAppliedReason,
					ObservedGeneration: crb.Generation,
				}
				availableCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingAvailable),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AvailableReason,
					ObservedGeneration: crb.Generation,
				}
				meta.SetStatusCondition(&crb.Status.Conditions, appliedCond)
				meta.SetStatusCondition(&crb.Status.Conditions, availableCond)
				if err := hubClient.Status().Update(ctx, crb); err != nil {
					return fmt.Errorf("failed to update status of CRB %s: %w", crb.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should start rolling back the second migration attempt", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: false, fromRolloutStarted: true, toProvisional: true, toWithdrawn: true, toRolloutStarted: true},
				{fromWithdrawn: false, fromRolloutStarted: true, toProvisional: true, toWithdrawn: true, toRolloutStarted: true},
			}
			wantCompletedConds := []metav1.Condition{
				{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
					ObservedGeneration: rebalancingReq.Generation - 1,
				},
				{
					Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
					ObservedGeneration: rebalancingReq.Generation - 1,
				},
			}
			wantRolledBackConds := []metav1.Condition{
				{},
				{},
			}

			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				states, completedConds, rolledBackConds, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s\n%+v", diff, rebalancingReq.Status.Migrations)
				}
				if diff := cmp.Diff(rolledBackConds, wantRolledBackConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration rolled back conditions mismatch (-got, +want):\n%s\n%+v", diff, rebalancingReq.Status.Migrations)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("can add the Applied and Available condition of the true status to the from-cluster binding of the second placement", func() {
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: rb.Name}, rb); err != nil {
					return fmt.Errorf("failed to get RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				appliedCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingApplied),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AllWorkAppliedReason,
					ObservedGeneration: rb.Generation,
				}
				availableCond := metav1.Condition{
					Type:               string(placementv1beta1.ResourceBindingAvailable),
					Status:             metav1.ConditionTrue,
					Reason:             condition.AvailableReason,
					ObservedGeneration: rb.Generation,
				}
				meta.SetStatusCondition(&rb.Status.Conditions, appliedCond)
				meta.SetStatusCondition(&rb.Status.Conditions, availableCond)
				if err := hubClient.Status().Update(ctx, rb); err != nil {
					return fmt.Errorf("failed to update status of RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should complete the rollback of all migrations and mark the rebalancing request as rolled back", func() {
			wantStates := []bindingPairState{
				{fromWithdrawn: false, fromRolloutStarted: true, toProvisional: true, toWithdrawn: true, toRolloutStarted: true},
				{fromWithdrawn: false, fromRolloutStarted: true, toProvisional: true, toWithdrawn: true, toRolloutStarted: true},
			}

			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					return fmt.Errorf("failed to get ClusterRebalancingRequest: %w", err)
				}
				wantCompletedConds := []metav1.Condition{
					{
						Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
						Status:             metav1.ConditionTrue,
						Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
						ObservedGeneration: rebalancingReq.Generation - 1,
					},
					{
						Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeCompleted,
						Status:             metav1.ConditionTrue,
						Reason:             placementv1beta1.RebalancingMigrationAttemptCompletedCondReasonSucceeded,
						ObservedGeneration: rebalancingReq.Generation - 1,
					},
				}
				wantRolledBackConds := []metav1.Condition{
					{
						Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack,
						Status:             metav1.ConditionTrue,
						Reason:             placementv1beta1.RebalancingMigrationAttemptRolledBackCondReasonSucceeded,
						ObservedGeneration: rebalancingReq.Generation,
					},
					{
						Type:               placementv1beta1.RebalancingMigrationAttemptConditionTypeRolledBack,
						Status:             metav1.ConditionTrue,
						Reason:             placementv1beta1.RebalancingMigrationAttemptRolledBackCondReasonSucceeded,
						ObservedGeneration: rebalancingReq.Generation,
					},
				}
				wantReqRolledBackCond := metav1.Condition{
					Type:               placementv1beta1.ClusterRebalancingRequestConditionTypeRolledBack,
					Status:             metav1.ConditionTrue,
					Reason:             placementv1beta1.ClusterRebalancingReqRolledBackCondReasonSucceeded,
					ObservedGeneration: rebalancingReq.Generation,
				}
				states, completedConds, rolledBackConds, err := snapshotTargetedBindingStateAndMigrationConds(rebalancingReq)
				if err != nil {
					return err
				}
				if diff := cmp.Diff(states, wantStates, cmp.AllowUnexported(bindingPairState{})); diff != "" {
					return fmt.Errorf("binding pair states mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(completedConds, wantCompletedConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration completion conditions mismatch (-got, +want):\n%s", diff)
				}
				if diff := cmp.Diff(rolledBackConds, wantRolledBackConds, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("per-migration rolled back conditions mismatch (-got, +want):\n%s", diff)
				}
				gotReqRolledBackCond := meta.FindStatusCondition(rebalancingReq.Status.Conditions, placementv1beta1.ClusterRebalancingRequestConditionTypeRolledBack)
				if diff := cmp.Diff(gotReqRolledBackCond, &wantReqRolledBackCond, ignoreFieldConditionLTTMsg); diff != "" {
					return fmt.Errorf("ClusterRebalancingRequest rolled back condition mismatch (-got, +want):\n%s", diff)
				}
				return nil
				// This step can take longer than the default duration to complete.
			}, eventuallyDuration*2, eventuallyInterval).Should(Succeed())
		})

		It("can delete the rebalancing request", func() {
			Eventually(func() error {
				if err := hubClient.Delete(ctx, rebalancingReq); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to delete ClusterRebalancingRequest: %w", err)
				}

				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get ClusterRebalancingRequest after deletion: %w", err)
				}

				return fmt.Errorf("ClusterRebalancingRequest %s still exists", rebalancingReq.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
		})

		It("should commit the rollback (provisional bindings deleted, from bindings restored)", func() {
			Eventually(func() error {
				// Check that the original from-cluster CRB is present and does not have the withdrawn annotation.
				gotCRB := &placementv1beta1.ClusterResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crb.Name}, gotCRB); err != nil {
					return fmt.Errorf("failed to get CRB %s: %w", crb.Name, err)
				}
				if _, found := gotCRB.Annotations[placementv1beta1.WithdrawnBindingAnnotationKey]; found {
					return fmt.Errorf("from-cluster CRB %s still has withdrawn annotation", crb.Name)
				}

				// Check that the original from-cluster RB is present and does not have the withdrawn annotation.
				gotRB := &placementv1beta1.ResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: rb.Name}, gotRB); err != nil {
					return fmt.Errorf("failed to get RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				if _, found := gotRB.Annotations[placementv1beta1.WithdrawnBindingAnnotationKey]; found {
					return fmt.Errorf("from-cluster RB %s/%s still has withdrawn annotation", rb.Namespace, rb.Name)
				}

				// Check that the provisional CRB has been deleted.
				gotProvisionalCRB := &placementv1beta1.ClusterResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: provisionalCRB.Name}, gotProvisionalCRB); err != nil {
					if !apierrors.IsNotFound(err) {
						return fmt.Errorf("failed to get provisional CRB %s: %w", provisionalCRB.Name, err)
					}
				} else {
					return fmt.Errorf("provisional CRB %s still exists", provisionalCRB.Name)
				}

				// Check that the provisional RB has been deleted.
				gotProvisionalRB := &placementv1beta1.ResourceBinding{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: provisionalRB.Namespace, Name: provisionalRB.Name}, gotProvisionalRB); err != nil {
					if !apierrors.IsNotFound(err) {
						return fmt.Errorf("failed to get provisional RB %s/%s: %w", provisionalRB.Namespace, provisionalRB.Name, err)
					}
				} else {
					return fmt.Errorf("provisional RB %s/%s still exists", provisionalRB.Namespace, provisionalRB.Name)
				}

				return nil
			}, eventuallyDuration*2, eventuallyInterval).Should(Succeed())
		})

		AfterAll(func() {
			// Delete the ClusterRebalancingRequest.
			Eventually(func() error {
				if err := hubClient.Delete(ctx, rebalancingReq); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete ClusterRebalancingRequest: %w", err)
				}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: rebalancingReq.Name}, rebalancingReq); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get ClusterRebalancingRequest after deletion: %w", err)
				}
				return fmt.Errorf("ClusterRebalancingRequest %s still exists", rebalancingReq.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Delete the CRB (clear finalizers first if still present).
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crb.Name}, crb); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get CRB %s: %w", crb.Name, err)
				}
				crb.Finalizers = nil
				if err := hubClient.Update(ctx, crb); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to update CRB %s: %w", crb.Name, err)
				}
				if err := hubClient.Delete(ctx, crb); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete CRB %s: %w", crb.Name, err)
				}
				return fmt.Errorf("CRB %s still exists", crb.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Delete the RB (clear finalizers first if still present).
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: rb.Namespace, Name: rb.Name}, rb); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				rb.Finalizers = nil
				if err := hubClient.Update(ctx, rb); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to update RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				if err := hubClient.Delete(ctx, rb); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete RB %s/%s: %w", rb.Namespace, rb.Name, err)
				}
				return fmt.Errorf("RB %s/%s still exists", rb.Namespace, rb.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Delete the provisional CRB if it was created.
			if provisionalCRB != nil {
				Eventually(func() error {
					if err := hubClient.Delete(ctx, provisionalCRB); err != nil && !apierrors.IsNotFound(err) {
						return fmt.Errorf("failed to delete provisional CRB %s: %w", provisionalCRB.Name, err)
					}
					if err := hubClient.Get(ctx, types.NamespacedName{Name: provisionalCRB.Name}, provisionalCRB); err != nil {
						if apierrors.IsNotFound(err) {
							return nil
						}
						return fmt.Errorf("failed to get provisional CRB %s after deletion: %w", provisionalCRB.Name, err)
					}
					return fmt.Errorf("provisional CRB %s still exists", provisionalCRB.Name)
				}, eventuallyDuration, eventuallyInterval).Should(Succeed())
			}

			// Delete the provisional RB if it was created.
			if provisionalRB != nil {
				Eventually(func() error {
					if err := hubClient.Delete(ctx, provisionalRB); err != nil && !apierrors.IsNotFound(err) {
						return fmt.Errorf("failed to delete provisional RB %s/%s: %w", provisionalRB.Namespace, provisionalRB.Name, err)
					}
					if err := hubClient.Get(ctx, types.NamespacedName{Namespace: provisionalRB.Namespace, Name: provisionalRB.Name}, provisionalRB); err != nil {
						if apierrors.IsNotFound(err) {
							return nil
						}
						return fmt.Errorf("failed to get provisional RB %s/%s after deletion: %w", provisionalRB.Namespace, provisionalRB.Name, err)
					}
					return fmt.Errorf("provisional RB %s/%s still exists", provisionalRB.Namespace, provisionalRB.Name)
				}, eventuallyDuration, eventuallyInterval).Should(Succeed())
			}

			// Delete the CRP.
			Eventually(func() error {
				if err := hubClient.Delete(ctx, crp); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete CRP %s: %w", crp.Name, err)
				}
				if err := hubClient.Get(ctx, types.NamespacedName{Name: crp.Name}, crp); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get CRP %s after deletion: %w", crp.Name, err)
				}
				return fmt.Errorf("CRP %s still exists", crp.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())

			// Delete the RP.
			Eventually(func() error {
				if err := hubClient.Delete(ctx, rp); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("failed to delete RP %s/%s: %w", rp.Namespace, rp.Name, err)
				}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: rp.Namespace, Name: rp.Name}, rp); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return fmt.Errorf("failed to get RP %s/%s after deletion: %w", rp.Namespace, rp.Name, err)
				}
				return fmt.Errorf("RP %s/%s still exists", rp.Namespace, rp.Name)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed())
			// The environment prepared by the envtest package does not support namespace
			// deletion; consequently this test suite would not attempt to verify its deletion.
		})
	})
})
