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
	"context"
	"flag"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

const (
	testNamespace = "test-ns"
)

var (
	vclusterCfg    *rest.Config
	vclusterEnv    *envtest.Environment
	vclusterClient client.Client

	hostCfg       *rest.Config
	hostEnv       *envtest.Environment
	hostK8sClient client.Client

	mgr manager.Manager

	ctx    context.Context
	cancel context.CancelFunc
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Source Tracker Suite")
}

var _ = BeforeSuite(func() {
	ctx, cancel = context.WithCancel(context.TODO())

	By("Setup klog")
	fs := flag.NewFlagSet("klog", flag.ContinueOnError)
	klog.InitFlags(fs)
	Expect(fs.Parse([]string{"--v", "5", "-add_dir_header", "true"})).Should(Succeed())

	// The sourcetracker controller talks to two clusters (the tenant vcluster, and the host
	// cluster), unlike, e.g. the placementpolicymaker controller, which only talks to the host
	// cluster; stand up a separate envtest environment for each to mirror this.
	By("bootstrapping the vcluster test environment")
	vclusterEnv = &envtest.Environment{}

	var err error
	vclusterCfg, err = vclusterEnv.Start()
	Expect(err).Should(Succeed())
	Expect(vclusterCfg).NotTo(BeNil())

	By("bootstrapping the host cluster test environment")
	hostEnv = &envtest.Environment{}

	hostCfg, err = hostEnv.Start()
	Expect(err).Should(Succeed())
	Expect(hostCfg).NotTo(BeNil())

	By("construct the host cluster k8s client")
	hostK8sClient, err = client.New(hostCfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).Should(Succeed())
	Expect(hostK8sClient).NotTo(BeNil())

	By("creating a test namespace in the host cluster")
	hostNS := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNamespace,
		},
	}
	Expect(hostK8sClient.Create(ctx, &hostNS)).Should(Succeed(), "failed to create namespace in the host cluster")

	By("starting the controller manager for the vcluster")
	klog.InitFlags(flag.CommandLine)
	flag.Parse()

	mgr, err = ctrl.NewManager(vclusterCfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
		Logger: textlogger.NewLogger(textlogger.NewConfig(textlogger.Verbosity(4))),
	})
	Expect(err).Should(Succeed())

	// The vcluster client is sourced from the manager (rather than built directly against
	// vclusterCfg) so that reads made by the controller under test go through the same cache
	// the manager populates; the test's own client below is built directly instead, so that
	// writes made by the test setup are visible immediately, without waiting on cache sync.
	vclusterClient = mgr.GetClient()

	err = (&Reconciler{
		vclusterClient:    vclusterClient,
		hostClusterClient: hostK8sClient,
	}).SetupWithManager(mgr)
	Expect(err).Should(Succeed())

	go func() {
		defer GinkgoRecover()
		err = mgr.Start(ctx)
		Expect(err).Should(Succeed(), "failed to run manager")
	}()

	// Wait for the cache to sync before moving onto the test stage, otherwise some Pod events
	// might not be caught properly by the controller under test.
	Eventually(func() bool {
		return mgr.GetCache().WaitForCacheSync(ctx)
	}).Should(BeTrue(), "failed to wait for cache to sync")

	By("creating a test namespace in the vcluster")
	vclusterNS := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: testNamespace,
		},
	}
	Expect(vclusterClient.Create(ctx, &vclusterNS)).Should(Succeed(), "failed to create namespace in the vcluster")
})

var _ = AfterSuite(func() {
	defer klog.Flush()

	cancel()

	By("tearing down the host cluster test environment")
	Expect(hostEnv.Stop()).Should(Succeed())

	By("tearing down the vcluster test environment")
	Expect(vclusterEnv.Stop()).Should(Succeed())
})
