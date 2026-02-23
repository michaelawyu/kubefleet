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

package options

import (
	"flag"
	"fmt"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	componentbaseconfig "k8s.io/component-base/config"

	"github.com/kubefleet-dev/kubefleet/pkg/utils"
)

// Options is the options to use for running the KubeFleet hub agent.
type OptionsRefreshed struct {
	// Leader election related options.
	LeaderElectionOpts LeaderElectionOptions

	// Options that concern the setup of the controller manager instance in use by the KubeFleet hub agent.
	CtrlMgrOpts ControllerManagerOptions

	// KubeFleet webhook related options.
	WebhookOpts WebhookOptions

	// Feature flags that controll the enabling of certain features in the hub agent.
	FeatureFlags FeatureFlags

	// Options that fine-tune how KubeFleet hub agent manages member clusters in the fleet.
	ClusterMgmtOpts ClusterManagementOptions

	// Options that fine-tune how KubeFleet hub agent manages resources placements in the fleet.
	PlacementMgmtOpts PlacementManagementOptions
}

// LeaderElectionOptions is a set of options the KubeFleet hub agent exposes for controlling
// the leader election behaviors.
//
// Only a subset of leader election options supported by the controller manager is added here,
// for simplicity reasons.
type LeaderElectionOptions struct {
	// Enable leader election or not. This helps ensure that there is only one active
	// hub agent controller manager when multiple instances of the hub agent are running.
	LeaderElect bool

	// The duration of a leader election lease. This is the period where a non-leader candidate
	// will wait after observing a leadership renewal before attempting to acquire leadership of the
	// current leader. And it is also effectively the maximum duration that a leader can be stopped
	// before it is replaced by another candidate. The option only applies if leader election is enabled.
	LeaseDuration metav1.Duration

	// The interval between attempts by the acting master to renew a leadership slot
	// before it stops leading. This must be less than or equal to the lease duration.
	// The option only applies if leader election is enabled.
	RenewDeadline metav1.Duration

	// The duration the clients should wait between attempting acquisition and renewal of a
	// leadership. The option only applies if leader election is enabled.
	RetryPeriod metav1.Duration

	// The namespace of the resource object that will be used to lock during leader election cycles.
	// This option only applies if leader election is enabled.
	ResourceNamespace string
}

type ControllerManagerOptions struct {
	// The TCP address that the hub agent controller manager would bind to
	// serve health probes. It can be set to "0" or the empty value to disable the healt probe server.
	HealthProbeBindAddress string

	// The TCP address that the hub agent controller manager should bind to serve prometheus metrics.
	// It can be set to "0" or the empty value to disable the metrics server.
	MetricsBindAddress string

	// Enable the pprof server for profiling the hub agent controller manager or not.
	EnablePprof bool

	// The port in use by the pprof server for profiling the hub agent controller manager.
	PprofPort int

	// The QPS limit set to the rate limiter of the Kubernetes client in use by the controller manager
	// and all of its managed controller, for client-side throttling purposes.
	HubQPS float64

	// The burst limit set to the rate limiter of the Kubernetes client in use by the controller manager
	// and all of its managed controller, for client-side throttling purposes.
	HubBurst int

	// The duration for the informers in the controller manager to resync.
	ResyncPeriod metav1.Duration
}

type WebhookOptions struct {
	// Enable the KubeFleet webhooks or not.
	EnableWebhooks bool

	// The connection type used by the webhook client. Valid values are `url` and `service`.
	// NOTE: at this moment this setting seems to be superficial, as even with the value set to `url`,
	// the system would just compose a Kubernetes service URL based on the provided service name.
	// This option only applies if webhooks are enabled.
	ClientConnectionType string

	// The Kubernetes service name for hosting the webhooks. This option applies only if
	// webhooks are enabled.
	ServiceName string

	// Enable the KubeFleet guard rail webhook or not. The guard rail webhook helps guard against
	// inadvertent modifications to Fleet resources. This option only applies if webhooks are enabled.
	EnableGuardRail bool

	// A list of comma-separated usernames who are whitelisted in the guard rail webhook and
	// thus allowed to modify KubeFleet resources. This option only applies if the guard rail
	// webhook is enabled.
	GuardRailWhitelistedUsers string

	// Set the guard rail webhook to block users (with certain exceptions) from modifying the labels
	// on the MemberCluster resources. This option only applies if the guard rail webhook is enabled.
	GuardRailDenyModifyMemberClusterLabels bool

	// Enable workload resources (pods and replicaSets) to be created in the hub cluster or not.
	// If set to false, the KubeFleet pod and replicaset validating webhooks, which blocks the creation
	// of pods and replicaSets outside KubeFleet reserved namespaces for most users, will be disabled.
	// This option only applies if webhooks are enabled.
	EnableWorkload bool

	// Use the cert-manager project for managing KubeFleet webhook server certificates or not.
	// If set to false, the system will use self-signed certificates.
	// This option only applies if webhooks are enabled.
	UseCertManager bool
}

