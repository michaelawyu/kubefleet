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

package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	cmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	objectbrowsingv1beta1 "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1"
)

var (
	ignoreTypeMetaInWrappers                  = cmpopts.IgnoreFields(objectbrowsingv1beta1.ClusterAPIObjectWrapper{}, "TypeMeta")
	ignoreAutoPopulatedFieldsInObjectMetadata = cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ManagedFields")

	lessFuncWrappers = func(a, b objectbrowsingv1beta1.ClusterAPIObjectWrapper) bool {
		keyA := a.Namespace + "/" + a.Name
		keyB := b.Namespace + "/" + b.Name
		return keyA < keyB
	}
)

var _ = Describe("Wrapper CRUD ops", func() {
	Context("GET with NotFound error", Ordered, func() {
		wrapperObjNSName := "fleet-member-bravelion"
		wrapperObjName := "bravelion-work-app-1a2b3c"

		It("should return a NotFound error when the object does not exist", func() {
			wrapper := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{}
			err := hubClient.Get(ctx, types.NamespacedName{Name: wrapperObjName, Namespace: wrapperObjNSName}, wrapper)
			Expect(err).NotTo(BeNil())
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("CREATE, UPDATE, GET, LIST", Ordered, func() {
		wrapperObjNSName := "fleet-member-bravelion"
		wrapperObjName1 := "bravelion-work-1-app-1-1a2b3c"
		wrapperObjName2 := "bravelion-work-2-app-2-1a2b3c"

		wrappedObjNSName1 := "work-1"
		wrappedObjName1 := "app-1"
		wrappedObjNSName2 := "work-2"
		wrappedObjName2 := "app-2"

		It("can create ClusterAPIObjectWrappers", func() {
			wrapper1 := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wrapperObjName1,
					Namespace: wrapperObjNSName,
				},
				Identifier: objectbrowsingv1beta1.ObjectIdentifier{
					OriginCluster: "bravelion",
					Group:         "apps",
					Version:       "v1",
					Kind:          "Deployment",
					Namespace:     wrappedObjNSName1,
					Name:          wrappedObjName1,
				},
				RawData: []byte("sample-data-0"),
			}
			Expect(hubClient.Create(ctx, wrapper1)).To(Succeed())

			wrapper2 := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wrapperObjName2,
					Namespace: wrapperObjNSName,
				},
				Identifier: objectbrowsingv1beta1.ObjectIdentifier{
					OriginCluster: "bravelion",
					Group:         "apps",
					Version:       "v1",
					Kind:          "Deployment",
					Namespace:     wrappedObjNSName2,
					Name:          wrappedObjName2,
				},
				RawData: []byte("sample-data-0"),
			}
			Expect(hubClient.Create(ctx, wrapper2)).To(Succeed())
		})

		It("can update the ClusterAPIObjectWrapper", func() {
			wrapper := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: wrapperObjName1, Namespace: wrapperObjNSName}, wrapper)).To(Succeed())

			wrapper.RawData = []byte("sample-data-1")
			Expect(hubClient.Update(ctx, wrapper)).To(Succeed())
		})

		It("can get the created ClusterAPIObjectWrapper", func() {
			wrapper := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: wrapperObjName1, Namespace: wrapperObjNSName}, wrapper)).To(Succeed())

			wantWrapper := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wrapperObjName1,
					Namespace: wrapperObjNSName,
				},
				Identifier: objectbrowsingv1beta1.ObjectIdentifier{
					OriginCluster: "bravelion",
					Group:         "apps",
					Version:       "v1",
					Kind:          "Deployment",
					Namespace:     wrappedObjNSName1,
					Name:          wrappedObjName1,
				},
				RawData: []byte("sample-data-1"),
			}
			diff := cmp.Diff(wrapper, wantWrapper, ignoreAutoPopulatedFieldsInObjectMetadata)
			Expect(diff).To(BeEmpty(), "objects mismatch (-got, +want): %s", diff)
		})

		It("can list all ClusterAPIObjectWrappers", func() {
			wrapperList := &objectbrowsingv1beta1.ClusterAPIObjectWrapperList{}
			Expect(hubClient.List(ctx, wrapperList, client.InNamespace(wrapperObjNSName))).To(Succeed())
			Expect(wrapperList.Items).To(HaveLen(2))

			wantWrappers := []objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperObjName1,
						Namespace: wrapperObjNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "apps",
						Version:       "v1",
						Kind:          "Deployment",
						Namespace:     wrappedObjNSName1,
						Name:          wrappedObjName1,
					},
					RawData: []byte("sample-data-1"),
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperObjName2,
						Namespace: wrapperObjNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "apps",
						Version:       "v1",
						Kind:          "Deployment",
						Namespace:     wrappedObjNSName2,
						Name:          wrappedObjName2,
					},
					RawData: []byte("sample-data-0"),
				},
			}
			diff := cmp.Diff(wrapperList.Items, wantWrappers,
				ignoreTypeMetaInWrappers,
				ignoreAutoPopulatedFieldsInObjectMetadata,
				cmpopts.SortSlices(lessFuncWrappers),
			)
			Expect(diff).To(BeEmpty(), "objects mismatch (-got, +want): %s", diff)
		})

		AfterAll(func() {
			for _, name := range []string{wrapperObjName1, wrapperObjName2} {
				wrapper := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: wrapperObjNSName,
					},
				}
				Expect(hubClient.Delete(ctx, wrapper)).To(Succeed())
			}
		})
	})
})

