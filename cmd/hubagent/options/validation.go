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
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/kubefleet-dev/kubefleet/pkg/utils"
)

// Validate checks OptionsRefreshed and return a slice of found errs.
//
// Note: the logic here concerns primarily cross-option validation; for single-option validation,
// consider adding the logic directly as part of the flag parsing function, for clarity reasons.
func (o *OptionsRefreshed) Validate() field.ErrorList {
	errs := field.ErrorList{}
	newPath := field.NewPath("OptionsRefreshed")

	if float64(o.CtrlMgrOpts.HubBurst) < o.CtrlMgrOpts.HubQPS {
		errs = append(errs, field.Invalid(newPath.Child("HubBurst"), o.CtrlMgrOpts.HubBurst, "The burst limit for client-side throttling must be greater than or equal to its QPS limit"))
	}

	// Note: this validation logic is a bit weird in the sense that the system accepts
	// either a URL-based connection or a service-based connection for webhook calls,
	// but here the logic enforces that a service name must be provided. The way we handle
	// URLs is also problematic as the code will always format a service-targeted URL using
	// the input. We keep this logic for now for compatibility reasons.
	if o.WebhookOpts.EnableWebhooks && o.WebhookOpts.ServiceName == "" {
		errs = append(errs, field.Invalid(newPath.Child("WebhookServiceName"), o.WebhookOpts.ServiceName, "A webhook service name is required when webhooks are enabled"))
	}

	if o.WebhookOpts.UseCertManager && !o.WebhookOpts.EnableWorkload {
		errs = append(errs, field.Invalid(newPath.Child("UseCertManager"), o.WebhookOpts.UseCertManager, "If cert manager is used for securing webhook connections, the EnableWorkload option must be set to true, so that cert manager pods can run in the hub cluster."))
	}

	if o.FeatureFlags.EnableV1Alpha1APIs == true || o.FeatureFlags.EnableV1Beta1APIs == false {
		errs = append(errs, field.Required(newPath.Child("EnableV1Alpha1APIs"), "Either EnableV1Alpha1APIs or EnableV1Beta1APIs is required"))
	}


	return errs
}

// Validate checks Options and return a slice of found errs.
func (o *Options) Validate() field.ErrorList {
	errs := field.ErrorList{}
	newPath := field.NewPath("Options")

	if o.AllowedPropagatingAPIs != "" && o.SkippedPropagatingAPIs != "" {
		errs = append(errs, field.Invalid(newPath.Child("AllowedPropagatingAPIs"), o.AllowedPropagatingAPIs, "AllowedPropagatingAPIs and SkippedPropagatingAPIs are mutually exclusive"))
	}

	resourceConfig := utils.NewResourceConfig(o.AllowedPropagatingAPIs != "")
	if err := resourceConfig.Parse(o.SkippedPropagatingAPIs); err != nil {
		errs = append(errs, field.Invalid(newPath.Child("SkippedPropagatingAPIs"), o.SkippedPropagatingAPIs, "Invalid API string"))
	}
	if err := resourceConfig.Parse(o.AllowedPropagatingAPIs); err != nil {
		errs = append(errs, field.Invalid(newPath.Child("AllowedPropagatingAPIs"), o.AllowedPropagatingAPIs, "Invalid API string"))
	}

	if o.ClusterUnhealthyThreshold.Duration <= 0 {
		errs = append(errs, field.Invalid(newPath.Child("ClusterUnhealthyThreshold"), o.ClusterUnhealthyThreshold, "Must be greater than 0"))
	}
	if o.WorkPendingGracePeriod.Duration <= 0 {
		errs = append(errs, field.Invalid(newPath.Child("WorkPendingGracePeriod"), o.WorkPendingGracePeriod, "Must be greater than 0"))
	}

	if o.EnableWebhook && o.WebhookServiceName == "" {
		errs = append(errs, field.Invalid(newPath.Child("WebhookServiceName"), o.WebhookServiceName, "Webhook service name is required when webhook is enabled"))
	}

	if o.UseCertManager && !o.EnableWorkload {
		errs = append(errs, field.Invalid(newPath.Child("UseCertManager"), o.UseCertManager, "UseCertManager requires EnableWorkload to be true (when EnableWorkload is false, a validating webhook blocks pod creation except for certain system pods; cert-manager controller pods must be allowed to run in the hub cluster)"))
	}

	connectionType := o.WebhookClientConnectionType
	if _, err := parseWebhookClientConnectionString(connectionType); err != nil {
		errs = append(errs, field.Invalid(newPath.Child("WebhookClientConnectionType"), o.WebhookClientConnectionType, err.Error()))
	}

	if !o.EnableV1Alpha1APIs && !o.EnableV1Beta1APIs {
		errs = append(errs, field.Required(newPath.Child("EnableV1Alpha1APIs"), "Either EnableV1Alpha1APIs or EnableV1Beta1APIs is required"))
	}

	return errs
}