type FeatureFlags struct {
	// Enable the hub agent to watch the KubeFleet v1alpha1 APIs or not.
	// TO-DO (weiweng): remove this field soon. Only kept for backward compatibility.
	EnableV1Alpha1APIs bool

	// Enable the hub agent to watch the KubeFleet v1beta1 APIs or not. This flag is kept only for
	// compatibility reasons; it has no effect at this moment, as KubeFleet v1alpha1 APIs have
	// been removed and the v1beta1 APIs are the storage version in use.
	EnableV1Beta1APIs bool

	// Enable the ClusterInventory API support in the KubeFleet hub agent or not.
	//
	// ClusterInventory APIs are a set of Kubernetes Multi-Cluster SIG standard APIs for discovering
	// currently registered member clusters in a multi-cluster management platform.
	EnableClusterInventoryAPIs bool

	// Enable the StagedUpdateRun API support in the KubeFleet hub agent or not.
	//
	// StagedUpdateRun APIs are a set of KubeFleet APIs for progressively updating resource placements.
	EnableStagedUpdateRunAPIs bool

	// Enable the Eviction API support in the KubeFleet hub agent or not.
	//
	// Eviction APIs are a set of KubeFleet APIs for evicting resource placements from member clusters
	// with minimal disruptions.
	EnableEvictionAPIs bool

	// Enable the ResourcePlacement API support in the KubeFleet hub agent or not.
	//
	// ResourcePlacement APIs are a set of KubeFleet APIs for processing namespace scoped resource placements.
	// This flag does not concern the cluster-scoped placement APIs (`ClusterResourcePlacement` and its related APIs).
	EnableResourcePlacementAPIs bool
}

type ClusterManagementOptions struct {
	// Expect that Fleet networking agents have been installed in the fleet or not. If set to true,
	// the hub agent will start to expect heartbeats from the networking agents on the member cluster related
	// resources.
	NetworkingAgentsEnabled bool

	// The duration the KubeFleet hub agent will wait for new heartbeats before marking a member cluster as unhealthy.
	UnhealthyThreshold metav1.Duration

	// The duration the KubeFleet hub agent will wait before force-deleting a member cluster resource after it has been
	// marked for deletion.
	ForceDeleteWaitTime metav1.Duration
}

