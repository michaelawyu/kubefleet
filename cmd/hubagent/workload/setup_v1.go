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

package workload

import (
	"context"
	"math"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kubefleet-dev/kubefleet/cmd/hubagent/options"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/informer"
	parallelizerutil "github.com/kubefleet-dev/kubefleet/pkg/utils/parallelizer"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/annotatedobjectwatcher"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/orasmanifests"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/placementmigrationrequest"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/placementpolicy"
	stagedupdate "github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/stagedupdaterun"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/workgenerator"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/managers/ociartifactcachedlocalfsstore"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/managers/placementresourcesnapshot"
	hubfieldindexers "github.com/kubefleet-dev/kubefleet/pkg/v1/utils/fieldindexers"
)

const (
	// v1MuSlotCnt is the number of mutex slots the placement resource snapshot manager uses to
	// serialize snapshot creation per placement policy. It is kept at the manager's own minimum, as
	// the v1 controllers do not (yet) expose a dedicated, fleet-size-based tuning knob for it.
	v1MuSlotCnt = 256

	// v1OCIArtifactCacheWorkDir is the on-disk directory the work generator and ORAS manifests
	// controllers use to cache OCI artifacts pulled from a registry.
	//
	// TODO: expose this as a dedicated, configurable option once the v1 controllers are further along.
	v1OCIArtifactCacheWorkDir = "/tmp/kubefleet-oras-cache"

	// v1PlacementMigrationRequestMaxWaitTimePerRun bounds how long a single round of migration
	// processing may take before the placement migration request controller gives up on it and
	// reports failure.
	v1PlacementMigrationRequestMaxWaitTimePerRun = 40 * time.Second
)

// SetupV1Controllers sets up the (still evolving) pkg/v1 controllers that run on the hub cluster: the
// annotated object watcher, ORAS manifests, placement migration request, placement policy, staged
// update run, and work generator controllers.
//
// The work applier and remote request run controllers, the two pkg/v1 controllers that instead run on
// the member cluster side, are set up by the member agent; see cmd/memberagent.
func SetupV1Controllers(ctx context.Context, mgr ctrl.Manager, config *rest.Config, opts *options.Options) error {
	klog.V(2).InfoS("Setting up the v1 hub controllers")

	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		klog.ErrorS(err, "unable to create the dynamic client for the v1 hub controllers")
		return err
	}
	// A dedicated informer manager, distinct from the one the (legacy) v1beta1 controllers use, backs
	// the placement resource snapshot manager below.
	dynamicInformerManager := informer.NewInformerManager(dynamicClient, opts.CtrlMgrOpts.ResyncPeriod.Duration, ctx.Done())

	klog.V(2).InfoS("Setting up the field indices the v1 hub controllers require")
	if err := hubfieldindexers.SetupWithHubControllerManager(ctx, mgr); err != nil {
		klog.ErrorS(err, "unable to set up the field indices for the v1 hub controllers")
		return err
	}

	klog.V(2).InfoS("Setting up the placement resource snapshot manager")
	snapshotMgr, err := placementresourcesnapshot.New(mgr, dynamicClient, dynamicInformerManager, mgr.GetRESTMapper(), v1MuSlotCnt)
	if err != nil {
		klog.ErrorS(err, "unable to create the placement resource snapshot manager")
		return err
	}

	klog.V(2).InfoS("Setting up the annotated object watcher controller")
	annotatedObjectWatcherReconciler := &annotatedobjectwatcher.Reconciler{
		HubClient: mgr.GetClient(),
	}
	if err := annotatedObjectWatcherReconciler.SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "unable to create controller", "controller", "AnnotatedObjectWatcher")
		return err
	}

	klog.V(2).InfoS("Setting up the ORAS manifests controller")
	orasManifestsReconciler := &orasmanifests.Reconciler{
		HubClient: mgr.GetClient(),
		// Registries reachable in production are expected to be served over HTTPS; HTTP is only used
		// for local/test registries.
		UseHTTPToConnectToOCIRegistry: false,
	}
	if err := orasManifestsReconciler.SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "unable to create controller", "controller", "ORASManifests")
		return err
	}

	klog.V(2).InfoS("Setting up the placement migration request controller")
	placementMigrationRequestReconciler := &placementmigrationrequest.Reconciler{
		HubClient:         mgr.GetClient(),
		MaxWaitTimePerRun: v1PlacementMigrationRequestMaxWaitTimePerRun,
	}
	if err := placementMigrationRequestReconciler.SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "unable to create controller", "controller", "PlacementMigrationRequest")
		return err
	}

	klog.V(2).InfoS("Setting up the placement policy controller")
	placementPolicyReconciler := &placementpolicy.Reconciler{
		HubClient:                        mgr.GetClient(),
		PlacementResourceSnapshotManager: snapshotMgr,
	}
	if err := placementPolicyReconciler.SetupWithManager(mgr); err != nil {
		klog.ErrorS(err, "unable to create controller", "controller", "PlacementPolicy")
		return err
	}

	// Re-use the existing (fleet-size based) concurrency tuning knob for the two remaining v1
	// controllers below, mirroring how the legacy v1beta1 staged update run and work generator
	// controllers are set up.
	maxConcurrentReconciles := int(math.Ceil(float64(opts.PlacementMgmtOpts.MaxConcurrentClusterPlacement) / 10))
	if maxConcurrentReconciles < 1 {
		maxConcurrentReconciles = 1
	}

	klog.V(2).InfoS("Setting up the staged update run controller")
	stagedUpdateRunReconciler := &stagedupdate.Reconciler{
		HubClient:                        mgr.GetClient(),
		PlacementResourceSnapshotManager: snapshotMgr,
		Parallelizer:                     parallelizerutil.NewParallelizer(parallelizerutil.DefaultNumOfWorkers),
		BindingManagerClaimRateLimiter:   workqueue.DefaultTypedControllerRateLimiter[ctrl.Request](),
		RefreshPeriod:                    opts.CtrlMgrOpts.ResyncPeriod.Duration,
	}
	if err := stagedUpdateRunReconciler.SetupWithManager(mgr, maxConcurrentReconciles); err != nil {
		klog.ErrorS(err, "unable to create controller", "controller", "StagedUpdateRun")
		return err
	}

	klog.V(2).InfoS("Setting up the work generator controller")
	workGeneratorReconciler := workgenerator.New(mgr.GetClient(), parallelizerutil.DefaultNumOfWorkers)
	workGeneratorReconciler.OCIArtifactCachedStoreManager = ociartifactcachedlocalfsstore.NewManager(v1OCIArtifactCacheWorkDir)
	workGeneratorReconciler.UseHTTPToConnectToOCIRegistry = false
	if err := workGeneratorReconciler.SetupWithManager(mgr, maxConcurrentReconciles); err != nil {
		klog.ErrorS(err, "unable to create controller", "controller", "WorkGenerator")
		return err
	}

	return nil
}
