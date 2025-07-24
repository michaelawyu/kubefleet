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
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/Azure/karpenter-provider-azure/pkg/providers/pricing"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/propertyprovider"
	"github.com/kubefleet-dev/kubefleet/pkg/propertyprovider/azure/trackers"
	"github.com/kubefleet-dev/kubefleet/pkg/utils"
)

const (
	nodeName1 = "node-1"
	nodeName2 = "node-2"
	nodeName3 = "node-3"
	nodeName4 = "node-4"

	namespaceName1 = "work-1"
	namespaceName2 = "work-2"
	namespaceName3 = "work-3"

	nodeSKU1 = "Standard_1"
	nodeSKU2 = "Standard_2"
	nodeSKU3 = "Standard_3"

	nodeKnownMissingSKU = "Standard_Missing"

	imageName = "nginx"
)

var (
	currentTime = time.Now()
)

func TestMain(m *testing.M) {
	// Add the K8s metrics API to the scheme so that the NodeMetrics objects can be
	// created and used in the tests.
	if err := metricsv1beta1.AddToScheme(scheme.Scheme); err != nil {
		log.Fatalf("failed to add metrics v1beta1 API to scheme: %v", err)
	}

	os.Exit(m.Run())
}

// dummyPricingProvider is a mock implementation that implements the PricingProvider interface.
type dummyPricingProvider struct{}

var _ trackers.PricingProvider = &dummyPricingProvider{}

func (d *dummyPricingProvider) OnDemandPrice(instanceType string) (float64, bool) {
	switch instanceType {
	case nodeSKU1:
		return 1.0, true
	case nodeSKU2:
		return 1.0, true
	case nodeKnownMissingSKU:
		return pricing.MissingPrice, true
	default:
		return 0.0, false
	}
}

func (d *dummyPricingProvider) LastUpdated() time.Time {
	return currentTime
}

// dummyPricingProviderWithStaleData is a mock implementation that implements the PricingProvider interface.
type dummyPricingProviderWithStaleData struct{}

var _ trackers.PricingProvider = &dummyPricingProviderWithStaleData{}

func (d *dummyPricingProviderWithStaleData) OnDemandPrice(instanceType string) (float64, bool) {
	switch instanceType {
	case nodeSKU1:
		return 1.0, true
	case nodeSKU2:
		return 1.0, true
	case nodeKnownMissingSKU:
		return pricing.MissingPrice, true
	default:
		return 0.0, false
	}
}

func (d *dummyPricingProviderWithStaleData) LastUpdated() time.Time {
	return currentTime.Add(-time.Hour * 48)
}