var _ = Describe("MultiClusterAPIObjectQuery CRUD ops", func() {
	Context("Resolving queries", Ordered, func() {
		wrapperObjNSName := "fleet-member-bravelion"

		wrapperForNS1Name := "bravelion-work-1-1a2b3c"
		wrapperForNS2Name := "bravelion-work-2-1a2b3c"
		wrapperForDeployment1Name := "bravelion-work-1-app-1-1a2b3c"
		wrapperForDeployment2Name := "bravelion-work-2-app-2-1a2b3c"
		wrapperForCM1Name := "bravelion-work-1-cm-1-1a2b3c"
		wrapperForCM2Name := "bravelion-work-1-cm-2-1a2b3c"

		queryAllNamespacesName := "query-all-namespaces"
		queryAllDeploymentsName := "query-all-deployments"
		queryCMsInFirstNSName := "query-configmaps-work-1"
		queryDeployment1Name := "query-deployment-work-1-app-1"
		queryCMsInSecondNSName := "query-configmaps-work-2"

		BeforeAll(func() {
			// Create namespace wrappers.
			wrapperNS1 := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wrapperForNS1Name,
					Namespace: wrapperObjNSName,
				},
				Identifier: objectbrowsingv1beta1.ObjectIdentifier{
					OriginCluster: "bravelion",
					Group:         "",
					Version:       "v1",
					Kind:          "Namespace",
					Name:          "work-1",
				},
				RawData: []byte("namespace-1"),
			}
			Expect(hubClient.Create(ctx, wrapperNS1)).To(Succeed())

			wrapperNS2 := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wrapperForNS2Name,
					Namespace: wrapperObjNSName,
				},
				Identifier: objectbrowsingv1beta1.ObjectIdentifier{
					OriginCluster: "bravelion",
					Group:         "",
					Version:       "v1",
					Kind:          "Namespace",
					Name:          "work-2",
				},
				RawData: []byte("namespace-2"),
			}
			Expect(hubClient.Create(ctx, wrapperNS2)).To(Succeed())

			// Create deployment wrappers.
			wrapperDeployment1 := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wrapperForDeployment1Name,
					Namespace: wrapperObjNSName,
				},
				Identifier: objectbrowsingv1beta1.ObjectIdentifier{
					OriginCluster: "bravelion",
					Group:         "apps",
					Version:       "v1",
					Kind:          "Deployment",
					Namespace:     "work-1",
					Name:          "app-1",
				},
				RawData: []byte("namespace-1-deployment-1"),
			}
			Expect(hubClient.Create(ctx, wrapperDeployment1)).To(Succeed())

			wrapperDeployment2 := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wrapperForDeployment2Name,
					Namespace: wrapperObjNSName,
				},
				Identifier: objectbrowsingv1beta1.ObjectIdentifier{
					OriginCluster: "bravelion",
					Group:         "apps",
					Version:       "v1",
					Kind:          "Deployment",
					Namespace:     "work-2",
					Name:          "app-2",
				},
				RawData: []byte("namespace-2-deployment-2"),
			}
			Expect(hubClient.Create(ctx, wrapperDeployment2)).To(Succeed())

			// Create configmap wrappers.
			wrapperCM1 := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wrapperForCM1Name,
					Namespace: wrapperObjNSName,
				},
				Identifier: objectbrowsingv1beta1.ObjectIdentifier{
					OriginCluster: "bravelion",
					Group:         "",
					Version:       "v1",
					Kind:          "ConfigMap",
					Namespace:     "work-1",
					Name:          "cm-1",
				},
				RawData: []byte("namespace-1-configmap-1"),
			}
			Expect(hubClient.Create(ctx, wrapperCM1)).To(Succeed())

			wrapperCM2 := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				ObjectMeta: metav1.ObjectMeta{
					Name:      wrapperForCM2Name,
					Namespace: wrapperObjNSName,
				},
				Identifier: objectbrowsingv1beta1.ObjectIdentifier{
					OriginCluster: "bravelion",
					Group:         "",
					Version:       "v1",
					Kind:          "ConfigMap",
					Namespace:     "work-1",
					Name:          "cm-2",
				},
				RawData: []byte("namespace-1-configmap-2"),
			}
			Expect(hubClient.Create(ctx, wrapperCM2)).To(Succeed())
		})

		It("can query all namespaces", func() {
			group := ""
			kind := "Namespace"

			q := &objectbrowsingv1beta1.MultiClusterAPIObjectQuery{
				ObjectMeta: metav1.ObjectMeta{
					Name: queryAllNamespacesName,
				},
				CachedObjectSelectorTerms: []objectbrowsingv1beta1.CachedObjectSelectorTerm{
					{
						Group: &group,
						Kind:  &kind,
					},
				},
			}
			Expect(hubClient.Create(ctx, q)).To(Succeed())

			wantResults := []objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperForNS1Name,
						Namespace: wrapperObjNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "",
						Version:       "v1",
						Kind:          "Namespace",
						Name:          "work-1",
					},
					RawData: []byte("namespace-1"),
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperForNS2Name,
						Namespace: wrapperObjNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "",
						Version:       "v1",
						Kind:          "Namespace",
						Name:          "work-2",
					},
					RawData: []byte("namespace-2"),
				},
			}
			diff := cmp.Diff(q.Status.Results, wantResults,
				ignoreTypeMetaInWrappers,
				ignoreAutoPopulatedFieldsInObjectMetadata,
				cmpopts.SortSlices(lessFuncWrappers),
			)
			Expect(diff).To(BeEmpty(), "objects mismatch (-got, +want): %s", diff)
		})

		It("can query all deployments", func() {
			group := "apps"
			kind := "Deployment"

			q := &objectbrowsingv1beta1.MultiClusterAPIObjectQuery{
				ObjectMeta: metav1.ObjectMeta{
					Name: queryAllDeploymentsName,
				},
				CachedObjectSelectorTerms: []objectbrowsingv1beta1.CachedObjectSelectorTerm{
					{
						Group: &group,
						Kind:  &kind,
					},
				},
			}
			Expect(hubClient.Create(ctx, q)).To(Succeed())

			wantResults := []objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperForDeployment1Name,
						Namespace: wrapperObjNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "apps",
						Version:       "v1",
						Kind:          "Deployment",
						Namespace:     "work-1",
						Name:          "app-1",
					},
					RawData: []byte("namespace-1-deployment-1"),
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperForDeployment2Name,
						Namespace: wrapperObjNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "apps",
						Version:       "v1",
						Kind:          "Deployment",
						Namespace:     "work-2",
						Name:          "app-2",
					},
					RawData: []byte("namespace-2-deployment-2"),
				},
			}
			diff := cmp.Diff(q.Status.Results, wantResults,
				ignoreTypeMetaInWrappers,
				ignoreAutoPopulatedFieldsInObjectMetadata,
				cmpopts.SortSlices(lessFuncWrappers),
			)
			Expect(diff).To(BeEmpty(), "objects mismatch (-got, +want): %s", diff)
		})

		It("can query all configmaps under the first namespace", func() {
			group := ""
			kind := "ConfigMap"
			namespace := "work-1"

			q := &objectbrowsingv1beta1.MultiClusterAPIObjectQuery{
				ObjectMeta: metav1.ObjectMeta{
					Name: queryCMsInFirstNSName,
				},
				CachedObjectSelectorTerms: []objectbrowsingv1beta1.CachedObjectSelectorTerm{
					{
						Group:     &group,
						Kind:      &kind,
						Namespace: &namespace,
					},
				},
			}
			Expect(hubClient.Create(ctx, q)).To(Succeed())

			wantResults := []objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperForCM1Name,
						Namespace: wrapperObjNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "",
						Version:       "v1",
						Kind:          "ConfigMap",
						Namespace:     "work-1",
						Name:          "cm-1",
					},
					RawData: []byte("namespace-1-configmap-1"),
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperForCM2Name,
						Namespace: wrapperObjNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "",
						Version:       "v1",
						Kind:          "ConfigMap",
						Namespace:     "work-1",
						Name:          "cm-2",
					},
					RawData: []byte("namespace-1-configmap-2"),
				},
			}
			diff := cmp.Diff(q.Status.Results, wantResults,
				ignoreTypeMetaInWrappers,
				ignoreAutoPopulatedFieldsInObjectMetadata,
				cmpopts.SortSlices(lessFuncWrappers),
			)
			Expect(diff).To(BeEmpty(), "objects mismatch (-got, +want): %s", diff)
		})

		It("can query the first deployment specifically", func() {
			group := "apps"
			kind := "Deployment"
			namespace := "work-1"
			name := "app-1"

			q := &objectbrowsingv1beta1.MultiClusterAPIObjectQuery{
				ObjectMeta: metav1.ObjectMeta{
					Name: queryDeployment1Name,
				},
				CachedObjectSelectorTerms: []objectbrowsingv1beta1.CachedObjectSelectorTerm{
					{
						Group:     &group,
						Kind:      &kind,
						Namespace: &namespace,
						Name:      &name,
					},
				},
			}
			Expect(hubClient.Create(ctx, q)).To(Succeed())

			wantResults := []objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperForDeployment1Name,
						Namespace: wrapperObjNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "apps",
						Version:       "v1",
						Kind:          "Deployment",
						Namespace:     "work-1",
						Name:          "app-1",
					},
					RawData: []byte("namespace-1-deployment-1"),
				},
			}
			diff := cmp.Diff(q.Status.Results, wantResults,
				ignoreTypeMetaInWrappers,
				ignoreAutoPopulatedFieldsInObjectMetadata,
				cmpopts.SortSlices(lessFuncWrappers),
			)
			Expect(diff).To(BeEmpty(), "objects mismatch (-got, +want): %s", diff)
		})

		It("can query all configmaps under the second namespace", func() {
			group := ""
			kind := "ConfigMap"
			namespace := "work-2"

			q := &objectbrowsingv1beta1.MultiClusterAPIObjectQuery{
				ObjectMeta: metav1.ObjectMeta{
					Name: queryCMsInSecondNSName,
				},
				CachedObjectSelectorTerms: []objectbrowsingv1beta1.CachedObjectSelectorTerm{
					{
						Group:     &group,
						Kind:      &kind,
						Namespace: &namespace,
					},
				},
			}
			Expect(hubClient.Create(ctx, q)).To(Succeed())
			Expect(q.Status.Results).To(BeEmpty())
		})

		AfterAll(func() {
			for _, name := range []string{
				wrapperForNS1Name,
				wrapperForNS2Name,
				wrapperForDeployment1Name,
				wrapperForDeployment2Name,
				wrapperForCM1Name,
				wrapperForCM2Name,
			} {
				wrapper := &objectbrowsingv1beta1.ClusterAPIObjectWrapper{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: wrapperObjNSName,
					},
				}
				Expect(hubClient.Delete(ctx, wrapper)).To(Succeed())
			}

			for _, name := range []string{
				queryAllNamespacesName,
				queryAllDeploymentsName,
				queryCMsInFirstNSName,
				queryDeployment1Name,
				queryCMsInSecondNSName,
			} {
				q := &objectbrowsingv1beta1.MultiClusterAPIObjectQuery{
					ObjectMeta: metav1.ObjectMeta{
						Name: name,
					},
				}
				Expect(hubClient.Delete(ctx, q)).To(Succeed())
			}
		})
	})
})

