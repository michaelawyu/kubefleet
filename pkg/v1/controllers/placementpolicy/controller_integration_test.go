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
	"fmt"
	"sort"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/resource"
)

const (
	// Eventually polling interval and timeout.
	eventuallyInterval = 500 * time.Millisecond
	eventuallyDuration = 10 * time.Second
)

// listBindingsOwnedBy returns all placement bindings in the given namespace owned by the named placement
// policy, sorted by name for deterministic comparisons.
//
// Bindings are filtered by their spec field rather than the PlacementBindingOwnedByLabelKey label,
// mirroring the production reconciler's own listing logic (see controller.go).
func listBindingsOwnedBy(namespace, placementPolicyName string) ([]placementv1alpha1.PlacementBinding, error) {
	bindingList := &placementv1alpha1.PlacementBindingList{}
	if err := hubClient.List(ctx, bindingList, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	owned := make([]placementv1alpha1.PlacementBinding, 0, len(bindingList.Items))
	for idx := range bindingList.Items {
		if bindingList.Items[idx].Spec.PlacementPolicyName == placementPolicyName {
			owned = append(owned, bindingList.Items[idx])
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })
	return owned, nil
}

// listClusterBindingsOwnedBy returns all cluster placement bindings (cluster-scoped) owned by the named
// cluster placement policy, sorted by name for deterministic comparisons.
//
// Bindings are filtered by their spec field rather than the PlacementBindingOwnedByLabelKey label,
// mirroring the production reconciler's own listing logic (see bindings.go).
func listClusterBindingsOwnedBy(placementPolicyName string) ([]placementv1alpha1.ClusterPlacementBinding, error) {
	bindingList := &placementv1alpha1.ClusterPlacementBindingList{}
	if err := hubClient.List(ctx, bindingList); err != nil {
		return nil, err
	}
	owned := make([]placementv1alpha1.ClusterPlacementBinding, 0, len(bindingList.Items))
	for idx := range bindingList.Items {
		if bindingList.Items[idx].Spec.PlacementPolicyName == placementPolicyName {
			owned = append(owned, bindingList.Items[idx])
		}
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })
	return owned, nil
}

