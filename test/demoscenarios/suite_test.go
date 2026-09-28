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

// Package demoscenarios hosts end-to-end style integration test scenarios that exercise the full
// KubeFleet v1 controller pipeline (hub and member) running together, as opposed to the individual
// per-controller integration test suites found alongside each controller under pkg/v1/controllers.
package demoscenarios

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clusterv1beta1 "github.com/kubefleet-dev/kubefleet/apis/cluster/v1beta1"
	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	rolloutv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/rollout/v1alpha1"
	kueuev1beta2 "github.com/kubefleet-dev/kubefleet/apis/kueue/v1beta2"
	"github.com/kubefleet-dev/kubefleet/pkg/utils/informer"
	parallelizerutil "github.com/kubefleet-dev/kubefleet/pkg/utils/parallelizer"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/annotatedobjectwatcher"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/orasmanifests"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/placementmigrationrequest"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/placementpolicy"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/remoterequestrun"
	stagedupdaterun "github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/stagedupdaterun"
	memberfieldindexers "github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/utils/fieldindexers"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/workapplier"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/workgenerator"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/managers/ociartifactcachedlocalfsstore"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/managers/placementresourcesnapshot"
	hubfieldindexers "github.com/kubefleet-dev/kubefleet/pkg/v1/utils/fieldindexers"
	localregistry "github.com/kubefleet-dev/kubefleet/test/oci"
)

// workerCnt and maxConcurrentReconciles are kept small and deterministic; this suite does not (yet)
// exercise concurrency, so there is no benefit in running more than a couple of workers.
//
// muSlotCnt is the number of mutex slots the placement resource snapshot manager uses; it must be at
// least as large as the manager's own minimum (256).
//
// cleanupRequeueAfter, periodicRequeueAfter, cleanupWaitTime, and refreshPeriod are kept short so that
// (future) tests relying on requeues do not have to wait long for the respective controller to
// re-reconcile.
const (
	workerCnt               = 2
	maxConcurrentReconciles = 1
	muSlotCnt               = 256

	cleanupRequeueAfter  = time.Second * 5
	periodicRequeueAfter = time.Second * 5
	cleanupWaitTime      = time.Second * 5
	refreshPeriod        = time.Second * 10
	maxWaitTimePerRun    = time.Minute * 15
)

const (
	workNSName = "work"

	// memberClusterName and memberClusterReservedNSName identify the first (simulated) member cluster
	// used across this suite's integration tests, and its corresponding reserved namespace on the hub
	// cluster. It is the suite's one real, physical member cluster with its own work applier instance.
	memberClusterName           = "bravelion"
	memberClusterReservedNSName = "fleet-member-bravelion"

	// secondMemberClusterName and secondMemberClusterReservedNSName identify a second (simulated)
	// member cluster, also backed by a real, physical envtest environment and its own dedicated work
	// applier instance, distinct from "bravelion".
	secondMemberClusterName           = "swiftcheetah"
	secondMemberClusterReservedNSName = "fleet-member-swiftcheetah"
)

var (
	// The hub controller manager runs every pkg/v1 controller in this suite, including workapplier and
	// remoterequestrun: the objects those two controllers reconcile (Work, RemoteRequestRun) reside on
	// the hub, in the member cluster's reserved namespace; only the actions they take (applying
	// manifests, running remote requests) are actually directed at the member cluster, via the direct
	// clients built from memberCfg below.
	hubCfg    *rest.Config
	hubEnv    *envtest.Environment
	hubClient client.Client
	hubMgr    manager.Manager

	// workApplierMgr is a separate controller manager, dedicated to the work applier controller and
	// scoped (via its cache) to just the member cluster's reserved namespace, matching the work
	// applier's own established convention (see its per-controller integration test suite). Sharing
	// hubMgr's unscoped cache here instead would make the work applier reconcile Work objects created
	// in every namespace, including ones simulating other (non-"bravelion") member clusters that have
	// no real work applier of their own.
	workApplierMgr manager.Manager

	// secondWorkApplierMgr is the analogous dedicated, namespace-scoped controller manager for the
	// second work applier instance, which reconciles Work objects in secondMemberClusterReservedNSName
	// and applies them to the second (physical) member cluster below.
	secondWorkApplierMgr manager.Manager

	// The member (spoke) cluster is where the work applier actually applies manifests (and creates
	// AppliedWork objects), and where the remote request run controller reads pod/log information from.
	memberCfg           *rest.Config
	memberEnv           *envtest.Environment
	memberClient        client.Client
	memberDynamicClient dynamic.Interface
	memberCoreClient    corev1client.CoreV1Interface

	// The second (physical) member cluster, backing the second work applier instance. It has no
	// dedicated core client since no remote request run instance is wired up against it.
	secondMemberCfg           *rest.Config
	secondMemberEnv           *envtest.Environment
	secondMemberClient        client.Client
	secondMemberDynamicClient dynamic.Interface

	// ociStoreOutputDir is the on-disk cache directory backing the OCI artifact cached store manager
	// the work generator controller uses.
	ociStoreOutputDir string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Demo Scenarios Integration Test Suite")
}

