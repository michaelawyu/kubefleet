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

package annotatedobjectwatcher

import (
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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
)

const (
	// Eventually polling interval and timeout.
	eventuallyInterval = 500 * time.Millisecond
	eventuallyDuration = 10 * time.Second
)

var _ = Describe("deployment operations", Ordered, func() {
	Context("creating a new deployment with annotation, updating the annotation, and removing the annotation", Ordered, func() {
		deployName := "my-app"
		placementPolicyName := derivedPlacementPolicyName("Deployment", deployName)

		BeforeAll(func() {
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deployName,
					Namespace: workNameName,
					Annotations: map[string]string{
						placementv1alpha1.ClusterSelectorsAnnotation: "env=useast;env=eastasia;env=uksouth",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": deployName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": deployName},
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
		})

		AfterAll(func() {
			// Forcefully strip the finalizer and delete the deployment, in case the controller
			// did not remove it (e.g., if an earlier It node failed before removing the annotation).
			Eventually(func() error {
				deploy := &appsv1.Deployment{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: deployName}, deploy); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				// Strip the finalizer so the API server can honor the deletion.
				if controllerutil.ContainsFinalizer(deploy, derivedPlacementPolicyCleanupFinalizer) {
					controllerutil.RemoveFinalizer(deploy, derivedPlacementPolicyCleanupFinalizer)
					if err := hubClient.Update(ctx, deploy); err != nil {
						return err
					}
				}
				if deploy.DeletionTimestamp.IsZero() {
					if err := hubClient.Delete(ctx, deploy); err != nil {
						return err
					}
				}
				return fmt.Errorf("deployment still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "deployment should be fully deleted")

			// Always issue a Delete on the PlacementPolicy (idempotent — ignore not-found),
			// then wait for it to be fully gone.
			Eventually(func() error {
				placement := &placementv1alpha1.PlacementPolicy{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if err := hubClient.Delete(ctx, placement); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
				return fmt.Errorf("PlacementPolicy still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be fully deleted")
		})

		It("should add the finalizer to the deployment and create the PlacementPolicy", func() {
			By("waiting for the finalizer to be added to the deployment")
			deploy := &appsv1.Deployment{}
			Eventually(func() ([]string, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: deployName}, deploy); err != nil {
					return nil, err
				}
				return deploy.Finalizers, nil
			}, eventuallyDuration, eventuallyInterval).Should(ContainElement(derivedPlacementPolicyCleanupFinalizer), "deployment should have the cleanup finalizer")

			By("waiting for the PlacementPolicy to be created")
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be created")

			By("verifying the PlacementPolicy spec matches the expected state")
			// The controller sorts the target regions alphabetically before building selectors.
			wantPlacement := &placementv1alpha1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      placementPolicyName,
					Namespace: workNameName,
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "eastasia"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "uksouth"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "useast"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Kind:       "Deployment",
							APIGroup:   "apps",
							APIVersion: "v1",
							Name:       deployName,
						},
					},
				},
			}
			if diff := cmp.Diff(placement, wantPlacement,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicySpec{}, "ResourceRevisionHistoryLimit", "SyncStrategy", "Tolerations"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicy{}, "Status"),
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
			); diff != "" {
				Fail(fmt.Sprintf("PlacementPolicy mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should update the PlacementPolicy when the annotation changes", func() {
			By("updating the deployment annotation to drop useast and uksouth and add uscentral")
			deploy := &appsv1.Deployment{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: deployName}, deploy)).To(Succeed())
			updatedDeploy := deploy.DeepCopy()
			updatedDeploy.Annotations[placementv1alpha1.ClusterSelectorsAnnotation] = "env=eastasia;env=uscentral"
			Expect(hubClient.Update(ctx, updatedDeploy)).To(Succeed())

			By("waiting for the PlacementPolicy to be updated with the new regions")
			// The controller sorts the target regions alphabetically: eastasia, uscentral.
			wantPlacement := &placementv1alpha1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      placementPolicyName,
					Namespace: workNameName,
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "eastasia"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "uscentral"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Kind:       "Deployment",
							APIGroup:   "apps",
							APIVersion: "v1",
							Name:       deployName,
						},
					},
				},
			}
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() ([]placementv1alpha1.ClusterSelector, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement); err != nil {
					return nil, err
				}
				return placement.Spec.ClusterSelectors, nil
			}, eventuallyDuration, eventuallyInterval).Should(HaveLen(2), "PlacementPolicy should have exactly two cluster selectors")

			if diff := cmp.Diff(placement, wantPlacement,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicySpec{}, "ResourceRevisionHistoryLimit", "SyncStrategy", "Tolerations"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicy{}, "Status"),
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
			); diff != "" {
				Fail(fmt.Sprintf("PlacementPolicy mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should delete the PlacementPolicy when the annotation is removed", func() {
			By("removing the place-to annotation from the deployment")
			deploy := &appsv1.Deployment{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: deployName}, deploy)).To(Succeed())
			updatedDeploy := deploy.DeepCopy()
			delete(updatedDeploy.Annotations, placementv1alpha1.ClusterSelectorsAnnotation)
			Expect(hubClient.Update(ctx, updatedDeploy)).To(Succeed())

			By("waiting for the PlacementPolicy to be deleted")
			Eventually(func() error {
				placement := &placementv1alpha1.PlacementPolicy{}
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("PlacementPolicy still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be deleted")
		})
	})

	Context("creating a deployment with annotation then deleting the deployment", Ordered, func() {
		deployName := "my-app-delete"
		placementPolicyName := derivedPlacementPolicyName("Deployment", deployName)

		BeforeAll(func() {
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deployName,
					Namespace: workNameName,
					Annotations: map[string]string{
						placementv1alpha1.ClusterSelectorsAnnotation: "env=useast;env=eastasia",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: ptr.To(int32(1)),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"app": deployName},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": deployName},
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
		})

		AfterAll(func() {
			// Forcefully strip the finalizer and delete the deployment, in case the controller
			// did not remove it (e.g., if an It node failed before the deletion test ran).
			Eventually(func() error {
				deploy := &appsv1.Deployment{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: deployName}, deploy); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if controllerutil.ContainsFinalizer(deploy, derivedPlacementPolicyCleanupFinalizer) {
					controllerutil.RemoveFinalizer(deploy, derivedPlacementPolicyCleanupFinalizer)
					if err := hubClient.Update(ctx, deploy); err != nil {
						return err
					}
				}
				if deploy.DeletionTimestamp.IsZero() {
					if err := hubClient.Delete(ctx, deploy); err != nil {
						return err
					}
				}
				return fmt.Errorf("deployment still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "deployment should be fully deleted")

			// Always issue a Delete on the PlacementPolicy (idempotent — ignore not-found),
			// then wait for it to be fully gone.
			Eventually(func() error {
				placement := &placementv1alpha1.PlacementPolicy{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if err := hubClient.Delete(ctx, placement); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
				return fmt.Errorf("PlacementPolicy still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be fully deleted")
		})

		It("should add the finalizer to the deployment and create the PlacementPolicy", func() {
			By("waiting for the finalizer to be added to the deployment")
			deploy := &appsv1.Deployment{}
			Eventually(func() ([]string, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: deployName}, deploy); err != nil {
					return nil, err
				}
				return deploy.Finalizers, nil
			}, eventuallyDuration, eventuallyInterval).Should(ContainElement(derivedPlacementPolicyCleanupFinalizer), "deployment should have the cleanup finalizer")

			By("waiting for the PlacementPolicy to be created")
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be created")
		})

		It("should delete the PlacementPolicy and remove the finalizer when the deployment is deleted", func() {
			By("deleting the deployment")
			deploy := &appsv1.Deployment{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: deployName}, deploy)).To(Succeed())
			Expect(hubClient.Delete(ctx, deploy)).To(Succeed())

			By("waiting for the controller to delete the PlacementPolicy")
			Eventually(func() error {
				placement := &placementv1alpha1.PlacementPolicy{}
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("PlacementPolicy still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be deleted by the controller")

			By("waiting for the controller to remove the finalizer and fully delete the deployment")
			Eventually(func() error {
				deploy := &appsv1.Deployment{}
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: deployName}, deploy)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("deployment still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "deployment should be fully deleted after the controller removes its finalizer")
		})
	})
})

var _ = Describe("orasmanifests operations", Ordered, func() {
	Context("creating a new ORASManifests object with annotation, updating the annotation, and removing the annotation", Ordered, func() {
		orasManifestsName := "my-manifests"
		placementPolicyName := derivedPlacementPolicyName("ORASManifests", orasManifestsName)

		BeforeAll(func() {
			orasManifests := &placementv1alpha1.ORASManifests{
				ObjectMeta: metav1.ObjectMeta{
					Name:      orasManifestsName,
					Namespace: workNameName,
					Annotations: map[string]string{
						placementv1alpha1.ClusterSelectorsAnnotation: "env=useast;env=eastasia;env=uksouth",
					},
				},
				Spec: placementv1alpha1.ORASManifestsSpec{
					OCIArtifact: &placementv1alpha1.OCIArtifact{
						URL: "example.azurecr.io/repo/artifact",
					},
				},
			}
			Expect(hubClient.Create(ctx, orasManifests)).To(Succeed())
		})

		AfterAll(func() {
			// Forcefully strip the finalizer and delete the ORASManifests object, in case the
			// controller did not remove it (e.g., if an earlier It node failed before removing the
			// annotation).
			Eventually(func() error {
				orasManifests := &placementv1alpha1.ORASManifests{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: orasManifestsName}, orasManifests); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				// Strip the finalizer so the API server can honor the deletion.
				if controllerutil.ContainsFinalizer(orasManifests, derivedPlacementPolicyCleanupFinalizer) {
					controllerutil.RemoveFinalizer(orasManifests, derivedPlacementPolicyCleanupFinalizer)
					if err := hubClient.Update(ctx, orasManifests); err != nil {
						return err
					}
				}
				if orasManifests.DeletionTimestamp.IsZero() {
					if err := hubClient.Delete(ctx, orasManifests); err != nil {
						return err
					}
				}
				return fmt.Errorf("ORASManifests object still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ORASManifests object should be fully deleted")

			// Always issue a Delete on the PlacementPolicy (idempotent — ignore not-found),
			// then wait for it to be fully gone.
			Eventually(func() error {
				placement := &placementv1alpha1.PlacementPolicy{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if err := hubClient.Delete(ctx, placement); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
				return fmt.Errorf("PlacementPolicy still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be fully deleted")
		})

		It("should add the finalizer to the ORASManifests object and create the PlacementPolicy", func() {
			By("waiting for the finalizer to be added to the ORASManifests object")
			orasManifests := &placementv1alpha1.ORASManifests{}
			Eventually(func() ([]string, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: orasManifestsName}, orasManifests); err != nil {
					return nil, err
				}
				return orasManifests.Finalizers, nil
			}, eventuallyDuration, eventuallyInterval).Should(ContainElement(derivedPlacementPolicyCleanupFinalizer), "ORASManifests object should have the cleanup finalizer")

			By("waiting for the PlacementPolicy to be created")
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be created")

			By("verifying the PlacementPolicy spec matches the expected state")
			// The controller sorts the target regions alphabetically before building selectors.
			wantPlacement := &placementv1alpha1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      placementPolicyName,
					Namespace: workNameName,
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "eastasia"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "uksouth"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "useast"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Kind:       "ORASManifests",
							APIGroup:   "placement.kubefleet.dev",
							APIVersion: "v1alpha1",
							Name:       orasManifestsName,
						},
					},
				},
			}
			if diff := cmp.Diff(placement, wantPlacement,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicySpec{}, "ResourceRevisionHistoryLimit", "SyncStrategy", "Tolerations"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicy{}, "Status"),
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
			); diff != "" {
				Fail(fmt.Sprintf("PlacementPolicy mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should update the PlacementPolicy when the annotation changes", func() {
			By("updating the ORASManifests annotation to drop useast and uksouth and add uscentral")
			orasManifests := &placementv1alpha1.ORASManifests{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: orasManifestsName}, orasManifests)).To(Succeed())
			updatedORASManifests := orasManifests.DeepCopy()
			updatedORASManifests.Annotations[placementv1alpha1.ClusterSelectorsAnnotation] = "env=eastasia;env=uscentral"
			Expect(hubClient.Update(ctx, updatedORASManifests)).To(Succeed())

			By("waiting for the PlacementPolicy to be updated with the new regions")
			// The controller sorts the target regions alphabetically: eastasia, uscentral.
			wantPlacement := &placementv1alpha1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      placementPolicyName,
					Namespace: workNameName,
				},
				Spec: placementv1alpha1.PlacementPolicySpec{
					ClusterSelectors: []placementv1alpha1.ClusterSelector{
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "eastasia"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
						{
							Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
								{MatchLabels: map[string]string{"topology.kubernetes.io/region": "uscentral"}},
							},
							Count:           ptr.To(intstr.FromInt(1)),
							WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
						},
					},
					ResourceSelectors: []placementv1alpha1.ResourceSelector{
						{
							Kind:       "ORASManifests",
							APIGroup:   "placement.kubefleet.dev",
							APIVersion: "v1alpha1",
							Name:       orasManifestsName,
						},
					},
				},
			}
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() ([]placementv1alpha1.ClusterSelector, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement); err != nil {
					return nil, err
				}
				return placement.Spec.ClusterSelectors, nil
			}, eventuallyDuration, eventuallyInterval).Should(HaveLen(2), "PlacementPolicy should have exactly two cluster selectors")

			if diff := cmp.Diff(placement, wantPlacement,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicySpec{}, "ResourceRevisionHistoryLimit", "SyncStrategy", "Tolerations"),
				cmpopts.IgnoreFields(placementv1alpha1.PlacementPolicy{}, "Status"),
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
			); diff != "" {
				Fail(fmt.Sprintf("PlacementPolicy mismatch (-got, +want):\n%s", diff))
			}
		})

		It("should delete the PlacementPolicy when the annotation is removed", func() {
			By("removing the place-to annotation from the ORASManifests object")
			orasManifests := &placementv1alpha1.ORASManifests{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: orasManifestsName}, orasManifests)).To(Succeed())
			updatedORASManifests := orasManifests.DeepCopy()
			delete(updatedORASManifests.Annotations, placementv1alpha1.ClusterSelectorsAnnotation)
			Expect(hubClient.Update(ctx, updatedORASManifests)).To(Succeed())

			By("waiting for the PlacementPolicy to be deleted")
			Eventually(func() error {
				placement := &placementv1alpha1.PlacementPolicy{}
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("PlacementPolicy still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be deleted")
		})
	})

	Context("creating an ORASManifests object with annotation then deleting the object", Ordered, func() {
		orasManifestsName := "my-manifests-delete"
		placementPolicyName := derivedPlacementPolicyName("ORASManifests", orasManifestsName)

		BeforeAll(func() {
			orasManifests := &placementv1alpha1.ORASManifests{
				ObjectMeta: metav1.ObjectMeta{
					Name:      orasManifestsName,
					Namespace: workNameName,
					Annotations: map[string]string{
						placementv1alpha1.ClusterSelectorsAnnotation: "env=useast;env=eastasia",
					},
				},
				Spec: placementv1alpha1.ORASManifestsSpec{
					OCIArtifact: &placementv1alpha1.OCIArtifact{
						URL: "example.azurecr.io/repo/artifact",
					},
				},
			}
			Expect(hubClient.Create(ctx, orasManifests)).To(Succeed())
		})

		AfterAll(func() {
			// Forcefully strip the finalizer and delete the ORASManifests object, in case the
			// controller did not remove it (e.g., if an It node failed before the deletion test ran).
			Eventually(func() error {
				orasManifests := &placementv1alpha1.ORASManifests{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: orasManifestsName}, orasManifests); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if controllerutil.ContainsFinalizer(orasManifests, derivedPlacementPolicyCleanupFinalizer) {
					controllerutil.RemoveFinalizer(orasManifests, derivedPlacementPolicyCleanupFinalizer)
					if err := hubClient.Update(ctx, orasManifests); err != nil {
						return err
					}
				}
				if orasManifests.DeletionTimestamp.IsZero() {
					if err := hubClient.Delete(ctx, orasManifests); err != nil {
						return err
					}
				}
				return fmt.Errorf("ORASManifests object still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ORASManifests object should be fully deleted")

			// Always issue a Delete on the PlacementPolicy (idempotent — ignore not-found),
			// then wait for it to be fully gone.
			Eventually(func() error {
				placement := &placementv1alpha1.PlacementPolicy{}
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement); err != nil {
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}
				if err := hubClient.Delete(ctx, placement); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
				return fmt.Errorf("PlacementPolicy still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be fully deleted")
		})

		It("should add the finalizer to the ORASManifests object and create the PlacementPolicy", func() {
			By("waiting for the finalizer to be added to the ORASManifests object")
			orasManifests := &placementv1alpha1.ORASManifests{}
			Eventually(func() ([]string, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: orasManifestsName}, orasManifests); err != nil {
					return nil, err
				}
				return orasManifests.Finalizers, nil
			}, eventuallyDuration, eventuallyInterval).Should(ContainElement(derivedPlacementPolicyCleanupFinalizer), "ORASManifests object should have the cleanup finalizer")

			By("waiting for the PlacementPolicy to be created")
			placement := &placementv1alpha1.PlacementPolicy{}
			Eventually(func() error {
				return hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement)
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be created")
		})

		It("should delete the PlacementPolicy and remove the finalizer when the ORASManifests object is deleted", func() {
			By("deleting the ORASManifests object")
			orasManifests := &placementv1alpha1.ORASManifests{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: orasManifestsName}, orasManifests)).To(Succeed())
			Expect(hubClient.Delete(ctx, orasManifests)).To(Succeed())

			By("waiting for the controller to delete the PlacementPolicy")
			Eventually(func() error {
				placement := &placementv1alpha1.PlacementPolicy{}
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: placementPolicyName}, placement)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("PlacementPolicy still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be deleted by the controller")

			By("waiting for the controller to remove the finalizer and fully delete the ORASManifests object")
			Eventually(func() error {
				orasManifests := &placementv1alpha1.ORASManifests{}
				err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNameName, Name: orasManifestsName}, orasManifests)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("ORASManifests object still exists")
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ORASManifests object should be fully deleted after the controller removes its finalizer")
		})
	})
})