var _ = Describe("placement policy ops", func() {
	Context("creating a new placement policy that targets existing clusters", Ordered, func() {
		BeforeAll(func() {
			By("creating a Deployment")
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

			By("creating a ConfigMap")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app-config",
					Namespace: workNSName,
				},
				Data: map[string]string{
					"key": "value",
				},
			}
			Expect(hubClient.Create(ctx, cm)).To(Succeed())

			By("creating 3 member clusters, one per region")
			clusters := []struct {
				name   string
				region string
			}{
				{"useast", "useast"},
				{"chinanorth", "chinanorth"},
				{"uksouth", "uksouth"},
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

			By("creating the PlacementPolicy")
			placement := &placementv1alpha1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-placement",
					Namespace: workNSName,
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "useast"}}}},
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "chinanorth"}}}},
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "uksouth"}}}},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Name:       "app",
							APIGroup:   "apps",
							APIVersion: "v1",
							Kind:       "Deployment",
						},
						{
							Name:       "app-config",
							APIGroup:   "",
							APIVersion: "v1",
							Kind:       "ConfigMap",
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, placement)).To(Succeed())
		})

		It("should add the cleanup finalizer to the placement policy", func() {
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() ([]string, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement"}, placement); err != nil {
					return nil, err
				}
				return placement.Finalizers, nil
			}, eventuallyDuration, eventuallyInterval).Should(ContainElement(placementPolicyCleanupFinalizer),
				"placement should have the cleanup finalizer")

			wantFinalizers := []string{placementPolicyCleanupFinalizer}
			if diff := cmp.Diff(placement.Finalizers, wantFinalizers); diff != "" {
				Fail(fmt.Sprintf("placement finalizers mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should create a resource snapshot for the placement policy", func() {
			wantSnapshotName := "my-placement-resource-snapshot-0"
			wantSnapshot := &placementv1alpha1.PlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wantSnapshotName,
					Namespace: workNSName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         "my-placement",
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "0",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
				Spec: placementv1alpha1.PlacementResourceSnapshotSpec{
					// The resources are sorted (by API group, kind, namespace, and name) for deterministic
					// ordering; the empty API group of ConfigMap sorts before the "apps" group of Deployment.
					Resources: []placementv1alpha1.SnapshottedResource{
						{
							Identifier: placementv1alpha1.ObjectReference{
								Namespace:  workNSName,
								APIGroup:   "",
								APIVersion: "v1",
								Kind:       "ConfigMap",
								Name:       "app-config",
							},
						},
						{
							Identifier: placementv1alpha1.ObjectReference{
								Namespace:  workNSName,
								APIGroup:   "apps",
								APIVersion: "v1",
								Kind:       "Deployment",
								Name:       "app",
							},
						},
					},
				},
			}

			By("waiting for the resource snapshot to be created")
			snapshot := &placementv1alpha1.PlacementResourceSnapshot{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: wantSnapshotName}, snapshot)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementResourceSnapshot should be created")

			By("verifying the resource snapshot matches the expected state")
			if diff := cmp.Diff(snapshot, wantSnapshot,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations"),
				cmpopts.IgnoreFields(placementv1alpha1.SnapshottedResource{}, "Manifest"),
			); diff != "" {
				Fail(fmt.Sprintf("resource snapshot mismatch (-got, +want):\n%s", diff))
			}

			// The exact content hash depends on the API server's defaulting of the selected resources, which is
			// not worth hard-coding here; just verify that the annotation is present and looks like a hash.
			Expect(snapshot.Annotations).To(HaveKeyWithValue(placementv1alpha1.PlacementResourceSnapshotContentsHashAnnotationKey, MatchRegexp("^[0-9a-f]{64}$")))
		})

		It("should create 3 placement bindings for the placement policy", func() {
			By("waiting for 3 placement bindings to be created")
			var bindings []placementv1alpha1.PlacementBinding
			Eventually(func() (int, error) {
				var err error
				bindings, err = listBindingsOwnedBy(workNSName, "my-placement")
				if err != nil {
					return 0, err
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(Equal(3), "3 placement bindings should be created")

			By("computing expected cluster selector hashes")
			useastTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "useast"}}
			chinanorthTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "chinanorth"}}
			uksouthTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "uksouth"}}
			useastHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{useastTerm}})
			Expect(err).NotTo(HaveOccurred())
			chinanorthHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{chinanorthTerm}})
			Expect(err).NotTo(HaveOccurred())
			uksouthHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uksouthTerm}})
			Expect(err).NotTo(HaveOccurred())

			snapshotRevision := "my-placement-resource-snapshot-0"
			wantBindings := []placementv1alpha1.PlacementBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-useast",
						Namespace: workNSName,
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-placement",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: useastHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-placement",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{useastTerm}}},
						ClusterName:          "useast",
						ResourceSnapshotName: snapshotRevision,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-chinanorth",
						Namespace: workNSName,
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-placement",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: chinanorthHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-placement",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{chinanorthTerm}}},
						ClusterName:          "chinanorth",
						ResourceSnapshotName: snapshotRevision,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-uksouth",
						Namespace: workNSName,
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-placement",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: uksouthHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-placement",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uksouthTerm}}},
						ClusterName:          "uksouth",
						ResourceSnapshotName: snapshotRevision,
					},
				},
			}

			By("verifying the placement bindings match the expected state")
			if diff := cmp.Diff(bindings, wantBindings,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementBindingStatus{}, "Conditions"),
				cmpopts.SortSlices(func(a, b placementv1alpha1.PlacementBinding) bool {
					return a.Name < b.Name
				}),
			); diff != "" {
				Fail(fmt.Sprintf("placement bindings mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should set the placement policy status correctly", func() {
			wantStatus := placementv1alpha1.PlacementPolicyStatus{
				LatestResourceRevisionName: ptr.To("my-placement-resource-snapshot-0"),
				Conditions: []metav1.Condition{
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeSynchronized,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveSynchronizedResources",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeAvailable,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveResourcesAvailable",
					},
				},
			}

			By("waiting for all 3 status conditions to be populated")
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement"}, placement); err != nil {
					return nil, err
				}
				return placement.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(HaveLen(3),
				"placement should have 3 status conditions")

			By("verifying the placement policy status matches the expected state")
			if diff := cmp.Diff(placement.Status, wantStatus,
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicyStatus{}, "BindingManager"),
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("placement policy status mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should patch all 3 binding statuses with Synchronized=True and AllResourcesAvailable=True", func() {
			bindingNames := []string{
				"my-placement-useast",
				"my-placement-chinanorth",
				"my-placement-uksouth",
			}
			for _, name := range bindingNames {
				By("patching binding " + name)
				binding := &placementv1alpha1.PlacementBinding{}
				Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: name}, binding)).To(Succeed())

				updatedBinding := binding.DeepCopy()
				updatedBinding.Status.Conditions = []metav1.Condition{
					{
						Type:               placementv1alpha1.PlacementBindingCondTypeSynchronized,
						Status:             metav1.ConditionTrue,
						Reason:             "AllResourcesApplied",
						ObservedGeneration: binding.Generation,
						LastTransitionTime: metav1.Now(),
					},
					{
						Type:               placementv1alpha1.PlacementBindingCondTypeAvailable,
						Status:             metav1.ConditionTrue,
						Reason:             "AllResourcesAvailable",
						ObservedGeneration: binding.Generation,
						LastTransitionTime: metav1.Now(),
					},
				}
				Expect(hubClient.Status().Update(ctx, updatedBinding)).To(Succeed())
			}
		})

		It("should reflect Synchronized=True and AllResourcesAvailable=True on the placement status", func() {
			wantStatus := placementv1alpha1.PlacementPolicyStatus{
				LatestResourceRevisionName: ptr.To("my-placement-resource-snapshot-0"),
				Conditions: []metav1.Condition{
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeSynchronized,
						Status: metav1.ConditionTrue,
						Reason: "AllBindingsHaveUpToDateSnapshot",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeAvailable,
						Status: metav1.ConditionTrue,
						Reason: "AllBindingsHaveResourcesAvailable",
					},
				},
			}

			By("waiting for the placement status to reflect Synchronized=True and AllResourcesAvailable=True")
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement"}, placement); err != nil {
					return err.Error()
				}
				return cmp.Diff(placement.Status, wantStatus,
					cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicyStatus{}, "BindingManager"),
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"placement policy status should reflect Synchronized=True and AllResourcesAvailable=True")
		})

		AfterAll(func() {
			By("deleting the PlacementPolicy")
			placement := &placementv1alpha1.PlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement"}, placement)).To(Succeed())
			Expect(hubClient.Delete(ctx, placement)).To(Succeed())

			By("waiting for the PlacementPolicy to be fully removed")
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement"}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("placement still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be fully removed")

			By("verifying all resource snapshots owned by the placement are gone")
			Eventually(func() (int, error) {
				snapshotList := &placementv1alpha1.PlacementResourceSnapshotList{}
				if err := hubClient.List(ctx, snapshotList,
					client.InNamespace(workNSName),
					client.MatchingLabels{placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey: "my-placement"},
				); err != nil {
					return 0, err
				}
				for i := range snapshotList.Items {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &snapshotList.Items[i])); err != nil {
						return len(snapshotList.Items), err
					}
				}
				return len(snapshotList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all resource snapshots should be cleaned up")

			By("verifying all placement bindings owned by the placement are gone")
			Eventually(func() (int, error) {
				bindings, err := listBindingsOwnedBy(workNSName, "my-placement")
				if err != nil {
					return 0, err
				}
				for i := range bindings {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &bindings[i])); err != nil {
						return len(bindings), err
					}
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all placement bindings should be cleaned up")

			By("deleting the Deployment and waiting for it to disappear")
			deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, deploy))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app"}, deploy)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("Deployment still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Deployment should be fully removed")

			By("deleting the ConfigMap and waiting for it to disappear")
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, cm))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app-config"}, cm)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ConfigMap still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ConfigMap should be fully removed")

			By("deleting the useast, chinanorth, and uksouth member clusters and waiting for them to disappear")
			for _, name := range []string{"useast", "chinanorth", "uksouth"} {
				mc := &clusterv1beta1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Name: name}}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, mc))).To(Succeed())
				Eventually(func() error {
					err := hubClient.Get(ctx, types.NamespacedName{Name: name}, mc)
					if apierrors.IsNotFound(err) {
						return nil
					}
					if err != nil {
						return err
					}
					return fmt.Errorf("MemberCluster %s still exists", name)
				}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "MemberCluster "+name+" should be fully removed")
			}
		})
	})

	Context("creating a new placement policy that targets non-existent clusters", Ordered, func() {
		BeforeAll(func() {
			By("creating a Deployment")
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app2",
					Namespace: workNSName,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "app2"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "app2"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "app2",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, deploy)).To(Succeed())

			By("creating a ConfigMap")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app2-config",
					Namespace: workNSName,
				},
				Data: map[string]string{
					"key": "value",
				},
			}
			Expect(hubClient.Create(ctx, cm)).To(Succeed())

			By("creating the PlacementPolicy targeting australiaeast")
			placement := &placementv1alpha1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-placement-2",
					Namespace: workNSName,
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "australiaeast"}}}},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Name:       "app2",
							APIGroup:   "apps",
							APIVersion: "v1",
							Kind:       "Deployment",
						},
						{
							Name:       "app2-config",
							APIGroup:   "",
							APIVersion: "v1",
							Kind:       "ConfigMap",
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, placement)).To(Succeed())
		})

		It("should add the cleanup finalizer to the placement policy", func() {
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() ([]string, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-2"}, placement); err != nil {
					return nil, err
				}
				return placement.Finalizers, nil
			}, eventuallyDuration, eventuallyInterval).Should(ContainElement(placementPolicyCleanupFinalizer),
				"placement should have the cleanup finalizer")

			wantFinalizers := []string{placementPolicyCleanupFinalizer}
			if diff := cmp.Diff(placement.Finalizers, wantFinalizers); diff != "" {
				Fail(fmt.Sprintf("placement finalizers mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should create a resource snapshot for the placement policy", func() {
			wantSnapshotName := "my-placement-2-resource-snapshot-0"
			wantSnapshot := &placementv1alpha1.PlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wantSnapshotName,
					Namespace: workNSName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         "my-placement-2",
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "0",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
				Spec: placementv1alpha1.PlacementResourceSnapshotSpec{
					// The resources are sorted (by API group, kind, namespace, and name) for deterministic
					// ordering; the empty API group of ConfigMap sorts before the "apps" group of Deployment.
					Resources: []placementv1alpha1.SnapshottedResource{
						{
							Identifier: placementv1alpha1.ObjectReference{
								Namespace:  workNSName,
								APIGroup:   "",
								APIVersion: "v1",
								Kind:       "ConfigMap",
								Name:       "app2-config",
							},
						},
						{
							Identifier: placementv1alpha1.ObjectReference{
								Namespace:  workNSName,
								APIGroup:   "apps",
								APIVersion: "v1",
								Kind:       "Deployment",
								Name:       "app2",
							},
						},
					},
				},
			}

			By("waiting for the resource snapshot to be created")
			snapshot := &placementv1alpha1.PlacementResourceSnapshot{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: wantSnapshotName}, snapshot)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementResourceSnapshot should be created")

			By("verifying the resource snapshot matches the expected state")
			if diff := cmp.Diff(snapshot, wantSnapshot,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations"),
				cmpopts.IgnoreFields(placementv1alpha1.SnapshottedResource{}, "Manifest"),
			); diff != "" {
				Fail(fmt.Sprintf("resource snapshot mismatch (-got, +want):\n%s", diff))
			}

			// The exact content hash depends on the API server's defaulting of the selected resources, which is
			// not worth hard-coding here; just verify that the annotation is present and looks like a hash.
			Expect(snapshot.Annotations).To(HaveKeyWithValue(placementv1alpha1.PlacementResourceSnapshotContentsHashAnnotationKey, MatchRegexp("^[0-9a-f]{64}$")))
		})

		It("should not create any placement bindings for the placement policy", func() {
			// Give the controller time to reconcile; a binding should never appear
			// because no cluster with region=australiaeast exists.
			Consistently(func() (int, error) {
				bindings, err := listBindingsOwnedBy(workNSName, "my-placement-2")
				if err != nil {
					return 0, err
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(),
				"no placement bindings should be created when no matching cluster exists")
		})

		It("should create a cluster claim for the australiaeast selector", func() {
			placementRef := &placementv1alpha1.PlacementPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: "my-placement-2"}}
			claimName := clusterClaimName(placementRef)
			placementPolicyRef := placementPolicyObjectRef(placementRef)

			wantClaim := &placementv1alpha1.ClusterClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: claimName,
				},
				Spec: placementv1alpha1.ClusterClaimSpec{
					PlacementPolicyRef:   &placementPolicyRef,
					ClusterSelectorTerms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "australiaeast"}}},
				},
			}

			By("waiting for the ClusterClaim to be created")
			claim := &placementv1alpha1.ClusterClaim{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Name: claimName}, claim)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterClaim should be created")

			By("verifying the ClusterClaim matches the expected state")
			if diff := cmp.Diff(claim, wantClaim,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.ClusterClaimStatus{}, "Conditions", "LastObservedMostRecentClusterCreationTimestamp", "ProvisionedClusterName"),
			); diff != "" {
				Fail(fmt.Sprintf("cluster claim mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should create a member cluster in the australiaeast region", func() {
			By("creating a member cluster with region=australiaeast")
			mc := &clusterv1beta1.MemberCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "australiaeast",
					Labels: map[string]string{
						"region": "australiaeast",
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

		It("should create a binding to the australiaeast member cluster", func() {
			australiaeastTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "australiaeast"}}
			australiaeastHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{australiaeastTerm}})
			Expect(err).NotTo(HaveOccurred())

			snapshotRevision := "my-placement-2-resource-snapshot-0"
			wantBinding := placementv1alpha1.PlacementBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-placement-2-australiaeast",
					Namespace: workNSName,
					Labels: map[string]string{
						placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-placement-2",
					},
					Annotations: map[string]string{
						clusterSelectorHashAnnotationKey: australiaeastHash,
					},
				},
				Spec: placementv1alpha1.PlacementBindingSpec{
					PlacementPolicyName:  "my-placement-2",
					ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{australiaeastTerm}}},
					ClusterName:          "australiaeast",
					ResourceSnapshotName: snapshotRevision,
				},
			}

			By("waiting for the binding to be created")
			binding := &placementv1alpha1.PlacementBinding{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-2-australiaeast"}, binding)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "binding to australiaeast should be created")

			By("verifying the binding matches the expected state")
			if diff := cmp.Diff(*binding, wantBinding,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementBindingStatus{}, "Conditions"),
			); diff != "" {
				Fail(fmt.Sprintf("binding mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should update the placement status after the australiaeast cluster is found", func() {
			wantStatus := placementv1alpha1.PlacementPolicyStatus{
				LatestResourceRevisionName: ptr.To("my-placement-2-resource-snapshot-0"),
				Conditions: []metav1.Condition{
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeSynchronized,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveSynchronizedResources",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeAvailable,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveResourcesAvailable",
					},
				},
			}

			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-2"}, placement); err != nil {
					return err.Error()
				}
				return cmp.Diff(placement.Status, wantStatus,
					cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicyStatus{}, "BindingManager"),
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"placement status should reflect Scheduled=True after australiaeast cluster is found")
		})

		It("should delete the cluster claim once the selector is fulfilled", func() {
			claimName := clusterClaimName(&placementv1alpha1.PlacementPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: "my-placement-2"}})
			Eventually(func() error {
				claim := &placementv1alpha1.ClusterClaim{}
				err := hubClient.Get(ctx, types.NamespacedName{Name: claimName}, claim)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("cluster claim still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"cluster claim should be deleted once all selectors are fulfilled")
		})

		AfterAll(func() {
			By("deleting the PlacementPolicy")
			placement := &placementv1alpha1.PlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-2"}, placement)).To(Succeed())
			Expect(hubClient.Delete(ctx, placement)).To(Succeed())

			By("waiting for the PlacementPolicy to be fully removed")
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-2"}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("placement still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be fully removed")

			By("verifying all resource snapshots owned by the placement are gone")
			Eventually(func() (int, error) {
				snapshotList := &placementv1alpha1.PlacementResourceSnapshotList{}
				if err := hubClient.List(ctx, snapshotList,
					client.InNamespace(workNSName),
					client.MatchingLabels{placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey: "my-placement-2"},
				); err != nil {
					return 0, err
				}
				for i := range snapshotList.Items {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &snapshotList.Items[i])); err != nil {
						return len(snapshotList.Items), err
					}
				}
				return len(snapshotList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all resource snapshots should be cleaned up")

			By("verifying all placement bindings owned by the placement are gone")
			Eventually(func() (int, error) {
				bindings, err := listBindingsOwnedBy(workNSName, "my-placement-2")
				if err != nil {
					return 0, err
				}
				for i := range bindings {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &bindings[i])); err != nil {
						return len(bindings), err
					}
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all placement bindings should be cleaned up")

			By("cleaning up any leftover cluster claim (cluster claims are cluster-scoped and are not garbage-collected with the placement)")
			claim := &placementv1alpha1.ClusterClaim{ObjectMeta: metav1.ObjectMeta{
				Name: clusterClaimName(&placementv1alpha1.PlacementPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: "my-placement-2"}}),
			}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, claim))).To(Succeed())

			By("deleting the Deployment and waiting for it to disappear")
			deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app2", Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, deploy))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app2"}, deploy)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("Deployment still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Deployment should be fully removed")

			By("deleting the ConfigMap and waiting for it to disappear")
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app2-config", Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, cm))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app2-config"}, cm)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ConfigMap still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ConfigMap should be fully removed")

			By("deleting the australiaeast member cluster and waiting for it to disappear")
			mc := &clusterv1beta1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Name: "australiaeast"}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, mc))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "australiaeast"}, mc)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("MemberCluster still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "MemberCluster should be fully removed")
		})
	})

	Context("updating a placement policy to target different clusters", Ordered, func() {
		BeforeAll(func() {
			By("creating a Deployment")
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app3",
					Namespace: workNSName,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": "app3"},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "app3"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "app3",
									Image: "nginx:latest",
								},
							},
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, deploy)).To(Succeed())

			By("creating a ConfigMap")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "app3-config",
					Namespace: workNSName,
				},
				Data: map[string]string{
					"key": "value",
				},
			}
			Expect(hubClient.Create(ctx, cm)).To(Succeed())

			By("creating 2 member clusters in useast and uswest")
			clusters := []struct {
				name   string
				region string
			}{
				{"useast2", "useast"},
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

			By("creating the PlacementPolicy targeting useast and uswest")
			placement := &placementv1alpha1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-placement-3",
					Namespace: workNSName,
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "useast"}}}},
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "uswest"}}}},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Name:       "app3",
							APIGroup:   "apps",
							APIVersion: "v1",
							Kind:       "Deployment",
						},
						{
							Name:       "app3-config",
							APIGroup:   "",
							APIVersion: "v1",
							Kind:       "ConfigMap",
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, placement)).To(Succeed())
		})

		It("should create 2 placement bindings for the placement policy", func() {
			By("waiting for 2 placement bindings to be created")
			var bindings []placementv1alpha1.PlacementBinding
			Eventually(func() (int, error) {
				var err error
				bindings, err = listBindingsOwnedBy(workNSName, "my-placement-3")
				if err != nil {
					return 0, err
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(Equal(2), "2 placement bindings should be created")

			By("computing expected cluster selector hashes")
			useastTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "useast"}}
			uswestTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "uswest"}}
			useastHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{useastTerm}})
			Expect(err).NotTo(HaveOccurred())
			uswestHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uswestTerm}})
			Expect(err).NotTo(HaveOccurred())

			snapshotRevision := "my-placement-3-resource-snapshot-0"
			wantBindings := []placementv1alpha1.PlacementBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-3-useast2",
						Namespace: workNSName,
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-placement-3",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: useastHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-placement-3",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{useastTerm}}},
						ClusterName:          "useast2",
						ResourceSnapshotName: snapshotRevision,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-3-uswest",
						Namespace: workNSName,
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-placement-3",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: uswestHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-placement-3",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uswestTerm}}},
						ClusterName:          "uswest",
						ResourceSnapshotName: snapshotRevision,
					},
				},
			}

			By("verifying the placement bindings match the expected state")
			if diff := cmp.Diff(bindings, wantBindings,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementBindingStatus{}, "Conditions"),
				cmpopts.SortSlices(func(a, b placementv1alpha1.PlacementBinding) bool {
					return a.Name < b.Name
				}),
			); diff != "" {
				Fail(fmt.Sprintf("placement bindings mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should set the placement policy status correctly", func() {
			wantStatus := placementv1alpha1.PlacementPolicyStatus{
				LatestResourceRevisionName: ptr.To("my-placement-3-resource-snapshot-0"),
				Conditions: []metav1.Condition{
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeSynchronized,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveSynchronizedResources",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeAvailable,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveResourcesAvailable",
					},
				},
			}

			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-3"}, placement); err != nil {
					return err.Error()
				}
				return cmp.Diff(placement.Status, wantStatus,
					cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicyStatus{}, "BindingManager"),
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"placement status should be set correctly")
		})

		It("should update the placement to select uscentral and uswest", func() {
			By("fetching the current placement")
			placement := &placementv1alpha1.PlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-3"}, placement)).To(Succeed())

			By("updating the cluster selectors to uscentral and uswest")
			updatedPlacement := placement.DeepCopy()
			updatedPlacement.Spec.ClusterSelectors = []placementv1alpha1.ClusterSelector{
				{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "uscentral"}}}},
				{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "uswest"}}}},
			}
			Expect(hubClient.Update(ctx, updatedPlacement)).To(Succeed())
		})

		It("should delete the stale useast binding and retain the uswest binding", func() {
			uswestTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "uswest"}}
			uswestHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uswestTerm}})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []placementv1alpha1.PlacementBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-placement-3-uswest",
						Namespace: workNSName,
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-placement-3",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: uswestHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-placement-3",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uswestTerm}}},
						ClusterName:          "uswest",
						ResourceSnapshotName: "my-placement-3-resource-snapshot-0",
					},
				},
			}

			Eventually(func() string {
				bindings, err := listBindingsOwnedBy(workNSName, "my-placement-3")
				if err != nil {
					return err.Error()
				}
				return cmp.Diff(bindings, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
					cmpopts.IgnoreFields(placementv1alpha1.PlacementBindingStatus{}, "Conditions"),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"only the uswest binding should remain after the selector update")
		})

		It("should create a cluster claim for the uscentral selector", func() {
			placementRef := &placementv1alpha1.PlacementPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: "my-placement-3"}}
			claimName := clusterClaimName(placementRef)
			placementPolicyRef := placementPolicyObjectRef(placementRef)

			wantClaim := &placementv1alpha1.ClusterClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: claimName,
				},
				Spec: placementv1alpha1.ClusterClaimSpec{
					PlacementPolicyRef:   &placementPolicyRef,
					ClusterSelectorTerms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "uscentral"}}},
				},
			}

			By("waiting for the ClusterClaim to be created")
			claim := &placementv1alpha1.ClusterClaim{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Name: claimName}, claim)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterClaim should be created")

			By("verifying the ClusterClaim matches the expected state")
			if diff := cmp.Diff(claim, wantClaim,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.ClusterClaimStatus{}, "Conditions", "LastObservedMostRecentClusterCreationTimestamp", "ProvisionedClusterName"),
			); diff != "" {
				Fail(fmt.Sprintf("cluster claim mismatch (-got, +want):\n%s", diff))
			}
		})

		AfterAll(func() {
			By("deleting the PlacementPolicy")
			placement := &placementv1alpha1.PlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-3"}, placement)).To(Succeed())
			Expect(hubClient.Delete(ctx, placement)).To(Succeed())

			By("waiting for the PlacementPolicy to be fully removed")
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "my-placement-3"}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("placement still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be fully removed")

			By("verifying all resource snapshots owned by the placement are gone")
			Eventually(func() (int, error) {
				snapshotList := &placementv1alpha1.PlacementResourceSnapshotList{}
				if err := hubClient.List(ctx, snapshotList,
					client.InNamespace(workNSName),
					client.MatchingLabels{placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey: "my-placement-3"},
				); err != nil {
					return 0, err
				}
				for i := range snapshotList.Items {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &snapshotList.Items[i])); err != nil {
						return len(snapshotList.Items), err
					}
				}
				return len(snapshotList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all resource snapshots should be cleaned up")

			By("verifying all placement bindings owned by the placement are gone")
			Eventually(func() (int, error) {
				bindings, err := listBindingsOwnedBy(workNSName, "my-placement-3")
				if err != nil {
					return 0, err
				}
				for i := range bindings {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &bindings[i])); err != nil {
						return len(bindings), err
					}
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all placement bindings should be cleaned up")

			By("cleaning up any leftover cluster claim (cluster claims are cluster-scoped and are not garbage-collected with the placement)")
			claim := &placementv1alpha1.ClusterClaim{ObjectMeta: metav1.ObjectMeta{
				Name: clusterClaimName(&placementv1alpha1.PlacementPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: workNSName, Name: "my-placement-3"}}),
			}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, claim))).To(Succeed())

			By("deleting the Deployment and waiting for it to disappear")
			deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app3", Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, deploy))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app3"}, deploy)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("Deployment still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Deployment should be fully removed")

			By("deleting the ConfigMap and waiting for it to disappear")
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "app3-config", Namespace: workNSName}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, cm))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "app3-config"}, cm)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ConfigMap still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ConfigMap should be fully removed")

			By("deleting the useast2 and uswest member clusters and waiting for them to disappear")
			for _, name := range []string{"useast2", "uswest"} {
				mc := &clusterv1beta1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Name: name}}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, mc))).To(Succeed())
				Eventually(func() error {
					err := hubClient.Get(ctx, types.NamespacedName{Name: name}, mc)
					if apierrors.IsNotFound(err) {
						return nil
					}
					if err != nil {
						return err
					}
					return fmt.Errorf("MemberCluster %s still exists", name)
				}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "MemberCluster "+name+" should be fully removed")
			}
		})
	})
})