// setupNamespaces creates the namespace that holds placement policies and their resource snapshots
// (workNSName), plus the reserved namespaces for the two (simulated) member clusters used across this
// suite.
func setupNamespaces() {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: workNSName,
		},
	}
	Expect(hubClient.Create(ctx, ns)).To(Succeed())

	memberNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: memberClusterReservedNSName,
		},
	}
	Expect(hubClient.Create(ctx, memberNS)).To(Succeed())

	secondMemberNS := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: secondMemberClusterReservedNSName,
		},
	}
	Expect(hubClient.Create(ctx, secondMemberNS)).To(Succeed())
}

// setupMemberCluster registers the first (simulated) member cluster with the hub.
func setupMemberCluster() {
	mc := &clusterv1beta1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: memberClusterName,
			Labels: map[string]string{
				"env": "prod",
			},
		},
		Spec: clusterv1beta1.MemberClusterSpec{
			Identity: rbacv1.Subject{
				Kind: rbacv1.ServiceAccountKind,
				Name: "hub-access",
			},
		},
	}
	Expect(hubClient.Create(ctx, mc)).To(Succeed())
}

// setupSecondMemberCluster registers the second (simulated) member cluster with the hub.
func setupSecondMemberCluster() {
	mc := &clusterv1beta1.MemberCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: secondMemberClusterName,
			Labels: map[string]string{
				"env": "canary",
			},
		},
		Spec: clusterv1beta1.MemberClusterSpec{
			Identity: rbacv1.Subject{
				Kind: rbacv1.ServiceAccountKind,
				Name: "hub-access",
			},
		},
	}
	Expect(hubClient.Create(ctx, mc)).To(Succeed())
}

