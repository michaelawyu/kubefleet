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

package demoscenarios

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	rolloutv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/rollout/v1alpha1"
	kueuev1beta2 "github.com/kubefleet-dev/kubefleet/apis/kueue/v1beta2"
	"github.com/kubefleet-dev/kubefleet/pkg/utils"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/condition"
	localregistry "github.com/kubefleet-dev/kubefleet/test/oci"
)

// Eventually polling interval and timeout for this suite's scenarios. These are longer than the
// per-controller integration test suites', since a scenario here typically spans several controllers
// (e.g., annotatedobjectwatcher deriving a ClusterPlacementPolicy, then placementpolicy scheduling and
// reporting on it) reconciling in sequence.
const (
	eventuallyInterval = 500 * time.Millisecond
	eventuallyDuration = 30 * time.Second
)

// This scenario walks through placing a Kueue ResourceFlavor to the (single, simulated) "prod" member
// cluster purely by annotating it: the annotated object watcher controller derives a
// ClusterPlacementPolicy from the annotation, and the placement policy controller then schedules and
// reports on it.
var _ = Describe("placing a Kueue ResourceFlavor to the prod environment", Ordered, func() {
	const resourceFlavorName = "prod-on-demand"

	// clusterPlacementPolicyName mirrors derivedPlacementPolicyName from the annotated object watcher
	// controller (kind, lowercased, dash, object name); ResourceFlavor is cluster-scoped, so the
	// derived placement policy is a ClusterPlacementPolicy.
	clusterPlacementPolicyName := fmt.Sprintf("resourceflavor-%s", resourceFlavorName)

	It("should create a Kueue ResourceFlavor annotated for placement to the prod environment", func() {
		resourceFlavor := &kueuev1beta2.ResourceFlavor{
			ObjectMeta: metav1.ObjectMeta{
				Name: resourceFlavorName,
				Annotations: map[string]string{
					// Request annotation-based placement to any member cluster labeled env=prod; the
					// single simulated member cluster in this suite, "bravelion", carries that label.
					placementv1alpha1.ClusterSelectorsAnnotation: "env=prod",
				},
			},
		}
		Expect(hubClient.Create(ctx, resourceFlavor)).To(Succeed())
	})

	It("should have a ClusterPlacementPolicy created for the ResourceFlavor", func() {
		Eventually(func() error {
			policy := &placementv1alpha1.ClusterPlacementPolicy{}
			return hubClient.Get(ctx, types.NamespacedName{Name: clusterPlacementPolicyName}, policy)
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterPlacementPolicy should be created for the ResourceFlavor")
	})

	It("should fully update the ClusterPlacementPolicy status", func() {
		Eventually(func() error {
			policy := &placementv1alpha1.ClusterPlacementPolicy{}
			if err := hubClient.Get(ctx, types.NamespacedName{Name: clusterPlacementPolicyName}, policy); err != nil {
				return err
			}

			for _, condType := range []string{
				placementv1alpha1.PlacementPolicyCondTypeScheduled,
				placementv1alpha1.PlacementPolicyCondTypeSynchronized,
				placementv1alpha1.PlacementPolicyCondTypeAvailable,
			} {
				cond := meta.FindStatusCondition(policy.Status.Conditions, condType)
				if !condition.IsConditionStatusTrue(cond, policy.Generation) {
					return fmt.Errorf("condition %s is not yet true: %+v", condType, cond)
				}
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
			"ClusterPlacementPolicy status should be fully updated (Scheduled, Synchronized, and Available all true)")
	})

	It("should have the ResourceFlavor applied to the member cluster", func() {
		Eventually(func() error {
			memberResourceFlavor := &kueuev1beta2.ResourceFlavor{}
			return memberClient.Get(ctx, types.NamespacedName{Name: resourceFlavorName}, memberResourceFlavor)
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ResourceFlavor should exist on the member cluster")
	})

	AfterAll(func() {
		By("deleting the ResourceFlavor")
		resourceFlavor := &kueuev1beta2.ResourceFlavor{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: resourceFlavorName}, resourceFlavor)).To(Succeed())
		Expect(hubClient.Delete(ctx, resourceFlavor)).To(Succeed())

		By("waiting for the ClusterPlacementPolicy to be deleted")
		Eventually(func() error {
			policy := &placementv1alpha1.ClusterPlacementPolicy{}
			err := hubClient.Get(ctx, types.NamespacedName{Name: clusterPlacementPolicyName}, policy)
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("ClusterPlacementPolicy still exists")
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ClusterPlacementPolicy should be fully deleted")

		// envtest does not run the Kubernetes garbage collector, so the ClusterPlacementBinding the
		// placement policy controller owns (and would, on a real cluster, rely on owner-reference-based
		// GC to remove) is deleted explicitly here instead, simulating what GC would otherwise do. This
		// lets the work generator and work applier controllers carry out their own finalizer-based
		// cleanup for the rest of the pipeline.
		By("deleting the owned ClusterPlacementBinding to simulate garbage collection")
		bindingList := &placementv1alpha1.ClusterPlacementBindingList{}
		Expect(hubClient.List(ctx, bindingList, client.MatchingLabels{
			placementv1alpha1.PlacementBindingOwnedByLabelKey: clusterPlacementPolicyName,
		})).To(Succeed())
		for idx := range bindingList.Items {
			Expect(hubClient.Delete(ctx, &bindingList.Items[idx])).To(Succeed())
		}

		By("waiting for the ClusterPlacementBinding to be fully deleted")
		Eventually(func() (int, error) {
			bindingList := &placementv1alpha1.ClusterPlacementBindingList{}
			if err := hubClient.List(ctx, bindingList, client.MatchingLabels{
				placementv1alpha1.PlacementBindingOwnedByLabelKey: clusterPlacementPolicyName,
			}); err != nil {
				return -1, err
			}
			return len(bindingList.Items), nil
		}, eventuallyDuration, eventuallyInterval).Should(Equal(0), "ClusterPlacementBinding should be fully deleted")

		// The ClusterPlacementBinding's own deletion (above) causes the work generator controller to
		// delete the primary Work object it created; the work applier controller then finalizes the
		// deletion of that Work object (and the AppliedWork/manifests it owns on the member cluster) once
		// it is done cleaning up. Since envtest does not run a garbage collector on the member cluster,
		// the work applier cannot rely on the AppliedWork's foreground-deletion cascade to complete
		// there, and instead waits out its own configured grace period (2x cleanupWaitTime, see
		// suite_test.go) before forcing the cleanup through; this wait must therefore be long enough to
		// cover that grace period.
		By("verifying that all Work objects for the deleted binding are fully deleted")
		Eventually(func() (int, error) {
			workList := &placementv1alpha1.WorkList{}
			if err := hubClient.List(ctx, workList, client.InNamespace(memberClusterReservedNSName)); err != nil {
				return -1, err
			}
			return len(workList.Items), nil
		}, 2*cleanupWaitTime+eventuallyDuration, eventuallyInterval).Should(Equal(0), "all Work objects should be fully deleted")

		// At this point the work applier has force-removed the KubeFleet owner reference from the
		// (no longer finalizer-blocked) ResourceFlavor manifest, so that it no longer blocks the
		// AppliedWork object's own (foreground) deletion; on a real cluster, the garbage collector would
		// then delete the now-dependent-free ResourceFlavor itself as part of that same cascade. envtest
		// runs no garbage collector on the member cluster, so that last step is simulated here too.
		By("deleting the (now-orphaned) ResourceFlavor from the member cluster")
		memberResourceFlavor := &kueuev1beta2.ResourceFlavor{}
		Expect(memberClient.Get(ctx, types.NamespacedName{Name: resourceFlavorName}, memberResourceFlavor)).To(Succeed())
		Expect(memberClient.Delete(ctx, memberResourceFlavor)).To(Succeed())

		By("waiting for the ResourceFlavor to be fully removed from the member cluster")
		Eventually(func() error {
			err := memberClient.Get(ctx, types.NamespacedName{Name: resourceFlavorName}, &kueuev1beta2.ResourceFlavor{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("ResourceFlavor still exists on the member cluster")
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "ResourceFlavor should be removed from the member cluster")
	})
})

// This scenario walks through placing a Deployment to a member cluster that does not exist yet at the
// time the placement policy is created: the placement policy controller cannot find a cluster matching
// the region=eastus selector, so it submits a cluster claim instead of scheduling the placement; once a
// matching member cluster shows up, the placement policy controller schedules the placement onto it and
// creates a Work object for it. Since this scenario's member cluster ("eastus") has no work applier of
// its own (unlike "bravelion", the suite's one real, physical member cluster), the Work object is marked
// as applied and available directly, simulating what a work applier would otherwise report.
var _ = Describe("placing a Deployment to a to-be-created eastus member cluster via cluster claims", Ordered, func() {
	const (
		deployName            = "busybox-looper"
		placementPolicyName   = "busybox-looper-placement"
		matchingClusterName   = "eastus"
		matchingClusterRegion = "eastus"
	)
	matchingClusterReservedNSName := fmt.Sprintf(utils.NamespaceNameFormat, matchingClusterName)

	It("should create a Deployment running busybox in a loop", func() {
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
								Name:    "busybox",
								Image:   "busybox:latest",
								Command: []string{"/bin/sh", "-c", "while true; do echo looping; sleep 3600; done"},
							},
						},
					},
				},
			},
		}
		Expect(hubClient.Create(ctx, deploy)).To(Succeed())
	})

	It("should create a PlacementPolicy targeting clusters labeled region=eastus", func() {
		policy := &placementv1alpha1.PlacementPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      placementPolicyName,
				Namespace: workNSName,
			},
			Spec: placementv1alpha1.PlacementPolicySpec{
				ClusterSelectors: []placementv1alpha1.ClusterSelector{
					{
						Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
							{MatchLabels: map[string]string{"region": matchingClusterRegion}},
						},
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
		Expect(hubClient.Create(ctx, policy)).To(Succeed())
	})

	It("should submit a ClusterClaim since no cluster matches the region=eastus selector", func() {
		Eventually(func() (bool, error) {
			claimList := &placementv1alpha1.ClusterClaimList{}
			if err := hubClient.List(ctx, claimList); err != nil {
				return false, err
			}
			for idx := range claimList.Items {
				ref := claimList.Items[idx].Spec.PlacementPolicyRef
				if ref != nil && ref.Name == placementPolicyName && ref.Namespace == workNSName {
					return true, nil
				}
			}
			return false, nil
		}, eventuallyDuration, eventuallyInterval).Should(BeTrue(),
			"a ClusterClaim referencing the placement policy should be submitted since no cluster matches the region=eastus selector")
	})

	It("should create a matching member cluster and its reserved namespace", func() {
		cluster := &clusterv1beta1.MemberCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: matchingClusterName,
				Labels: map[string]string{
					"region": matchingClusterRegion,
				},
			},
			Spec: clusterv1beta1.MemberClusterSpec{
				Identity: rbacv1.Subject{
					Kind: rbacv1.ServiceAccountKind,
					Name: "hub-access",
				},
			},
		}
		Expect(hubClient.Create(ctx, cluster)).To(Succeed())

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: matchingClusterReservedNSName,
			},
		}
		Expect(hubClient.Create(ctx, ns)).To(Succeed())
	})

	It("should have a Work object created in the matching cluster's reserved namespace", func() {
		Eventually(func() (int, error) {
			workList := &placementv1alpha1.WorkList{}
			if err := hubClient.List(ctx, workList, client.InNamespace(matchingClusterReservedNSName)); err != nil {
				return -1, err
			}
			return len(workList.Items), nil
		}, eventuallyDuration, eventuallyInterval).Should(Equal(1),
			"exactly one Work object should be created in the eastus cluster's reserved namespace")
	})

	It("should mark the Work object as applied and available", func() {
		// The eastus member cluster is purely simulated (there is no real work applier reconciling
		// its reserved namespace; see suite_test.go), so its Work object's status is set directly here,
		// standing in for what a real work applier would otherwise report after actually applying the
		// manifest and confirming the resulting object is available.
		workList := &placementv1alpha1.WorkList{}
		Expect(hubClient.List(ctx, workList, client.InNamespace(matchingClusterReservedNSName))).To(Succeed())
		Expect(workList.Items).To(HaveLen(1))
		work := &workList.Items[0]

		meta.SetStatusCondition(&work.Status.Conditions, metav1.Condition{
			Type:               placementv1alpha1.WorkCondTypeApplied,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: work.Generation,
			Reason:             "Applied",
			Message:            "Simulated: all manifests have been applied",
		})
		meta.SetStatusCondition(&work.Status.Conditions, metav1.Condition{
			Type:               placementv1alpha1.WorkCondTypeAvailable,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: work.Generation,
			Reason:             "Available",
			Message:            "Simulated: all manifests are available",
		})
		Expect(hubClient.Status().Update(ctx, work)).To(Succeed())
	})

	It("should fully update the PlacementPolicy status", func() {
		Eventually(func() error {
			policy := &placementv1alpha1.PlacementPolicy{}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementPolicyName}, policy); err != nil {
				return err
			}

			for _, condType := range []string{
				placementv1alpha1.PlacementPolicyCondTypeScheduled,
				placementv1alpha1.PlacementPolicyCondTypeSynchronized,
				placementv1alpha1.PlacementPolicyCondTypeAvailable,
			} {
				cond := meta.FindStatusCondition(policy.Status.Conditions, condType)
				if !condition.IsConditionStatusTrue(cond, policy.Generation) {
					return fmt.Errorf("condition %s is not yet true: %+v", condType, cond)
				}
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
			"PlacementPolicy status should be fully updated (Scheduled, Synchronized, and Available all true)")
	})

	AfterAll(func() {
		By("deleting the Deployment")
		deploy := &appsv1.Deployment{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: deployName}, deploy)).To(Succeed())
		Expect(hubClient.Delete(ctx, deploy)).To(Succeed())

		By("deleting the PlacementPolicy")
		policy := &placementv1alpha1.PlacementPolicy{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementPolicyName}, policy)).To(Succeed())
		Expect(hubClient.Delete(ctx, policy)).To(Succeed())

		By("waiting for the PlacementPolicy to be fully deleted")
		Eventually(func() error {
			err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementPolicyName}, &placementv1alpha1.PlacementPolicy{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("PlacementPolicy still exists")
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be fully deleted")

		// envtest does not run the Kubernetes garbage collector, so the PlacementBinding the placement
		// policy controller owns (and would, on a real cluster, rely on owner-reference-based GC to
		// remove) is deleted explicitly here instead, simulating what GC would otherwise do.
		By("deleting the owned PlacementBinding to simulate garbage collection")
		bindingList := &placementv1alpha1.PlacementBindingList{}
		Expect(hubClient.List(ctx, bindingList, client.InNamespace(workNSName), client.MatchingLabels{
			placementv1alpha1.PlacementBindingOwnedByLabelKey: placementPolicyName,
		})).To(Succeed())
		for idx := range bindingList.Items {
			Expect(hubClient.Delete(ctx, &bindingList.Items[idx])).To(Succeed())
		}

		By("waiting for the PlacementBinding to be fully deleted")
		Eventually(func() (int, error) {
			bindingList := &placementv1alpha1.PlacementBindingList{}
			if err := hubClient.List(ctx, bindingList, client.InNamespace(workNSName), client.MatchingLabels{
				placementv1alpha1.PlacementBindingOwnedByLabelKey: placementPolicyName,
			}); err != nil {
				return -1, err
			}
			return len(bindingList.Items), nil
		}, eventuallyDuration, eventuallyInterval).Should(Equal(0), "PlacementBinding should be fully deleted")

		// Unlike the "bravelion" work applier, there is no work applier reconciling the eastus reserved
		// namespace, so the Work object created for it never gains a work-applier cleanup finalizer;
		// the work generator controller's own explicit delete (triggered by the binding's deletion
		// above) is therefore all that is needed here, and this should complete quickly.
		By("waiting for the Work object to be fully deleted")
		Eventually(func() (int, error) {
			workList := &placementv1alpha1.WorkList{}
			if err := hubClient.List(ctx, workList, client.InNamespace(matchingClusterReservedNSName)); err != nil {
				return -1, err
			}
			return len(workList.Items), nil
		}, eventuallyDuration, eventuallyInterval).Should(Equal(0), "the Work object should be fully deleted")

		By("deleting the eastus member cluster")
		cluster := &clusterv1beta1.MemberCluster{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: matchingClusterName}, cluster)).To(Succeed())
		Expect(hubClient.Delete(ctx, cluster)).To(Succeed())
	})
})

