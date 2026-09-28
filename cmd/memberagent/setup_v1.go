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

package main

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kubefleet-dev/kubefleet/cmd/memberagent/options"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/parallelizer"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/remoterequestrun"
	memberfieldindexers "github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/utils/fieldindexers"
	workapplierv1 "github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/workapplier"
)

const (
	// v1WorkApplierConcurrentReconciles mirrors the concurrency the legacy v1beta1 work applier uses.
	v1WorkApplierConcurrentReconciles = 5

	// v1WorkApplierCleanupRequeueAfter and v1WorkApplierPeriodicRequeueAfter mirror the static requeue
	// intervals the legacy v1beta1 work applier historically used (see its own set up code for more
	// background): a short interval while a work object (or its applied manifests) is still being
	// cleaned up, and a longer, steady-state interval otherwise.
	v1WorkApplierCleanupRequeueAfter  = 5 * time.Second
	v1WorkApplierPeriodicRequeueAfter = 15 * time.Second
)

// SetupV1Controllers sets up the (still evolving) pkg/v1 controllers that run on the member cluster
// side: the work applier and remote request run controllers. Both reconcile objects (Work,
// RemoteRequestRun) that reside on the hub, in this member cluster's reserved namespace -- hence hubMgr,
// whose cache is scoped to that namespace, is used to set them up -- but they take action (applying
// manifests, running remote requests) against the member cluster itself, via the direct clients built
// from memberConfig.
//
// The remaining pkg/v1 controllers all run on the hub cluster side; see cmd/hubagent/workload.
func SetupV1Controllers(
	ctx context.Context,
	hubMgr, memberMgr manager.Manager,
	memberConfig *rest.Config,
	targetNS string,
	restMapper meta.RESTMapper,
	spokeDynamicClient dynamic.Interface,
	globalOpts options.Options,
) error {
	klog.V(2).InfoS("Setting up the v1 member controllers")

	klog.V(2).InfoS("Setting up the field indices the v1 work applier controller requires")
	if err := memberfieldindexers.SetupWithMemberAgentManager(ctx, hubMgr); err != nil {
		klog.ErrorS(err, "unable to set up the field indices for the v1 work applier controller")
		return err
	}

	klog.V(2).InfoS("Setting up the work applier controller")
	workApplierReconciler := workapplierv1.New(
		targetNS,
		hubMgr.GetClient(), hubMgr.GetAPIReader(),
		memberMgr.GetClient(), spokeDynamicClient,
		restMapper,
		parallelizer.NewParallelizer(parallelizer.DefaultNumOfWorkers),
		v1WorkApplierConcurrentReconciles,
		v1WorkApplierCleanupRequeueAfter,
		v1WorkApplierPeriodicRequeueAfter,
		time.Minute*time.Duration(globalOpts.ApplierOpts.ResourceForceDeletionWaitTimeMinutes),
	)
	if err := workApplierReconciler.SetupWithManager(hubMgr, "work-applier-v1"); err != nil {
		klog.ErrorS(err, "unable to create controller", "controller", "WorkApplier")
		return err
	}

	// TODO: the work applier only starts reconciling once the member cluster has joined the fleet; once
	// a v1 counterpart to the (v1beta1) InternalMemberCluster controller exists to drive that lifecycle,
	// move this Join call there (and wire up Leave on cluster removal) instead of calling it
	// unconditionally here.
	if err := workApplierReconciler.Join(); err != nil {
		klog.ErrorS(err, "unable to mark the v1 work applier as ready")
		return err
	}

	klog.V(2).InfoS("Setting up the remote request run controller")
	memberClientset, err := kubernetes.NewForConfig(memberConfig)
	if err != nil {
		klog.ErrorS(err, "unable to create the core client for the v1 remote request run controller")
		return err
	}
	remoteRequestRunReconciler := remoterequestrun.New(hubMgr.GetClient(), memberClientset.CoreV1())
	if err := remoteRequestRunReconciler.SetupWithManager(hubMgr); err != nil {
		klog.ErrorS(err, "unable to create controller", "controller", "RemoteRequestRun")
		return err
	}

	return nil
}