func TestCollect(t *testing.T) {
	nodes := []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName1,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: nodeSKU1,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("3.2"),
					corev1.ResourceMemory: resource.MustParse("15.2Gi"),
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeName2,
				Labels: map[string]string{
					trackers.AKSClusterNodeSKULabelName: nodeSKU2,
				},
			},
			Spec: corev1.NodeSpec{},
			Status: corev1.NodeStatus{
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("32Gi"),
				},
				Allocatable: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("7"),
					corev1.ResourceMemory: resource.MustParse("30Gi"),
				},
			},
		},
	}

	testCases := []struct {
		name                         string
		nodes                        []corev1.Node
		nodeMetrics                  []metricsv1beta1.NodeMetrics
		pricingprovider              trackers.PricingProvider
		wantMetricCollectionResponse propertyprovider.PropertyCollectionResponse
	}{
		{
			name:  "can report properties",
			nodes: nodes,
			nodeMetrics: []metricsv1beta1.NodeMetrics{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
					},
					Usage: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
					},
					Usage: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("16Gi"),
					},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
			},
			pricingprovider: &dummyPricingProvider{},
			wantMetricCollectionResponse: propertyprovider.PropertyCollectionResponse{
				Properties: map[clusterv1beta1.PropertyName]clusterv1beta1.PropertyValue{
					propertyprovider.NodeCountProperty: {
						Value: "2",
					},
					PerCPUCoreCostProperty: {
						Value: "0.167",
					},
					PerGBMemoryCostProperty: {
						Value: "0.042",
					},
				},
				Resources: clusterv1beta1.ResourceUsage{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("12"),
						corev1.ResourceMemory: resource.MustParse("48Gi"),
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10.2"),
						corev1.ResourceMemory: resource.MustParse("45.2Gi"),
					},
					Available: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4.2"),
						corev1.ResourceMemory: resource.MustParse("21.2Gi"),
					},
				},
				Conditions: []metav1.Condition{
					{
						Type:    CostPropertiesCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  CostPropertiesCollectionSucceededReason,
						Message: CostPropertiesCollectionSucceededMsg,
					},
					{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  AvailableCapacityPropertyCollectionSucceededReason,
						Message: AvailableCapacityPropertyCollectionSucceededMsg,
					},
				},
			},
		},
		{
			name:  "will report zero values if the requested resources exceed the allocatable resources",
			nodes: nodes,
			nodeMetrics: []metricsv1beta1.NodeMetrics{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
					},
					Usage: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("16Gi"),
					},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
					},
					Usage: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("8"),
						corev1.ResourceMemory: resource.MustParse("32Gi"),
					},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
			},
			pricingprovider: &dummyPricingProvider{},
			wantMetricCollectionResponse: propertyprovider.PropertyCollectionResponse{
				Properties: map[clusterv1beta1.PropertyName]clusterv1beta1.PropertyValue{
					propertyprovider.NodeCountProperty: {
						Value: "2",
					},
					PerCPUCoreCostProperty: {
						Value: "0.167",
					},
					PerGBMemoryCostProperty: {
						Value: "0.042",
					},
				},
				Resources: clusterv1beta1.ResourceUsage{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("12"),
						corev1.ResourceMemory: resource.MustParse("48Gi"),
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10.2"),
						corev1.ResourceMemory: resource.MustParse("45.2Gi"),
					},
					Available: corev1.ResourceList{
						corev1.ResourceCPU:    resource.Quantity{},
						corev1.ResourceMemory: resource.Quantity{},
					},
				},
				Conditions: []metav1.Condition{
					{
						Type:    CostPropertiesCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  CostPropertiesCollectionSucceededReason,
						Message: CostPropertiesCollectionSucceededMsg,
					},
					{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  AvailableCapacityPropertyCollectionSucceededReason,
						Message: AvailableCapacityPropertyCollectionSucceededMsg,
					},
				},
			},
		},
		{
			name: "can report cost properties collection failures (no pricing data for any SKU in presence)",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
						Labels: map[string]string{
							trackers.AKSClusterNodeSKULabelName: nodeSKU3,
						},
					},
					Spec: corev1.NodeSpec{},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
						Labels: map[string]string{
							trackers.AKSClusterNodeSKULabelName: nodeSKU3,
						},
					},
					Spec: corev1.NodeSpec{},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
			nodeMetrics: []metricsv1beta1.NodeMetrics{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
					},
					Usage: corev1.ResourceList{},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
					},
					Usage: corev1.ResourceList{},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
			},
			pricingprovider: &dummyPricingProvider{},
			wantMetricCollectionResponse: propertyprovider.PropertyCollectionResponse{
				Properties: map[clusterv1beta1.PropertyName]clusterv1beta1.PropertyValue{
					propertyprovider.NodeCountProperty: {
						Value: "2",
					},
				},
				Resources: clusterv1beta1.ResourceUsage{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
					Available: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
					},
				},
				Conditions: []metav1.Condition{
					{
						Type:   CostPropertiesCollectionSucceededCondType,
						Status: metav1.ConditionFalse,
						Reason: CostPropertiesCollectionFailedReason,
						Message: fmt.Sprintf(CostPropertiesCollectionFailedMsgTemplate,
							fmt.Sprintf("nodes are present, but no pricing data is available for any node SKUs (%v)", []string{nodeSKU3}),
						),
					},
					{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  AvailableCapacityPropertyCollectionSucceededReason,
						Message: AvailableCapacityPropertyCollectionSucceededMsg,
					},
				},
			},
		},
		{
			name: "can report cost properties collection failures (no pricing data for some SKUs in presence)",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
						Labels: map[string]string{
							trackers.AKSClusterNodeSKULabelName: nodeSKU1,
						},
					},
					Spec: corev1.NodeSpec{},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("16Gi"),
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("3.2"),
							corev1.ResourceMemory: resource.MustParse("15.2Gi"),
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
						Labels: map[string]string{
							trackers.AKSClusterNodeSKULabelName: nodeSKU3,
						},
					},
					Spec: corev1.NodeSpec{},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
			nodeMetrics: []metricsv1beta1.NodeMetrics{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
					},
					Usage: corev1.ResourceList{},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
					},
					Usage: corev1.ResourceList{},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
			},
			pricingprovider: &dummyPricingProvider{},
			wantMetricCollectionResponse: propertyprovider.PropertyCollectionResponse{
				Properties: map[clusterv1beta1.PropertyName]clusterv1beta1.PropertyValue{
					propertyprovider.NodeCountProperty: {
						Value: "2",
					},
				},
				Resources: clusterv1beta1.ResourceUsage{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("6"),
						corev1.ResourceMemory: resource.MustParse("18Gi"),
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("5.2"),
						corev1.ResourceMemory: resource.MustParse("17.2Gi"),
					},
					Available: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("5.2"),
						corev1.ResourceMemory: resource.MustParse("17.2Gi"),
					},
				},
				Conditions: []metav1.Condition{
					{
						Type:   CostPropertiesCollectionSucceededCondType,
						Status: metav1.ConditionFalse,
						Reason: CostPropertiesCollectionFailedReason,
						Message: fmt.Sprintf(CostPropertiesCollectionFailedMsgTemplate,
							fmt.Sprintf("no pricing data is available for one or more of the node SKUs (%v) in the cluster", []string{nodeSKU3}),
						),
					},
					{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  AvailableCapacityPropertyCollectionSucceededReason,
						Message: AvailableCapacityPropertyCollectionSucceededMsg,
					},
				},
			},
		},
		{
			name: "can report cost properties collection failures (no SKU labels)",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
						Labels: map[string]string{
							trackers.AKSClusterNodeSKULabelName: nodeSKU1,
						},
					},
					Spec: corev1.NodeSpec{},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("16Gi"),
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("3.2"),
							corev1.ResourceMemory: resource.MustParse("15.2Gi"),
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
					},
					Spec: corev1.NodeSpec{},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
			nodeMetrics: []metricsv1beta1.NodeMetrics{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
					},
					Usage: corev1.ResourceList{},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
					},
					Usage: corev1.ResourceList{},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
			},
			pricingprovider: &dummyPricingProvider{},
			wantMetricCollectionResponse: propertyprovider.PropertyCollectionResponse{
				Properties: map[clusterv1beta1.PropertyName]clusterv1beta1.PropertyValue{
					propertyprovider.NodeCountProperty: {
						Value: "2",
					},
				},
				Resources: clusterv1beta1.ResourceUsage{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("6"),
						corev1.ResourceMemory: resource.MustParse("18Gi"),
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("5.2"),
						corev1.ResourceMemory: resource.MustParse("17.2Gi"),
					},
					Available: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("5.2"),
						corev1.ResourceMemory: resource.MustParse("17.2Gi"),
					},
				},
				Conditions: []metav1.Condition{
					{
						Type:   CostPropertiesCollectionSucceededCondType,
						Status: metav1.ConditionFalse,
						Reason: CostPropertiesCollectionFailedReason,
						Message: fmt.Sprintf(CostPropertiesCollectionFailedMsgTemplate,
							fmt.Sprintf("no pricing data is available for one or more of the node SKUs (%v) in the cluster", []string{}),
						),
					},
					{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  AvailableCapacityPropertyCollectionSucceededReason,
						Message: AvailableCapacityPropertyCollectionSucceededMsg,
					},
				},
			},
		},
		{
			name: "can report cost properties collection failures (known missing node SKU)",
			nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
						Labels: map[string]string{
							trackers.AKSClusterNodeSKULabelName: nodeSKU1,
						},
					},
					Spec: corev1.NodeSpec{},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("16Gi"),
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("3.2"),
							corev1.ResourceMemory: resource.MustParse("15.2Gi"),
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
						Labels: map[string]string{
							trackers.AKSClusterNodeSKULabelName: nodeKnownMissingSKU,
						},
					},
					Spec: corev1.NodeSpec{},
					Status: corev1.NodeStatus{
						Capacity: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
						Allocatable: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("2Gi"),
						},
					},
				},
			},
			nodeMetrics: []metricsv1beta1.NodeMetrics{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
					},
					Usage: corev1.ResourceList{},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
					},
					Usage: corev1.ResourceList{},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
			},
			pricingprovider: &dummyPricingProvider{},
			wantMetricCollectionResponse: propertyprovider.PropertyCollectionResponse{
				Properties: map[clusterv1beta1.PropertyName]clusterv1beta1.PropertyValue{
					propertyprovider.NodeCountProperty: {
						Value: "2",
					},
				},
				Resources: clusterv1beta1.ResourceUsage{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("6"),
						corev1.ResourceMemory: resource.MustParse("18Gi"),
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("5.2"),
						corev1.ResourceMemory: resource.MustParse("17.2Gi"),
					},
					Available: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("5.2"),
						corev1.ResourceMemory: resource.MustParse("17.2Gi"),
					},
				},
				Conditions: []metav1.Condition{
					{
						Type:   CostPropertiesCollectionSucceededCondType,
						Status: metav1.ConditionFalse,
						Reason: CostPropertiesCollectionFailedReason,
						Message: fmt.Sprintf(CostPropertiesCollectionFailedMsgTemplate,
							fmt.Sprintf("no pricing data is available for one or more of the node SKUs (%v) in the cluster", []string{nodeKnownMissingSKU}),
						),
					},
					{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  AvailableCapacityPropertyCollectionSucceededReason,
						Message: AvailableCapacityPropertyCollectionSucceededMsg,
					},
				},
			},
		},
		{
			name:  "can report cost properties collection warnings (stale pricing data)",
			nodes: nodes,
			nodeMetrics: []metricsv1beta1.NodeMetrics{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName1,
					},
					Usage: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
					},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName2,
					},
					Usage: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("5.5"),
						corev1.ResourceMemory: resource.MustParse("14Gi"),
					},
					// Set fresh timestamp and window for the metrics.
					Timestamp: metav1.Now(),
					Window:    metav1.Duration{Duration: time.Second * 10},
				},
			},
			pricingprovider: &dummyPricingProviderWithStaleData{},
			wantMetricCollectionResponse: propertyprovider.PropertyCollectionResponse{
				Properties: map[clusterv1beta1.PropertyName]clusterv1beta1.PropertyValue{
					propertyprovider.NodeCountProperty: {
						Value: "2",
					},
					PerCPUCoreCostProperty: {
						Value: "0.167",
					},
					PerGBMemoryCostProperty: {
						Value: "0.042",
					},
				},
				Resources: clusterv1beta1.ResourceUsage{
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("12"),
						corev1.ResourceMemory: resource.MustParse("48Gi"),
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10.2"),
						corev1.ResourceMemory: resource.MustParse("45.2Gi"),
					},
					Available: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1.7"),
						corev1.ResourceMemory: resource.MustParse("23.2Gi"),
					},
				},
				Conditions: []metav1.Condition{
					{
						Type:   CostPropertiesCollectionSucceededCondType,
						Status: metav1.ConditionTrue,
						Reason: CostPropertiesCollectionDegradedReason,
						Message: fmt.Sprintf(CostPropertiesCollectionDegradedMsgTemplate,
							[]string{
								fmt.Sprintf("the pricing data is stale (last updated at %v); the system might have issues connecting to the Azure Retail Prices API, or the current region is unsupported", currentTime.Add(-time.Hour*48)),
							},
						),
					},
					{
						Type:    AvailableCapacityPropertyCollectionSucceededCondType,
						Status:  metav1.ConditionTrue,
						Reason:  AvailableCapacityPropertyCollectionSucceededReason,
						Message: AvailableCapacityPropertyCollectionSucceededMsg,
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			// Build the trackers manually for testing purposes.
			nodeTracker := trackers.NewNodeTracker(tc.pricingprovider)
			for idx := range tc.nodes {
				nodeTracker.AddOrUpdate(&tc.nodes[idx])
			}
			nodeMetricsTracker := trackers.NewNodeMetricsTracker()
			for idx := range tc.nodeMetrics {
				nodeMetricsTracker.AddOrUpdate(&tc.nodeMetrics[idx])
			}

			p := &PropertyProvider{
				nodeTracker:        nodeTracker,
				nodeMetricsTracker: nodeMetricsTracker,
			}
			res := p.Collect(ctx)
			if diff := cmp.Diff(
				res, tc.wantMetricCollectionResponse,
				ignoreObservationTimeFieldInPropertyValue,
				cmpopts.SortSlices(utils.LessFuncConditionByType),
			); diff != "" {
				t.Fatalf("Collect() property collection response diff (-got, +want):\n%s", diff)
			}
		})
	}
}
