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

package azure

import (
	"fmt"
	"slices"
	"time"

	"github.com/Azure/karpenter-provider-azure/pkg/providers/pricing"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/propertyprovider"
	"github.com/kubefleet-dev/kubefleet/pkg/propertyprovider/azure/trackers"
	"github.com/kubefleet-dev/kubefleet/pkg/utils"
)

var (
	ignoreObservationTimeFieldInPropertyValue = cmpopts.IgnoreFields(clusterv1beta1.PropertyValue{}, "ObservationTime")
)

var (
	nodeDeletedActual = func(name string) func() error {
		return func() error {
			node := &corev1.Node{}
			if err := memberClient.Get(ctx, types.NamespacedName{Name: name}, node); !errors.IsNotFound(err) {
				return fmt.Errorf("node has not been deleted yet")
			}
			return nil
		}
	}
	nodeMetricsDeletedActual = func(name string) func() error {
		return func() error {
			nodeMetrics := &metricsv1beta1.NodeMetrics{}
			if err := memberClient.Get(ctx, types.NamespacedName{Name: name}, nodeMetrics); !errors.IsNotFound(err) {
				return fmt.Errorf("node metrics have not been deleted yet")
			}
			return nil
		}
	}

	shouldCreateNodes = func(nodes ...corev1.Node) func() {
		return func() {
			for idx := range nodes {
				node := nodes[idx].DeepCopy()
				Expect(memberClient.Create(ctx, node)).To(Succeed(), "Failed to create node")

				Expect(memberClient.Get(ctx, types.NamespacedName{Name: node.Name}, node)).To(Succeed(), "Failed to get node")
				node.Status.Allocatable = nodes[idx].Status.Allocatable.DeepCopy()
				node.Status.Capacity = nodes[idx].Status.Capacity.DeepCopy()
				Expect(memberClient.Status().Update(ctx, node)).To(Succeed(), "Failed to update node status")
			}
		}
	}
	shouldDeleteNodes = func(nodes ...corev1.Node) func() {
		return func() {
			for idx := range nodes {
				node := &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodes[idx].Name,
					},
				}
				Expect(memberClient.Delete(ctx, node)).To(SatisfyAny(Succeed(), MatchError(errors.IsNotFound)), "Failed to delete node")

				// Wait for the node to be deleted.
				Eventually(nodeDeletedActual(node.Name), eventuallyDuration, eventuallyInterval).Should(BeNil())
			}
		}
	}
	shouldCreateNodeMetrics = func(nodeMetrics ...metricsv1beta1.NodeMetrics) func() {
		return func() {
			for idx := range nodeMetrics {
				nm := nodeMetrics[idx].DeepCopy()
				// Set fresh timestamp and window for the metrics.
				nm.Timestamp = metav1.Now()
				nm.Window = metav1.Duration{Duration: time.Second * 10}
				Expect(memberClient.Create(ctx, nm)).To(Succeed(), "Failed to create node metrics")
			}
		}
	}
	shouldCreateEmptyMetricsForNodes = func(nodes ...corev1.Node) func() {
		return func() {
			for idx := range nodes {
				node := nodes[idx]
				emptyNodeMetric := metricsv1beta1.NodeMetrics{
					ObjectMeta: metav1.ObjectMeta{
						Name: node.Name,
					},
					Usage: corev1.ResourceList{
						corev1.ResourceCPU:    resource.Quantity{},
						corev1.ResourceMemory: resource.Quantity{},
					},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				}
				Expect(memberClient.Create(ctx, &emptyNodeMetric)).To(Succeed(), "Failed to create empty node metrics for node %s", node.Name)
			}
		}
	}
	emptyMetricsForNodes = func(nodes ...corev1.Node) []metricsv1beta1.NodeMetrics {
		emptyNodeMetrics := []metricsv1beta1.NodeMetrics{}
		for idx := range nodes {
			node := nodes[idx]
			emptyNodeMetrics = append(emptyNodeMetrics, metricsv1beta1.NodeMetrics{
				ObjectMeta: metav1.ObjectMeta{
					Name: node.Name,
				},
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.Quantity{},
					corev1.ResourceMemory: resource.Quantity{},
				},
				// Set fresh timestamp and window for the metrics.
				Timestamp: metav1.Now(),
				Window:    metav1.Duration{Duration: time.Second * 10},
			})
		}
		return emptyNodeMetrics
	}
	shouldDeleteMetricsForNodes = func(nodes ...corev1.Node) func() {
		return func() {
			for idx := range nodes {
				node := nodes[idx]
				Expect(memberClient.Delete(ctx, &metricsv1beta1.NodeMetrics{
					ObjectMeta: metav1.ObjectMeta{
						Name: node.Name,
					},
				})).To(SatisfyAny(Succeed(), MatchError(errors.IsNotFound)), "Failed to delete node metrics")

				// Wait for the node metrics to be deleted.
				Eventually(nodeMetricsDeletedActual(node.Name), eventuallyDuration, eventuallyInterval).Should(BeNil())
			}
		}
	}
	shouldReportCorrectPropertiesForNodes = func(nodes []corev1.Node, nodeMetrics []metricsv1beta1.NodeMetrics) func() {
		return func() {
			totalCPUCapacity := resource.Quantity{}
			allocatableCPUCapacity := resource.Quantity{}
			totalMemoryCapacity := resource.Quantity{}
			allocatableMemoryCapacity := resource.Quantity{}

			for idx := range nodes {
				node := nodes[idx]
				totalCPUCapacity.Add(node.Status.Capacity[corev1.ResourceCPU])
				allocatableCPUCapacity.Add(node.Status.Allocatable[corev1.ResourceCPU])
				totalMemoryCapacity.Add(node.Status.Capacity[corev1.ResourceMemory])
				allocatableMemoryCapacity.Add(node.Status.Allocatable[corev1.ResourceMemory])
			}

			totalCPUCores := totalCPUCapacity.AsApproximateFloat64()
			totalMemoryBytes := totalMemoryCapacity.AsApproximateFloat64()
			totalMemoryGBs := totalMemoryBytes / (1024.0 * 1024.0 * 1024.0)

			requestedCPUCapacity := resource.Quantity{}
			requestedMemoryCapacity := resource.Quantity{}
			for idx := range nodeMetrics {
				nm := nodeMetrics[idx]
				requestedCPUCapacity.Add(nm.Usage[corev1.ResourceCPU])
				requestedMemoryCapacity.Add(nm.Usage[corev1.ResourceMemory])
			}

			availableCPUCapacity := allocatableCPUCapacity.DeepCopy()
			availableCPUCapacity.Sub(requestedCPUCapacity)
			if availableCPUCapacity.Cmp(resource.Quantity{}) < 0 {
				allocatableCPUCapacity = resource.Quantity{}
			}
			availableMemoryCapacity := allocatableMemoryCapacity.DeepCopy()
			availableMemoryCapacity.Sub(requestedMemoryCapacity)
			if availableMemoryCapacity.Cmp(resource.Quantity{}) < 0 {
				allocatableMemoryCapacity = resource.Quantity{}
			}

			Eventually(func() error {
				// Calculate the costs manually; hardcoded values cannot be used as Azure pricing
				// is subject to periodic change.

				// Note that this is done within an eventually block to ensure that the
				// calculation is done using the latest pricing data. Inconsistency
				// should seldom occur though.
				totalCost := 0.0
				missingSKUSet := map[string]bool{}
				isPricingDataStale := false

				pricingDataLastUpdated := pp.LastUpdated()
				pricingDataBestAfter := time.Now().Add(-trackers.PricingDataShelfLife)
				if pricingDataLastUpdated.Before(pricingDataBestAfter) {
					isPricingDataStale = true
				}

				for idx := range nodes {
					node := nodes[idx]
					sku := node.Labels[trackers.AKSClusterNodeSKULabelName]
					cost, found := pp.OnDemandPrice(sku)
					if !found || cost == pricing.MissingPrice {
						missingSKUSet[sku] = true
						continue
					}
					totalCost += cost
				}
				missingSKUs := []string{}
				for sku := range missingSKUSet {
					missingSKUs = append(missingSKUs, sku)
				}
				slices.Sort(missingSKUs)

				perCPUCoreCost := "0.0"
				perGBMemoryCost := "0.0"
				costCollectionWarnings := []string{}
				var costCollectionErr error

				switch {
				case len(nodes) == 0:
				case totalCost == 0.0:
					costCollectionErr = fmt.Errorf("nodes are present, but no pricing data is available for any node SKUs (%v)", missingSKUs)
				case len(missingSKUs) > 0:
					costCollectionErr = fmt.Errorf("no pricing data is available for one or more of the node SKUs (%v) in the cluster", missingSKUs)
				default:
					perCPUCoreCost = fmt.Sprintf(CostPrecisionTemplate, totalCost/totalCPUCores)
					perGBMemoryCost = fmt.Sprintf(CostPrecisionTemplate, totalCost/totalMemoryGBs)
				}

				if isPricingDataStale {
					costCollectionWarnings = append(costCollectionWarnings,
						fmt.Sprintf("the pricing data is stale (last updated at %v); the system might have issues connecting to the Azure Retail Prices API, or the current region is unsupported", pricingDataLastUpdated),
					)
				}

				wantProperties := map[clusterv1beta1.PropertyName]clusterv1beta1.PropertyValue{
					propertyprovider.NodeCountProperty: {
						Value: fmt.Sprintf("%d", len(nodes)),
					},
				}
				if costCollectionErr == nil {
					wantProperties[PerCPUCoreCostProperty] = clusterv1beta1.PropertyValue{
						Value: perCPUCoreCost,
					}
					wantProperties[PerGBMemoryCostProperty] = clusterv1beta1.PropertyValue{
						Value: perGBMemoryCost,
					}
				}

				var wantConditions []metav1.Condition
				switch {
				case costCollectionErr != nil:
					wantConditions = append(wantConditions, metav1.Condition{
						Type:    CostPropertiesCollectionSucceededCondType,
						Status:  metav1.ConditionFalse,
						Reason:  CostPropertiesCollectionFailedReason,
						Message: fmt.Sprintf(CostPropertiesCollectionFailedMsgTemplate, costCollectionErr),
					})
				case len(costCollectionWarnings) > 0:
					wantConditions = append(wantConditions, metav1.Condition{
						Type:    CostPropertiesCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  CostPropertiesCollectionDegradedReason,
						Message: fmt.Sprintf(CostPropertiesCollectionDegradedMsgTemplate, costCollectionWarnings),
					})
				default:
					wantConditions = append(wantConditions, metav1.Condition{
						Type:    CostPropertiesCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  CostPropertiesCollectionSucceededReason,
						Message: CostPropertiesCollectionSucceededMsg,
					})
				}

				switch {
				case len(nodeMetrics) == 0 && len(nodes) != 0:
					wantConditions = append(wantConditions, metav1.Condition{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionFalse,
						Reason:  AvailableCapacityPropertyCollectionFailedReason,
						Message: fmt.Sprintf(AvailableCapacityPropertyCollectionFailedMsgTemplate, "no node metrics have been tracked; metrics API might not be installed, or metrics server is unavailable"),
					})
				case len(nodeMetrics) != len(nodes):
					errDetails := fmt.Sprintf("not all nodes have metrics tracked (%d out of %d nodes)", len(nodeMetrics), len(nodes))
					wantConditions = append(wantConditions, metav1.Condition{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionFalse,
						Reason:  AvailableCapacityPropertyCollectionFailedReason,
						Message: fmt.Sprintf(AvailableCapacityPropertyCollectionFailedMsgTemplate, errDetails),
					})
				default:
					wantConditions = append(wantConditions, metav1.Condition{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  AvailableCapacityPropertyCollectionSucceededReason,
						Message: AvailableCapacityPropertyCollectionSucceededMsg,
					})
				}

				expectedRes := propertyprovider.PropertyCollectionResponse{
					Properties: wantProperties,
					Resources: clusterv1beta1.ResourceUsage{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    totalCPUCapacity,
							corev1.ResourceMemory: totalMemoryCapacity,
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    allocatableCPUCapacity,
							corev1.ResourceMemory: allocatableMemoryCapacity,
						},
						Available: corev1.ResourceList{
							corev1.ResourceCPU:    availableCPUCapacity,
							corev1.ResourceMemory: availableMemoryCapacity,
						},
					},
					Conditions: wantConditions,
				}

				res := p.Collect(ctx)
				if diff := cmp.Diff(
					res, expectedRes,
					ignoreObservationTimeFieldInPropertyValue,
					cmpopts.SortSlices(utils.LessFuncConditionByType),
				); diff != "" {
					return fmt.Errorf("property collection response (-got, +want):\n%s", diff)
				}
				return nil
			}, eventuallyDuration, eventuallyInterval).Should(Succeed(), "Property collection response does not match the expected one")
		}
	}
)