var _ = Describe("cluster placement policy ops", func() {
	Context("creating a new cluster placement policy that targets existing clusters", Ordered, func() {
		BeforeAll(func() {
			By("creating a ClusterRole")
			cr := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cs-app-role",
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{""},
						Resources: []string{"pods"},
						Verbs:     []string{"get", "list"},
					},
				},
			}
			Expect(hubClient.Create(ctx, cr)).To(Succeed())

			By("creating a ClusterRoleBinding")
			crb := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cs-app-rolebinding",
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName,
					Kind:     "ClusterRole",
					Name:     "cs-app-role",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:     rbacv1.UserKind,
						APIGroup: rbacv1.GroupName,
						Name:     "cs-app-user",
					},
				},
			}
			Expect(hubClient.Create(ctx, crb)).To(Succeed())

			By("creating 3 member clusters, one per region")
			clusters := []struct {
				name   string
				region string
			}{
				{"cs-useast", "cs-useast-a"},
				{"cs-chinanorth", "cs-chinanorth-a"},
				{"cs-uksouth", "cs-uksouth-a"},
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

			By("creating the ClusterPlacementPolicy")
			placement := &placementv1alpha1.ClusterPlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-cluster-placement",
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-useast-a"}}}},
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-chinanorth-a"}}}},
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-uksouth-a"}}}},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Name:       "cs-app-role",
							APIGroup:   rbacv1.GroupName,
							APIVersion: "v1",
							Kind:       "ClusterRole",
						},
						{
							Name:       "cs-app-rolebinding",
							APIGroup:   rbacv1.GroupName,
							APIVersion: "v1",
							Kind:       "ClusterRoleBinding",
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, placement)).To(Succeed())
		})

		It("should add the cleanup finalizer to the cluster placement policy", func() {
			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Eventually(func() ([]string, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement"}, placement); err != nil {
					return nil, err
				}
				return placement.Finalizers, nil
			}, eventuallyDuration, eventuallyInterval).Should(ContainElement(placementPolicyCleanupFinalizer),
				"cluster placement should have the cleanup finalizer")

			wantFinalizers := []string{placementPolicyCleanupFinalizer}
			if diff := cmp.Diff(placement.Finalizers, wantFinalizers); diff != "" {
				Fail(fmt.Sprintf("cluster placement finalizers mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should create a resource snapshot for the cluster placement policy", func() {
			wantSnapshotName := "my-cluster-placement-resource-snapshot-0"
			wantSnapshot := &placementv1alpha1.ClusterPlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name: wantSnapshotName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         "my-cluster-placement",
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "0",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
				Spec: placementv1alpha1.PlacementResourceSnapshotSpec{
					// The resources are sorted (by API group, kind, namespace, and name) for deterministic
					// ordering; ClusterRole sorts before ClusterRoleBinding within the same API group.
					Resources: []placementv1alpha1.SnapshottedResource{
						{
							Identifier: placementv1alpha1.ObjectReference{
								APIGroup:   rbacv1.GroupName,
								APIVersion: "v1",
								Kind:       "ClusterRole",
								Name:       "cs-app-role",
							},
						},
						{
							Identifier: placementv1alpha1.ObjectReference{
								APIGroup:   rbacv1.GroupName,
								APIVersion: "v1",
								Kind:       "ClusterRoleBinding",
								Name:       "cs-app-rolebinding",
							},
						},
					},
				},
			}

			By("waiting for the resource snapshot to be created")
			snapshot := &placementv1alpha1.ClusterPlacementResourceSnapshot{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Name: wantSnapshotName}, snapshot)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterPlacementResourceSnapshot should be created")

			By("verifying the resource snapshot matches the expected state")
			if diff := cmp.Diff(snapshot, wantSnapshot,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations"),
				cmpopts.IgnoreFields(placementv1alpha1.SnapshottedResource{}, "Manifest"),
			); diff != "" {
				Fail(fmt.Sprintf("resource snapshot mismatch (-got, +want):\n%s", diff))
			}

			// The exact content hash depends on the API server's defaulting of the selected resources, which is
			// not worth hard-coding here; just verify that the annotation is present and looks like a hash.
			Expect(snapshot.Annotations).To(HaveKeyWithValue(placementv1alpha1.PlacementResourceSnapshotContentsHashAnnotationKey, MatchRegexp("^[0-9a-f]{64}$")))
		})

		It("should create 3 cluster placement bindings for the cluster placement policy", func() {
			By("waiting for 3 cluster placement bindings to be created")
			var bindings []placementv1alpha1.ClusterPlacementBinding
			Eventually(func() (int, error) {
				var err error
				bindings, err = listClusterBindingsOwnedBy("my-cluster-placement")
				if err != nil {
					return 0, err
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(Equal(3), "3 cluster placement bindings should be created")

			By("computing expected cluster selector hashes")
			useastTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "cs-useast-a"}}
			chinanorthTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "cs-chinanorth-a"}}
			uksouthTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "cs-uksouth-a"}}
			useastHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{useastTerm}})
			Expect(err).NotTo(HaveOccurred())
			chinanorthHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{chinanorthTerm}})
			Expect(err).NotTo(HaveOccurred())
			uksouthHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uksouthTerm}})
			Expect(err).NotTo(HaveOccurred())

			snapshotRevision := "my-cluster-placement-resource-snapshot-0"
			wantBindings := []placementv1alpha1.ClusterPlacementBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-cluster-placement-cs-useast",
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-cluster-placement",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: useastHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-cluster-placement",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{useastTerm}}},
						ClusterName:          "cs-useast",
						ResourceSnapshotName: snapshotRevision,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-cluster-placement-cs-chinanorth",
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-cluster-placement",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: chinanorthHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-cluster-placement",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{chinanorthTerm}}},
						ClusterName:          "cs-chinanorth",
						ResourceSnapshotName: snapshotRevision,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-cluster-placement-cs-uksouth",
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-cluster-placement",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: uksouthHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-cluster-placement",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uksouthTerm}}},
						ClusterName:          "cs-uksouth",
						ResourceSnapshotName: snapshotRevision,
					},
				},
			}

			By("verifying the cluster placement bindings match the expected state")
			if diff := cmp.Diff(bindings, wantBindings,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementBindingStatus{}, "Conditions"),
				cmpopts.SortSlices(func(a, b placementv1alpha1.ClusterPlacementBinding) bool {
					return a.Name < b.Name
				}),
			); diff != "" {
				Fail(fmt.Sprintf("cluster placement bindings mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should set the cluster placement policy status correctly", func() {
			wantStatus := placementv1alpha1.PlacementPolicyStatus{
				LatestResourceRevisionName: ptr.To("my-cluster-placement-resource-snapshot-0"),
				Conditions: []metav1.Condition{
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeSynchronized,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveSynchronizedResources",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeAvailable,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveResourcesAvailable",
					},
				},
			}

			By("waiting for all 3 status conditions to be populated")
			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Eventually(func() ([]metav1.Condition, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement"}, placement); err != nil {
					return nil, err
				}
				return placement.Status.Conditions, nil
			}, eventuallyDuration, eventuallyInterval).Should(HaveLen(3),
				"cluster placement should have 3 status conditions")

			By("verifying the cluster placement policy status matches the expected state")
			if diff := cmp.Diff(placement.Status, wantStatus,
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicyStatus{}, "BindingManager"),
				cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
			); diff != "" {
				Fail(fmt.Sprintf("cluster placement policy status mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should patch all 3 binding statuses with Synchronized=True and AllResourcesAvailable=True", func() {
			bindingNames := []string{
				"my-cluster-placement-cs-useast",
				"my-cluster-placement-cs-chinanorth",
				"my-cluster-placement-cs-uksouth",
			}
			for _, name := range bindingNames {
				By("patching binding " + name)
				binding := &placementv1alpha1.ClusterPlacementBinding{}
				Expect(hubClient.Get(ctx, types.NamespacedName{Name: name}, binding)).To(Succeed())

				updatedBinding := binding.DeepCopy()
				updatedBinding.Status.Conditions = []metav1.Condition{
					{
						Type:               placementv1alpha1.PlacementBindingCondTypeSynchronized,
						Status:             metav1.ConditionTrue,
						Reason:             "AllResourcesApplied",
						ObservedGeneration: binding.Generation,
						LastTransitionTime: metav1.Now(),
					},
					{
						Type:               placementv1alpha1.PlacementBindingCondTypeAvailable,
						Status:             metav1.ConditionTrue,
						Reason:             "AllResourcesAvailable",
						ObservedGeneration: binding.Generation,
						LastTransitionTime: metav1.Now(),
					},
				}
				Expect(hubClient.Status().Update(ctx, updatedBinding)).To(Succeed())
			}
		})

		It("should reflect Synchronized=True and AllResourcesAvailable=True on the cluster placement status", func() {
			wantStatus := placementv1alpha1.PlacementPolicyStatus{
				LatestResourceRevisionName: ptr.To("my-cluster-placement-resource-snapshot-0"),
				Conditions: []metav1.Condition{
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeSynchronized,
						Status: metav1.ConditionTrue,
						Reason: "AllBindingsHaveUpToDateSnapshot",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeAvailable,
						Status: metav1.ConditionTrue,
						Reason: "AllBindingsHaveResourcesAvailable",
					},
				},
			}

			By("waiting for the cluster placement status to reflect Synchronized=True and AllResourcesAvailable=True")
			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement"}, placement); err != nil {
					return err.Error()
				}
				return cmp.Diff(placement.Status, wantStatus,
					cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicyStatus{}, "BindingManager"),
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"cluster placement policy status should reflect Synchronized=True and AllResourcesAvailable=True")
		})

		AfterAll(func() {
			By("deleting the ClusterPlacementPolicy")
			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement"}, placement)).To(Succeed())
			Expect(hubClient.Delete(ctx, placement)).To(Succeed())

			By("waiting for the ClusterPlacementPolicy to be fully removed")
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement"}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("cluster placement still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterPlacementPolicy should be fully removed")

			By("verifying all resource snapshots owned by the cluster placement are gone")
			Eventually(func() (int, error) {
				snapshotList := &placementv1alpha1.ClusterPlacementResourceSnapshotList{}
				if err := hubClient.List(ctx, snapshotList,
					client.MatchingLabels{placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey: "my-cluster-placement"},
				); err != nil {
					return 0, err
				}
				for i := range snapshotList.Items {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &snapshotList.Items[i])); err != nil {
						return len(snapshotList.Items), err
					}
				}
				return len(snapshotList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all resource snapshots should be cleaned up")

			By("verifying all cluster placement bindings owned by the cluster placement are gone")
			Eventually(func() (int, error) {
				bindings, err := listClusterBindingsOwnedBy("my-cluster-placement")
				if err != nil {
					return 0, err
				}
				for i := range bindings {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &bindings[i])); err != nil {
						return len(bindings), err
					}
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all cluster placement bindings should be cleaned up")

			By("deleting the ClusterRole and waiting for it to disappear")
			cr := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cs-app-role"}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, cr))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "cs-app-role"}, cr)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ClusterRole still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterRole should be fully removed")

			By("deleting the ClusterRoleBinding and waiting for it to disappear")
			crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cs-app-rolebinding"}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, crb))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "cs-app-rolebinding"}, crb)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ClusterRoleBinding still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterRoleBinding should be fully removed")

			By("deleting the member clusters and waiting for them to disappear")
			for _, name := range []string{"cs-useast", "cs-chinanorth", "cs-uksouth"} {
				mc := &clusterv1beta1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Name: name}}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, mc))).To(Succeed())
				Eventually(func() error {
					err := hubClient.Get(ctx, types.NamespacedName{Name: name}, mc)
					if apierrors.IsNotFound(err) {
						return nil
					}
					if err != nil {
						return err
					}
					return fmt.Errorf("MemberCluster %s still exists", name)
				}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "MemberCluster "+name+" should be fully removed")
			}
		})
	})

	Context("creating a new cluster placement policy that targets non-existent clusters", Ordered, func() {
		BeforeAll(func() {
			By("creating a ClusterRole")
			cr := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cs-app2-role",
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{""},
						Resources: []string{"pods"},
						Verbs:     []string{"get", "list"},
					},
				},
			}
			Expect(hubClient.Create(ctx, cr)).To(Succeed())

			By("creating a ClusterRoleBinding")
			crb := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cs-app2-rolebinding",
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName,
					Kind:     "ClusterRole",
					Name:     "cs-app2-role",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:     rbacv1.UserKind,
						APIGroup: rbacv1.GroupName,
						Name:     "cs-app2-user",
					},
				},
			}
			Expect(hubClient.Create(ctx, crb)).To(Succeed())

			By("creating the ClusterPlacementPolicy targeting cs-australiaeast-b")
			placement := &placementv1alpha1.ClusterPlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-cluster-placement-2",
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-australiaeast-b"}}}},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Name:       "cs-app2-role",
							APIGroup:   rbacv1.GroupName,
							APIVersion: "v1",
							Kind:       "ClusterRole",
						},
						{
							Name:       "cs-app2-rolebinding",
							APIGroup:   rbacv1.GroupName,
							APIVersion: "v1",
							Kind:       "ClusterRoleBinding",
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, placement)).To(Succeed())
		})

		It("should add the cleanup finalizer to the cluster placement policy", func() {
			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Eventually(func() ([]string, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement-2"}, placement); err != nil {
					return nil, err
				}
				return placement.Finalizers, nil
			}, eventuallyDuration, eventuallyInterval).Should(ContainElement(placementPolicyCleanupFinalizer),
				"cluster placement should have the cleanup finalizer")

			wantFinalizers := []string{placementPolicyCleanupFinalizer}
			if diff := cmp.Diff(placement.Finalizers, wantFinalizers); diff != "" {
				Fail(fmt.Sprintf("cluster placement finalizers mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should create a resource snapshot for the cluster placement policy", func() {
			wantSnapshotName := "my-cluster-placement-2-resource-snapshot-0"
			wantSnapshot := &placementv1alpha1.ClusterPlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name: wantSnapshotName,
					Labels: map[string]string{
						placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey:         "my-cluster-placement-2",
						placementv1alpha1.PlacementResourceSnapshotIndexLabelKey:           "0",
						placementv1alpha1.PlacementResourceSnapshotSubIndexLabelKey:        "0",
						placementv1alpha1.SubIndexedPlacementResourceSnapshotCountLabelKey: "1",
					},
				},
				Spec: placementv1alpha1.PlacementResourceSnapshotSpec{
					// The resources are sorted (by API group, kind, namespace, and name) for deterministic
					// ordering; ClusterRole sorts before ClusterRoleBinding within the same API group.
					Resources: []placementv1alpha1.SnapshottedResource{
						{
							Identifier: placementv1alpha1.ObjectReference{
								APIGroup:   rbacv1.GroupName,
								APIVersion: "v1",
								Kind:       "ClusterRole",
								Name:       "cs-app2-role",
							},
						},
						{
							Identifier: placementv1alpha1.ObjectReference{
								APIGroup:   rbacv1.GroupName,
								APIVersion: "v1",
								Kind:       "ClusterRoleBinding",
								Name:       "cs-app2-rolebinding",
							},
						},
					},
				},
			}

			By("waiting for the resource snapshot to be created")
			snapshot := &placementv1alpha1.ClusterPlacementResourceSnapshot{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Name: wantSnapshotName}, snapshot)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterPlacementResourceSnapshot should be created")

			By("verifying the resource snapshot matches the expected state")
			if diff := cmp.Diff(snapshot, wantSnapshot,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences", "Annotations"),
				cmpopts.IgnoreFields(placementv1alpha1.SnapshottedResource{}, "Manifest"),
			); diff != "" {
				Fail(fmt.Sprintf("resource snapshot mismatch (-got, +want):\n%s", diff))
			}

			// The exact content hash depends on the API server's defaulting of the selected resources, which is
			// not worth hard-coding here; just verify that the annotation is present and looks like a hash.
			Expect(snapshot.Annotations).To(HaveKeyWithValue(placementv1alpha1.PlacementResourceSnapshotContentsHashAnnotationKey, MatchRegexp("^[0-9a-f]{64}$")))
		})

		It("should not create any cluster placement bindings for the cluster placement policy", func() {
			// Give the controller time to reconcile; a binding should never appear
			// because no cluster with region=cs-australiaeast-b exists.
			Consistently(func() (int, error) {
				bindings, err := listClusterBindingsOwnedBy("my-cluster-placement-2")
				if err != nil {
					return 0, err
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(),
				"no cluster placement bindings should be created when no matching cluster exists")
		})

		It("should create a cluster claim for the cs-australiaeast-b selector", func() {
			placementRef := &placementv1alpha1.ClusterPlacementPolicy{ObjectMeta: metav1.ObjectMeta{Name: "my-cluster-placement-2"}}
			claimName := clusterClaimName(placementRef)
			placementPolicyRef := placementPolicyObjectRef(placementRef)

			wantClaim := &placementv1alpha1.ClusterClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: claimName,
				},
				Spec: placementv1alpha1.ClusterClaimSpec{
					PlacementPolicyRef:   &placementPolicyRef,
					ClusterSelectorTerms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-australiaeast-b"}}},
				},
			}

			By("waiting for the ClusterClaim to be created")
			claim := &placementv1alpha1.ClusterClaim{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Name: claimName}, claim)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterClaim should be created")

			By("verifying the ClusterClaim matches the expected state")
			if diff := cmp.Diff(claim, wantClaim,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.ClusterClaimStatus{}, "Conditions", "LastObservedMostRecentClusterCreationTimestamp", "ProvisionedClusterName"),
			); diff != "" {
				Fail(fmt.Sprintf("cluster claim mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should create a member cluster in the cs-australiaeast-b region", func() {
			By("creating a member cluster with region=cs-australiaeast-b")
			mc := &clusterv1beta1.MemberCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cs-australiaeast",
					Labels: map[string]string{
						"region": "cs-australiaeast-b",
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

		It("should create a binding to the cs-australiaeast member cluster", func() {
			australiaeastTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "cs-australiaeast-b"}}
			australiaeastHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{australiaeastTerm}})
			Expect(err).NotTo(HaveOccurred())

			snapshotRevision := "my-cluster-placement-2-resource-snapshot-0"
			wantBinding := placementv1alpha1.ClusterPlacementBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-cluster-placement-2-cs-australiaeast",
					Labels: map[string]string{
						placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-cluster-placement-2",
					},
					Annotations: map[string]string{
						clusterSelectorHashAnnotationKey: australiaeastHash,
					},
				},
				Spec: placementv1alpha1.PlacementBindingSpec{
					PlacementPolicyName:  "my-cluster-placement-2",
					ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{australiaeastTerm}}},
					ClusterName:          "cs-australiaeast",
					ResourceSnapshotName: snapshotRevision,
				},
			}

			By("waiting for the binding to be created")
			binding := &placementv1alpha1.ClusterPlacementBinding{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement-2-cs-australiaeast"}, binding)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "binding to cs-australiaeast should be created")

			By("verifying the binding matches the expected state")
			if diff := cmp.Diff(*binding, wantBinding,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementBindingStatus{}, "Conditions"),
			); diff != "" {
				Fail(fmt.Sprintf("binding mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should update the cluster placement status after the cs-australiaeast cluster is found", func() {
			wantStatus := placementv1alpha1.PlacementPolicyStatus{
				LatestResourceRevisionName: ptr.To("my-cluster-placement-2-resource-snapshot-0"),
				Conditions: []metav1.Condition{
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeSynchronized,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveSynchronizedResources",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeAvailable,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveResourcesAvailable",
					},
				},
			}

			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement-2"}, placement); err != nil {
					return err.Error()
				}
				return cmp.Diff(placement.Status, wantStatus,
					cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicyStatus{}, "BindingManager"),
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"cluster placement status should reflect Scheduled=True after cs-australiaeast cluster is found")
		})

		It("should delete the cluster claim once the selector is fulfilled", func() {
			claimName := clusterClaimName(&placementv1alpha1.ClusterPlacementPolicy{ObjectMeta: metav1.ObjectMeta{Name: "my-cluster-placement-2"}})
			Eventually(func() error {
				claim := &placementv1alpha1.ClusterClaim{}
				err := hubClient.Get(ctx, types.NamespacedName{Name: claimName}, claim)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("cluster claim still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
				"cluster claim should be deleted once all selectors are fulfilled")
		})

		AfterAll(func() {
			By("deleting the ClusterPlacementPolicy")
			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement-2"}, placement)).To(Succeed())
			Expect(hubClient.Delete(ctx, placement)).To(Succeed())

			By("waiting for the ClusterPlacementPolicy to be fully removed")
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement-2"}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("cluster placement still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterPlacementPolicy should be fully removed")

			By("verifying all resource snapshots owned by the cluster placement are gone")
			Eventually(func() (int, error) {
				snapshotList := &placementv1alpha1.ClusterPlacementResourceSnapshotList{}
				if err := hubClient.List(ctx, snapshotList,
					client.MatchingLabels{placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey: "my-cluster-placement-2"},
				); err != nil {
					return 0, err
				}
				for i := range snapshotList.Items {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &snapshotList.Items[i])); err != nil {
						return len(snapshotList.Items), err
					}
				}
				return len(snapshotList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all resource snapshots should be cleaned up")

			By("verifying all cluster placement bindings owned by the cluster placement are gone")
			Eventually(func() (int, error) {
				bindings, err := listClusterBindingsOwnedBy("my-cluster-placement-2")
				if err != nil {
					return 0, err
				}
				for i := range bindings {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &bindings[i])); err != nil {
						return len(bindings), err
					}
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all cluster placement bindings should be cleaned up")

			By("cleaning up any leftover cluster claim (cluster claims are cluster-scoped and are not garbage-collected with the placement)")
			claim := &placementv1alpha1.ClusterClaim{ObjectMeta: metav1.ObjectMeta{
				Name: clusterClaimName(&placementv1alpha1.ClusterPlacementPolicy{ObjectMeta: metav1.ObjectMeta{Name: "my-cluster-placement-2"}}),
			}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, claim))).To(Succeed())

			By("deleting the ClusterRole and waiting for it to disappear")
			cr := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cs-app2-role"}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, cr))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "cs-app2-role"}, cr)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ClusterRole still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterRole should be fully removed")

			By("deleting the ClusterRoleBinding and waiting for it to disappear")
			crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cs-app2-rolebinding"}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, crb))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "cs-app2-rolebinding"}, crb)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ClusterRoleBinding still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterRoleBinding should be fully removed")

			By("deleting the cs-australiaeast member cluster and waiting for it to disappear")
			mc := &clusterv1beta1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Name: "cs-australiaeast"}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, mc))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "cs-australiaeast"}, mc)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("MemberCluster still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "MemberCluster should be fully removed")
		})
	})

	Context("updating a cluster placement policy to target different clusters", Ordered, func() {
		BeforeAll(func() {
			By("creating a ClusterRole")
			cr := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cs-app3-role",
				},
				Rules: []rbacv1.PolicyRule{
					{
						APIGroups: []string{""},
						Resources: []string{"pods"},
						Verbs:     []string{"get", "list"},
					},
				},
			}
			Expect(hubClient.Create(ctx, cr)).To(Succeed())

			By("creating a ClusterRoleBinding")
			crb := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cs-app3-rolebinding",
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName,
					Kind:     "ClusterRole",
					Name:     "cs-app3-role",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:     rbacv1.UserKind,
						APIGroup: rbacv1.GroupName,
						Name:     "cs-app3-user",
					},
				},
			}
			Expect(hubClient.Create(ctx, crb)).To(Succeed())

			By("creating 2 member clusters in cs-useast-c and cs-uswest-c")
			clusters := []struct {
				name   string
				region string
			}{
				{"cs-useast2", "cs-useast-c"},
				{"cs-uswest", "cs-uswest-c"},
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

			By("creating the ClusterPlacementPolicy targeting cs-useast-c and cs-uswest-c")
			placement := &placementv1alpha1.ClusterPlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name: "my-cluster-placement-3",
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-useast-c"}}}},
						{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-uswest-c"}}}},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Name:       "cs-app3-role",
							APIGroup:   rbacv1.GroupName,
							APIVersion: "v1",
							Kind:       "ClusterRole",
						},
						{
							Name:       "cs-app3-rolebinding",
							APIGroup:   rbacv1.GroupName,
							APIVersion: "v1",
							Kind:       "ClusterRoleBinding",
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, placement)).To(Succeed())
		})

		It("should create 2 cluster placement bindings for the cluster placement policy", func() {
			By("waiting for 2 cluster placement bindings to be created")
			var bindings []placementv1alpha1.ClusterPlacementBinding
			Eventually(func() (int, error) {
				var err error
				bindings, err = listClusterBindingsOwnedBy("my-cluster-placement-3")
				if err != nil {
					return 0, err
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(Equal(2), "2 cluster placement bindings should be created")

			By("computing expected cluster selector hashes")
			useastTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "cs-useast-c"}}
			uswestTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "cs-uswest-c"}}
			useastHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{useastTerm}})
			Expect(err).NotTo(HaveOccurred())
			uswestHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uswestTerm}})
			Expect(err).NotTo(HaveOccurred())

			snapshotRevision := "my-cluster-placement-3-resource-snapshot-0"
			wantBindings := []placementv1alpha1.ClusterPlacementBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-cluster-placement-3-cs-useast2",
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-cluster-placement-3",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: useastHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-cluster-placement-3",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{useastTerm}}},
						ClusterName:          "cs-useast2",
						ResourceSnapshotName: snapshotRevision,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-cluster-placement-3-cs-uswest",
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-cluster-placement-3",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: uswestHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-cluster-placement-3",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uswestTerm}}},
						ClusterName:          "cs-uswest",
						ResourceSnapshotName: snapshotRevision,
					},
				},
			}

			By("verifying the cluster placement bindings match the expected state")
			if diff := cmp.Diff(bindings, wantBindings,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementBindingStatus{}, "Conditions"),
				cmpopts.SortSlices(func(a, b placementv1alpha1.ClusterPlacementBinding) bool {
					return a.Name < b.Name
				}),
			); diff != "" {
				Fail(fmt.Sprintf("cluster placement bindings mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should set the cluster placement policy status correctly", func() {
			wantStatus := placementv1alpha1.PlacementPolicyStatus{
				LatestResourceRevisionName: ptr.To("my-cluster-placement-3-resource-snapshot-0"),
				Conditions: []metav1.Condition{
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeScheduled,
						Status: metav1.ConditionTrue,
						Reason: "FoundClustersForAllSelectors",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeSynchronized,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveSynchronizedResources",
					},
					{
						Type:   placementv1alpha1.PlacementPolicyCondTypeAvailable,
						Status: metav1.ConditionFalse,
						Reason: "NotAllBindingsHaveResourcesAvailable",
					},
				},
			}

			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement-3"}, placement); err != nil {
					return err.Error()
				}
				return cmp.Diff(placement.Status, wantStatus,
					cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicyStatus{}, "BindingManager"),
					cmpopts.IgnoreFields(metav1.Condition{}, "ObservedGeneration", "LastTransitionTime", "Message"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"cluster placement status should be set correctly")
		})

		It("should update the cluster placement to select cs-uscentral-c and cs-uswest-c", func() {
			By("fetching the current cluster placement")
			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement-3"}, placement)).To(Succeed())

			By("updating the cluster selectors to cs-uscentral-c and cs-uswest-c")
			updatedPlacement := placement.DeepCopy()
			updatedPlacement.Spec.ClusterSelectors = []placementv1alpha1.ClusterSelector{
				{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-uscentral-c"}}}},
				{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-uswest-c"}}}},
			}
			Expect(hubClient.Update(ctx, updatedPlacement)).To(Succeed())
		})

		It("should delete the stale cs-useast2 binding and retain the cs-uswest binding", func() {
			uswestTerm := placementv1alpha1.ClusterLabelAndPropertySelectorTerm{MatchLabels: map[string]string{"region": "cs-uswest-c"}}
			uswestHash, err := resource.HashOf(&placementv1alpha1.ClusterSelector{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uswestTerm}})
			Expect(err).NotTo(HaveOccurred())

			wantBindings := []placementv1alpha1.ClusterPlacementBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "my-cluster-placement-3-cs-uswest",
						Labels: map[string]string{
							placementv1alpha1.PlacementBindingOwnedByLabelKey: "my-cluster-placement-3",
						},
						Annotations: map[string]string{
							clusterSelectorHashAnnotationKey: uswestHash,
						},
					},
					Spec: placementv1alpha1.PlacementBindingSpec{
						PlacementPolicyName:  "my-cluster-placement-3",
						ClusterSelectors:     []placementv1alpha1.ClusterSelectorWithTermsOnly{{Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{uswestTerm}}},
						ClusterName:          "cs-uswest",
						ResourceSnapshotName: "my-cluster-placement-3-resource-snapshot-0",
					},
				},
			}

			Eventually(func() string {
				bindings, err := listClusterBindingsOwnedBy("my-cluster-placement-3")
				if err != nil {
					return err.Error()
				}
				return cmp.Diff(bindings, wantBindings,
					cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
					cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
					cmpopts.IgnoreFields(placementv1alpha1.PlacementBindingStatus{}, "Conditions"),
				)
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(),
				"only the cs-uswest binding should remain after the selector update")
		})

		It("should create a cluster claim for the cs-uscentral-c selector", func() {
			placementRef := &placementv1alpha1.ClusterPlacementPolicy{ObjectMeta: metav1.ObjectMeta{Name: "my-cluster-placement-3"}}
			claimName := clusterClaimName(placementRef)
			placementPolicyRef := placementPolicyObjectRef(placementRef)

			wantClaim := &placementv1alpha1.ClusterClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: claimName,
				},
				Spec: placementv1alpha1.ClusterClaimSpec{
					PlacementPolicyRef:   &placementPolicyRef,
					ClusterSelectorTerms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{{MatchLabels: map[string]string{"region": "cs-uscentral-c"}}},
				},
			}

			By("waiting for the ClusterClaim to be created")
			claim := &placementv1alpha1.ClusterClaim{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Name: claimName}, claim)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterClaim should be created")

			By("verifying the ClusterClaim matches the expected state")
			if diff := cmp.Diff(claim, wantClaim,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "OwnerReferences"),
				cmpopts.IgnoreFields(placementv1alpha1.ClusterClaimStatus{}, "Conditions", "LastObservedMostRecentClusterCreationTimestamp", "ProvisionedClusterName"),
			); diff != "" {
				Fail(fmt.Sprintf("cluster claim mismatch (-got, +want):\n%s", diff))
			}
		})

		AfterAll(func() {
			By("deleting the ClusterPlacementPolicy")
			placement := &placementv1alpha1.ClusterPlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement-3"}, placement)).To(Succeed())
			Expect(hubClient.Delete(ctx, placement)).To(Succeed())

			By("waiting for the ClusterPlacementPolicy to be fully removed")
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "my-cluster-placement-3"}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("cluster placement still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterPlacementPolicy should be fully removed")

			By("verifying all resource snapshots owned by the cluster placement are gone")
			Eventually(func() (int, error) {
				snapshotList := &placementv1alpha1.ClusterPlacementResourceSnapshotList{}
				if err := hubClient.List(ctx, snapshotList,
					client.MatchingLabels{placementv1alpha1.PlacementResourceSnapshotOwnedByLabelKey: "my-cluster-placement-3"},
				); err != nil {
					return 0, err
				}
				for i := range snapshotList.Items {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &snapshotList.Items[i])); err != nil {
						return len(snapshotList.Items), err
					}
				}
				return len(snapshotList.Items), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all resource snapshots should be cleaned up")

			By("verifying all cluster placement bindings owned by the cluster placement are gone")
			Eventually(func() (int, error) {
				bindings, err := listClusterBindingsOwnedBy("my-cluster-placement-3")
				if err != nil {
					return 0, err
				}
				for i := range bindings {
					if err := client.IgnoreNotFound(hubClient.Delete(ctx, &bindings[i])); err != nil {
						return len(bindings), err
					}
				}
				return len(bindings), nil
			}, eventuallyDuration, eventuallyInterval).Should(BeZero(), "all cluster placement bindings should be cleaned up")

			By("cleaning up any leftover cluster claim (cluster claims are cluster-scoped and are not garbage-collected with the placement)")
			claim := &placementv1alpha1.ClusterClaim{ObjectMeta: metav1.ObjectMeta{
				Name: clusterClaimName(&placementv1alpha1.ClusterPlacementPolicy{ObjectMeta: metav1.ObjectMeta{Name: "my-cluster-placement-3"}}),
			}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, claim))).To(Succeed())

			By("deleting the ClusterRole and waiting for it to disappear")
			cr := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cs-app3-role"}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, cr))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "cs-app3-role"}, cr)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ClusterRole still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterRole should be fully removed")

			By("deleting the ClusterRoleBinding and waiting for it to disappear")
			crb := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "cs-app3-rolebinding"}}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, crb))).To(Succeed())
			Eventually(func() error {
				err := hubClient.Get(ctx, types.NamespacedName{Name: "cs-app3-rolebinding"}, crb)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ClusterRoleBinding still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterRoleBinding should be fully removed")

			By("deleting the cs-useast2 and cs-uswest member clusters and waiting for them to disappear")
			for _, name := range []string{"cs-useast2", "cs-uswest"} {
				mc := &clusterv1beta1.MemberCluster{ObjectMeta: metav1.ObjectMeta{Name: name}}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, mc))).To(Succeed())
				Eventually(func() error {
					err := hubClient.Get(ctx, types.NamespacedName{Name: name}, mc)
					if apierrors.IsNotFound(err) {
						return nil
					}
					if err != nil {
						return err
					}
					return fmt.Errorf("MemberCluster %s still exists", name)
				}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "MemberCluster "+name+" should be fully removed")
			}
		})
	})
})