type PlacementManagementOptions struct {
	// The period the KubeFleet hub agent will wait before marking a Work ojbect as failed.
	//
	// This option is no longer in use and is only kept for compatibility reasons.
	WorkPendingGracePeriod metav1.Duration

	// A list of APIs that are block-listed for resource placement. Any resources under such APIs will be ignored
	// by the KubeFleet hub agent and will not be selected for resource placement.
	//
	// The list is a collection of GVKs separated by semicolons. A GVK can be of the format GROUP,
	// GROUP/VERSION, or GROUP/VERSION/KINDS, where KINDS is a comma separated array of Kind values. If you would
	// like to skip specific versions and/or kinds in the core API group, use the format VERSION, or
	// VERSION/KINDS instead. Below are some examples:
	//
	// * networking.k8s.io: skip all resources in the networking.k8s.io API group for placement;
	// * networking.k8s.io/v1beta1: skip all resources in the networking.k8s.io/v1beta1 group version for placement;
	// * networking.k8s.io/v1beta1/Ingress,IngressClass: skip the Ingress and IngressClass resources
	//   in the networking.k8s.io/v1beta1 group version for placement;
	// * v1beta1: skip all resources of version v1beta1 in the core API group for placement;
	// * v1/ConfigMap: skip ConfigMap resources of version v1 in the core API group for placement;
	// * networking.k8s.io/v1beta1/Ingress; v1beta1: skip the Ingress resource in the networking.k8s.io/v1beta1 group version
	//   and all resources of version v1beta1 in the core API group for placement.
	//
	// This option is mutually exclusive with the AllowedPropagatingAPIs option. KubeFleet comes with a built-in
	// block list of APIs for resource placement that covers most KubeFleet APIs and a select few of critical Kubernetes
	// system APIs; see the source code for more information.
	SkippedPropagatingAPIs string
	// A list of APIs that are allow-listed for resource placement. If specified, only resources under such APIs
	// will be selected for resource placement by the KubeFleet hub agent.
	//
	// The list is a collection of GVKs separated by semicolons. A GVK can be of the format GROUP,
	// GROUP/VERSION, or GROUP/VERSION/KINDS, where KINDS is a comma separated array of Kind values. If you would
	// like to skip specific versions and/or kinds in the core API group, use the format VERSION, or
	// VERSION/KINDS instead. Below are some examples:
	//
	// * networking.k8s.io: allow all resources in the networking.k8s.io API group for placement only;
	// * networking.k8s.io/v1beta1: allow all resources in the networking.k8s.io/v1beta1 group version for placement only;
	// * networking.k8s.io/v1beta1/Ingress,IngressClass: allow the Ingress and IngressClass resources
	//   in the networking.k8s.io/v1beta1 group version for placement only;
	// * v1beta1: allow all resources of version v1beta1 in the core API group for placement only;
	// * v1/ConfigMap: allow ConfigMap resources of version v1 in the core API group for placement only;
	// * networking.k8s.io/v1beta1/Ingress; v1beta1: only allow the Ingress resource in the networking.k8s.io/v1beta1
	//   group version and all resources of version v1beta1 in the core API group for placement.
	//
	// This option is mutually exclusive with the SkippedPropagatingAPIs option.
	AllowedPropagatingAPIs string

	// A list of namespace names that are block-listed for resource placement. The KubeFleet hub agent
	// will ignore the namespaces and any resources within them when selecting resources for placement.
	//
	// This list is a collection of names separated by commas, such as `internals,monitoring`. KubeFleet
	// also blocks a number of reserved namespace names for placement by default; such namespaces include
	// those that are prefixed with `kube-`, and `fleet-system`.
	SkippedPropagatingNamespaces string

	// The number of concurrent workers that help process resource changes for the placement APIs.
	ConcurrentResourceChangeSyncs int

	// The expected maximum number of member clusters in the fleet. This is used specifically for the purpose
	// of setting the number of concurrent workers for several key placement related controllers; setting the value
	// higher increases the concurrency of such controllers. KubeFleet will not enforce this limit on the actual
	// number of member clusters in the fleet.
	MaxFleetSize int

	// The expected maximum number of placements that are allowed to run concurrently. This is used specifically for
	// the purpose of setting the number of concurrent workers for several key placement related controllers; setting
	// the value higher increases the concurrency of such controllers.
	MaxConcurrentClusterPlacement int

	// The rate limiting options for work queues in use by several placement related controllers.
	WorkQueueRateLimiterOpts RateLimitOptions

	// The minimum interval between resource snapshot creations.
	//
	// KubeFleet will collect resource changes periodically (as controlled by the ResourceChangesCollectionDuration parameter);
	// if new changes are found, KubeFleet will build a new resource snapshot if there has not been any
	// new snapshot built within the ResourceSnapshotCreationMinimumInterval.
	ResourceSnapshotCreationMinimumInterval time.Duration

	// The interval between resource change collection attempts.
	//
	// KubeFleet will collect resource changes periodically (as controlled by the ResourceChangesCollectionDuration parameter);
	// if new changes are found, KubeFleet will build a new resource snapshot if there has not been any
	// new snapshot built within the ResourceSnapshotCreationMinimumInterval.
	ResourceChangesCollectionDuration time.Duration
}

func NewOptionsRefreshed() *OptionsRefreshed {
	return &OptionsRefreshed{}
}