var (
	// The nodes below use actual capacities of their respective AKS SKUs; for more information,
	// see:
	// https://learn.microsoft.com/en-us/azure/virtual-machines/av2-series (for A-series nodes),
	// https://learn.microsoft.com/en-us/azure/virtual-machines/sizes-b-series-burstable (for B-series nodes), and
	// https://learn.microsoft.com/en-us/azure/virtual-machines/dv2-dsv2-series (for D/DS v2 series nodes).
	nodes = []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName1,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeSKU1,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8130080Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3860m"),
					corev1.ResourceMemory: resource.MustParse("5474848Ki"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName2,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeSKU2,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("16374624Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3860m"),
					corev1.ResourceMemory: resource.MustParse("12880736Ki"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName3,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeSKU3,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("7097684Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1900m"),
					corev1.ResourceMemory: resource.MustParse("4652372Ki"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName4,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeSKU3,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("7097684Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1900m"),
					corev1.ResourceMemory: resource.MustParse("4652372Ki"),
				},
			},
		},
	}

	nodeMetrics = []metricsv1beta1.NodeMetrics{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName1,
			},
			Usage: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("2Gi"),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName2,
			},
			Usage: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("10Gi"),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName3,
			},
			Usage: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("600m"),
				corev1.ResourceMemory: resource.MustParse("800Mi"),
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName4,
			},
			Usage: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1.3"),
				corev1.ResourceMemory: resource.MustParse("1400Mi"),
			},
		},
	}

	nodesWithSomeUnsupportedSKUs = []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName1,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeSKU1,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8130080Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3860m"),
					corev1.ResourceMemory: resource.MustParse("5474848Ki"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName2,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: unsupportedSKU1,
				},
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName3,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: unsupportedSKU2,
				},
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("2000000Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("2000000Ki"),
				},
			},
		},
	}

	nodesWithSomeEmptySKUs = []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName1,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeSKU1,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8130080Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3860m"),
					corev1.ResourceMemory: resource.MustParse("5474848Ki"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName2,
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName3,
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("2000000Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("2000000Ki"),
				},
			},
		},
	}

	nodesWithAllUnsupportedSKUs = []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName1,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: unsupportedSKU1,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8130080Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3860m"),
					corev1.ResourceMemory: resource.MustParse("5474848Ki"),
				},
			},
		},
	}

	nodesWithAllEmptySKUs = []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName1,
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8130080Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3860m"),
					corev1.ResourceMemory: resource.MustParse("5474848Ki"),
				},
			},
		},
	}

	nodesWithSomeKnownMissingSKUs = []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName1,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeSKU1,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("8130080Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3860m"),
					corev1.ResourceMemory: resource.MustParse("5474848Ki"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName2,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeKnownMissingSKU1,
				},
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
			},
		},
	}

	nodesWithAllKnownMissingSKUs = []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName1,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeKnownMissingSKU1,
				},
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName2,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: aksNodeKnownMissingSKU2,
				},
			},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1000000Ki"),
				},
			},
		},
	}
)