var _ = Describe("PerClusterAPIObjectRawQuery CRUD ops", func() {
	queryNSName := "fleet-member-bravelion"
	queryName := "bravelion-raw-query-1a2b3c"

	Context("Resolving queries", Ordered, func() {
		wrapperNSName := "fleet-member-bravelion"
		wrapperName := "bravelion-work-1-app-1-raw-1a2b3c"

		It("can create a PerClusterAPIObjectRawQuery", func() {
			q := &objectbrowsingv1beta1.PerClusterAPIObjectRawQuery{
				ObjectMeta: metav1.ObjectMeta{
					Name:      queryName,
					Namespace: queryNSName,
				},
				RawQueryPath: "/apis/apps/v1/namespaces/work-1/deployments",
			}
			Expect(hubClient.Create(ctx, q)).To(Succeed())
		})

		It("can update a PerClusterAPIObjectRawQuery to include the results", func() {
			q := &objectbrowsingv1beta1.PerClusterAPIObjectRawQuery{}
			Expect(hubClient.Get(ctx, types.NamespacedName{Name: queryName, Namespace: queryNSName}, q)).To(Succeed())

			q.Status.Results = []objectbrowsingv1beta1.ClusterAPIObjectWrapper{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wrapperName,
						Namespace: wrapperNSName,
					},
					Identifier: objectbrowsingv1beta1.ObjectIdentifier{
						OriginCluster: "bravelion",
						Group:         "apps",
						Version:       "v1",
						Kind:          "Deployment",
						Namespace:     "work-1",
						Name:          "app-1",
					},
					RawData: []byte("deployment-1"),
				},
			}
			Expect(hubClient.Update(ctx, q)).To(Succeed())
		})

		AfterAll(func() {
			q := &objectbrowsingv1beta1.PerClusterAPIObjectRawQuery{
				ObjectMeta: metav1.ObjectMeta{
					Name:      queryName,
					Namespace: queryNSName,
				},
			}
			Expect(hubClient.Delete(ctx, q)).To(Succeed())
		})
	})
})
