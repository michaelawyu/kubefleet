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

package remoterequestrun

import (
	"encoding/json"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
)

const (
	eventuallyInterval = 500 * time.Millisecond
	eventuallyDuration = 10 * time.Second
)

var _ = Describe("read requests", Ordered, func() {
	Context("can list pods", Ordered, func() {
		BeforeAll(func() {
			By("creating a pod in the default namespace")
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Labels: map[string]string{
						"foo": "bar",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test-container",
							Image: "nginx:latest",
						},
					},
				},
			}
			Expect(hubClient.Create(ctx, pod)).To(Succeed())
		})

		AfterAll(func() {
			By("deleting the test pod")
			Expect(client.IgnoreNotFound(hubClient.Delete(ctx, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
			}))).To(Succeed())
		})

		It("should create a RemoteRequestRun to list all pods in the default namespace", func() {
			req := &placementv1alpha1.RemoteRequestRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "list-pods",
					Namespace: workNSName,
				},
				Spec: placementv1alpha1.RemoteRequestRunSpec{
					APIGroup:   "",
					APIVersion: "v1",
					Resource:   "pods",
					Namespace:  "default",
					LabelMatchers: map[string]string{
						"foo": "bar",
					},
				},
			}
			Expect(hubClient.Create(ctx, req)).To(Succeed())
		})

		It("should complete the RemoteRequestRun and return the test pod in the result", func() {
			By("waiting for the RemoteRequestRun to be completed")
			remoteReq := &placementv1alpha1.RemoteRequestRun{}
			Eventually(func() (bool, error) {
				if err := hubClient.Get(ctx, types.NamespacedName{Namespace: workNSName, Name: "list-pods"}, remoteReq); err != nil {
					return false, err
				}
				for _, c := range remoteReq.Status.Conditions {
					if c.Type == placementv1alpha1.RemoteRequestRunCondTypeCompleted && c.Status == metav1.ConditionTrue {
						return true, nil
					}
				}
				return false, nil
			}, eventuallyDuration, eventuallyInterval).Should(BeTrue(),
				"RemoteRequestRun should be completed successfully")

			By("unmarshalling the result into a PodList")
			var gotPodList corev1.PodList
			Expect(json.Unmarshal(remoteReq.Status.Result, &gotPodList)).To(Succeed())

			By("verifying the result contains exactly the test pod")
			wantPodList := corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "default",
							Labels:    map[string]string{"foo": "bar"},
						},
					},
				},
			}

			if diff := cmp.Diff(gotPodList, wantPodList,
				cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion"),
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "UID", "CreationTimestamp", "ManagedFields", "Generation", "Annotations"),
				cmpopts.IgnoreFields(corev1.PodSpec{}, "Containers", "RestartPolicy", "TerminationGracePeriodSeconds", "DNSPolicy", "SecurityContext", "SchedulerName", "Tolerations", "Priority", "EnableServiceLinks", "PreemptionPolicy"),
				cmpopts.IgnoreFields(corev1.PodStatus{}, "Phase", "QOSClass"),
				cmpopts.IgnoreFields(corev1.PodList{}, "TypeMeta", "ListMeta"),
			); diff != "" {
				Fail("RemoteRequestRun result mismatch (-got, +want):\n" + diff)
			}
		})
	})
})
