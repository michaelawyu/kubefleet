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

package sourcetracker

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	eventuallyTimeout = time.Second * 10
	interval          = time.Millisecond * 250
)

var _ = Describe("Test SourceTracker controller", func() {
	const (
		deploymentName   = "test-deployment"
		podNamePrefix    = "test-pod"
		podCount         = 3
		clusterSelectors = "region=eastus"
	)

	var (
		deployment *appsv1.Deployment
		pods       []*corev1.Pod
	)

	BeforeEach(func() {
		By("Creating a Deployment with the cluster-selectors annotation")
		deployment = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: testNamespace,
				Annotations: map[string]string{
					clusterSelectorsAnnotationKey: clusterSelectors,
				},
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": deploymentName},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": deploymentName},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "test-container",
								Image: "test-image",
							},
						},
					},
				},
			},
		}
		Expect(vclusterClient.Create(ctx, deployment)).Should(Succeed(), "failed to create deployment")

		// The test environment runs no Deployment controller, so the child Pods that a real
		// Deployment would normally have a ReplicaSet create on its behalf are instead created
		// here directly, with an owner reference back to the Deployment, to stand in for them.
		By("Creating the child Pods with an owner reference to the Deployment")
		pods = make([]*corev1.Pod, podCount)
		for i := 0; i < podCount; i++ {
			pods[i] = &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%d", podNamePrefix, i),
					Namespace: testNamespace,
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "apps/v1",
							Kind:               "Deployment",
							Name:               deployment.Name,
							UID:                deployment.UID,
							Controller:         ptr.To(true),
							BlockOwnerDeletion: ptr.To(true),
						},
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
			Expect(vclusterClient.Create(ctx, pods[i])).Should(Succeed(), "failed to create pod")
		}
	})

	AfterEach(func() {
		By("Deleting the Pods")
		for _, pod := range pods {
			Expect(client.IgnoreNotFound(vclusterClient.Delete(ctx, pod))).Should(Succeed(), "failed to delete pod")
		}

		By("Deleting the Deployment")
		Expect(client.IgnoreNotFound(vclusterClient.Delete(ctx, deployment))).Should(Succeed(), "failed to delete deployment")

		By("Verifying that the Pods are fully deleted")
		for _, pod := range pods {
			podName := pod.Name
			Eventually(func() bool {
				err := vclusterClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: podName}, &corev1.Pod{})
				return apierrors.IsNotFound(err)
			}, eventuallyTimeout, interval).Should(BeTrue(), "Pod is not fully deleted")
		}
	})

	It("should add the source object, cluster selectors, and source hash annotations/label to every Pod owned (transitively) by an object with the cluster-selectors annotation", func() {
		for _, pod := range pods {
			podName := pod.Name

			// This mirrors the recipe used by the controller under test: a "Group/Version/Kind/
			// Namespace/Name" reference plus the first 32 hex characters of its SHA-256 hash.
			wantSourceObjectRef := fmt.Sprintf("/v1/Pod/%s/%s", testNamespace, podName)
			hash := sha256.Sum256([]byte(wantSourceObjectRef))
			wantSourceHash := hex.EncodeToString(hash[:])[:32]

			Eventually(func() error {
				gotPod := &corev1.Pod{}
				if err := vclusterClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: podName}, gotPod); err != nil {
					return err
				}

				annotations := gotPod.GetAnnotations()
				if got := annotations[sourceObjectAnnotationKey]; got != wantSourceObjectRef {
					return fmt.Errorf("source object annotation, got %q, want %q", got, wantSourceObjectRef)
				}
				if got := annotations[clusterSelectorsAnnotationKey]; got != clusterSelectors {
					return fmt.Errorf("cluster selectors annotation, got %q, want %q", got, clusterSelectors)
				}
				fetchedTimestamp, ok := annotations[clusterSelectorsLastFetchedTimestampAnnotationKey]
				if !ok {
					return fmt.Errorf("cluster selectors last fetched timestamp annotation is absent")
				}
				if _, err := time.Parse(time.RFC3339, fetchedTimestamp); err != nil {
					return fmt.Errorf("cluster selectors last fetched timestamp annotation %q is not a valid RFC3339 timestamp: %w", fetchedTimestamp, err)
				}

				if got := gotPod.GetLabels()[sourceHashLabelKey]; got != wantSourceHash {
					return fmt.Errorf("source hash label, got %q, want %q", got, wantSourceHash)
				}

				return nil
			}, eventuallyTimeout, interval).Should(Succeed(), "Get() Pod annotations/labels mismatch for pod %q", podName)
		}
	})
})
