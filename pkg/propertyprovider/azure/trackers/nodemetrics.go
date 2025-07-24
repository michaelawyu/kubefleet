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

package trackers

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

const (
	// nodeMetricsShelfLife determines how long the metrics about a node
	// (typically collected by the K8s metrics server) are considered valid
	// since its collection.
	//
	// For reference, the K8s metrics server by default collects metrics
	// for each node every 15 seconds.
	//
	// TO-DO (chenyu1): evaluate if this value should be exposed as a user
	// configurable parameter.
	NodeMetricsShelfLife = time.Second * 30 // 30 seconds
)

// NodeMetricsTracker helps track stats about nodes in a Kubernetes cluster, e.g., the sum
// of requested resources of all nodes.
type NodeMetricsTracker struct {
	totalRequested corev1.ResourceList

	requestedByNode map[string]corev1.ResourceList

	// mu is a RWMutex that protects the tracker against concurrent access.
	mu sync.RWMutex
}

// NewNodeMetricsTracker returns a node metrics tracker.
func NewNodeMetricsTracker() *NodeMetricsTracker {
	nmt := &NodeMetricsTracker{
		totalRequested:  make(corev1.ResourceList),
		requestedByNode: make(map[string]corev1.ResourceList),
	}

	for _, rn := range supportedResourceNames {
		nmt.totalRequested[rn] = resource.Quantity{}
	}

	return nmt
}

func (nmt *NodeMetricsTracker) AddOrUpdate(nodeMetrics *metricsv1beta1.NodeMetrics) {
	nmt.mu.Lock()
	defer nmt.mu.Unlock()

	// The name of a node metrics object is the same as the name of the
	// node that the metrics object concerns.
	requested, ok := nmt.requestedByNode[nodeMetrics.Name]
	if ok {
		// The metrics about the node have been tracked.

		// Check if the node metrics have become stale. If so, untrack the data.
		if time.Since(nodeMetrics.Timestamp.Time) > NodeMetricsShelfLife {
			// This is not considered an error.
			klog.V(2).InfoS("node metrics have become stale; untrack the data", "nodeMetrics", klog.KObj(nodeMetrics))
			delete(nmt.requestedByNode, nodeMetrics.Name)
			return
		}

		// Update the tracked data.
		for _, rn := range supportedResourceNames {
			r1 := requested[rn]
			r2 := nodeMetrics.Usage[rn]
			if !r1.Equal(r2) {
				// The reported requested resource quantity has changed.

				// Update the tracked total requested resources.
				tr := nmt.totalRequested[rn]
				tr.Sub(r1)
				tr.Add(r2)
				nmt.totalRequested[rn] = tr

				// Update the tracked requested resources for the node.
				requested[rn] = r2
			}
		}
	} else {
		// No metrics about the node have ever been tracked.
		requested = make(corev1.ResourceList)

		for _, rn := range supportedResourceNames {
			r := nodeMetrics.Usage[rn]
			requested[rn] = r

			// Update the tracked total requested resources.
			tr := nmt.totalRequested[rn]
			tr.Add(r)
			nmt.totalRequested[rn] = tr
		}

		// Update the tracked requested resources for the node.
		nmt.requestedByNode[nodeMetrics.Name] = requested
	}
}

// Remove stops tracking the metrics about a node.
func (nmt *NodeMetricsTracker) Remove(nodeMetricsName string) {
	nmt.mu.Lock()
	defer nmt.mu.Unlock()

	requested, ok := nmt.requestedByNode[nodeMetricsName]
	if ok {
		// Untrack the requested resources of the node.
		for _, rn := range supportedResourceNames {
			r := requested[rn]
			tr := nmt.totalRequested[rn]
			tr.Sub(r)
			nmt.totalRequested[rn] = tr
		}

		delete(nmt.requestedByNode, nodeMetricsName)
	}
}

// TotalRequestedFor returns the total requested quantity of a specific resource that the
// node metrics tracker tracks.
func (nmt *NodeMetricsTracker) TotalRequestedFor(rn corev1.ResourceName) resource.Quantity {
	nmt.mu.RLock()
	defer nmt.mu.RUnlock()

	return nmt.totalRequested[rn]
}

// TotalRequested returns the total requested quantites of all supported resource names that
// the node metrics tracker tracks.
func (nmt *NodeMetricsTracker) TotalRequested() corev1.ResourceList {
	nmt.mu.RLock()
	defer nmt.mu.RUnlock()

	return nmt.totalRequested.DeepCopy()
}

// TrackedNodeCount returns the number of nodes that the node metrics tracker tracks, i.e.,
// the number of nodes that have metrics.
func (nmt *NodeMetricsTracker) TrackedNodeCount() int {
	nmt.mu.RLock()
	defer nmt.mu.RUnlock()

	return len(nmt.requestedByNode)
}