var _ = BeforeSuite(func() {
	ctx, cancel = context.WithCancel(context.TODO())

	By("Setup klog")
	fs := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(fs)
	Expect(fs.Parse([]string{"--v", "5", "-add_dir_header", "true"})).Should(Succeed())

	logger := zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true))
	klog.SetLogger(logger)
	ctrl.SetLogger(logger)

	By("Bootstrapping the local OCI registry")
	// The ORAS manifests and work generator controllers connect to a remote OCI registry to resolve
	// manifests, so a local registry is required for the integration tests.
	Expect(localregistry.BootstrapLocalRegistry()).To(Succeed())

	By("Bootstrapping the hub and member test environments")
	hubEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("../../", "config", "crd", "bases"),
		},
	}
	memberEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("../../", "config", "crd", "bases"),
		},
	}
	secondMemberEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("../../", "config", "crd", "bases"),
		},
	}

	var err error
	hubCfg, err = hubEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(hubCfg).ToNot(BeNil())

	memberCfg, err = memberEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(memberCfg).ToNot(BeNil())

	secondMemberCfg, err = secondMemberEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(secondMemberCfg).ToNot(BeNil())

	By("Adding custom APIs to the runtime scheme")
	Expect(clusterv1beta1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(placementv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(rolloutv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(kueuev1beta2.AddToScheme(scheme.Scheme)).To(Succeed())

	By("Building the Kubernetes clients")
	hubClient, err = client.New(hubCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(hubClient).ToNot(BeNil())

	memberClient, err = client.New(memberCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(memberClient).ToNot(BeNil())

	secondMemberClient, err = client.New(secondMemberCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(secondMemberClient).ToNot(BeNil())

	By("Building the dynamic client for the member cluster")
	memberDynamicClient, err = dynamic.NewForConfig(memberCfg)
	Expect(err).ToNot(HaveOccurred())
	Expect(memberDynamicClient).ToNot(BeNil())

	By("Building the dynamic client for the second member cluster")
	secondMemberDynamicClient, err = dynamic.NewForConfig(secondMemberCfg)
	Expect(err).ToNot(HaveOccurred())
	Expect(secondMemberDynamicClient).ToNot(BeNil())

	By("Building the core client for the member cluster")
	memberClientset, err := kubernetes.NewForConfig(memberCfg)
	Expect(err).ToNot(HaveOccurred())
	memberCoreClient = memberClientset.CoreV1()

	By("Setting up test namespaces")
	setupNamespaces()

	By("Registering the member clusters with the hub")
	setupMemberCluster()
	setupSecondMemberCluster()

	By("Setting up the hub controller manager")
	hubMgr, err = ctrl.NewManager(hubCfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
	})
	Expect(err).ToNot(HaveOccurred())

	By("Setting up the field indices the hub controllers require")
	// This must happen before the manager starts (and thus before its cache begins syncing).
	Expect(hubfieldindexers.SetupWithHubControllerManager(ctx, hubMgr)).To(Succeed())

	By("Building the dynamic client and informer manager for the hub cluster")
	hubDynamicClient, err := dynamic.NewForConfig(hubCfg)
	Expect(err).ToNot(HaveOccurred())
	// A fresh informer manager with no static resources registered always falls back to reading
	// resources directly from the API server; this keeps the test setup simple, at the cost of the
	// (here, negligible) performance benefit an informer cache would otherwise provide.
	hubDynamicInformerManager := informer.NewInformerManager(hubDynamicClient, 0, ctx.Done())

	By("Setting up the placement resource snapshot manager")
	// This manager is shared between the placement policy and staged update run controllers, mirroring
	// how the hub agent wires a single instance to both controllers in production.
	snapshotMgr, err := placementresourcesnapshot.New(hubMgr, hubDynamicClient, hubDynamicInformerManager, hubMgr.GetRESTMapper(), muSlotCnt)
	Expect(err).ToNot(HaveOccurred())

	By("Setting up the OCI artifact cached store manager")
	ociStoreOutputDir, err = os.MkdirTemp("", "demoscenarios-oci-store-*")
	Expect(err).ToNot(HaveOccurred())

	By("Wiring up the annotated object watcher controller")
	annotatedObjectWatcherReconciler := &annotatedobjectwatcher.Reconciler{
		HubClient: hubMgr.GetClient(),
	}
	Expect(annotatedObjectWatcherReconciler.SetupWithManager(hubMgr)).To(Succeed())

	By("Wiring up the ORAS manifests controller")
	orasManifestsReconciler := &orasmanifests.Reconciler{
		HubClient:                     hubMgr.GetClient(),
		UseHTTPToConnectToOCIRegistry: true,
	}
	Expect(orasManifestsReconciler.SetupWithManager(hubMgr)).To(Succeed())

	By("Wiring up the placement migration request controller")
	placementMigrationRequestReconciler := &placementmigrationrequest.Reconciler{
		HubClient:         hubMgr.GetClient(),
		MaxWaitTimePerRun: maxWaitTimePerRun,
	}
	Expect(placementMigrationRequestReconciler.SetupWithManager(hubMgr)).To(Succeed())

	By("Wiring up the placement policy controller")
	placementPolicyReconciler := &placementpolicy.Reconciler{
		HubClient:                        hubMgr.GetClient(),
		PlacementResourceSnapshotManager: snapshotMgr,
	}
	Expect(placementPolicyReconciler.SetupWithManager(hubMgr)).To(Succeed())

	By("Wiring up the staged update run controller")
	stagedUpdateRunReconciler := &stagedupdaterun.Reconciler{
		HubClient:                        hubMgr.GetClient(),
		PlacementResourceSnapshotManager: snapshotMgr,
		Parallelizer:                     parallelizerutil.NewParallelizer(parallelizerutil.DefaultNumOfWorkers),
		BindingManagerClaimRateLimiter:   workqueue.DefaultTypedControllerRateLimiter[ctrl.Request](),
		RefreshPeriod:                    refreshPeriod,
	}
	Expect(stagedUpdateRunReconciler.SetupWithManager(hubMgr, maxConcurrentReconciles)).To(Succeed())

	By("Wiring up the work generator controller")
	workGeneratorReconciler := workgenerator.New(hubMgr.GetClient(), workerCnt)
	workGeneratorReconciler.OCIArtifactCachedStoreManager = ociartifactcachedlocalfsstore.NewManager(ociStoreOutputDir)
	workGeneratorReconciler.UseHTTPToConnectToOCIRegistry = true
	Expect(workGeneratorReconciler.SetupWithManager(hubMgr, maxConcurrentReconciles)).To(Succeed())

	By("Wiring up the work applier controller (member agent)")
	// The work applier reconciles Work objects, which reside on the hub (in the member cluster's
	// reserved namespace); it is therefore set up against a hub-backed controller manager, but it
	// applies manifests to, and creates AppliedWork objects on, the member cluster via the direct
	// clients built from memberCfg. Its manager is dedicated (rather than shared with hubMgr) and its
	// cache is scoped to just the member cluster's reserved namespace, so that it never reconciles Work
	// objects created for other (simulated) member clusters that have no work applier of their own.
	workApplierMgr, err = ctrl.NewManager(hubCfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				memberClusterReservedNSName: {},
			},
		},
	})
	Expect(err).ToNot(HaveOccurred())

	By("Setting up the field indices the work applier requires")
	Expect(memberfieldindexers.SetupWithMemberAgentManager(ctx, workApplierMgr)).To(Succeed())

	workApplierReconciler := workapplier.New(
		memberClusterReservedNSName,
		workApplierMgr.GetClient(), workApplierMgr.GetAPIReader(),
		memberClient, memberDynamicClient,
		memberClient.RESTMapper(),
		parallelizerutil.NewParallelizer(workerCnt),
		maxConcurrentReconciles,
		cleanupRequeueAfter,
		periodicRequeueAfter,
		cleanupWaitTime,
	)
	Expect(workApplierReconciler.SetupWithManager(workApplierMgr, "work-applier-bravelion")).To(Succeed())

	By("Wiring up the second work applier controller (member agent), connected to the second member cluster")
	// A second, independent work applier instance, mirroring the one above but scoped to
	// secondMemberClusterReservedNSName and backed by the second (physical) member cluster's clients.
	secondWorkApplierMgr, err = ctrl.NewManager(hubCfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				secondMemberClusterReservedNSName: {},
			},
		},
	})
	Expect(err).ToNot(HaveOccurred())

	By("Setting up the field indices the second work applier requires")
	Expect(memberfieldindexers.SetupWithMemberAgentManager(ctx, secondWorkApplierMgr)).To(Succeed())

	secondWorkApplierReconciler := workapplier.New(
		secondMemberClusterReservedNSName,
		secondWorkApplierMgr.GetClient(), secondWorkApplierMgr.GetAPIReader(),
		secondMemberClient, secondMemberDynamicClient,
		secondMemberClient.RESTMapper(),
		parallelizerutil.NewParallelizer(workerCnt),
		maxConcurrentReconciles,
		cleanupRequeueAfter,
		periodicRequeueAfter,
		cleanupWaitTime,
	)
	Expect(secondWorkApplierReconciler.SetupWithManager(secondWorkApplierMgr, "work-applier-swiftcheetah")).To(Succeed())

	By("Wiring up the remote request run controller (member agent)")
	// Similarly, the remote request run controller watches RemoteRequestRun objects on the hub, but
	// runs the requests themselves (e.g., listing pods, reading logs) against the member cluster.
	remoteRequestRunReconciler := remoterequestrun.New(hubMgr.GetClient(), memberCoreClient)
	Expect(remoteRequestRunReconciler.SetupWithManager(hubMgr)).To(Succeed())

	wg = sync.WaitGroup{}
	wg.Add(3)
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		Expect(hubMgr.Start(ctx)).To(Succeed())
	}()
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		Expect(workApplierMgr.Start(ctx)).To(Succeed())
	}()
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		Expect(secondWorkApplierMgr.Start(ctx)).To(Succeed())
	}()

	By("Marking the work appliers as ready")
	// The work appliers only reconcile once their respective member cluster has joined the fleet;
	// simulate that here, since there is no separate join/leave controller running in this test suite.
	Expect(workApplierReconciler.Join()).To(Succeed())
	Expect(secondWorkApplierReconciler.Join()).To(Succeed())
})

var _ = AfterSuite(func() {
	defer klog.Flush()

	cancel()
	wg.Wait()

	By("Cleaning up the OCI artifact cached store work directory")
	Expect(os.RemoveAll(ociStoreOutputDir)).To(Succeed())

	By("Tearing down the local OCI registry")
	Expect(localregistry.TearDownLocalRegistry()).To(Succeed())

	By("Tearing down the hub and member test environments")
	Expect(hubEnv.Stop()).To(Succeed())
	Expect(memberEnv.Stop()).To(Succeed())
	Expect(secondMemberEnv.Stop()).To(Succeed())
})
