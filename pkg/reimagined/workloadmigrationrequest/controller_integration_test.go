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

package workloadmigrationrequest

import (
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/resource"
)

const (
	// Eventually polling interval and timeout.
	eventuallyInterval = 500 * time.Millisecond
	eventuallyDuration = 10 * time.Second

	// The cleanup finalizer added to bindings by the workload resource cluster binding controller.
	bindingCleanupFinalizer = "experimental.kubefleet.dev/workload-resource-cluster-binding-cleanup"
)

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

var _ = Describe("workload migration request ops", func() {
	Context("single placement, migrate from useast to uscentral", Ordered, func() {
		BeforeAll(func() {
			By("creating 2 member clusters in useast and uscentral regions")
			clusters := []struct {
				name   string
				region string
			}{
				{"useast", "useast"},
				{"uscentral", "uscentral"},
				{"uswest", "uswest"},
			}
			for _, c := range clusters {
				mc := &clusterv1beta1.MemberCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: c.name,
						Labels: map[string]string{
							"region": c.region,
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
			}

			By("creating a WorkloadPlacement in the work namespace targeting both useast and uscentral")
			placement := &experimentalv1beta1.WorkloadPlacement{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-placement",
					Namespace: workNSName,
					Labels: map[string]string{
						"foo": "bar",
					},
				},
				Spec: experimentalv1beta1.WorkloadPlacementSpec{
					ClusterSelectors: []map[string]string{
						{"region": "useast"},
						{"region": "uscentral"},
					},
					WorkloadRef: experimentalv1beta1.SameNamespacedObjectReference{
						Name:       "app",
						APIVersion: "apps/v1",
						Kind:       "Deployment",
						Resource:   "deployments",
					},
				},
			}
			Expect(hubClient.Create(ctx, placement)).To(Succeed())

			useastHash, err := resource.HashOf(map[string]string{"region": "useast"})
			Expect(err).NotTo(HaveOccurred())
			uscentralHash, err := resource.HashOf(map[string]string{"region": "uscentral"})
			Expect(err).NotTo(HaveOccurred())

			By("creating a binding that associates the placement with the useast cluster")
			useastBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-placement-useast",
					Namespace: workNSName,
					Finalizers: []string{
						bindingCleanupFinalizer,
					},
					Labels: map[string]string{
						experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement",
					},
					Annotations: map[string]string{
						experimentalv1beta1.WorkloadResourceClusterBindingSelectorHashAnnotationKey: useastHash,
					},
				},
				Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
					WorkloadPlacementName: "my-placement",
					ClusterSelectorHash:   useastHash,
					MemberClusterName:     ptr.To("useast"),
				},
			}
			Expect(hubClient.Create(ctx, useastBinding)).To(Succeed())

			By("creating a binding that associates the placement with the uscentral cluster")
			uscentralBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-placement-uscentral",
					Namespace: workNSName,
					Finalizers: []string{
						bindingCleanupFinalizer,
					},
					Labels: map[string]string{
						experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement",
					},
					Annotations: map[string]string{
						experimentalv1beta1.WorkloadResourceClusterBindingSelectorHashAnnotationKey: uscentralHash,
					},
				},
				Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
					WorkloadPlacementName: "my-placement",
					ClusterSelectorHash:   uscentralHash,
					MemberClusterName:     ptr.To("uscentral"),
				},
			}
			Expect(hubClient.Create(ctx, uscentralBinding)).To(Succeed())
		})

		It("should create a workload migration request that migrates placements with foo=bar from useast to uswest", func() {
			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-migration",
				},
				Spec: experimentalv1beta1.WorkloadMigrationRequestSpec{
					WorkloadPlacementSelectors: []map[string]string{
						{"foo": "bar"},
					},
					FromClusterSelector: map[string]string{
						"region": "useast",
					},
					ToClusterSelector: map[string]string{
						"region": "uswest",
					},
					FailurePolicy: experimentalv1beta1.WorkloadMigrationFailurePolicy{
						MaxFailureCount: 1,
					},
				},
			}
			Expect(hubClient.Create(ctx, migrationReq)).To(Succeed())
		})

		It("should initialize the migration request with the correct migration attempts", func() {
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
							Name:       "my-placement-useast",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadResourceClusterBinding",
							Resource:   "workloadresourceclusterbindings",
						},
						WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadPlacement",
							Resource:   "workloadplacements",
						},
						FromClusterName: "useast",
						ToClusterName:   ptr.To("uswest"),
					},
				},
			}

			By("waiting for the migration request to be initialized")
			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-migration"}, migrationReq); err != nil {
					return err.Error()
				}
				return cmp.Diff(migrationReq.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadMigrationAttempt{}, "Conditions", "ToClusterRequestName"),
					cmpopts.IgnoreSliceElements(func(c metav1.Condition) bool {
						return c.Type != experimentalv1beta1.WorkloadMigrationRequestCondTypeInitialized
					}),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"migration request should be initialized with the correct migration attempts")
		})
		It("should create a new binding on the uswest cluster as part of the migration", func() {
			useastHash, err := resource.HashOf(map[string]string{"region": "useast"})
			Expect(err).NotTo(HaveOccurred())

			wantBinding := experimentalv1beta1.WorkloadResourceClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-placement-uswest-migrated",
					Namespace: workNSName,
					Labels: map[string]string{
						experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey:      "my-placement",
						experimentalv1beta1.WorkloadResourceClusterBindingCreatedInPlaceForKey: "my-placement-useast",
					},
				},
				Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
					WorkloadPlacementName: "my-placement",
					ClusterSelectorHash:   useastHash,
					MemberClusterName:     ptr.To("uswest"),
				},
			}

			By("waiting for the to-cluster binding to be created")
			binding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-uswest-migrated"}, binding)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"to-cluster binding should be created on uswest")

			By("verifying the to-cluster binding matches the expected state")
			if diff := cmp.Diff(*binding, wantBinding,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingStatus{}, "Conditions"),
				cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
			); diff != "" {
				Fail("to-cluster binding mismatch (-got, +want):\n" + diff)
			}
		})

		It("should not suspend the from-cluster binding before the to-cluster binding is available", func() {
			fromBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Consistently(func() (bool, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-useast"}, fromBinding); err != nil {
					return false, err
				}
				return fromBinding.Spec.Suspended, nil
			}, eventuallyDuration, eventuallyInterval).Should(BeFalse(),
				"from-cluster binding should not be suspended before the to-cluster binding is available")
		})

		It("should mark the to-cluster binding as Synchronized and AllResourcesAvailable", func() {
			By("fetching the to-cluster binding")
			toBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-uswest-migrated"}, toBinding)).To(Succeed())

			By("patching the to-cluster binding status")
			updatedBinding := toBinding.DeepCopy()
			updatedBinding.Status.Conditions = []metav1.Condition{
				{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesApplied",
					ObservedGeneration: toBinding.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesAvailable",
					ObservedGeneration: toBinding.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedBinding)).To(Succeed())
		})

		It("should suspend the from-cluster binding after the to-cluster binding is available", func() {
			fromBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Eventually(func() (bool, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-useast"}, fromBinding); err != nil {
					return false, err
				}
				return fromBinding.Spec.Suspended, nil
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(),
				"from-cluster binding should be suspended once the to-cluster binding is available")
		})

		It("should remove the finalizer from the from-cluster binding", func() {
			fromBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-useast"}, fromBinding)).To(Succeed())

			updatedBinding := fromBinding.DeepCopy()
			updatedBinding.Finalizers = nil
			Expect(hubClient.Update(ctx, updatedBinding)).To(Succeed())
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
							Name:       "my-placement-useast",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadResourceClusterBinding",
							Resource:   "workloadresourceclusterbindings",
						},
						WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadPlacement",
							Resource:   "workloadplacements",
						},
						FromClusterName: "useast",
						ToClusterName:   ptr.To("uswest"),
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

			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-migration"}, migrationReq); err != nil {
					return err.Error()
				}
				return cmp.Diff(migrationReq.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"migration request should be completed successfully")
		})

		It("should set the migration request to roll back", func() {
			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "my-migration"}, migrationReq)).To(Succeed())

			updatedReq := migrationReq.DeepCopy()
			updatedReq.Spec.Rollback = true
			Expect(hubClient.Update(ctx, updatedReq)).To(Succeed())
		})

		It("should unsuspend the from-cluster binding as part of the rollback", func() {
			fromBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Eventually(func() (bool, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-useast"}, fromBinding); err != nil {
					return false, err
				}
				return fromBinding.Spec.Suspended, nil
			}, eventuallyDuration, eventuallyInterval).Should(BeFalse(),
				"from-cluster binding should be unsuspended during rollback")
		})

		It("should mark the from-cluster binding as Synchronized and AllResourcesAvailable", func() {
			By("fetching the from-cluster binding")
			fromBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-useast"}, fromBinding)).To(Succeed())

			By("adding the cleanup finalizer back to the from-cluster binding")
			updatedBinding := fromBinding.DeepCopy()
			if !containsString(updatedBinding.Finalizers, bindingCleanupFinalizer) {
				updatedBinding.Finalizers = append(updatedBinding.Finalizers, bindingCleanupFinalizer)
				Expect(hubClient.Update(ctx, updatedBinding)).To(Succeed())
				Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-useast"}, fromBinding)).To(Succeed())
			}

			By("patching the from-cluster binding status")
			updatedBinding = fromBinding.DeepCopy()
			updatedBinding.Status.Conditions = []metav1.Condition{
				{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesApplied",
					ObservedGeneration: fromBinding.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesAvailable",
					ObservedGeneration: fromBinding.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedBinding)).To(Succeed())
		})

		It("should suspend the to-cluster binding as part of the rollback drain", func() {
			toBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Eventually(func() (bool, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-uswest-migrated"}, toBinding); err != nil {
					return false, err
				}
				return toBinding.Spec.Suspended, nil
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(),
				"to-cluster binding should be suspended during rollback drain")
		})

		It("should remove the finalizer from the to-cluster binding", func() {
			toBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-uswest-migrated"}, toBinding)).To(Succeed())

			updatedBinding := toBinding.DeepCopy()
			updatedBinding.Finalizers = nil
			Expect(hubClient.Update(ctx, updatedBinding)).To(Succeed())
		})

		It("should report the migration request as rolled back", func() {
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
					{
						Type:   experimentalv1beta1.WorkloadMigrationRequestCondTypeRolledBack,
						Status: metav1.ConditionTrue,
						Reason: experimentalv1beta1.WorkloadMigrationRequestRolledBackCondReasonSucceeded,
					},
				},
				MigrationAttempts: []experimentalv1beta1.WorkloadMigrationAttempt{
					{
						WorkloadResourceBindingRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement-useast",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadResourceClusterBinding",
							Resource:   "workloadresourceclusterbindings",
						},
						WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadPlacement",
							Resource:   "workloadplacements",
						},
						FromClusterName: "useast",
						ToClusterName:   ptr.To("uswest"),
						Conditions: []metav1.Condition{
							{
								Type:   experimentalv1beta1.WorkloadMigrationAttemptCondTypeCompleted,
								Status: metav1.ConditionTrue,
								Reason: experimentalv1beta1.WorkloadMigrationAttemptCompletedCondReasonSucceeded,
							},
							{
								Type:   experimentalv1beta1.WorkloadMigrationAttemptCondTypeRolledBack,
								Status: metav1.ConditionTrue,
								Reason: experimentalv1beta1.WorkloadMigrationAttemptRolledBackCondReasonSucceeded,
							},
						},
					},
				},
			}

			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-migration"}, migrationReq); err != nil {
					return err.Error()
				}
				return cmp.Diff(migrationReq.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"migration request should be fully rolled back")
		})

		It("should delete the migration request and wait for it to disappear", func() {
			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "my-migration"}, migrationReq)).To(Succeed())
			Expect(hubClient.Delete(ctx, migrationReq)).To(Succeed())

			By("waiting for the migration request to be fully removed")
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "my-migration"}, migrationReq)
				return client.IgnoreNotFound(err)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"migration request should be fully removed")
		})

		It("should reflect the completed migration in the binding state", func() {
			useastHash, err := resource.HashOf(map[string]string{"region": "useast"})
			Expect(err).NotTo(HaveOccurred())
			uscentralHash, err := resource.HashOf(map[string]string{"region": "uscentral"})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []experimentalv1beta1.WorkloadResourceClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-useast",
						Namespace: workNSName,
						Finalizers: []string{
							bindingCleanupFinalizer,
						},
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement",
						},
						Annotations: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingSelectorHashAnnotationKey: useastHash,
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("useast"),
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
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-uscentral",
						Namespace: workNSName,
						Finalizers: []string{
							bindingCleanupFinalizer,
						},
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement",
						},
						Annotations: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingSelectorHashAnnotationKey: uscentralHash,
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement",
						ClusterSelectorHash:   uscentralHash,
						MemberClusterName:     ptr.To("uscentral"),
						Suspended:             false,
					},
				},
			}

			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, bindingList, client.InNamespace(workNSName)); err != nil {
					return err.Error()
				}
				return cmp.Diff(bindingList.Items, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
					cmpopts.SortSlices(func(a, b experimentalv1beta1.WorkloadResourceClusterBinding) bool { return a.Name < b.Name }),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"binding state should reflect the completed and rolled-back migration")
		})

		AfterAll(func() {
			By("deleting all bindings in the work namespace and stripping their finalizers")
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
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(),
				"all bindings should be removed")

			By("deleting the WorkloadPlacement")
			placement := &experimentalv1beta1.WorkloadPlacement{}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &experimentalv1beta1.WorkloadPlacement{
				ObjectMeta: metav1.ObjectMeta{Name: "my-placement", Namespace: workNSName},
			}))).To(Succeed())
			Eventually(func() error {
				return client.IgnoreNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement"}, placement))
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"WorkloadPlacement should be removed")

			By("deleting the member clusters")
			for _, name := range []string{"useast", "uscentral", "uswest"} {
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &clusterv1beta1.MemberCluster{
					ObjectMeta: metav1.ObjectMeta{Name: name},
				}))).To(Succeed())
				mc := &clusterv1beta1.MemberCluster{}
				Eventually(func() error {
					return client.IgnoreNotFound(hubClient.Get(ctx, types.NamespacedName{Name: name}, mc))
				}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
					"MemberCluster "+name+" should be removed")
			}
		})
	})

	Context("multiple placements, migrate from useast to uscentral", Ordered, func() {
		BeforeAll(func() {
			By("creating a member cluster in the useast region")
			mc := &clusterv1beta1.MemberCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "useast",
					Labels: map[string]string{
						"region": "useast",
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

			useastHash, err := resource.HashOf(map[string]string{"region": "useast"})
			Expect(err).NotTo(HaveOccurred())

			placements := []string{"my-placement-a", "my-placement-b"}
			for _, name := range placements {
				By("creating WorkloadPlacement " + name)
				placement := &experimentalv1beta1.WorkloadPlacement{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: workNSName,
						Labels: map[string]string{
							"foo": "bar",
						},
					},
					Spec: experimentalv1beta1.WorkloadPlacementSpec{
						ClusterSelectors: []map[string]string{
							{"region": "useast"},
						},
						WorkloadRef: experimentalv1beta1.SameNamespacedObjectReference{
							Name:       name,
							APIVersion: "apps/v1",
							Kind:       "Deployment",
							Resource:   "deployments",
						},
					},
				}
				Expect(hubClient.Create(ctx, placement)).To(Succeed())

				By("creating a binding for " + name + " on the useast cluster")
				binding := &experimentalv1beta1.WorkloadResourceClusterBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name + "-useast",
						Namespace: workNSName,
						Finalizers: []string{
							bindingCleanupFinalizer,
						},
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: name,
						},
						Annotations: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingSelectorHashAnnotationKey: useastHash,
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: name,
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("useast"),
					},
				}
				Expect(hubClient.Create(ctx, binding)).To(Succeed())
			}
		})

		It("should create a migration request that moves all workloads from useast to uscentral", func() {
			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-migration-2",
				},
				Spec: experimentalv1beta1.WorkloadMigrationRequestSpec{
					FromClusterSelector: map[string]string{
						"region": "useast",
					},
					ToClusterSelector: map[string]string{
						"region": "uscentral",
					},
					FailurePolicy: experimentalv1beta1.WorkloadMigrationFailurePolicy{
						MaxFailureCount: 1,
					},
				},
			}
			Expect(hubClient.Create(ctx, migrationReq)).To(Succeed())
		})

		It("should initialize the migration request with two migration attempts", func() {
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
							Name:       "my-placement-a-useast",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadResourceClusterBinding",
							Resource:   "workloadresourceclusterbindings",
						},
						WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement-a",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadPlacement",
							Resource:   "workloadplacements",
						},
						FromClusterName:      "useast",
						ToClusterRequestName: ptr.To("my-migration-2-useast-replacement"),
					},
					{
						WorkloadResourceBindingRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement-b-useast",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadResourceClusterBinding",
							Resource:   "workloadresourceclusterbindings",
						},
						WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement-b",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadPlacement",
							Resource:   "workloadplacements",
						},
						FromClusterName:      "useast",
						ToClusterRequestName: ptr.To("my-migration-2-useast-replacement"),
					},
				},
			}

			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-migration-2"}, migrationReq); err != nil {
					return err.Error()
				}
				return cmp.Diff(migrationReq.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadMigrationAttempt{}, "Conditions", "ToClusterName"),
					cmpopts.IgnoreSliceElements(func(c metav1.Condition) bool {
						return c.Type != experimentalv1beta1.WorkloadMigrationRequestCondTypeInitialized
					}),
					cmpopts.SortSlices(func(a, b experimentalv1beta1.WorkloadMigrationAttempt) bool {
						return a.WorkloadResourceBindingRef.Name < b.WorkloadResourceBindingRef.Name
					}),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"migration request should be initialized with two migration attempts")
		})

		It("should create a cluster request for the uscentral region", func() {
			wantReq := &experimentalv1beta1.ClusterRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-migration-2-useast-replacement",
				},
				Spec: experimentalv1beta1.ClusterRequestSpec{
					ClusterSelector: map[string]string{"region": "uscentral"},
				},
			}

			clusterReq := &experimentalv1beta1.ClusterRequest{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Name: "my-migration-2-useast-replacement"}, clusterReq)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"cluster request should be created")

			if diff := cmp.Diff(clusterReq, wantReq,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(experimentalv1beta1.ClusterRequestStatus{}, "Conditions", "LatestObservedClusterCreationTimestamp", "ProvisionedClusterName"),
			); diff != "" {
				Fail("cluster request mismatch (-got, +want):\n" + diff)
			}
		})
		It("should fulfill the cluster request by creating the uscentral cluster and marking the request as completed", func() {
			By("creating the uscentral member cluster")
			mc := &clusterv1beta1.MemberCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "uscentral",
					Labels: map[string]string{
						"region": "uscentral",
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

			By("patching the cluster request status with Completed=True and ProvisionedClusterName")
			clusterReq := &experimentalv1beta1.ClusterRequest{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "my-migration-2-useast-replacement"}, clusterReq)).To(Succeed())

			updatedReq := clusterReq.DeepCopy()
			updatedReq.Status.ProvisionedClusterName = ptr.To("uscentral")
			updatedReq.Status.Conditions = []metav1.Condition{
				{
					Type:               experimentalv1beta1.ClusterRequestCondTypeCompleted,
					Status:             metav1.ConditionTrue,
					Reason:             "ClusterProvisioned",
					ObservedGeneration: clusterReq.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedReq)).To(Succeed())
		})

		It("should create a to-cluster binding for the first placement on uscentral", func() {
			useastHash, err := resource.HashOf(map[string]string{"region": "useast"})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []experimentalv1beta1.WorkloadResourceClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "my-placement-a-useast",
						Namespace:  workNSName,
						Finalizers: []string{bindingCleanupFinalizer},
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement-a",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-a",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("useast"),
						Suspended:             false,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-a-uscentral-migrated",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey:      "my-placement-a",
							experimentalv1beta1.WorkloadResourceClusterBindingCreatedInPlaceForKey: "my-placement-a-useast",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-a",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("uscentral"),
						Suspended:             false,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "my-placement-b-useast",
						Namespace:  workNSName,
						Finalizers: []string{bindingCleanupFinalizer},
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement-b",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-b",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("useast"),
						Suspended:             false,
					},
				},
			}

			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, bindingList, client.InNamespace(workNSName)); err != nil {
					return err.Error()
				}
				return cmp.Diff(bindingList.Items, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingStatus{}, "Conditions"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
					cmpopts.SortSlices(func(a, b experimentalv1beta1.WorkloadResourceClusterBinding) bool { return a.Name < b.Name }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"should have exactly 3 bindings: 2 originals and the new to-cluster binding for placement-a")
		})

		It("should mark the to-cluster binding for placement-a as Synchronized and AllResourcesAvailable", func() {
			toBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-a-uscentral-migrated"}, toBinding)).To(Succeed())

			updatedBinding := toBinding.DeepCopy()
			updatedBinding.Status.Conditions = []metav1.Condition{
				{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesApplied",
					ObservedGeneration: toBinding.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesAvailable",
					ObservedGeneration: toBinding.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedBinding)).To(Succeed())
		})

		It("should suspend the from-cluster binding for placement-a after its to-cluster binding becomes available", func() {
			useastHash, err := resource.HashOf(map[string]string{"region": "useast"})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []experimentalv1beta1.WorkloadResourceClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-a-useast",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement-a",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-a",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("useast"),
						Suspended:             true,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-a-uscentral-migrated",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey:      "my-placement-a",
							experimentalv1beta1.WorkloadResourceClusterBindingCreatedInPlaceForKey: "my-placement-a-useast",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-a",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("uscentral"),
						Suspended:             false,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "my-placement-b-useast",
						Namespace:  workNSName,
						Finalizers: []string{bindingCleanupFinalizer},
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement-b",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-b",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("useast"),
						Suspended:             false,
					},
				},
			}

			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, bindingList, client.InNamespace(workNSName)); err != nil {
					return err.Error()
				}
				return cmp.Diff(bindingList.Items, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations", "Finalizers"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingStatus{}, "Conditions"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
					cmpopts.SortSlices(func(a, b experimentalv1beta1.WorkloadResourceClusterBinding) bool { return a.Name < b.Name }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"placement-a from-binding should be suspended; placement-b should be untouched; 3 bindings total")
		})

		It("should remove the finalizer from the suspended placement-a from-binding", func() {
			fromBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-a-useast"}, fromBinding)).To(Succeed())

			updatedBinding := fromBinding.DeepCopy()
			updatedBinding.Finalizers = nil
			Expect(hubClient.Update(ctx, updatedBinding)).To(Succeed())
		})

		It("should show 4 bindings after placement-b migration starts", func() {
			useastHash, err := resource.HashOf(map[string]string{"region": "useast"})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []experimentalv1beta1.WorkloadResourceClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-a-useast",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement-a",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-a",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("useast"),
						Suspended:             true,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-a-uscentral-migrated",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey:      "my-placement-a",
							experimentalv1beta1.WorkloadResourceClusterBindingCreatedInPlaceForKey: "my-placement-a-useast",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-a",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("uscentral"),
						Suspended:             false,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:       "my-placement-b-useast",
						Namespace:  workNSName,
						Finalizers: []string{bindingCleanupFinalizer},
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement-b",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-b",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("useast"),
						Suspended:             false,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-b-uscentral-migrated",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey:      "my-placement-b",
							experimentalv1beta1.WorkloadResourceClusterBindingCreatedInPlaceForKey: "my-placement-b-useast",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-b",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("uscentral"),
						Suspended:             false,
					},
				},
			}

			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, bindingList, client.InNamespace(workNSName)); err != nil {
					return err.Error()
				}
				return cmp.Diff(bindingList.Items, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations", "Finalizers"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingStatus{}, "Conditions"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
					cmpopts.SortSlices(func(a, b experimentalv1beta1.WorkloadResourceClusterBinding) bool { return a.Name < b.Name }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"should have 4 bindings: placement-a done, placement-b migration started")
		})

		It("should mark the to-cluster binding for placement-b as Synchronized and AllResourcesAvailable", func() {
			toBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-b-uscentral-migrated"}, toBinding)).To(Succeed())

			updatedBinding := toBinding.DeepCopy()
			updatedBinding.Status.Conditions = []metav1.Condition{
				{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeSynchronized,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesApplied",
					ObservedGeneration: toBinding.Generation,
					LastTransitionTime: metav1.Now(),
				},
				{
					Type:               experimentalv1beta1.WorkloadResourceClusterBindingCondTypeAllResourcesAvailable,
					Status:             metav1.ConditionTrue,
					Reason:             "AllResourcesAvailable",
					ObservedGeneration: toBinding.Generation,
					LastTransitionTime: metav1.Now(),
				},
			}
			Expect(hubClient.Status().Update(ctx, updatedBinding)).To(Succeed())
		})

		It("should suspend the from-cluster binding for placement-b", func() {
			fromBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Eventually(func() (bool, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-b-useast"}, fromBinding); err != nil {
					return false, err
				}
				return fromBinding.Spec.Suspended, nil
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(),
				"placement-b from-binding should be suspended after its to-binding becomes available")
		})

		It("should remove the finalizer from the suspended placement-b from-binding", func() {
			fromBinding := &experimentalv1beta1.WorkloadResourceClusterBinding{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-b-useast"}, fromBinding)).To(Succeed())

			updatedBinding := fromBinding.DeepCopy()
			updatedBinding.Finalizers = nil
			Expect(hubClient.Update(ctx, updatedBinding)).To(Succeed())
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
							Name:       "my-placement-a-useast",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadResourceClusterBinding",
							Resource:   "workloadresourceclusterbindings",
						},
						WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement-a",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadPlacement",
							Resource:   "workloadplacements",
						},
						FromClusterName:      "useast",
						ToClusterRequestName: ptr.To("my-migration-2-useast-replacement"),
						Conditions: []metav1.Condition{
							{
								Type:   experimentalv1beta1.WorkloadMigrationAttemptCondTypeCompleted,
								Status: metav1.ConditionTrue,
								Reason: experimentalv1beta1.WorkloadMigrationAttemptCompletedCondReasonSucceeded,
							},
						},
					},
					{
						WorkloadResourceBindingRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement-b-useast",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadResourceClusterBinding",
							Resource:   "workloadresourceclusterbindings",
						},
						WorkloadPlacementRef: experimentalv1beta1.CrossNamespaceObjectReference{
							Namespace:  workNSName,
							Name:       "my-placement-b",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Kind:       "WorkloadPlacement",
							Resource:   "workloadplacements",
						},
						FromClusterName:      "useast",
						ToClusterRequestName: ptr.To("my-migration-2-useast-replacement"),
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

			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-migration-2"}, migrationReq); err != nil {
					return err.Error()
				}
				return cmp.Diff(migrationReq.Status, wantStatus,
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
					cmpopts.SortSlices(func(a, b experimentalv1beta1.WorkloadMigrationAttempt) bool {
						return a.WorkloadResourceBindingRef.Name < b.WorkloadResourceBindingRef.Name
					}),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"migration request should be completed with both attempts succeeded")
		})

		It("should delete the migration request and wait for it to disappear", func() {
			migrationReq := &experimentalv1beta1.WorkloadMigrationRequest{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "my-migration-2"}, migrationReq)).To(Succeed())
			Expect(hubClient.Delete(ctx, migrationReq)).To(Succeed())

			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "my-migration-2"}, migrationReq)
				return client.IgnoreNotFound(err)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"migration request should be fully removed")
		})

		It("should have 2 promoted to-bindings after commit", func() {
			useastHash, err := resource.HashOf(map[string]string{"region": "useast"})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []experimentalv1beta1.WorkloadResourceClusterBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-a-uscentral-migrated",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement-a",
							// CreatedInPlaceFor label should be removed after promotion
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-a",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("uscentral"),
						Suspended:             false,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-b-uscentral-migrated",
						Namespace: workNSName,
						Labels: map[string]string{
							experimentalv1beta1.WorkloadResourceClusterBindingOwnedByLabelKey: "my-placement-b",
						},
					},
					Spec: experimentalv1beta1.WorkloadResourceClusterBindingSpec{
						WorkloadPlacementName: "my-placement-b",
						ClusterSelectorHash:   useastHash,
						MemberClusterName:     ptr.To("uscentral"),
						Suspended:             false,
					},
				},
			}

			bindingList := &experimentalv1beta1.WorkloadResourceClusterBindingList{}
			Eventually(func() string {
				if err := hubClient.List(ctx, bindingList, client.InNamespace(workNSName)); err != nil {
					return err.Error()
				}
				return cmp.Diff(bindingList.Items, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations", "Finalizers"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingStatus{}, "Conditions"),
					cmpopts.IgnoreFields(experimentalv1beta1.WorkloadResourceClusterBindingSpec{}, "ResourceSnapshotRevisionName"),
					cmpopts.SortSlices(func(a, b experimentalv1beta1.WorkloadResourceClusterBinding) bool { return a.Name < b.Name }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"only the 2 promoted to-bindings should remain after commit")
		})

		AfterAll(func() {
			By("deleting all bindings in the work namespace and stripping their finalizers")
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
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(),
				"all bindings should be removed")

			By("deleting the WorkloadPlacements")
			for _, name := range []string{"my-placement-a", "my-placement-b"} {
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &experimentalv1beta1.WorkloadPlacement{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: workNSName},
				}))).To(Succeed())
				placement := &experimentalv1beta1.WorkloadPlacement{}
				Eventually(func() error {
					return client.IgnoreNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: name}, placement))
				}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
					"WorkloadPlacement "+name+" should be removed")
			}

			By("deleting the cluster request if it still exists")
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &experimentalv1beta1.ClusterRequest{
				ObjectMeta: metav1.ObjectMeta{Name: "my-migration-2-useast-replacement"},
			}))).To(Succeed())

			By("deleting the member clusters")
			for _, name := range []string{"useast", "uscentral"} {
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &clusterv1beta1.MemberCluster{
					ObjectMeta: metav1.ObjectMeta{Name: name},
				}))).To(Succeed())
				mc := &clusterv1beta1.MemberCluster{}
				Eventually(func() error {
					return client.IgnoreNotFound(hubClient.Get(ctx, types.NamespacedName{Name: name}, mc))
				}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
					"MemberCluster "+name+" should be removed")
			}
		})
	})
})
