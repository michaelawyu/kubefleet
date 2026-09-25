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

package workapplier

import (
	"context"
	"flag"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	parallelizerutil "github.com/kubefleet-dev/kubefleet/pkg/utils/parallelizer"
	"github.com/kubefleet-dev/kubefleet/pkg/v1/controllers/utils/fieldindexers"
)

// workerCount and maxConcurrentReconciles are kept small and deterministic; this suite does not (yet)
// exercise concurrency, so there is no benefit in running more than a couple of workers.
//
// cleanupRequeueAfter, periodicRequeueAfter, and cleanupWaitTime are kept short so that (future) tests
// relying on requeues do not have to wait long for the work applier to re-reconcile.
const (
	workerCount             = 2
	maxConcurrentReconciles = 1

	cleanupRequeueAfter  = time.Second * 5
	periodicRequeueAfter = time.Second * 5
	cleanupWaitTime      = time.Second * 30
)

const (
	// memberClusterReservedNSName is the name of the hub cluster namespace reserved for the (single,
	// simulated) member cluster used across this suite's integration tests.
	memberClusterReservedNSName = "fleet-member-bravelion"
)

var (
	// The work applier reconciles Work objects, which reside on the hub cluster (in the member cluster's
	// reserved namespace); hubMgr is therefore built against the hub, not the member, config.
	hubCfg    *rest.Config
	hubEnv    *envtest.Environment
	hubClient client.Client
	hubMgr    manager.Manager

	// The member (spoke) cluster is where the work applier actually applies manifests and creates
	// AppliedWork objects.
	memberCfg           *rest.Config
	memberEnv           *envtest.Environment
	memberClient        client.Client
	memberDynamicClient dynamic.Interface

	reconciler *Reconciler

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Work Applier Controller Integration Test Suite")
}

func setupNamespaces() {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: memberClusterReservedNSName,
		},
	}
	Expect(hubClient.Create(ctx, ns)).To(Succeed())
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

	By("Bootstrapping the hub and member test environments")
	hubEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("../../../../", "config", "crd", "bases"),
		},
	}
	memberEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("../../../../", "config", "crd", "bases"),
		},
	}

	var err error
	hubCfg, err = hubEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(hubCfg).ToNot(BeNil())

	memberCfg, err = memberEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(memberCfg).ToNot(BeNil())

	Expect(placementv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())

	By("Building the Kubernetes clients")
	hubClient, err = client.New(hubCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(hubClient).ToNot(BeNil())

	memberClient, err = client.New(memberCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).ToNot(HaveOccurred())
	Expect(memberClient).ToNot(BeNil())

	By("Building the dynamic client for the member cluster")
	memberDynamicClient, err = dynamic.NewForConfig(memberCfg)
	Expect(err).ToNot(HaveOccurred())
	Expect(memberDynamicClient).ToNot(BeNil())

	By("Setting up test namespaces")
	setupNamespaces()

	By("Setting up the controller manager")
	hubMgr, err = ctrl.NewManager(hubCfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		// Scope the cache to the member cluster's reserved namespace, where the work applier's Work
		// objects reside.
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				memberClusterReservedNSName: {},
			},
		},
	})
	Expect(err).ToNot(HaveOccurred())

	By("Setting up the field indices the work applier requires")
	// This must happen before the manager starts (and thus before its cache begins syncing); the work
	// applier looks up work objects by their owner placement binding via this indexed field (see
	// retrieval.go).
	Expect(fieldindexers.SetupWithMemberAgentManager(ctx, hubMgr)).To(Succeed())

	reconciler = New(
		memberClusterReservedNSName,
		hubMgr.GetClient(), hubMgr.GetAPIReader(),
		memberClient, memberDynamicClient,
		memberClient.RESTMapper(),
		parallelizerutil.NewParallelizer(workerCount),
		maxConcurrentReconciles,
		cleanupRequeueAfter,
		periodicRequeueAfter,
		cleanupWaitTime,
	)
	Expect(reconciler.SetupWithManager(hubMgr)).To(Succeed())

	wg = sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer GinkgoRecover()
		defer wg.Done()
		Expect(hubMgr.Start(ctx)).To(Succeed())
	}()

	By("Marking the work applier as ready")
	// The work applier only reconciles once the member cluster has joined the fleet; simulate that here,
	// since there is no separate join/leave controller running in this test suite.
	Expect(reconciler.Join()).To(Succeed())
})

var _ = AfterSuite(func() {
	defer klog.Flush()

	cancel()
	wg.Wait()
	By("Tearing down the test environments")
	Expect(hubEnv.Stop()).To(Succeed())
	Expect(memberEnv.Stop()).To(Succeed())
})