func (o *OptionsRefreshed) AddFlags(flags *flag.FlagSet) {
	// Leader election options.

	flags.BoolVar(
		&o.LeaderElectionOpts.LeaderElect,
		"leader-elect",
		// Note: this should be overriden to true even in cases where the hub agent deployment runs with only
		// one replica, as this can help ensure system correctness during rolling updates.
		false,
		"Enable a leader election client to gain leadership before the hub agent controller manager starts to run or not.")

	// This input is sent to the controller manager for validation; no further check here.
	flags.DurationVar(
		&o.LeaderElectionOpts.LeaseDuration.Duration,
		"leader-lease-duration",
		15*time.Second,
		"The duration of a leader election lease. This is the period where a non-leader candidate will wait after observing a leadership renewal before attempting to acquire leadership of the current leader. And it is also effectively the maximum duration that a leader can be stopped before it is replaced by another candidate. The option only applies if leader election is enabled.",
	)

	// This input is sent to the controller manager for validation; no further check here.
	flags.DurationVar(
		&o.LeaderElectionOpts.RenewDeadline.Duration,
		"leader-renew-deadline",
		10*time.Second,
		"The interval between attempts by the acting master to renew a leadership slot before it stops leading. This must be less than or equal to the lease duration. The option only applies if leader election is enabled",
	)

	// This input is sent to the controller manager for validation; no further check here.
	flags.DurationVar(
		&o.LeaderElectionOpts.RetryPeriod.Duration,
		"leader-retry-period",
		2*time.Second,
		"The duration the clients should wait between attempting acquisition and renewal of a leadership. The option only applies if leader election is enabled",
	)

	// This input is sent to the controller manager for validation; no further check here.
	flags.StringVar(
		&o.LeaderElectionOpts.ResourceNamespace,
		"leader-election-namespace",
		utils.FleetSystemNamespace,
		"The namespace of the resource object that will be used to lock during leader election cycles. The option only applies if leader election is enabled.",
	)

	// Controller manager options.

	// This input is sent to the controller manager for validation; no further check here.
	flags.StringVar(
		&o.CtrlMgrOpts.HealthProbeBindAddress,
		"health-probe-bind-address",
		":8081",
		"The address (and port) on which the controller manager serves health probe requests. Set to '0' or empty value to disable the health probe server. Defaults to ':8081'.")

	// This input is sent to the controller manager for validation; no further check here.
	flag.StringVar(
		&o.CtrlMgrOpts.MetricsBindAddress,
		"metrics-bind-address",
		":8080",
		"The address (and port) on which the controller manager serves prometheus metrics. Set to '0' or empty value to disable the metrics server. Defaults to ':8080'.")

	flag.BoolVar(
		&o.CtrlMgrOpts.EnablePprof,
		"enable-pprof",
		false,
		"Enable the pprof server for profiling the hub agent controller manager or not.",
	)

	// This input is sent to the controller manager for validation; no further check here.
	flag.IntVar(
		&o.CtrlMgrOpts.PprofPort,
		"pprof-port",
		6065,
		"The port that the agent will listen for serving pprof profiling requests. This option only applies if the pprof server is enabled. Defaults to 6065.",
	)

	flags.Func(
		"hub-api-qps",
		"The QPS limit set to the rate limiter of the Kubernetes client in use by the controller manager and all of its managed controller, for client-side throttling purposes. Defaults to 250. Use a positive float64 value in the range [10.0, 10000.0], or set a less or equal to zero value to disable client-side throttling.",
		func(s string) error {
			// Some validation is also performed on the controller manager side and the client-go side. Just
			// to be on the safer side we also impose some limits here.
			if len(s) == 0 {
				o.CtrlMgrOpts.HubQPS = 250
				return nil
			}

			qps, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return fmt.Errorf("failed to parse float64 value: %w", err)
			}

			if qps < 0.0 {
				// Disable client-side throttling.
				o.CtrlMgrOpts.HubQPS = -1
				return nil
			}

			if qps < 10.0 || qps > 10000.0 {
				return fmt.Errorf("QPS limit is set to an invalid value (%f), must be a value in the range [10.0, 10000.0]", qps)
			}
			o.CtrlMgrOpts.HubQPS = qps
			return nil
		},
	)

	flags.Func(
		"hub-api-burst",
		"The burst limit set to the rate limiter of the Kubernetes client in use by the controller manager and all of its managed controller, for client-side throttling purposes. Defaults to 1000. Must be a positive value in the range [10, 20000], and it should be no less than the QPS limit.",
		func(s string) error {
			// Some validation is also performed on the controller manager side and the client-go side. Just
			// to be on the safer side we also impose some limits here.
			if len(s) == 0 {
				o.CtrlMgrOpts.HubBurst = 1000
				return nil
			}

			burst, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("failed to parse int value: %w", err)
			}

			if burst < 10 || burst > 20000 {
				return fmt.Errorf("burst limit is set to an invalid value (%d), must be a value in the range [10, 20000]", burst)
			}
			o.CtrlMgrOpts.HubBurst = burst
			return nil
		},
	)

	flags.Func(
		"resync-period",
		"The duration for the informers in the controller manager to resync. Defaults to 6 hours. Must be a duration in the range [1h, 12h].",
		func(s string) error {
			// Some validation is also performed on the controller manager side. Just
			// to be on the safer side we also impose some limits here.
			if len(s) == 0 {
				o.CtrlMgrOpts.ResyncPeriod = metav1.Duration{Duration: 6 * time.Hour}
			}

			dur, err := time.ParseDuration(s)
			if err != nil {
				return fmt.Errorf("failed to parse duration value: %w", err)
			}
			if dur < time.Hour || dur > 12*time.Hour {
				return fmt.Errorf("resync period is set to an invalid value (%s), must be a value in the range [1h, 12h]", s)
			}
			o.CtrlMgrOpts.ResyncPeriod = metav1.Duration{Duration: dur}
			return nil
		},
	)

	// Webhook options.

	flags.BoolVar(
		&o.WebhookOpts.EnableWebhooks,
		"enable-webhook",
		true,
		"Enable the KubeFleet webhooks or not.",
	)

	flags.Func(
		"webhook-client-connection-type",
		"The connection type used by the webhook client. Valid values are `url` and `service`. Defaults to `url`. This option only applies if webhooks are enabled.",
		func(s string) error {
			if len(s) == 0 {
				o.WebhookOpts.ClientConnectionType = "url"
				return nil
			}

			parsedStr, err := parseWebhookClientConnectionString(s)
			if err != nil {
				return fmt.Errorf("invalid webhook client connection type: %w", err)
			}
			o.WebhookOpts.ClientConnectionType = string(parsedStr)
			return nil
		},
	)

	flags.StringVar(
		&o.WebhookOpts.ServiceName,
		"webhook-service-name",
		"fleetwebhook",
		"The Kubernetes service name for hosting the webhooks. This option only applies if webhooks are enabled.",
	)

	flags.BoolVar(
		&o.WebhookOpts.EnableGuardRail,
		"enable-guard-rail",
		false,
		"Enable the KubeFleet guard rail webhook or not. The guard rail webhook helps guard against inadvertent modifications to Fleet resources. This option only applies if webhooks are enabled.",
	)

	flags.StringVar(
		&o.WebhookOpts.GuardRailWhitelistedUsers,
		"whitelisted-users",
		"",
		"A list of comma-separated usernames who are whitelisted in the guard rail webhook and thus allowed to modify KubeFleet resources. This option only applies if the guard rail webhook is enabled.",
	)

	flags.BoolVar(
		&o.WebhookOpts.GuardRailDenyModifyMemberClusterLabels,
		"deny-modify-member-cluster-labels",
		false,
		"Set the guard rail webhook to block users (with certain exceptions) from modifying the labels on the MemberCluster resources. This option only applies if the guard rail webhook is enabled.",
	)

	flags.BoolVar(
		&o.WebhookOpts.EnableWorkload,
		"enable-workload",
		false,
		"Enable workload resources (pods and replicaSets) to be created in the hub cluster or not. If set to false, the KubeFleet pod and replicaset validating webhooks, which blocks the creation of pods and replicaSets outside KubeFleet reserved namespaces for most users, will be disabled. This option only applies if webhooks are enabled.",
	)

	flags.BoolVar(
		&o.WebhookOpts.UseCertManager,
		"use-cert-manager",
		false,
		"Use the cert-manager project for managing KubeFleet webhook server certificates or not. If set to false, the system will use self-signed certificates. If set to true, the EnableWorkload option must be set to true as well. This option only applies if webhooks are enabled.",
	)

	// Feature flags.

	flags.Func(
		"enable-v1alpha1-apis",
		"Enable the hub agent to watch the KubeFleet v1alpha1 APIs or not.",
		func(s string) error {
			if len(s) == 0 {
				o.FeatureFlags.EnableV1Alpha1APIs = false
				return nil
			}

			enabled, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("failed to parse bool value: %w", err)
			}
			if enabled {
				return fmt.Errorf("KubeFleet v1alpha1 APIs are obsolete and must be disabled.")
			}
			return nil
		},
	)

	flags.Func(
		"enable-v1beta1-apis",
		"Enable the hub agent to watch the KubeFleet v1beta1 APIs or not.",
		func(s string) error {
			if len(s) == 0 {
				o.FeatureFlags.EnableV1Beta1APIs = true
				return nil
			}

			enabled, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("failed to parse bool value: %w", err)
			}
			if !enabled {
				return fmt.Errorf("KubeFleet v1beta1 APIs are the storage version and must be enabled.")
			}
			return nil
		},
	)

	flags.BoolVar(
		&o.FeatureFlags.EnableClusterInventoryAPIs,
		"enable-cluster-inventory-apis",
		true,
		"Enable the ClusterInventory API support in the KubeFleet hub agent or not.",
	)

	flags.BoolVar(
		&o.FeatureFlags.EnableStagedUpdateRunAPIs,
		"enable-staged-update-run-apis",
		true,
		"Enable the StagedUpdateRun API support in the KubeFleet hub agent or not.",
	)

	flags.BoolVar(
		&o.FeatureFlags.EnableEvictionAPIs,
		"enable-eviction-apis",
		true,
		"Enable the Eviction API support in the KubeFleet hub agent or not.",
	)

	flags.BoolVar(
		&o.FeatureFlags.EnableResourcePlacementAPIs,
		"enable-resource-placement",
		true,
		"Enable the ResourcePlacement API support (for namespace-scoped placements) in the KubeFleet hub agent or not.",
	)

	// Cluster management options.

	flags.BoolVar(
		&o.ClusterMgmtOpts.NetworkingAgentsEnabled,
		"networking-agents-enabled",
		false,
		"Expect that Fleet networking agents have been installed in the fleet or not. If set to true, the hub agent will start to expect heartbeats from the networking agents on the member cluster related resources.",
	)

	flags.Func(
		"cluster-unhealthy-threshold",
		"The duration the KubeFleet hub agent will wait for new heartbeats before marking a member cluster as unhealthy. Defaults to 60 seconds. Must be a duration in the range [30s, 1h].",
		func(s string) error {
			if len(s) == 0 {
				o.ClusterMgmtOpts.UnhealthyThreshold = metav1.Duration{Duration: 60 * time.Second}
				return nil
			}
			duration, err := time.ParseDuration(s)
			if err != nil {
				return fmt.Errorf("failed to parse duration: %w", err)
			}
			if duration < 30*time.Second || duration > time.Hour {
				return fmt.Errorf("duration must be in the range [30s, 1h]")
			}
			o.ClusterMgmtOpts.UnhealthyThreshold = metav1.Duration{Duration: duration}
			return nil
		},
	)

	flags.Func(
		"force-delete-wait-time",
		"The duration the KubeFleet hub agent will wait before force-deleting a member cluster resource after it has been marked for deletion. Defaults to 15 minutes. Must be a duration in the range [5m, 1h].",
		func(s string) error {
			if len(s) == 0 {
				o.ClusterMgmtOpts.ForceDeleteWaitTime = metav1.Duration{Duration: 15 * time.Minute}
				return nil
			}
			duration, err := time.ParseDuration(s)
			if err != nil {
				return fmt.Errorf("failed to parse duration: %w", err)
			}
			if duration < 5*time.Minute || duration > time.Hour {
				return fmt.Errorf("duration must be in the range [5m, 1h]")
			}
			o.ClusterMgmtOpts.ForceDeleteWaitTime = metav1.Duration{Duration: duration}
			return nil
		},
	)

	// Placement management options.
}

