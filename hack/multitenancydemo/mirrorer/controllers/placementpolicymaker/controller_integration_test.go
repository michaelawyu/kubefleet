/*
Copyright 2025 The KubeFleet Authors.

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

package placementpolicymaker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/go-cmp/cmp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
)

const (
	eventuallyTimeout = time.Second * 10
	interval          = time.Millisecond * 250
)

var _ = Describe("Test PlacementPolicyMaker controller", func() {
	const (
		podName = "test-pod"
		region  = "eastus"
	)

	var (
		pod              *corev1.Pod
		sourceHash       string
		expectedPPName   string
		fetchedTimestamp string
	)

	BeforeEach(func() {
		// This mirrors the recipe used by the sourcetracker controller: a "Group/Version/Kind/
		// Namespace/Name" reference plus the first 32 hex characters of its SHA-256 hash.
		sourceObjectRef := fmt.Sprintf("/v1/Pod/%s/%s", testNamespace, podName)
		hash := sha256.Sum256([]byte(sourceObjectRef))
		sourceHash = hex.EncodeToString(hash[:])[:32]

		// Independently derive the expected PlacementPolicy name (rather than calling the
		// placementPolicyNameFor helper under test), so that this test can actually catch a
		// regression in that helper.
		strippedRef := strings.Trim(strings.ToLower(strings.NewReplacer("/", "-", ".", "-").Replace(sourceObjectRef)), "-")
		expectedPPName = fmt.Sprintf("%s-%s", strippedRef, sourceHash[:12])

		fetchedTimestamp = time.Now().UTC().Format(time.RFC3339)

		pod = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: testNamespace,
				Annotations: map[string]string{
					sourceObjectAnnotationKey:                         sourceObjectRef,
					clusterSelectorsAnnotationKey:                     fmt.Sprintf("region=%s", region),
					clusterSelectorsLastFetchedTimestampAnnotationKey: fetchedTimestamp,
				},
				Labels: map[string]string{
					sourceHashLabelKey: sourceHash,
				},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "test-container",
						Image: "test-image",
					},
				},
			},
		}
	})

	AfterEach(func() {
		By("Deleting the Pod")
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, pod))).Should(Succeed(), "failed to delete pod")

		By("Verifying that the Pod is fully deleted")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: podName}, &corev1.Pod{})
			return apierrors.IsNotFound(err)
		}, eventuallyTimeout, interval).Should(BeTrue(), "Pod is not fully deleted")

		By("Verifying that the PlacementPolicy is cleaned up")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: expectedPPName}, &placementv1alpha1.PlacementPolicy{})
			return apierrors.IsNotFound(err)
		}, eventuallyTimeout, interval).Should(BeTrue(), "PlacementPolicy is not cleaned up")
	})

	It("should create a matching PlacementPolicy for a Pod with the expected annotations/labels", func() {
		By("Creating a Pod with the multi-cluster placement annotations/labels")
		Expect(k8sClient.Create(ctx, pod)).Should(Succeed(), "failed to create pod")

		By("Verifying that the PlacementPolicy is created with the expected resource and cluster selectors")
		wantResourceSelectors := []placementv1alpha1.ResourceSelector{
			{
				APIVersion: "v1",
				Kind:       "Pod",
				Name:       podName,
			},
		}
		wantClusterSelectors := []placementv1alpha1.ClusterSelector{
			{
				Terms: []placementv1alpha1.ClusterLabelAndPropertySelectorTerm{
					{
						MatchLabels: map[string]string{
							regionLabelKey: region,
						},
					},
				},
				Count: ptr.To(intstr.FromInt32(1)),
				// The API server defaults this field whenever the enclosing cluster selector
				// is present.
				WhenUnfulfilled: placementv1alpha1.WhenUnfulfilledOptionAddClusterClaim,
			},
		}

		pp := &placementv1alpha1.PlacementPolicy{}
		Eventually(func() error {
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: expectedPPName}, pp); err != nil {
				return err
			}
			if diff := cmp.Diff(wantResourceSelectors, pp.Spec.ResourceSelectors); diff != "" {
				return fmt.Errorf("resourceSelectors mismatch (-want, +got):\n%s", diff)
			}
			if diff := cmp.Diff(wantClusterSelectors, pp.Spec.ClusterSelectors); diff != "" {
				return fmt.Errorf("clusterSelectors mismatch (-want, +got):\n%s", diff)
			}
			if got := pp.GetAnnotations()[placementPolicyClusterSelectorsLastUpdatedTimestampAnnotationKey]; got != fetchedTimestamp {
				return fmt.Errorf("cluster selectors last updated timestamp annotation, got %q, want %q", got, fetchedTimestamp)
			}
			return nil
		}, eventuallyTimeout, interval).Should(Succeed(), "Get() PlacementPolicy mismatch")

		By("Verifying that the cleanup finalizer is added to the Pod")
		Eventually(func() bool {
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: podName}, pod); err != nil {
				return false
			}
			return controllerutil.ContainsFinalizer(pod, placementPolicyMakerCleanupFinalizer)
		}, eventuallyTimeout, interval).Should(BeTrue(), "Pod does not have the cleanup finalizer")
	})
})