// This scenario walks through placing a set of Kueue manifests, resolved from an OCI artifact via an
// ORASManifests object, to the "bravelion" member cluster (env=prod): the work generator controller
// extracts the manifests found under the "base/" path of the artifact (a Namespace, a ResourceFlavor, a
// ClusterQueue, and a LocalQueue) and places them all as a single unit.
var _ = Describe("placing Kueue manifests from an OCI artifact to the prod environment", Ordered, func() {
	const (
		ociSecretName            = "kueue-oci-registry-secret"
		orasManifestsName        = "kueue-manifests"
		placementPolicyName      = "kueue-manifests-placement"
		stagedUpdateStrategyName = "kueue-manifests-rollout-strategy"
		stagedUpdateRunName      = "kueue-manifests-rollout-run"
		stageName                = "prod"
		migrationRequestName     = "kueue-manifests-migration"
	)

	It("should create an image pull secret for the OCI registry", func() {
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
	})

	It("should create an ORASManifests object pointing to the Kueue OCI artifact's base/ path", func() {
		orasManifests := &placementv1alpha1.ORASManifests{
			ObjectMeta: metav1.ObjectMeta{
				Name:      orasManifestsName,
				Namespace: workNSName,
			},
			Spec: placementv1alpha1.ORASManifestsSpec{
				OCIArtifact: &placementv1alpha1.OCIArtifact{
					URL: localregistry.KueueArtifactURL,
					Ref: &placementv1alpha1.OCIArtifactReference{
						Tag: localregistry.KueueArtifactTag,
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
				Path: "base/",
			},
		}
		Expect(hubClient.Create(ctx, orasManifests)).To(Succeed())
	})

	It("should create a PlacementPolicy targeting the prod environment", func() {
		policy := &placementv1alpha1.PlacementPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      placementPolicyName,
				Namespace: workNSName,
			},
			Spec: placementv1alpha1.PlacementPolicySpec{
				ClusterSelectors: []placementv1alpha1.ClusterSelector{
					{
						Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
							{MatchLabels: map[string]string{"env": "prod"}},
						},
					},
				},
				ResourceSelectors: []placementv1alpha1.ResourceSelector{
					{
						Kind:       "ORASManifests",
						APIGroup:   placementv1alpha1.GroupVersion.Group,
						APIVersion: placementv1alpha1.GroupVersion.Version,
						Name:       orasManifestsName,
					},
				},
			},
		}
		Expect(hubClient.Create(ctx, policy)).To(Succeed())
	})

	It("should fully update the PlacementPolicy status", func() {
		Eventually(func() error {
			policy := &placementv1alpha1.PlacementPolicy{}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementPolicyName}, policy); err != nil {
				return err
			}

			for _, condType := range []string{
				placementv1alpha1.PlacementPolicyCondTypeScheduled,
				placementv1alpha1.PlacementPolicyCondTypeSynchronized,
				placementv1alpha1.PlacementPolicyCondTypeAvailable,
			} {
				cond := meta.FindStatusCondition(policy.Status.Conditions, condType)
				if !condition.IsConditionStatusTrue(cond, policy.Generation) {
					return fmt.Errorf("condition %s is not yet true: %+v", condType, cond)
				}
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
			"PlacementPolicy status should be fully updated (Scheduled, Synchronized, and Available all true)")
	})

	It("should have the ResourceFlavor, Namespace, LocalQueue, and ClusterQueue all created on the member cluster", func() {
		Eventually(func() error {
			if err := memberClient.Get(ctx, types.NamespacedName{Name: "playground"}, &corev1.Namespace{}); err != nil {
				return err
			}
			if err := memberClient.Get(ctx, types.NamespacedName{Name: "common"}, &kueuev1beta2.ResourceFlavor{}); err != nil {
				return err
			}
			if err := memberClient.Get(ctx, types.NamespacedName{Name: "global-default"}, &kueuev1beta2.ClusterQueue{}); err != nil {
				return err
			}
			if err := memberClient.Get(ctx, types.NamespacedName{Namespace: "playground", Name: "shared"}, &kueuev1beta2.LocalQueue{}); err != nil {
				return err
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
			"the Namespace, ResourceFlavor, ClusterQueue, and LocalQueue should all exist on the member cluster")
	})

	It("should resolve a new digest after the OCI artifact is refreshed", func() {
		By("recording the currently resolved digest")
		orasManifests := &placementv1alpha1.ORASManifests{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: orasManifestsName}, orasManifests)).To(Succeed())
		Expect(orasManifests.Status.OCIArtifactDetails).ToNot(BeNil())
		originalDigest := orasManifests.Status.OCIArtifactDetails.Digest
		Expect(originalDigest).ToNot(BeEmpty())

		By("refreshing the local OCI registry, rebuilding the artifact with newer changes")
		Expect(localregistry.Refresh()).To(Succeed())

		// The ORAS manifests controller only resolves the OCI artifact's digest when it reconciles, and
		// it only reconciles on changes to the ORASManifests object itself -- there is no periodic
		// re-check. Refresh() only changes the remote registry content behind the same tag, which is not
		// by itself a change to the ORASManifests object, so a trivial annotation update is used here to
		// trigger a fresh reconcile.
		By("nudging the ORASManifests object so the controller re-resolves the (same-tag, now different) artifact")
		Eventually(func() error {
			latest := &placementv1alpha1.ORASManifests{}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: orasManifestsName}, latest); err != nil {
				return err
			}
			if latest.Annotations == nil {
				latest.Annotations = map[string]string{}
			}
			latest.Annotations["kubefleet.dev/refreshed-at"] = time.Now().Format(time.RFC3339Nano)
			return hubClient.Update(ctx, latest)
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "the ORASManifests object should be nudged into a fresh reconcile")

		By("waiting for the ORASManifests object to resolve a new digest")
		Eventually(func() (string, error) {
			refreshed := &placementv1alpha1.ORASManifests{}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: orasManifestsName}, refreshed); err != nil {
				return "", err
			}
			if refreshed.Status.OCIArtifactDetails == nil {
				return "", fmt.Errorf("the ORASManifests object has not resolved any OCI artifact details yet")
			}
			return refreshed.Status.OCIArtifactDetails.Digest, nil
		}, eventuallyDuration, eventuallyInterval).ShouldNot(Equal(originalDigest),
			"the resolved digest should have changed after the OCI artifact was refreshed")
	})

	It("should update the ORASManifests object to point at the overlays/boost/ path", func() {
		orasManifests := &placementv1alpha1.ORASManifests{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: orasManifestsName}, orasManifests)).To(Succeed())
		orasManifests.Spec.Path = "overlays/boost/"
		Expect(hubClient.Update(ctx, orasManifests)).To(Succeed())
	})

	It("should have the PlacementPolicy report as no longer synchronized", func() {
		// The placement policy controller does not watch the arbitrary resources a placement policy
		// selects (here, the ORASManifests object), only the placement policy, its bindings, and its
		// resource snapshots; changing the ORASManifests object's path alone does not trigger a fresh
		// reconcile. A trivial annotation update on the PlacementPolicy itself is used here to trigger
		// one, so that it re-hashes its selected resources and notices the (now stale) resource
		// snapshot -- exactly as an unrelated, otherwise-innocuous update to the placement policy would
		// do on a real cluster.
		By("nudging the PlacementPolicy so the controller re-hashes its selected resources")
		Eventually(func() error {
			policy := &placementv1alpha1.PlacementPolicy{}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementPolicyName}, policy); err != nil {
				return err
			}
			if policy.Annotations == nil {
				policy.Annotations = map[string]string{}
			}
			policy.Annotations["kubefleet.dev/refreshed-at"] = time.Now().Format(time.RFC3339Nano)
			return hubClient.Update(ctx, policy)
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "the PlacementPolicy should be nudged into a fresh reconcile")

		By("waiting for the PlacementPolicy to report Synchronized=False")
		Eventually(func() error {
			policy := &placementv1alpha1.PlacementPolicy{}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementPolicyName}, policy); err != nil {
				return err
			}
			cond := meta.FindStatusCondition(policy.Status.Conditions, placementv1alpha1.PlacementPolicyCondTypeSynchronized)
			if !condition.IsConditionStatusFalse(cond, policy.Generation) {
				return fmt.Errorf("Synchronized condition is not yet false: %+v", cond)
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
			"PlacementPolicy should report Synchronized=False once the ORASManifests object points at a different, not-yet-rolled-out path")
	})

	It("should start a StagedUpdateRun to roll out the new resource snapshot", func() {
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
						MaxConcurrency: ptr.To(intstr.FromInt32(1)),
					},
				},
			},
		}
		Expect(hubClient.Create(ctx, strategy)).To(Succeed())

		run := &rolloutv1alpha1.StagedUpdateRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      stagedUpdateRunName,
				Namespace: workNSName,
			},
			Spec: rolloutv1alpha1.StagedUpdateRunSpec{
				PlacementPolicyName: placementPolicyName,
				// ResourceSnapshotName is left empty so that KubeFleet requests a new resource snapshot
				// based on the placement policy's currently selected resources (i.e., the ORASManifests
				// object now pointing at overlays/boost/) and rolls that out.
				StagedUpdateStrategyName: stagedUpdateStrategyName,
			},
		}
		Expect(hubClient.Create(ctx, run)).To(Succeed())
	})

	It("should have the StagedUpdateRun complete successfully", func() {
		Eventually(func() error {
			run := &rolloutv1alpha1.StagedUpdateRun{}
			if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: stagedUpdateRunName}, run); err != nil {
				return err
			}
			completedCond := meta.FindStatusCondition(run.Status.Conditions, rolloutv1alpha1.StagedUpdateRunCondTypeCompleted)
			if !condition.IsConditionStatusTrue(completedCond, run.Generation) {
				return fmt.Errorf("Completed condition is not yet true: %+v", completedCond)
			}
			if completedCond.Reason != rolloutv1alpha1.StagedUpdateRunCompletedCondReasonSucceeded {
				return fmt.Errorf("Completed condition reason is %q, want %q", completedCond.Reason, rolloutv1alpha1.StagedUpdateRunCompletedCondReasonSucceeded)
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "the StagedUpdateRun should complete successfully")
	})

	It("should have the ClusterQueue's quotas updated on the member cluster", func() {
		Eventually(func() error {
			clusterQueue := &kueuev1beta2.ClusterQueue{}
			if err := memberClient.Get(ctx, types.NamespacedName{Name: "global-default"}, clusterQueue); err != nil {
				return err
			}
			if len(clusterQueue.Spec.ResourceGroups) == 0 || len(clusterQueue.Spec.ResourceGroups[0].Flavors) == 0 {
				return fmt.Errorf("the ClusterQueue has no resource groups or flavors yet")
			}

			wantQuotas := map[string]resource.Quantity{
				"cpu":    resource.MustParse("40"),
				"memory": resource.MustParse("40Gi"),
			}
			for _, res := range clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources {
				wantQuota, tracked := wantQuotas[string(res.Name)]
				if !tracked {
					continue
				}
				if res.NominalQuota.Cmp(wantQuota) != 0 {
					return fmt.Errorf("nominal quota for resource %s is %s, want %s", res.Name, res.NominalQuota.String(), wantQuota.String())
				}
				delete(wantQuotas, string(res.Name))
			}
			if len(wantQuotas) > 0 {
				return fmt.Errorf("the ClusterQueue is missing quotas for: %v", wantQuotas)
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
			"the ClusterQueue's cpu and memory quotas should be updated to 40 and 40Gi respectively")
	})

	// migratedBindingName mirrors toClusterBindingNameFmt from the placement migration request
	// controller (owner placement policy name, dash, to-cluster name, dash, "migrated").
	migratedBindingName := fmt.Sprintf("%s-%s-migrated", placementPolicyName, secondMemberClusterName)

	It("should create a PlacementMigrationRequest to migrate the placement from bravelion to another member cluster", func() {
		migrationReq := &placementv1alpha1.PlacementMigrationRequest{
			ObjectMeta: metav1.ObjectMeta{
				Name: migrationRequestName,
			},
			Spec: placementv1alpha1.PlacementMigrationRequestSpec{
				// Leave PlacementPolicySelectors empty: with no label matchers to narrow things down,
				// every placement currently placed on a from cluster is selected for migration --
				// here, just the one PlacementPolicy created earlier in this scenario.
				FromClusterSelector: map[string]string{
					// Uniquely matches "bravelion" among the suite's member clusters.
					"env": "prod",
				},
				// Leave ToClusterSelector unset (matches any cluster); combined with excluding the
				// from cluster(s), this resolves to "swiftcheetah", the only other member cluster
				// registered with the hub at this point in the suite.
				FailurePolicy: placementv1alpha1.PlacementMigrationFailurePolicy{
					MaxFailureCount: 1,
				},
			},
		}
		Expect(hubClient.Create(ctx, migrationReq)).To(Succeed())
	})

	It("should have the migration request complete successfully", func() {
		Eventually(func() error {
			migrationReq := &placementv1alpha1.PlacementMigrationRequest{}
			if err := hubClient.Get(ctx, types.NamespacedName{Name: migrationRequestName}, migrationReq); err != nil {
				return err
			}
			completedCond := meta.FindStatusCondition(migrationReq.Status.Conditions, placementv1alpha1.PlacementMigrationRequestCondTypeCompleted)
			if !condition.IsConditionStatusTrue(completedCond, migrationReq.Generation) {
				return fmt.Errorf("Completed condition is not yet true: %+v", completedCond)
			}
			if completedCond.Reason != placementv1alpha1.PlacementMigrationRequestCompletedCondReasonSucceeded {
				return fmt.Errorf("Completed condition reason is %q, want %q", completedCond.Reason, placementv1alpha1.PlacementMigrationRequestCompletedCondReasonSucceeded)
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "the PlacementMigrationRequest should complete successfully")
	})

	It("should have the resources running on the swiftcheetah cluster", func() {
		By("verifying that the migrated PlacementBinding targets the swiftcheetah cluster")
		binding := &placementv1alpha1.PlacementBinding{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: migratedBindingName}, binding)).To(Succeed())
		Expect(binding.Spec.ClusterName).To(Equal(secondMemberClusterName))

		By("verifying that the Namespace, ResourceFlavor, ClusterQueue, and LocalQueue all exist on the swiftcheetah cluster")
		Eventually(func() error {
			if err := secondMemberClient.Get(ctx, types.NamespacedName{Name: "playground"}, &corev1.Namespace{}); err != nil {
				return err
			}
			if err := secondMemberClient.Get(ctx, types.NamespacedName{Name: "common"}, &kueuev1beta2.ResourceFlavor{}); err != nil {
				return err
			}
			if err := secondMemberClient.Get(ctx, types.NamespacedName{Name: "global-default"}, &kueuev1beta2.ClusterQueue{}); err != nil {
				return err
			}
			if err := secondMemberClient.Get(ctx, types.NamespacedName{Namespace: "playground", Name: "shared"}, &kueuev1beta2.LocalQueue{}); err != nil {
				return err
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(),
			"the Namespace, ResourceFlavor, ClusterQueue, and LocalQueue should all exist on the swiftcheetah cluster")
	})

	It("should delete the PlacementMigrationRequest to commit the migration", func() {
		// The migration controller only drops the original ("from") PlacementBinding once the
		// migration request itself is deleted -- reporting Completed=True on the request only means
		// the migrated ("to") binding has been surged in and confirmed available, not that the
		// original binding has been (or ever will be, without this delete) removed; deleting the
		// request here is how the migration is explicitly accepted/committed.
		migrationReq := &placementv1alpha1.PlacementMigrationRequest{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: migrationRequestName}, migrationReq)).To(Succeed())
		Expect(hubClient.Delete(ctx, migrationReq)).To(Succeed())

		By("waiting for the PlacementMigrationRequest to be fully deleted")
		Eventually(func() error {
			err := hubClient.Get(ctx, types.NamespacedName{Name: migrationRequestName}, &placementv1alpha1.PlacementMigrationRequest{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("PlacementMigrationRequest still exists")
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementMigrationRequest should be fully deleted")
	})

	It("should have the resources removed from the bravelion cluster", func() {
		// Deleting the PlacementMigrationRequest above triggers the migration controller's commit
		// step, which drops the original PlacementBinding on bravelion now that the migrated one on
		// swiftcheetah has been confirmed available.
		By("waiting for the original PlacementBinding on bravelion to be fully deleted")
		// migration reports completion the original binding should already be gone.
		By("waiting for the original PlacementBinding on bravelion to be fully deleted")
		Eventually(func() error {
			err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementPolicyName + "-" + memberClusterName}, &placementv1alpha1.PlacementBinding{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("the original PlacementBinding on bravelion still exists")
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "the original PlacementBinding on bravelion should be fully deleted")

		// As with the earlier ResourceFlavor and Kueue manifest scenarios' AfterAll blocks, envtest runs
		// no garbage collector on the member cluster, so the work applier cannot rely on the
		// AppliedWork's foreground-deletion cascade to complete there, and instead waits out its own
		// configured grace period before forcing the cleanup through.
		By("verifying that all Work objects owned by the deleted binding are fully deleted from bravelion")
		Eventually(func() (int, error) {
			workList := &placementv1alpha1.WorkList{}
			if err := hubClient.List(ctx, workList, client.InNamespace(memberClusterReservedNSName)); err != nil {
				return -1, err
			}
			return len(workList.Items), nil
		}, 2*cleanupWaitTime+eventuallyDuration, eventuallyInterval).Should(Equal(0), "all Work objects should be fully deleted from bravelion")

		// The work applier only force-removes the KubeFleet owner reference from the (no longer
		// finalizer-blocked) manifests so that they no longer block the AppliedWork object's own
		// deletion; on a real cluster, the garbage collector would then delete the now-dependent-free
		// manifests themselves as part of that same cascade. envtest runs no garbage collector on the
		// member cluster, so that last step is simulated here too. The "playground" namespace itself is
		// left alone (deleting a non-empty namespace would get stuck forever without a namespace
		// lifecycle controller, which envtest also does not run).
		By("deleting the (now-orphaned) Kueue manifests from the bravelion cluster")
		localQueue := &kueuev1beta2.LocalQueue{}
		Expect(memberClient.Get(ctx, types.NamespacedName{Namespace: "playground", Name: "shared"}, localQueue)).To(Succeed())
		Expect(memberClient.Delete(ctx, localQueue)).To(Succeed())

		clusterQueue := &kueuev1beta2.ClusterQueue{}
		Expect(memberClient.Get(ctx, types.NamespacedName{Name: "global-default"}, clusterQueue)).To(Succeed())
		Expect(memberClient.Delete(ctx, clusterQueue)).To(Succeed())

		resourceFlavor := &kueuev1beta2.ResourceFlavor{}
		Expect(memberClient.Get(ctx, types.NamespacedName{Name: "common"}, resourceFlavor)).To(Succeed())
		Expect(memberClient.Delete(ctx, resourceFlavor)).To(Succeed())

		By("waiting for the Kueue manifests to be fully removed from the bravelion cluster")
		Eventually(func() error {
			if err := memberClient.Get(ctx, types.NamespacedName{Namespace: "playground", Name: "shared"}, &kueuev1beta2.LocalQueue{}); !apierrors.IsNotFound(err) {
				return fmt.Errorf("LocalQueue still exists (or an unexpected error occurred): %v", err)
			}
			if err := memberClient.Get(ctx, types.NamespacedName{Name: "global-default"}, &kueuev1beta2.ClusterQueue{}); !apierrors.IsNotFound(err) {
				return fmt.Errorf("ClusterQueue still exists (or an unexpected error occurred): %v", err)
			}
			if err := memberClient.Get(ctx, types.NamespacedName{Name: "common"}, &kueuev1beta2.ResourceFlavor{}); !apierrors.IsNotFound(err) {
				return fmt.Errorf("ResourceFlavor still exists (or an unexpected error occurred): %v", err)
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "the Kueue manifests should be removed from the bravelion cluster")
	})

	AfterAll(func() {
		By("deleting the StagedUpdateRun")
		run := &rolloutv1alpha1.StagedUpdateRun{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: stagedUpdateRunName}, run)).To(Succeed())
		Expect(hubClient.Delete(ctx, run)).To(Succeed())

		By("waiting for the StagedUpdateRun to be fully deleted")
		Eventually(func() error {
			err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: stagedUpdateRunName}, &rolloutv1alpha1.StagedUpdateRun{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("StagedUpdateRun still exists")
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "StagedUpdateRun should be fully deleted")

		By("deleting the StagedUpdateStrategy")
		strategy := &rolloutv1alpha1.StagedUpdateStrategy{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: stagedUpdateStrategyName}, strategy)).To(Succeed())
		Expect(hubClient.Delete(ctx, strategy)).To(Succeed())

		By("deleting the ORASManifests object")
		orasManifests := &placementv1alpha1.ORASManifests{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: orasManifestsName}, orasManifests)).To(Succeed())
		Expect(hubClient.Delete(ctx, orasManifests)).To(Succeed())

		By("deleting the PlacementPolicy")
		policy := &placementv1alpha1.PlacementPolicy{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementPolicyName}, policy)).To(Succeed())
		Expect(hubClient.Delete(ctx, policy)).To(Succeed())

		By("deleting the image pull secret")
		secret := &corev1.Secret{}
		Expect(hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: ociSecretName}, secret)).To(Succeed())
		Expect(hubClient.Delete(ctx, secret)).To(Succeed())

		By("waiting for the PlacementPolicy to be fully deleted")
		Eventually(func() error {
			err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: placementPolicyName}, &placementv1alpha1.PlacementPolicy{})
			if apierrors.IsNotFound(err) {
				return nil
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("PlacementPolicy still exists")
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "PlacementPolicy should be fully deleted")

		// envtest does not run the Kubernetes garbage collector, so the PlacementBinding the placement
		// policy controller owns (and would, on a real cluster, rely on owner-reference-based GC to
		// remove) is deleted explicitly here instead, simulating what GC would otherwise do.
		By("deleting the owned PlacementBinding to simulate garbage collection")
		bindingList := &placementv1alpha1.PlacementBindingList{}
		Expect(hubClient.List(ctx, bindingList, client.InNamespace(workNSName), client.MatchingLabels{
			placementv1alpha1.PlacementBindingOwnedByLabelKey: placementPolicyName,
		})).To(Succeed())
		for idx := range bindingList.Items {
			Expect(hubClient.Delete(ctx, &bindingList.Items[idx])).To(Succeed())
		}

		By("waiting for the PlacementBinding to be fully deleted")
		Eventually(func() (int, error) {
			bindingList := &placementv1alpha1.PlacementBindingList{}
			if err := hubClient.List(ctx, bindingList, client.InNamespace(workNSName), client.MatchingLabels{
				placementv1alpha1.PlacementBindingOwnedByLabelKey: placementPolicyName,
			}); err != nil {
				return -1, err
			}
			return len(bindingList.Items), nil
		}, eventuallyDuration, eventuallyInterval).Should(Equal(0), "PlacementBinding should be fully deleted")

		// This is the same forced-cleanup grace period the "prod-on-demand" ResourceFlavor scenario's
		// AfterAll relies on (see its comments): envtest runs no garbage collector on the member cluster,
		// so the work applier cannot rely on the AppliedWork's foreground-deletion cascade to complete,
		// and instead waits out its own configured grace period before forcing the cleanup through.
		//
		// Note that, following the migration above, the live binding (and thus its Work objects) is on
		// swiftcheetah, not bravelion (whose own copy was already cleaned up as part of verifying the
		// migration itself).
		By("verifying that all Work objects for the deleted binding are fully deleted")
		Eventually(func() (int, error) {
			workList := &placementv1alpha1.WorkList{}
			if err := hubClient.List(ctx, workList, client.InNamespace(secondMemberClusterReservedNSName)); err != nil {
				return -1, err
			}
			return len(workList.Items), nil
		}, 2*cleanupWaitTime+eventuallyDuration, eventuallyInterval).Should(Equal(0), "all Work objects should be fully deleted")

		// As with the ResourceFlavor scenario, the work applier only force-removes the KubeFleet owner
		// reference from the (no longer finalizer-blocked) manifests so that they no longer block the
		// AppliedWork object's own deletion; on a real cluster, the garbage collector would then delete
		// the now-dependent-free manifests themselves as part of that same cascade. envtest runs no
		// garbage collector on the member cluster, so that last step is simulated here too. The
		// "playground" namespace itself is left alone (deleting a non-empty namespace would get stuck
		// forever without a namespace lifecycle controller, which envtest also does not run).
		By("deleting the (now-orphaned) Kueue manifests from the swiftcheetah cluster")
		localQueue := &kueuev1beta2.LocalQueue{}
		Expect(secondMemberClient.Get(ctx, types.NamespacedName{Namespace: "playground", Name: "shared"}, localQueue)).To(Succeed())
		Expect(secondMemberClient.Delete(ctx, localQueue)).To(Succeed())

		clusterQueue := &kueuev1beta2.ClusterQueue{}
		Expect(secondMemberClient.Get(ctx, types.NamespacedName{Name: "global-default"}, clusterQueue)).To(Succeed())
		Expect(secondMemberClient.Delete(ctx, clusterQueue)).To(Succeed())

		resourceFlavor := &kueuev1beta2.ResourceFlavor{}
		Expect(secondMemberClient.Get(ctx, types.NamespacedName{Name: "common"}, resourceFlavor)).To(Succeed())
		Expect(secondMemberClient.Delete(ctx, resourceFlavor)).To(Succeed())

		By("waiting for the Kueue manifests to be fully removed from the swiftcheetah cluster")
		Eventually(func() error {
			if err := secondMemberClient.Get(ctx, types.NamespacedName{Namespace: "playground", Name: "shared"}, &kueuev1beta2.LocalQueue{}); !apierrors.IsNotFound(err) {
				return fmt.Errorf("LocalQueue still exists (or an unexpected error occurred): %v", err)
			}
			if err := secondMemberClient.Get(ctx, types.NamespacedName{Name: "global-default"}, &kueuev1beta2.ClusterQueue{}); !apierrors.IsNotFound(err) {
				return fmt.Errorf("ClusterQueue still exists (or an unexpected error occurred): %v", err)
			}
			if err := secondMemberClient.Get(ctx, types.NamespacedName{Name: "common"}, &kueuev1beta2.ResourceFlavor{}); !apierrors.IsNotFound(err) {
				return fmt.Errorf("ResourceFlavor still exists (or an unexpected error occurred): %v", err)
			}
			return nil
		}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "the Kueue manifests should be removed from the swiftcheetah cluster")
	})
})
