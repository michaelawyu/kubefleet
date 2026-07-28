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

package placementresourcesnapshot

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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	localregistry "github.com/kubefleet-dev/kubefleet/test/oci"
)

const (
	// Eventually polling interval and timeout.
	eventuallyInterval = 500 * time.Millisecond
	eventuallyDuration = 10 * time.Second
)

var _ = Describe("processing placement resource snapshot requests", func() {
	Context("can request a new snapshot and then request for an updated snapshot (hub manifests)", Ordered, func() {
		deployName := "web-app-hub-manifests"
		cmName := "app-config-hub-manifests"
		placementName := "web-app-hub-manifests"

		BeforeAll(func() {
			By("creating the Deployment")
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      deployName,
					Namespace: workNSName,
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

			By("creating the ConfigMap")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: workNSName,
				},
				Data: map[string]string{
					"key": "value",
				},
			}
			Expect(hubClient.Create(ctx, cm)).To(Succeed())

			By("creating the PlacementPolicy referencing the Deployment and the ConfigMap")
			placement := &experimentalv1beta1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      placementName,
					Namespace: workNSName,
				},
				Spec: experimentalv1beta1.PlacementPolicySpec{
					ClusterSelectors: []experimentalv1beta1.ClusterSelector{
						{
							Terms: []experimentalv1beta1.LabelAndClusterPropertySelectorTerm{
								{
									MatchLabels: map[string]string{"topology.kubernetes.io/region": "useast"},
								},
							},
						},
					},
					ResourceSelectors: []experimentalv1beta1.SameNamespacedObjectReference{
						{
							Kind:       "Deployment",
							APIGroup:   "apps",
							APIVersion: "v1",
							Name:       deployName,
						},
						{
							Kind:       "ConfigMap",
							APIGroup:   "",
							APIVersion: "v1",
							Name:       cmName,
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, placement)).To(Succeed())
		})

		AfterAll(func() {
			By("deleting the PlacementResourceSnapshotRequests")
			for _, reqName := range []string{"snapshot-req-1", "snapshot-req-2"} {
				req := &experimentalv1beta1.PlacementResourceSnapshotRequest{
					ObjectMeta: metav1.ObjectMeta{Name: reqName, Namespace: workNSName},
				}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, req))).To(Succeed())
				Eventually(func() bool {
					return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: reqName}, req))
				}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "PlacementResourceSnapshotRequest should be deleted")
			}

			By("deleting the PlacementResourceSnapshots")
			for _, revision := range []int{1, 2} {
				snapshotName := fmt.Sprintf("%s-%d", placementName, revision)
				snapshot := &experimentalv1beta1.PlacementResourceSnapshot{
					ObjectMeta: metav1.ObjectMeta{Name: snapshotName, Namespace: workNSName},
				}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, snapshot))).To(Succeed())
				Eventually(func() bool {
					return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: snapshotName}, snapshot))
				}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "PlacementResourceSnapshot should be deleted")
			}

			By("deleting the PlacementPolicy")
			placement := &experimentalv1beta1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: placementName, Namespace: workNSName},
			}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, placement))).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementName}, placement))
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "PlacementPolicy should be deleted")

			By("deleting the ConfigMap")
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: workNSName},
			}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, cm))).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: cmName}, cm))
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "ConfigMap should be deleted")

			By("deleting the Deployment")
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: deployName, Namespace: workNSName},
			}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, deploy))).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: deployName}, deploy))
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "Deployment should be deleted")
		})

		It("should complete the request and set the Completed condition to True", func() {
			reqName := "snapshot-req-1"

			By("creating the PlacementResourceSnapshotRequest")
			req := &experimentalv1beta1.PlacementResourceSnapshotRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      reqName,
					Namespace: workNSName,
				},
				Spec: experimentalv1beta1.PlacementResourceSnapshotRequestSpec{
					PlacementPolicyRef: experimentalv1beta1.SameNamespacedObjectReference{
						Kind:       "PlacementPolicy",
						APIGroup:   "experimental.kubefleet.dev",
						APIVersion: "v1beta1",
						Name:       placementName,
					},
				},
			}
			Expect(hubClient.Create(ctx, req)).To(Succeed())

			By("waiting for the request to be completed")
			wantConditions := []metav1.Condition{
				{
					Type:               experimentalv1beta1.PlacementResourceSnapshotRequestCondTypeCompleted,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: 1,
					Reason:             experimentalv1beta1.PlacementResourceSnapshotRequestCompletedReasonSuccess,
					Message:            "Successfully added new snapshot for the placement policy",
				},
			}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: reqName}, req); err != nil {
					return fmt.Sprintf("failed to get PlacementResourceSnapshotRequest: %v", err)
				}
				return cmp.Diff(wantConditions, req.Status.Conditions, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"))
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(), "request status conditions mismatch (-want, +got)")
		})

		It("should have created exactly one PlacementResourceSnapshot with the expected content", func() {
			By("listing PlacementResourceSnapshots for the placement policy")
			snapshotList := &experimentalv1beta1.PlacementResourceSnapshotList{}
			Expect(hubClient.List(ctx, snapshotList,
				client.MatchingLabels{experimentalv1beta1.ResourceSnapshotOwnedByLabelKey: placementName},
				client.InNamespace(workNSName),
			)).To(Succeed())
			Expect(snapshotList.Items).To(HaveLen(1), "expected exactly one PlacementResourceSnapshot")

			By("verifying the content of the snapshot")
			wantSnapshot := experimentalv1beta1.PlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-1", placementName),
					Namespace: workNSName,
					Labels: map[string]string{
						experimentalv1beta1.ResourceSnapshotOwnedByLabelKey:  placementName,
						experimentalv1beta1.ResourceSnapshotRevisionLabelKey: "1",
					},
					Annotations: map[string]string{
						experimentalv1beta1.ResourceSnapshotContentsHashAnnotationKey: "c44c9f0cebf3273fd38b63b7bd4cd85fcc13df710cfd9f5d5f1a87bc1e8fa30b",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: experimentalv1beta1.GroupVersion.String(),
							Kind:       "PlacementPolicy",
							Name:       placementName,
							Controller: ptr.To(true),
						},
					},
				},
				Spec: experimentalv1beta1.PlacementResourceSnapshotSpec{
					Resources: []experimentalv1beta1.ResourceContent{
						{
							Identifier: experimentalv1beta1.SameNamespacedObjectReference{
								Kind:       "Deployment",
								APIGroup:   "apps",
								APIVersion: "v1",
								Name:       deployName,
							},
						},
						{
							Identifier: experimentalv1beta1.SameNamespacedObjectReference{
								Kind:       "ConfigMap",
								APIGroup:   "",
								APIVersion: "v1",
								Name:       cmName,
							},
						},
					},
				},
			}
			if diff := cmp.Diff(wantSnapshot, snapshotList.Items[0],
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID", "ResourceVersion", "Generation", "CreationTimestamp", "ManagedFields"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "UID"),
				cmpopts.IgnoreFields(experimentalv1beta1.ResourceContent{}, "Manifest"),
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
			); diff != "" {
				Fail(fmt.Sprintf("PlacementResourceSnapshot mismatch (-want, +got):\n%s", diff))
			}
		})

		It("should confirm the latest snapshot is up to date using the manager methods", func() {
			By("fetching the PlacementPolicy")
			placement := &experimentalv1beta1.PlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementName}, placement)).To(Succeed())

			By("retrieving the latest resource snapshot via RetrieveLatestResourceSnapshot")
			snapshot, err := snapshotMgr.RetrieveLatestResourceSnapshot(ctx, placement)
			Expect(err).NotTo(HaveOccurred())
			Expect(snapshot).NotTo(BeNil())
			Expect(snapshot.Name).To(Equal(fmt.Sprintf("%s-1", placementName)))

			By("verifying the snapshot is up to date via IsResourceSnapshotUpToDate")
			upToDate, err := snapshotMgr.IsResourceSnapshotUpToDate(ctx, placement, snapshot)
			Expect(err).NotTo(HaveOccurred())
			Expect(upToDate).To(BeTrue())
		})

		It("should report the latest snapshot is no longer up to date after the ConfigMap is updated", func() {
			By("updating the ConfigMap with an additional piece of data")
			cm := &corev1.ConfigMap{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: cmName}, cm)).To(Succeed())
			cm.Data["foo"] = "bar"
			Expect(hubClient.Update(ctx, cm)).To(Succeed())

			By("fetching the PlacementPolicy")
			placement := &experimentalv1beta1.PlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementName}, placement)).To(Succeed())

			By("retrieving the latest resource snapshot, which is still revision 1")
			snapshot, err := snapshotMgr.RetrieveLatestResourceSnapshot(ctx, placement)
			Expect(err).NotTo(HaveOccurred())
			Expect(snapshot).NotTo(BeNil())
			Expect(snapshot.Name).To(Equal(fmt.Sprintf("%s-1", placementName)))

			By("verifying the snapshot is no longer up to date via IsResourceSnapshotUpToDate")
			upToDate, err := snapshotMgr.IsResourceSnapshotUpToDate(ctx, placement, snapshot)
			Expect(err).NotTo(HaveOccurred())
			Expect(upToDate).To(BeFalse())
		})

		It("should complete a new request to snapshot the updated resources", func() {
			reqName := "snapshot-req-2"

			By("creating the second PlacementResourceSnapshotRequest")
			req := &experimentalv1beta1.PlacementResourceSnapshotRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      reqName,
					Namespace: workNSName,
				},
				Spec: experimentalv1beta1.PlacementResourceSnapshotRequestSpec{
					PlacementPolicyRef: experimentalv1beta1.SameNamespacedObjectReference{
						Kind:       "PlacementPolicy",
						APIGroup:   "experimental.kubefleet.dev",
						APIVersion: "v1beta1",
						Name:       placementName,
					},
				},
			}
			Expect(hubClient.Create(ctx, req)).To(Succeed())

			By("waiting for the request to be completed")
			wantConditions := []metav1.Condition{
				{
					Type:               experimentalv1beta1.PlacementResourceSnapshotRequestCondTypeCompleted,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: 1,
					Reason:             experimentalv1beta1.PlacementResourceSnapshotRequestCompletedReasonSuccess,
					Message:            "Successfully added new snapshot for the placement policy",
				},
			}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: reqName}, req); err != nil {
					return fmt.Sprintf("failed to get PlacementResourceSnapshotRequest: %v", err)
				}
				return cmp.Diff(wantConditions, req.Status.Conditions, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"))
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(), "request status conditions mismatch (-want, +got)")
		})

		It("should have created a second PlacementResourceSnapshot and report it up to date", func() {
			By("listing PlacementResourceSnapshots for the placement policy")
			snapshotList := &experimentalv1beta1.PlacementResourceSnapshotList{}
			Expect(hubClient.List(ctx, snapshotList,
				client.MatchingLabels{experimentalv1beta1.ResourceSnapshotOwnedByLabelKey: placementName},
				client.InNamespace(workNSName),
			)).To(Succeed())
			Expect(snapshotList.Items).To(HaveLen(2), "expected exactly two PlacementResourceSnapshots")

			By("verifying the content of the second snapshot")
			wantSnapshot := experimentalv1beta1.PlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-2", placementName),
					Namespace: workNSName,
					Labels: map[string]string{
						experimentalv1beta1.ResourceSnapshotOwnedByLabelKey:  placementName,
						experimentalv1beta1.ResourceSnapshotRevisionLabelKey: "2",
					},
					Annotations: map[string]string{
						experimentalv1beta1.ResourceSnapshotContentsHashAnnotationKey: "64c159c562f9329b7f49031c6bac56b6051ae134a3d03a0a88db355121e16acb",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: experimentalv1beta1.GroupVersion.String(),
							Kind:       "PlacementPolicy",
							Name:       placementName,
							Controller: ptr.To(false),
						},
					},
				},
				Spec: experimentalv1beta1.PlacementResourceSnapshotSpec{
					Resources: []experimentalv1beta1.ResourceContent{
						{
							Identifier: experimentalv1beta1.SameNamespacedObjectReference{
								Kind:       "Deployment",
								APIGroup:   "apps",
								APIVersion: "v1",
								Name:       deployName,
							},
						},
						{
							Identifier: experimentalv1beta1.SameNamespacedObjectReference{
								Kind:       "ConfigMap",
								APIGroup:   "",
								APIVersion: "v1",
								Name:       cmName,
							},
						},
					},
				},
			}

			var gotSnapshot experimentalv1beta1.PlacementResourceSnapshot
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: fmt.Sprintf("%s-2", placementName)}, &gotSnapshot)).To(Succeed())
			if diff := cmp.Diff(wantSnapshot, gotSnapshot,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID", "ResourceVersion", "Generation", "CreationTimestamp", "ManagedFields"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "UID"),
				cmpopts.IgnoreFields(experimentalv1beta1.ResourceContent{}, "Manifest"),
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
			); diff != "" {
				Fail(fmt.Sprintf("PlacementResourceSnapshot mismatch (-want, +got):\n%s", diff))
			}

			By("fetching the PlacementPolicy")
			placement := &experimentalv1beta1.PlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementName}, placement)).To(Succeed())

			By("retrieving the latest resource snapshot, which is now revision 2")
			snapshot, err := snapshotMgr.RetrieveLatestResourceSnapshot(ctx, placement)
			Expect(err).NotTo(HaveOccurred())
			Expect(snapshot).NotTo(BeNil())
			Expect(snapshot.Name).To(Equal(fmt.Sprintf("%s-2", placementName)))

			By("verifying the new snapshot is up to date via IsResourceSnapshotUpToDate")
			upToDate, err := snapshotMgr.IsResourceSnapshotUpToDate(ctx, placement, snapshot)
			Expect(err).NotTo(HaveOccurred())
			Expect(upToDate).To(BeTrue())
		})
	})

	Context("can request a new snapshot and then request for an updated snapshot (ORAS manifests)", Ordered, func() {
		orasManifestsName := "web-app-oras"
		ociSecretName := "local-registry-access"
		placementName := "web-app-oras"

		BeforeAll(func() {
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

			By("creating the ORASManifests object referencing the OCI artifact")
			orasManifests := &experimentalv1beta1.ORASManifests{
				ObjectMeta: metav1.ObjectMeta{
					Name:      orasManifestsName,
					Namespace: workNSName,
				},
				Spec: experimentalv1beta1.ORASManifestsSpec{
					OCIArtifact: &experimentalv1beta1.OCIArtifact{
						URL: localregistry.RawManifestsArtifactURL,
						Ref: &experimentalv1beta1.OCIArtifactReference{
							Tag: localregistry.RawManifestsArtifactTag,
						},
						AuthProvider: &experimentalv1beta1.OCIArtifactAuthProvider{
							Type: experimentalv1beta1.AuthProviderTypeGeneric,
							SecretRef: &experimentalv1beta1.CrossNamespaceObjectReference{
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
			Expect(hubClient.Create(ctx, orasManifests)).To(Succeed())

			By("creating the PlacementPolicy selecting the ORASManifests")
			placement := &experimentalv1beta1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      placementName,
					Namespace: workNSName,
				},
				Spec: experimentalv1beta1.PlacementPolicySpec{
					ClusterSelectors: []experimentalv1beta1.ClusterSelector{
						{
							Terms: []experimentalv1beta1.LabelAndClusterPropertySelectorTerm{
								{
									MatchLabels: map[string]string{"topology.kubernetes.io/region": "useast"},
								},
							},
						},
					},
					ResourceSelectors: []experimentalv1beta1.SameNamespacedObjectReference{
						{
							Kind:       "ORASManifests",
							APIGroup:   experimentalv1beta1.GroupVersion.Group,
							APIVersion: experimentalv1beta1.GroupVersion.Version,
							Name:       orasManifestsName,
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, placement)).To(Succeed())
		})

		AfterAll(func() {
			By("deleting the PlacementResourceSnapshotRequests")
			for _, reqName := range []string{"snapshot-req-oras-1", "snapshot-req-oras-2"} {
				req := &experimentalv1beta1.PlacementResourceSnapshotRequest{
					ObjectMeta: metav1.ObjectMeta{Name: reqName, Namespace: workNSName},
				}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, req))).To(Succeed())
				Eventually(func() bool {
					return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: reqName}, req))
				}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "PlacementResourceSnapshotRequest should be deleted")
			}

			By("deleting the PlacementResourceSnapshots")
			for _, revision := range []int{1} {
				snapshotName := fmt.Sprintf("%s-%d", placementName, revision)
				snapshot := &experimentalv1beta1.PlacementResourceSnapshot{
					ObjectMeta: metav1.ObjectMeta{Name: snapshotName, Namespace: workNSName},
				}
				Expect(client.IgnoreNotFound(hubClient.Delete(ctx, snapshot))).To(Succeed())
				Eventually(func() bool {
					return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: snapshotName}, snapshot))
				}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "PlacementResourceSnapshot should be deleted")
			}

			By("deleting the PlacementPolicy")
			placement := &experimentalv1beta1.PlacementPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: placementName, Namespace: workNSName},
			}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, placement))).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementName}, placement))
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "PlacementPolicy should be deleted")

			By("deleting the ORASManifests object")
			orasManifests := &experimentalv1beta1.ORASManifests{
				ObjectMeta: metav1.ObjectMeta{Name: orasManifestsName, Namespace: workNSName},
			}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, orasManifests))).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: orasManifestsName}, orasManifests))
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "ORASManifests object should be deleted")

			By("deleting the image pull secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: ociSecretName, Namespace: workNSName},
			}
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, secret))).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: ociSecretName}, secret))
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(), "image pull secret should be deleted")
		})

		It("should err the request and set the Completed condition to False", func() {
			reqName := "snapshot-req-oras-1"

			By("creating the PlacementResourceSnapshotRequest")
			req := &experimentalv1beta1.PlacementResourceSnapshotRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      reqName,
					Namespace: workNSName,
				},
				Spec: experimentalv1beta1.PlacementResourceSnapshotRequestSpec{
					PlacementPolicyRef: experimentalv1beta1.SameNamespacedObjectReference{
						Kind:       "PlacementPolicy",
						APIGroup:   "experimental.kubefleet.dev",
						APIVersion: "v1beta1",
						Name:       placementName,
					},
				},
			}
			Expect(hubClient.Create(ctx, req)).To(Succeed())

			By("waiting for the request to be erred")
			wantConditions := []metav1.Condition{
				{
					Type:               experimentalv1beta1.PlacementResourceSnapshotRequestCondTypeCompleted,
					Status:             metav1.ConditionFalse,
					ObservedGeneration: 1,
					Reason:             experimentalv1beta1.PlacementResourceSnapshotRequestCompletedReasonErred,
				},
			}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: reqName}, req); err != nil {
					return fmt.Sprintf("failed to get PlacementResourceSnapshotRequest: %v", err)
				}
				return cmp.Diff(wantConditions, req.Status.Conditions,
					cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message"))
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(), "request status conditions mismatch (-want, +got)")
		})

		It("should populate the ORASManifests status with the resolved artifact digest", func() {
			By("resolving the artifact digest")
			digest, err := localregistry.Resolve(localregistry.RawManifestsArtifactURL, localregistry.RawManifestsArtifactTag)
			Expect(err).NotTo(HaveOccurred())

			By("fetching the ORASManifests object")
			orasManifests := &experimentalv1beta1.ORASManifests{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: orasManifestsName}, orasManifests)).To(Succeed())

			By("updating the ORASManifests status to include the resolved digest")
			meta.SetStatusCondition(&orasManifests.Status.Conditions, metav1.Condition{
				Type:               experimentalv1beta1.ORASManifestCondTypeResolved,
				Status:             metav1.ConditionTrue,
				ObservedGeneration: orasManifests.Generation,
				Reason:             "Resolved",
				Message:            "The OCI artifact has been successfully resolved",
			})
			orasManifests.Status.OCIArtifactDetails = &experimentalv1beta1.OCIArtifactDetails{
				URL:    localregistry.RawManifestsArtifactURL,
				Tag:    localregistry.RawManifestsArtifactTag,
				Digest: digest,
			}
			Expect(hubClient.Status().Update(ctx, orasManifests)).To(Succeed())
		})

		It("should complete a second request now that the ORASManifests object is resolved", func() {
			reqName := "snapshot-req-oras-2"

			By("creating the second PlacementResourceSnapshotRequest")
			req := &experimentalv1beta1.PlacementResourceSnapshotRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      reqName,
					Namespace: workNSName,
				},
				Spec: experimentalv1beta1.PlacementResourceSnapshotRequestSpec{
					PlacementPolicyRef: experimentalv1beta1.SameNamespacedObjectReference{
						Kind:       "PlacementPolicy",
						APIGroup:   "experimental.kubefleet.dev",
						APIVersion: "v1beta1",
						Name:       placementName,
					},
				},
			}
			Expect(hubClient.Create(ctx, req)).To(Succeed())

			By("waiting for the request to be completed")
			wantConditions := []metav1.Condition{
				{
					Type:               experimentalv1beta1.PlacementResourceSnapshotRequestCondTypeCompleted,
					Status:             metav1.ConditionTrue,
					ObservedGeneration: 1,
					Reason:             experimentalv1beta1.PlacementResourceSnapshotRequestCompletedReasonSuccess,
					Message:            "Successfully added new snapshot for the placement policy",
				},
			}
			Eventually(func() string {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: reqName}, req); err != nil {
					return fmt.Sprintf("failed to get PlacementResourceSnapshotRequest: %v", err)
				}
				return cmp.Diff(wantConditions, req.Status.Conditions, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"))
			}, eventuallyDuration, eventuallyInterval).Should(BeEmpty(), "request status conditions mismatch (-want, +got)")
		})

		It("should have created exactly one PlacementResourceSnapshot with the expected content", func() {
			By("resolving the artifact digest")
			digest, err := localregistry.Resolve(localregistry.RawManifestsArtifactURL, localregistry.RawManifestsArtifactTag)
			Expect(err).NotTo(HaveOccurred())

			By("listing PlacementResourceSnapshots for the placement policy")
			snapshotList := &experimentalv1beta1.PlacementResourceSnapshotList{}
			Expect(hubClient.List(ctx, snapshotList,
				client.MatchingLabels{experimentalv1beta1.ResourceSnapshotOwnedByLabelKey: placementName},
				client.InNamespace(workNSName),
			)).To(Succeed())
			Expect(snapshotList.Items).To(HaveLen(1), "expected exactly one PlacementResourceSnapshot")

			By("verifying the content of the snapshot")
			wantAdditionalInfo, err := json.Marshal(experimentalv1beta1.ORASManifestsAdditionalInfoForSnapshots{OCIArtifactDigest: digest})
			Expect(err).NotTo(HaveOccurred())
			wantSnapshot := experimentalv1beta1.PlacementResourceSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-1", placementName),
					Namespace: workNSName,
					Labels: map[string]string{
						experimentalv1beta1.ResourceSnapshotOwnedByLabelKey:  placementName,
						experimentalv1beta1.ResourceSnapshotRevisionLabelKey: "1",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: experimentalv1beta1.GroupVersion.String(),
							Kind:       "PlacementPolicy",
							Name:       placementName,
							Controller: ptr.To(true),
						},
					},
				},
				Spec: experimentalv1beta1.PlacementResourceSnapshotSpec{
					Resources: []experimentalv1beta1.ResourceContent{
						{
							Identifier: experimentalv1beta1.SameNamespacedObjectReference{
								Kind:       "ORASManifests",
								APIGroup:   experimentalv1beta1.GroupVersion.Group,
								APIVersion: experimentalv1beta1.GroupVersion.Version,
								Name:       orasManifestsName,
							},
							AdditionalInfo: map[string][]byte{
								experimentalv1beta1.ResourceSnapshotAdditionalInfoKeyORASManifests: wantAdditionalInfo,
							},
						},
					},
				},
			}
			// The snapshot content hash annotation embeds the resolved OCI artifact digest, which can vary
			// across pushes, so the annotations are excluded from the comparison.
			if diff := cmp.Diff(wantSnapshot, snapshotList.Items[0],
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "UID", "ResourceVersion", "Generation", "CreationTimestamp", "ManagedFields", "Annotations"),
				cmpopts.IgnoreFields(metav1.OwnerReference{}, "UID"),
				cmpopts.IgnoreFields(experimentalv1beta1.ResourceContent{}, "Manifest"),
				cmpopts.IgnoreTypes(metav1.TypeMeta{}),
			); diff != "" {
				Fail(fmt.Sprintf("PlacementResourceSnapshot mismatch (-want, +got):\n%s", diff))
			}
		})

		It("should confirm the latest snapshot is up to date using the manager methods", func() {
			By("fetching the PlacementPolicy")
			placement := &experimentalv1beta1.PlacementPolicy{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementName}, placement)).To(Succeed())

			By("retrieving the latest resource snapshot via RetrieveLatestResourceSnapshot")
			snapshot, err := snapshotMgr.RetrieveLatestResourceSnapshot(ctx, placement)
			Expect(err).NotTo(HaveOccurred())
			Expect(snapshot).NotTo(BeNil())
			Expect(snapshot.Name).To(Equal(fmt.Sprintf("%s-1", placementName)))

			By("verifying the snapshot is up to date via IsResourceSnapshotUpToDate")
			upToDate, err := snapshotMgr.IsResourceSnapshotUpToDate(ctx, placement, snapshot)
			Expect(err).NotTo(HaveOccurred())
			Expect(upToDate).To(BeTrue())
		})
	})
})