// Options contains everything necessary to create and run controller-manager.
type Options struct {
	// Controllers is the list of controllers to enable or disable
	// '*' means "all enabled by default controllers"
	// 'foo' means "enable 'foo'"
	// '-foo' means "disable 'foo'"
	// first item for a particular name wins
	Controllers []string
	// LeaderElection defines the configuration of leader election client.
	LeaderElection componentbaseconfig.LeaderElectionConfiguration
	// HealthProbeAddress is the TCP address that the is used to serve the heath probes from k8s
	HealthProbeAddress string
	// MetricsBindAddress is the TCP address that the controller should bind to
	// for serving prometheus metrics.
	// It can be set to "0" to disable the metrics serving.
	// Defaults to ":8080".
	MetricsBindAddress string
	// EnableWebhook indicates if we will run a webhook
	EnableWebhook bool
	// Webhook service name
	WebhookServiceName string
	// EnableGuardRail indicates if we will enable fleet guard rail webhook configurations.
	EnableGuardRail bool
	// WhiteListedUsers indicates the list of user who are allowed to modify fleet resources
	WhiteListedUsers string
	// Sets the connection type for the webhook.
	WebhookClientConnectionType string
	// NetworkingAgentsEnabled indicates if we enable network agents
	NetworkingAgentsEnabled bool
	// ClusterUnhealthyThreshold is the duration of failure for the cluster to be considered unhealthy.
	ClusterUnhealthyThreshold metav1.Duration
	// WorkPendingGracePeriod represents the grace period after a work is created/updated.
	// We consider a work failed if a work's last applied condition doesn't change after period.
	WorkPendingGracePeriod metav1.Duration
	// SkippedPropagatingAPIs and AllowedPropagatingAPIs options are used to control the propagation of resources.
	// If none of them are set, the default skippedPropagatingAPIs list will be used.
	// SkippedPropagatingAPIs indicates semicolon separated resources that should be skipped for propagating.
	SkippedPropagatingAPIs string
	// AllowedPropagatingAPIs indicates semicolon separated resources that should be allowed for propagating.
	// This is mutually exclusive with SkippedPropagatingAPIs.
	AllowedPropagatingAPIs string
	// SkippedPropagatingNamespaces is a list of namespaces that will be skipped for propagating.
	SkippedPropagatingNamespaces string
	// HubQPS is the QPS to use while talking with hub-apiserver. Default is 20.0.
	HubQPS float64
	// HubBurst is the burst to allow while talking with hub-apiserver. Default is 100.
	HubBurst int
	// ResyncPeriod is the base frequency the informers are resynced. Defaults is 5 minutes.
	ResyncPeriod metav1.Duration
	// MaxConcurrentClusterPlacement is the number of cluster placement that are allowed to run concurrently.
	MaxConcurrentClusterPlacement int
	// ConcurrentResourceChangeSyncs is the number of resource change reconcilers that are allowed to sync concurrently.
	ConcurrentResourceChangeSyncs int
	// MaxFleetSizeSupported is the max number of member clusters this fleet supports.
	// We will set the max concurrency of related reconcilers (membercluster, rollout,workgenerator)
	// according to this value.
	MaxFleetSizeSupported int
	// RateLimiterOpts is the ratelimit parameters for the work queue
	RateLimiterOpts RateLimitOptions
	// EnableV1Alpha1APIs enables the agents to watch the v1alpha1 CRs.
	// TODO(weiweng): remove this field soon. Only kept for backward compatibility.
	EnableV1Alpha1APIs bool
	// EnableV1Beta1APIs enables the agents to watch the v1beta1 CRs.
	EnableV1Beta1APIs bool
	// EnableClusterInventoryAPIs enables the agents to watch the cluster inventory CRs.
	EnableClusterInventoryAPIs bool
	// ForceDeleteWaitTime is the duration the hub agent waits before force deleting a member cluster.
	ForceDeleteWaitTime metav1.Duration
	// EnableStagedUpdateRunAPIs enables the agents to watch the clusterStagedUpdateRun CRs.
	EnableStagedUpdateRunAPIs bool
	// EnableEvictionAPIs enables to agents to watch the eviction and placement disruption budget CRs.
	EnableEvictionAPIs bool
	// EnableResourcePlacement enables the agents to watch the ResourcePlacement APIs.
	EnableResourcePlacement bool
	// EnablePprof enables the pprof profiling.
	EnablePprof bool
	// PprofPort is the port for pprof profiling.
	PprofPort int
	// DenyModifyMemberClusterLabels indicates if the member cluster labels cannot be modified by groups (excluding system:masters)
	DenyModifyMemberClusterLabels bool
	// EnableWorkload enables workload resources (pods and replicasets) to be created in the hub cluster.
	// When set to true, the pod and replicaset validating webhooks are disabled.
	EnableWorkload bool
	// UseCertManager indicates whether to use cert-manager for webhook certificate management.
	// When enabled, webhook certificates are managed by cert-manager instead of self-signed generation.
	UseCertManager bool
	// ResourceSnapshotCreationMinimumInterval is the minimum interval at which resource snapshots could be created.
	// Whether the resource snapshot is created or not depends on the both ResourceSnapshotCreationMinimumInterval and ResourceChangesCollectionDuration.
	ResourceSnapshotCreationMinimumInterval time.Duration
	// ResourceChangesCollectionDuration is the duration for collecting resource changes into one snapshot.
	ResourceChangesCollectionDuration time.Duration
}

