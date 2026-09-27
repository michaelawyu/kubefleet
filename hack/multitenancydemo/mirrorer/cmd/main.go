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

// Package main implements a small standalone binary for the KubeFleet multi-tenancy demo. It
// starts two separate controller-runtime managers: one connected to the tenant cluster (the
// vcluster), and one connected to the host cluster. The sourcetracker controller runs on the
// vcluster manager, and the placementpolicymaker controller runs on the host cluster manager.
package main

import (
	"flag"
	"fmt"
	"os"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	"github.com/kubefleet-dev/kubefleet/hack/multitenancydemo/mirrorer/controllers/placementpolicymaker"
	"github.com/kubefleet-dev/kubefleet/hack/multitenancydemo/mirrorer/controllers/sourcetracker"
	//+kubebuilder:scaffold:imports
)

const (
	// vclusterKubeConfigEnvVar is the environment variable that holds the path to the
	// kubeconfig file for the tenant cluster (the vcluster).
	vclusterKubeConfigEnvVar = "VCLUSTER_KUBECONFIG"

	// The name of the vcluster. It is also the name of the namespace where the vcluster is hosted.
	vclusterNameEnvVar = "VCLUSTER_NAME"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	klog.InitFlags(nil)

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(placementv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	flag.Parse()
	defer klog.Flush()

	// Set up the controller-runtime logger.
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	vclusterConfig, err := buildConfigFromEnv(vclusterKubeConfigEnvVar)
	if err != nil {
		klog.ErrorS(err, "Failed to build Kubernetes client configuration for the vcluster")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	// Unlike the vcluster (a separate cluster reachable only via an explicit kubeconfig), the
	// mirrorer runs as a Pod on the host cluster itself, so its credentials for the host cluster
	// are discovered the standard in-cluster way, via the Pod's own ServiceAccount token and the
	// KUBERNETES_SERVICE_HOST/PORT environment variables Kubernetes injects automatically; no
	// kubeconfig file (and so no ConfigMap/Secret to mount) is needed for it.
	hostConfig, err := rest.InClusterConfig()
	if err != nil {
		klog.ErrorS(err, "Failed to build in-cluster Kubernetes client configuration for the host cluster")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	// The vcluster's namespaced resources are synced into a single namespace in the host cluster,
	// named after the vcluster itself, so the host manager's cache is scoped to that namespace.
	vclusterName := os.Getenv(vclusterNameEnvVar)
	if vclusterName == "" {
		klog.ErrorS(nil, "Environment variable is not set or is empty", "envVar", vclusterNameEnvVar)
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	// Each manager needs its own metrics and health probe bind addresses, as both managers run
	// in the same process and would otherwise try to bind the same default ports.
	vclusterMgr, err := ctrl.NewManager(vclusterConfig, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: ":8080",
		},
		HealthProbeBindAddress: ":8081",
	})
	if err != nil {
		klog.ErrorS(err, "Failed to create the controller manager for the vcluster")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	hostMgr, err := ctrl.NewManager(hostConfig, hostManagerOptions(vclusterName))
	if err != nil {
		klog.ErrorS(err, "Failed to create the controller manager for the host cluster")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	if err := vclusterMgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		klog.ErrorS(err, "Failed to set up health check for the vcluster controller manager")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}
	if err := hostMgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		klog.ErrorS(err, "Failed to set up health check for the host cluster controller manager")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	// The sourcetracker controller watches Pods in the vcluster, so it runs on the vcluster
	// manager; it also talks to the host cluster (via hostClusterClient) for future use.
	if err := sourcetracker.New(vclusterMgr.GetClient(), hostMgr.GetClient()).SetupWithManager(vclusterMgr); err != nil {
		klog.ErrorS(err, "Failed to set up the sourcetracker controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	// The placementpolicymaker controller only talks to the host cluster, where it watches Pods
	// (scoped to the vcluster's namespace, per the cache configuration above) and manages
	// PlacementPolicy objects.
	if err := placementpolicymaker.New(hostMgr.GetClient()).SetupWithManager(hostMgr); err != nil {
		klog.ErrorS(err, "Failed to set up the placementpolicymaker controller")
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
	}

	//+kubebuilder:scaffold:builder

	ctx := ctrl.SetupSignalHandler()

	// The wait group for synchronizing the exit of both controller managers.
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		klog.InfoS("Starting the controller manager for the vcluster")
		// Start() is a blocking call and it is set to exit on context cancellation.
		if err := vclusterMgr.Start(ctx); err != nil {
			klog.ErrorS(err, "Problem starting the controller manager for the vcluster")
			klog.FlushAndExit(klog.ExitFlushTimeout, 1)
		}
		klog.InfoS("The controller manager for the vcluster has exited")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		klog.InfoS("Starting the controller manager for the host cluster")
		// Start() is a blocking call and it is set to exit on context cancellation.
		if err := hostMgr.Start(ctx); err != nil {
			klog.ErrorS(err, "Problem starting the controller manager for the host cluster")
			klog.FlushAndExit(klog.ExitFlushTimeout, 1)
		}
		klog.InfoS("The controller manager for the host cluster has exited")
	}()

	// Wait for both controller managers to exit.
	wg.Wait()
}

func hostManagerOptions(namespace string) ctrl.Options {
	return ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: ":8090",
		},
		HealthProbeBindAddress: ":8091",
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				namespace: {},
			},
		},
	}
}

// buildConfigFromEnv builds a Kubernetes REST client configuration from the kubeconfig file
// path found in the given environment variable.
func buildConfigFromEnv(envVar string) (*rest.Config, error) {
	kubeConfigPath := os.Getenv(envVar)
	if kubeConfigPath == "" {
		return nil, fmt.Errorf("environment variable %q is not set or is empty", envVar)
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath},
		&clientcmd.ConfigOverrides{})
	return clientConfig.ClientConfig()
}
