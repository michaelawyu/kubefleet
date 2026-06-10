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

package tests

import (
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	placementv1beta1 "github.com/kubefleet-dev/kubefleet/apis/placement/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/resource"
)

const (
	eventuallyInterval = 500 * time.Millisecond
	eventuallyDuration = 10 * time.Second
)

var _ = Describe("integrated", func() {
	Context("single placement", Ordered, func() {
		BeforeAll(func() {
			By("creating a Deployment in the work namespace")
			deploy := &appsv1.Deployment{
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
			Expect(hubClient.Create(ctx, deploy)).To(Succeed())

			By("creating member cluster cluster-1 in the eastus region")
			mc := &clusterv1beta1.MemberCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster-1",
					Labels: map[string]string{
						"topology.kubernetes.io/region": "eastus",
					},
				},
				Spec: clusterv1beta1.MemberClusterSpec{
					Identity: rbacv1.Subject{
						Kind: rbacv1.ServiceAccountKind,
						Name: "hub-access",
					},
				},
			}
			Expect(hubClient.Create(ctx, mc)).To(Succeed())

			By("annotating the Deployment with place-to-regions=eastus")
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app"}, deploy)).To(Succeed())
			updatedDeploy := deploy.DeepCopy()
			if updatedDeploy.Annotations == nil {
				updatedDeploy.Annotations = map[string]string{}
			}
			updatedDeploy.Annotations["experimental.kubefleet.dev/place-to-regions"] = "eastus"
			Expect(hubClient.Update(ctx, updatedDeploy)).To(Succeed())

			By("waiting for the WorkloadPlacement to be created and labelling it with foo=bar")
			placement := &experimentalv1beta1.WorkloadPlacement{}
			Eventually(func() error {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app"}, placement); err != nil {
					return err
				}
				if placement.Labels == nil {
					placement.Labels = map[string]string{}
				}
				placement.Labels["foo"] = "bar"
				return hubClient.Update(ctx, placement)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"WorkloadPlacement should be created and labelled with foo=bar")
		})

		It("should create a WorkloadPlacement for the deployment", func() {
			wantPlacement := &experimentalv1beta1.WorkloadPlacement{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app",
					Namespace: workNSName,
					Labels: map[string]string{
						"foo": "bar",
					},
				},
				Spec: experimentalv1beta1.WorkloadPlacementSpec{
					ClusterSelectors: []map[string]string{
						{"topology.kubernetes.io/region": "eastus"},
					},
					WorkloadRef: experimentalv1beta1.SameNamespacedObjectReference{
						Kind:       "Deployment",
						APIGroup:   "apps",
						APIVersion: "v1",
						Resource:   "deployments",
						Name:       "app",
					},
				},
			}

			placement := &experimentalv1beta1.WorkloadPlacement{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app"}, placement); err != nil {
					return err.Error()
				}
				return cmp.Diff(placement, wantPlacement,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Finalizers"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadPlacementSpec{}, "AdditionalResourceRefs", "RevisionHistoryLimit"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadPlacementStatus{}, "Conditions", "LatestResourceSnapshotRevisionName", "BindingManagers"),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"WorkloadPlacement should be created with the correct spec")
		})

		It("should create a binding for cluster-1", func() {
			regionHash, err := resource.HashOf(map[string]string{"topology.kubernetes.io/region": "eastus"})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []experimentalv1beta1.WorkloadResourceClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "app-cluster-1",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "app",
						},
						Annotations: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingSelectorHashAnnotationKey: regionHash,
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "app",
						ClusterSelectorHash:   regionHash,
						MemberClusterName:     ptr.To("cluster-1"),
					},
				},
			}

			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, bindingList,
					client.InNamespace(workNSName),
					client.MatchingLabels{experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "app"},
				); err != nil {
					return err.Error()
				}
				return cmp.Diff(bindingList.Items, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Finalizers"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingStatus{}, "Conditions"),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"exactly one binding for cluster-1 should be created")

			if diff := cmp.Diff(bindingList.Items, wantBindings,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Finalizers"),
				cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
				cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingStatus{}, "Conditions"),
			); diff != "" {
				Fail(fmt.Sprintf("binding list mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should create a Work object in fleet-member-cluster-1", func() {
			wantWork := placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-cluster-1-0",
					Namespace: "fleet-member-cluster-1",
					Labels: map[string]string{
						experimentalv1beta1.WorkOwnedByClusterBindingLabelKey: "app-cluster-1",
						experimentalv1beta1.WorkOwnerNamespaceLabelKey:        workNSName,
						experimentalv1beta1.WorkOwnedByPlacementLabelKey:      "app",
					},
				},
			}

			workList := &placementv1beta1.WorkList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, workList,
					client.InNamespace("fleet-member-cluster-1"),
					client.MatchingLabels{experimentalv1beta1.WorkOwnedByPlacementLabelKey: "app"},
				); err != nil {
					return err.Error()
				}
				return cmp.Diff(workList.Items, []placementv1beta1.Work{wantWork},
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations"),
					cmpopts.IgnoreFields(placementv1beta1.WorkSpec{}, "Workload"),
					cmpopts.IgnoreFields(placementv1beta1.WorkStatus{}, "Conditions", "ManifestConditions"),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"a Work object should be created in fleet-member-cluster-1")
		})

		It("should mark the Work object as Applied and Available", func() {
			work := &placementv1beta1.Work{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: "fleet-member-cluster-1", Name: "app-cluster-1-0"}, work)).To(Succeed())

			updatedWork := work.DeepCopy()
			updatedWork.Status.Conditions = []metav1.Condition{
				{
					Type:               string(placementv1beta1.WorkConditionTypeApplied),
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsApplied",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               string(placementv1beta1.WorkConditionTypeAvailable),
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsAvailable",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedWork)).To(Succeed())
		})

		It("should reflect Synchronized=True and AllResourcesAvailable=True on the binding", func() {
			wantStatus := experimentalv1beta1.WorkloadResourceClusterBindingStatus{
				Conditions: []metav1.Condition{
					{
						Type:   experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized,
						Status: metav1.ConditionTrue,
						Reason: "AllResourcesApplied",
					},
					{
						Type:   experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable,
						Status: metav1.ConditionTrue,
						Reason: "AllResourcesAvailable",
					},
				},
			}

			binding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app-cluster-1"}, binding); err != nil {
					return err.Error()
				}
				return cmp.Diff(binding.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"binding should reflect Synchronized=True and AllResourcesAvailable=True")
		})

		It("should reflect Synchronized=True and AllResourcesAvailable=True on the WorkloadPlacement status", func() {
			wantStatus := experimentalv1beta1.WorkloadPlacementStatus{
				Conditions: []metav1.Condition{
					{
						Type:   experimentalv1beta1.WorkloadPlacementCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   experimentalv1beta1.WorkloadPlacementCondTypeSynchronized,
						Status: metav1.ConditionTrue,
						Reason: "AllBindingsHaveUpToDateSnapshot",
					},
					{
						Type:   experimentalv1beta1.WorkloadPlacementCondTypeAllResourcesAvailable,
						Status: metav1.ConditionTrue,
						Reason: "AllBindingsHaveResourcesAvailable",
					},
				},
			}

			placement := &experimentalv1beta1.WorkloadPlacement{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app"}, placement); err != nil {
					return err.Error()
				}
				return cmp.Diff(placement.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadPlacementStatus{}, "LatestResourceSnapshotRevisionName", "BindingManagers"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"WorkloadPlacement status should reflect Synchronized=True and AllResourcesAvailable=True")
		})

		It("can create a migration request from eastus to westus2", func() {
			By("creating a WorkloadMigrationRequest targeting placements with foo=bar")
			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "eastus-to-westus2",
				},
				Spec: experimentalv1beta1.WorkloadMigrationRequestSpec{
					WorkloadPlacementSelectors: []map[string]string{
						{"foo": "bar"},
					},
					FromClusterSelector: map[string]string{
						"topology.kubernetes.io/region": "eastus",
					},
					ToClusterSelector: map[string]string{
						"topology.kubernetes.io/region": "westus2",
					},
					FailurePolicy: experimentalv1beta1.WorkloadMigrationFailurePolicy{
						MaxFailureCount: 1,
					},
				},
			}
			Expect(hubClient.Create(ctx, migrationReq)).To(Succeed())
		})

		It("should initialize the migration request with one migration attempt", func() {
			wantStatus := experimentalv1beta1.WorkloadMigrationRequestStatus{
				Conditions: []metav1.Condition{
					{
						Type:   experimentalv1beta1.WorkloadMigrationRequestCondTypeInitialized,
						Status: metav1.ConditionTrue,
						Reason: "CalculatedAllMigrationAttempts",
					},
				},
				MigrationAttempts: []experimentalv1beta1.WorkloadMigrationAttempt{
					{
						WorkloadResourceBindingRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "app-cluster-1",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadResourceClusterBinding",
							Resource:   "workloadresourceclusterbindings",
						},
						WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "app",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadPlacement",
							Resource:   "workloadplacements",
						},
						FromClusterName:      "cluster-1",
						ToClusterRequestName: ptr.To("eastus-to-westus2-cluster-1-replacement"),
					},
				},
			}

			req := &experimentalv1beta1.WorkloadMigrationRequest{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "eastus-to-westus2"}, req); err != nil {
					return err.Error()
				}
				return cmp.Diff(req.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadMigrationAttempt{}, "Conditions", "ToClusterName"),
					cmpopts.IgnoreSliceElements(func(c metav1.Condition) bool {
						return c.Type != experimentalv1beta1.WorkloadMigrationRequestCondTypeInitialized
					}),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"migration request should be initialized with one attempt for cluster-1")
		})

		It("should create a cluster request for the westus2 region", func() {
			wantReq := &experimentalv1beta1.ClusterRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "eastus-to-westus2-cluster-1-replacement",
				},
				Spec: experimentalv1beta1.ClusterRequestSpec{
					ClusterSelector: map[string]string{
						"topology.kubernetes.io/region": "westus2",
					},
				},
			}

			clusterReq := &experimentalv1beta1.ClusterRequest{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Name: "eastus-to-westus2-cluster-1-replacement"}, clusterReq)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"cluster request should be created for the westus2 region")

			if diff := cmp.Diff(clusterReq, wantReq,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(experimentalv1beta1.ClusterRequestStatus{}, "Conditions", "LatestObservedClusterCreationTimestamp", "ProvisionedClusterName"),
			); diff != "" {
				Fail(fmt.Sprintf("cluster request mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should create cluster-2 in the westus2 region", func() {
			mc := &clusterv1beta1.MemberCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cluster-2",
					Labels: map[string]string{
						"topology.kubernetes.io/region": "westus2",
					},
				},
				Spec: clusterv1beta1.MemberClusterSpec{
					Identity: rbacv1.Subject{
						Kind: rbacv1.ServiceAccountKind,
						Name: "hub-access",
					},
				},
			}
			Expect(hubClient.Create(ctx, mc)).To(Succeed())
		})

		It("should mark the cluster request as completed with cluster-2 as the provisioned cluster", func() {
			clusterReq := &experimentalv1beta1.ClusterRequest{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "eastus-to-westus2-cluster-1-replacement"}, clusterReq)).To(Succeed())

			updated := clusterReq.DeepCopy()
			updated.Status.ProvisionedClusterName = ptr.To("cluster-2")
			updated.Status.Conditions = []metav1.Condition{
				{
					Type:               experimentalv1beta1.ClusterRequestCondTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             "ClusterProvisioned",
					ObservedGeneration: clusterReq.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updated)).To(Succeed())
		})

		It("should have 2 bindings: original and the new to-cluster binding for cluster-2", func() {
			regionHash, err := resource.HashOf(map[string]string{"topology.kubernetes.io/region": "eastus"})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []experimentalv1beta1.WorkloadResourceClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "app-cluster-1",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "app",
						},
						Annotations: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingSelectorHashAnnotationKey: regionHash,
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "app",
						ClusterSelectorHash:   regionHash,
						MemberClusterName:     ptr.To("cluster-1"),
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "app-cluster-2-migrated",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey:      "app",
							experimentalv1beta1.WorkloadResourceClusterBindingCreatedInPlaceForKey: "app-cluster-1",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "app",
						ClusterSelectorHash:   regionHash,
						MemberClusterName:     ptr.To("cluster-2"),
					},
				},
			}

			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, bindingList,
					client.InNamespace(workNSName),
					client.MatchingLabels{experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "app"},
				); err != nil {
					return err.Error()
				}
				return cmp.Diff(bindingList.Items, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Finalizers"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingStatus{}, "Conditions"),
					cmpopts.SortSlices(func(a, b experimentalv1beta1.WorkloadResourceClusterBinding) bool { return a.Name < b.Name }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"should have exactly 2 bindings: app-cluster-1 and app-cluster-2-migrated")
		})

		It("should create a Work object in fleet-member-cluster-2", func() {
			wantWork := placementv1beta1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-cluster-2-migrated-0",
					Namespace: "fleet-member-cluster-2",
					Labels: map[string]string{
						experimentalv1beta1.WorkOwnedByClusterBindingLabelKey: "app-cluster-2-migrated",
						experimentalv1beta1.WorkOwnerNamespaceLabelKey:        workNSName,
						experimentalv1beta1.WorkOwnedByPlacementLabelKey:      "app",
					},
				},
			}

			workList := &placementv1beta1.WorkList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, workList,
					client.InNamespace("fleet-member-cluster-2"),
					client.MatchingLabels{experimentalv1beta1.WorkOwnedByPlacementLabelKey: "app"},
				); err != nil {
					return err.Error()
				}
				return cmp.Diff(workList.Items, []placementv1beta1.Work{wantWork},
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations"),
					cmpopts.IgnoreFields(placementv1beta1.WorkSpec{}, "Workload"),
					cmpopts.IgnoreFields(placementv1beta1.WorkStatus{}, "Conditions", "ManifestConditions"),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"a Work object should be created in fleet-member-cluster-2")
		})

		It("should mark the cluster-2 Work object as Applied and Available", func() {
			work := &placementv1beta1.Work{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: "fleet-member-cluster-2", Name: "app-cluster-2-migrated-0"}, work)).To(Succeed())

			updatedWork := work.DeepCopy()
			updatedWork.Status.Conditions = []metav1.Condition{
				{
					Type:               string(placementv1beta1.WorkConditionTypeApplied),
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsApplied",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               string(placementv1beta1.WorkConditionTypeAvailable),
					Status:             metav1.ConditionTrue,
					Reason:             "AllManifestsAvailable",
					ObservedGeneration: work.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedWork)).To(Succeed())
		})

		It("should show the new binding as synced/available and the old binding as suspended with no finalizer", func() {
			regionHash, err := resource.HashOf(map[string]string{"topology.kubernetes.io/region": "eastus"})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []experimentalv1beta1.WorkloadResourceClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "app-cluster-1",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "app",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "app",
						ClusterSelectorHash:   regionHash,
						MemberClusterName:     ptr.To("cluster-1"),
						Suspended:             true,
					},
					Status: experimentalv1beta1.WorkloadResourceClusterBindingStatus{
						Conditions: []metav1.Condition{
							{
								Type:   experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized,
								Status: metav1.ConditionTrue,
								Reason: "AllResourcesApplied",
							},
							{
								Type:   experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable,
								Status: metav1.ConditionTrue,
								Reason: "AllResourcesAvailable",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "app-cluster-2-migrated",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey:      "app",
							experimentalv1beta1.WorkloadResourceClusterBindingCreatedInPlaceForKey: "app-cluster-1",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "app",
						ClusterSelectorHash:   regionHash,
						MemberClusterName:     ptr.To("cluster-2"),
						Suspended:             false,
					},
					Status: experimentalv1beta1.WorkloadResourceClusterBindingStatus{
						Conditions: []metav1.Condition{
							{
								Type:   experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized,
								Status: metav1.ConditionTrue,
								Reason: "AllResourcesApplied",
							},
							{
								Type:   experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable,
								Status: metav1.ConditionTrue,
								Reason: "AllResourcesAvailable",
							},
						},
					},
				},
			}

			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, bindingList,
					client.InNamespace(workNSName),
					client.MatchingLabels{experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "app"},
				); err != nil {
					return err.Error()
				}
				return cmp.Diff(bindingList.Items, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations", "Finalizers"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b experimentalv1beta1.WorkloadResourceClusterBinding) bool { return a.Name < b.Name }),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"app-cluster-2-migrated should be synced/available and app-cluster-1 should be suspended with no finalizer")
		})

		It("should have no Work object in fleet-member-cluster-1 after suspension", func() {
			workList := &placementv1beta1.WorkList{}
			Eventually(func() (int, error) {
				if err := hubClient.List(ctx, workList,
					client.InNamespace("fleet-member-cluster-1"),
					client.MatchingLabels{experimentalv1beta1.WorkOwnedByPlacementLabelKey: "app"},
				); err != nil {
					return 0, err
				}
				return len(workList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(),
				"no Work objects should remain in fleet-member-cluster-1 after the binding is suspended")
		})

		It("should report the migration request as completed", func() {
			wantStatus := experimentalv1beta1.WorkloadMigrationRequestStatus{
				Conditions: []metav1.Condition{
					{
						Type:   experimentalv1beta1.WorkloadMigrationRequestCondTypeInitialized,
						Status: metav1.ConditionTrue,
						Reason: "CalculatedAllMigrationAttempts",
					},
					{
						Type:   experimentalv1beta1.WorkloadMigrationRequestCondTypeCompleted,
						Status: metav1.ConditionTrue,
						Reason: experimentalv1beta1.WorkloadMigrationRequestCompletedCondReasonSucceeded,
					},
				},
				MigrationAttempts: []experimentalv1beta1.WorkloadMigrationAttempt{
					{
						WorkloadResourceBindingRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "app-cluster-1",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadResourceClusterBinding",
							Resource:   "workloadresourceclusterbindings",
						},
						WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "app",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadPlacement",
							Resource:   "workloadplacements",
						},
						FromClusterName:      "cluster-1",
						ToClusterRequestName: ptr.To("eastus-to-westus2-cluster-1-replacement"),
						Conditions: []metav1.Condition{
							{
								Type:   experimentalv1beta1.WorkloadMigrationAttemptCondTypeCompleted,
								Status: metav1.ConditionTrue,
								Reason: experimentalv1beta1.WorkloadMigrationAttemptCompletedCondReasonSucceeded,
							},
						},
					},
				},
			}

			req := &experimentalv1beta1.WorkloadMigrationRequest{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "eastus-to-westus2"}, req); err != nil {
					return err.Error()
				}
				return cmp.Diff(req.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"migration request should be completed successfully")
		})

		It("should commit the migration: delete the request and verify only the promoted to-binding remains", func() {
			By("deleting the migration request")
			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "eastus-to-westus2"}, migrationReq)).To(Succeed())
			Expect(hubClient.Delete(ctx, migrationReq)).To(Succeed())

			By("waiting for the migration request to disappear")
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "eastus-to-westus2"}, migrationReq)
				return client.IgnoreNotFound(err)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"migration request should be fully removed")

			By("verifying only the promoted to-cluster binding remains")
			regionHash, err := resource.HashOf(map[string]string{"topology.kubernetes.io/region": "eastus"})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []experimentalv1beta1.WorkloadResourceClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "app-cluster-2-migrated",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "app",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "app",
						ClusterSelectorHash:   regionHash,
						MemberClusterName:     ptr.To("cluster-2"),
						Suspended:             false,
					},
				},
			}

			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, bindingList,
					client.InNamespace(workNSName),
					client.MatchingLabels{experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "app"},
				); err != nil {
					return err.Error()
				}
				return cmp.Diff(bindingList.Items, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations", "Finalizers"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingStatus{}, "Conditions"),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"only the promoted app-cluster-2-migrated binding should remain after commit")
		})

		It("should update the WorkloadPlacement status to reflect the migrated state", func() {
			wantStatus := experimentalv1beta1.WorkloadPlacementStatus{
				Conditions: []metav1.Condition{
					{
						Type:   experimentalv1beta1.WorkloadPlacementCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   experimentalv1beta1.WorkloadPlacementCondTypeSynchronized,
						Status: metav1.ConditionTrue,
						Reason: "AllBindingsHaveUpToDateSnapshot",
					},
					{
						Type:   experimentalv1beta1.WorkloadPlacementCondTypeAllResourcesAvailable,
						Status: metav1.ConditionTrue,
						Reason: "AllBindingsHaveResourcesAvailable",
					},
				},
			}

			placement := &experimentalv1beta1.WorkloadPlacement{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app"}, placement); err != nil {
					return err.Error()
				}
				return cmp.Diff(placement.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadPlacementStatus{}, "LatestResourceSnapshotRevisionName", "BindingManagers"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"WorkloadPlacement status should reflect the migrated state")
		})

		AfterAll(func() {
			By("deleting all bindings in the work namespace")
			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Expect(hubClient.List(ctx, bindingList, client.InNamespace(workNSName))).To(Succeed())
			for i := range bindingList.Items {
				b := &bindingList.Items[i]
				if len(b.Finalizers) > 0 {
					updated := b.DeepCopy()
					updated.Finalizers = nil
					Expect(client.IgnoreNotFound(hubClient.Update(ctx, updated))).To(Succeed())
				}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, b))).To(Succeed())
			}
			Eventually(func() (int, error) {
				if err := hubClient.List(ctx, bindingList, client.InNamespace(workNSName)); err != nil {
					return 0, err
				}
				return len(bindingList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all bindings should be removed")

			By("deleting all Work objects in member cluster namespaces")
			for _, ns := range []string{"fleet-member-cluster-1", "fleet-member-cluster-2"} {
				workList := &placementv1beta1.WorkList{}
				Expect(hubClient.List(ctx, workList, client.InNamespace(ns))).To(Succeed())
				for i := range workList.Items {
					Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &workList.Items[i]))).To(Succeed())
				}
			}

			By("deleting the WorkloadPlacement")
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &experimentalv1beta1.WorkloadPlacement{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: workNSName},
			}))).To(Succeed())
			placement := &experimentalv1beta1.WorkloadPlacement{}
			Eventually(func() error {
				return client.IgnoreNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app"}, placement))
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "WorkloadPlacement should be removed")

			By("deleting the Deployment")
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: workNSName},
			}))).To(Succeed())

			By("deleting the member clusters")
			for _, name := range []string{"cluster-1", "cluster-2"} {
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &clusterv1beta1.MemberCluster{
					ObjectMeta: metav1.ObjectMeta{Name: name},
				}))).To(Succeed())
				mc := &clusterv1beta1.MemberCluster{}
				Eventually(func() error {
					return client.IgnoreNotFound(hubClient.Get(ctx, types.NamespacedName{Name: name}, mc))
				}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "MemberCluster "+name+" should be removed")
			}

			By("deleting the migration request if it still exists")
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &experimentalv1beta1.WorkloadMigrationRequest{
				ObjectMeta: metav1.ObjectMeta{Name: "eastus-to-westus2"},
			}))).To(Succeed())

			By("deleting the cluster request if it still exists")
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &experimentalv1beta1.ClusterRequest{
				ObjectMeta: metav1.ObjectMeta{Name: "eastus-to-westus2-cluster-1-replacement"},
			}))).To(Succeed())
		})
	})
})