// All the test cases in this block are serial + ordered, as manipulation of nodes/pods
// in one test case will disrupt another.
var _ = Describe("azure property provider", func() {
	Context("add a new node", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodes[0])()
			shouldCreateNodeMetrics(nodeMetrics[0])()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodes[0:1], nodeMetrics[0:1]))

		AfterAll(func() {
			shouldDeleteNodes(nodes[0])()
			shouldDeleteMetricsForNodes(nodes[0])()
		})
	})

	Context("add multiple nodes", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodes...)()
			shouldCreateNodeMetrics(nodeMetrics...)()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodes, nodeMetrics))

		AfterAll(func() {
			shouldDeleteNodes(nodes...)()
			shouldDeleteMetricsForNodes(nodes...)()
		})
	})

	Context("remove a node", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodes...)()
			shouldCreateNodeMetrics(nodeMetrics...)()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodes, nodeMetrics))

		It("can delete a node", shouldDeleteNodes(nodes[0]))

		It("can delete its metrics", shouldDeleteMetricsForNodes(nodes[0]))

		It("should report correct properties after deletion", shouldReportCorrectPropertiesForNodes(nodes[1:], nodeMetrics[1:]))

		AfterAll(func() {
			shouldDeleteNodes(nodes[1:]...)()
			shouldDeleteMetricsForNodes(nodes[1:]...)()
		})
	})

	Context("remove multiple nodes", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodes...)()
			shouldCreateNodeMetrics(nodeMetrics...)()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodes, nodeMetrics))

		It("can delete multiple nodes", shouldDeleteNodes(nodes[0], nodes[3]))

		It("can delete their metrics", shouldDeleteMetricsForNodes(nodes[0], nodes[3]))

		It("should report correct properties after deletion", shouldReportCorrectPropertiesForNodes(nodes[1:3], nodeMetrics[1:3]))

		AfterAll(func() {
			shouldDeleteNodes(nodes[1], nodes[2])()
			shouldDeleteMetricsForNodes(nodes[1], nodes[2])()
		})
	})

	Context("nodes with some unsupported SKUs", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodesWithSomeUnsupportedSKUs...)()
			shouldCreateEmptyMetricsForNodes(nodesWithSomeUnsupportedSKUs...)()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodesWithSomeUnsupportedSKUs, emptyMetricsForNodes(nodesWithSomeUnsupportedSKUs...)))

		AfterAll(func() {
			shouldDeleteNodes(nodesWithSomeUnsupportedSKUs...)()
			shouldDeleteMetricsForNodes(nodesWithSomeUnsupportedSKUs...)()
		})
	})

	Context("nodes with some empty SKUs", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodesWithSomeEmptySKUs...)()
			shouldCreateEmptyMetricsForNodes(nodesWithSomeEmptySKUs...)()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodesWithSomeEmptySKUs, emptyMetricsForNodes(nodesWithSomeEmptySKUs...)))

		AfterAll(func() {
			shouldDeleteNodes(nodesWithSomeEmptySKUs...)()
			shouldDeleteMetricsForNodes(nodesWithSomeEmptySKUs...)()
		})
	})

	Context("nodes with all unsupported SKUs", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodesWithAllUnsupportedSKUs...)()
			shouldCreateEmptyMetricsForNodes(nodesWithAllUnsupportedSKUs...)()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodesWithAllUnsupportedSKUs, emptyMetricsForNodes(nodesWithAllUnsupportedSKUs...)))

		AfterAll(func() {
			shouldDeleteNodes(nodesWithAllUnsupportedSKUs...)()
			shouldDeleteMetricsForNodes(nodesWithAllUnsupportedSKUs...)()
		})
	})

	Context("nodes with all empty SKUs", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodesWithAllEmptySKUs...)()
			shouldCreateEmptyMetricsForNodes(nodesWithAllEmptySKUs...)()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodesWithAllEmptySKUs, emptyMetricsForNodes(nodesWithAllEmptySKUs...)))

		AfterAll(func() {
			shouldDeleteNodes(nodesWithAllEmptySKUs...)()
			shouldDeleteMetricsForNodes(nodesWithAllEmptySKUs...)()
		})
	})

	// This covers a known issue with Azure Retail Prices API, where some deprecated SKUs are no longer
	// reported by the API, but can still be used in an AKS cluster.
	Context("nodes with some known missing SKUs", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodesWithSomeKnownMissingSKUs...)()
			shouldCreateEmptyMetricsForNodes(nodesWithSomeKnownMissingSKUs...)()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodesWithSomeKnownMissingSKUs, emptyMetricsForNodes(nodesWithSomeKnownMissingSKUs...)))

		AfterAll(func() {
			shouldDeleteNodes(nodesWithSomeKnownMissingSKUs...)()
			shouldDeleteMetricsForNodes(nodesWithSomeKnownMissingSKUs...)()
		})
	})

	// This covers a known issue with Azure Retail Prices API, where some deprecated SKUs are no longer
	// reported by the API, but can still be used in an AKS cluster.
	Context("nodes with all known missing SKUs", Serial, Ordered, func() {
		BeforeAll(func() {
			shouldCreateNodes(nodesWithAllKnownMissingSKUs...)()
			shouldCreateEmptyMetricsForNodes(nodesWithAllKnownMissingSKUs...)()
		})

		It("should report correct properties", shouldReportCorrectPropertiesForNodes(nodesWithAllKnownMissingSKUs, emptyMetricsForNodes(nodesWithAllKnownMissingSKUs...)))

		AfterAll(func() {
			shouldDeleteNodes(nodesWithAllKnownMissingSKUs...)()
			shouldDeleteMetricsForNodes(nodesWithAllKnownMissingSKUs...)()
		})
	})
})