// NewOptions builds an empty options.
func NewOptions() *Options {
	return &Options{
		LeaderElection: componentbaseconfig.LeaderElectionConfiguration{
			LeaderElect:       true,
			ResourceLock:      resourcelock.LeasesResourceLock,
			ResourceNamespace: utils.FleetSystemNamespace,
			ResourceName:      "136224848560.hub.fleet.azure.com",
		},
		MaxConcurrentClusterPlacement:           10,
		ConcurrentResourceChangeSyncs:           1,
		MaxFleetSizeSupported:                   100,
		EnableV1Alpha1APIs:                      false,
		EnableClusterInventoryAPIs:              true,
		EnableStagedUpdateRunAPIs:               true,
		EnableResourcePlacement:                 true,
		EnablePprof:                             false,
		PprofPort:                               6065,
		ResourceSnapshotCreationMinimumInterval: 30 * time.Second,
		ResourceChangesCollectionDuration:       15 * time.Second,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *Options) AddFlags(flags *flag.FlagSet) {
	flags.StringVar(&o.HealthProbeAddress, "health-probe-bind-address", ":8081",
		"The IP address on which to listen for the --secure-port port.")
	flags.StringVar(&o.MetricsBindAddress, "metrics-bind-address", ":8080", "The TCP address that the controller should bind to for serving prometheus metrics(e.g. 127.0.0.1:8088, :8088)")
	flags.BoolVar(&o.LeaderElection.LeaderElect, "leader-elect", false, "Start a leader election client and gain leadership before executing the main loop. Enable this when running replicated components for high availability.")
	flags.DurationVar(&o.LeaderElection.LeaseDuration.Duration, "leader-lease-duration", 15*time.Second, "This is effectively the maximum duration that a leader can be stopped before someone else will replace it.")
	flag.StringVar(&o.LeaderElection.ResourceNamespace, "leader-election-namespace", utils.FleetSystemNamespace, "The namespace in which the leader election resource will be created.")
	flag.BoolVar(&o.EnableWebhook, "enable-webhook", true, "If set, the fleet webhook is enabled.")
	// set a default value 'fleetwebhook' for webhook service name for backward compatibility. The service name was hard coded to 'fleetwebhook' in the past.
	flag.StringVar(&o.WebhookServiceName, "webhook-service-name", "fleetwebhook", "Fleet webhook service name.")
	flag.BoolVar(&o.EnableGuardRail, "enable-guard-rail", false, "If set, the fleet guard rail webhook configurations are enabled.")
	flag.StringVar(&o.WhiteListedUsers, "whitelisted-users", "", "If set, white listed users can modify fleet related resources.")
	flag.StringVar(&o.WebhookClientConnectionType, "webhook-client-connection-type", "url", "Sets the connection type used by the webhook client. Only URL or Service is valid.")
	flag.BoolVar(&o.NetworkingAgentsEnabled, "networking-agents-enabled", false, "Whether the networking agents are enabled or not.")
	flags.DurationVar(&o.ClusterUnhealthyThreshold.Duration, "cluster-unhealthy-threshold", 60*time.Second, "The duration for a member cluster to be in a degraded state before considered unhealthy.")
	flags.DurationVar(&o.WorkPendingGracePeriod.Duration, "work-pending-grace-period", 15*time.Second,
		"Specifies the grace period of allowing a manifest to be pending before marking it as failed.")
	flags.StringVar(&o.AllowedPropagatingAPIs, "allowed-propagating-apis", "", "Semicolon separated resources that should be allowed for propagation. Supported formats are:\n"+
		"<group> for allowing resources with a specific API group(e.g. networking.k8s.io),\n"+
		"<group>/<version> for allowing resources with a specific API version(e.g. networking.k8s.io/v1beta1),\n"+
		"<group>/<version>/<kind>,<kind> for allowing one or more specific resources (e.g. networking.k8s.io/v1beta1/Ingress,IngressClass) where the Kinds are case-insensitive.")
	flags.StringVar(&o.SkippedPropagatingAPIs, "skipped-propagating-apis", "", "Semicolon separated resources that should be skipped from propagating in addition to the default skip list(cluster.fleet.io;policy.fleet.io;work.fleet.io). Supported formats are:\n"+
		"<group> for skip resources with a specific API group(e.g. networking.k8s.io),\n"+
		"<group>/<version> for skip resources with a specific API version(e.g. networking.k8s.io/v1beta1),\n"+
		"<group>/<version>/<kind>,<kind> for skip one or more specific resource(e.g. networking.k8s.io/v1beta1/Ingress,IngressClass) where the kinds are case-insensitive.")
	flags.StringVar(&o.SkippedPropagatingNamespaces, "skipped-propagating-namespaces", "",
		"Comma-separated namespaces that should be skipped from propagating in addition to the default skipped namespaces(fleet-system, namespaces prefixed by kube- and fleet-work-).")
	flags.Float64Var(&o.HubQPS, "hub-api-qps", 250, "QPS to use while talking with fleet-apiserver. Doesn't cover events and node heartbeat apis which rate limiting is controlled by a different set of flags.")
	flags.IntVar(&o.HubBurst, "hub-api-burst", 1000, "Burst to use while talking with fleet-apiserver. Doesn't cover events and node heartbeat apis which rate limiting is controlled by a different set of flags.")
	flags.DurationVar(&o.ResyncPeriod.Duration, "resync-period", 6*time.Hour, "Base frequency the informers are resynced.")
	flags.IntVar(&o.MaxConcurrentClusterPlacement, "max-concurrent-cluster-placement", 100, "The max number of concurrent cluster placement to run concurrently.")
	flags.IntVar(&o.ConcurrentResourceChangeSyncs, "concurrent-resource-change-syncs", 20, "The number of resourceChange reconcilers that are allowed to run concurrently.")
	flags.IntVar(&o.MaxFleetSizeSupported, "max-fleet-size", 100, "The max number of member clusters supported in this fleet")
	flags.BoolVar(&o.EnableV1Alpha1APIs, "enable-v1alpha1-apis", false, "If set, the agents will watch for the v1alpha1 APIs.")
	flags.BoolVar(&o.EnableV1Beta1APIs, "enable-v1beta1-apis", true, "If set, the agents will watch for the v1beta1 APIs.")
	flags.BoolVar(&o.EnableClusterInventoryAPIs, "enable-cluster-inventory-apis", true, "If set, the agents will watch for the ClusterInventory APIs.")
	flags.DurationVar(&o.ForceDeleteWaitTime.Duration, "force-delete-wait-time", 15*time.Minute, "The duration the hub agent waits before force deleting a member cluster.")
	flags.BoolVar(&o.EnableStagedUpdateRunAPIs, "enable-staged-update-run-apis", true, "If set, the agents will watch for the ClusterStagedUpdateRun APIs.")
	flags.BoolVar(&o.EnableEvictionAPIs, "enable-eviction-apis", true, "If set, the agents will watch for the Eviction and PlacementDisruptionBudget APIs.")
	flags.BoolVar(&o.EnableResourcePlacement, "enable-resource-placement", true, "If set, the agents will watch for the ResourcePlacement APIs.")
	flags.BoolVar(&o.EnablePprof, "enable-pprof", false, "If set, the pprof profiling is enabled.")
	flags.IntVar(&o.PprofPort, "pprof-port", 6065, "The port for pprof profiling.")
	flags.BoolVar(&o.DenyModifyMemberClusterLabels, "deny-modify-member-cluster-labels", false, "If set, users not in the system:masters cannot modify member cluster labels.")
	flags.BoolVar(&o.EnableWorkload, "enable-workload", false, "If set, workloads (pods and replicasets) can be created in the hub cluster. This disables the pod and replicaset validating webhooks.")
	flags.BoolVar(&o.UseCertManager, "use-cert-manager", false, "If set, cert-manager will be used for webhook certificate management instead of self-signed certificates.")
	flags.DurationVar(&o.ResourceSnapshotCreationMinimumInterval, "resource-snapshot-creation-minimum-interval", 30*time.Second, "The minimum interval at which resource snapshots could be created.")
	flags.DurationVar(&o.ResourceChangesCollectionDuration, "resource-changes-collection-duration", 15*time.Second,
		"The duration for collecting resource changes into one snapshot. The default is 15 seconds, which means that the controller will collect resource changes for 15 seconds before creating a resource snapshot.")
	o.RateLimiterOpts.AddFlags(flags)
}
