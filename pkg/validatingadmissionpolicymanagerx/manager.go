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

package validatingadmissionpolicymanagerx

import (
	"context"
	"strings"
	"time"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	managedByKubeFleetLabelKey   = "app.kubernetes.io/managed-by"
	managedByKubeFleetLabelValue = "kubefleet"
)

var (
	policyRWOpBackoff = wait.Backoff{
		Steps:    3,
		Duration: 1 * time.Second,
		Factor:   2.0,
		Jitter:   0.1,
	}
)

var (
	AllPolicies = map[string]PolicyRenderer{
		"denyPodsAndReplicaSetsInNonReservedNamespaces": NewDenyPodsAndReplicaSetsInNonReservedNamespacesPolicyRenderer(),
	}
)

type Manager struct {
	Client client.Client

	enabledPolicies []string
}

func (m *Manager) Start(ctx context.Context) error {
	// When starting up, add/update/delete policies and their bindings based on the enabled policies and their
	// corresponding variables.
	policyList := &admissionregistrationv1.ValidatingAdmissionPolicyList{}
	bindingList := &admissionregistrationv1.ValidatingAdmissionPolicyBindingList{}
	labelSelector := client.MatchingLabels{managedByKubeFleetLabelKey: managedByKubeFleetLabelValue}
	// List all the policies and their bindings under management.
	err := retry.OnError(policyRWOpBackoff, func(err error) bool {
		// Retry on any error. Note that nil error is not passed to this function.
		return true
	}, func() error {
		if err := m.Client.List(ctx, policyList, labelSelector); err != nil {
			return errors.NewAPIServerError(err, "Failed to list validating admission policies", false)
		}
		if err := m.Client.List(ctx, bindingList, labelSelector); err != nil {
			return errors.NewAPIServerError(err, "Failed to list validating admission policy bindings", false)
		}
		return nil
	})
	if err != nil {
		return errors.Wraps(err, "Failed to look up existing validating admission policies and their bindings")
	}

	// Build an index for the existing policies and their bindings for easy lookup.
	currentPolicies := make(map[string]*admissionregistrationv1.ValidatingAdmissionPolicy)
	currentBindings := make(map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding)
	for idx := range policyList.Items {
		policy := &policyList.Items[idx]
		currentPolicies[policy.Name] = policy
	}
	for idx := range bindingList.Items {
		binding := &bindingList.Items[idx]
		currentBindings[binding.Name] = binding
	}

	// Prepare the new set of policies and their bindings based on the enabled policies and
	// their corresponding variables.
	newPolicies := map[string]*admissionregistrationv1.ValidatingAdmissionPolicy{}
	newBindings := map[string]*admissionregistrationv1.ValidatingAdmissionPolicyBinding{}
	for idx := range m.enabledPolicies {
		enabledPolicy := m.enabledPolicies[idx]
		policyRenderer, found := AllPolicies[enabledPolicy]
		if !found {
			// A policy is enabled but not found in the known policy renderers; normally this should never occur.
			return errors.NewUnexpectedError(nil, "No policy renderer found for an enabled policy", "enabledPolicy", enabledPolicy)
		}

		newPoliciesPerRenderer := policyRenderer.Policies()
		newBindingsPerRenderer := policyRenderer.PolicyBindings()

		for idx := range newPoliciesPerRenderer {
			newPolicy := newPoliciesPerRenderer[idx]

			// Add the managed by label.
			if newPolicy.Labels == nil {
				newPolicy.Labels = make(map[string]string)
			}
			newPolicy.Labels[managedByKubeFleetLabelKey] = managedByKubeFleetLabelValue

			newPolicies[newPolicy.Name] = newPolicy
		}

		for idx := range newBindingsPerRenderer {
			newBinding := newBindingsPerRenderer[idx]

			// Add the managed by label.
			if newBinding.Labels == nil {
				newBinding.Labels = make(map[string]string)
			}
			newBinding.Labels[managedByKubeFleetLabelKey] = managedByKubeFleetLabelValue

			newBindings[newBinding.Name] = newBinding
		}
	}

	// Create (or update) the new set of policies and their bindings.
	for _, policy := range newPolicies {
		policyToCreateOrUpdate := &admissionregistrationv1.ValidatingAdmissionPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name: policy.Name,
			},
		}
		opRes, err := controllerutil.CreateOrUpdate(ctx, m.Client, policyToCreateOrUpdate, func() error {
			policyCopy := policy.DeepCopy()
			policyToCreateOrUpdate.Spec = policyCopy.Spec
			policyToCreateOrUpdate.Labels = policyCopy.Labels
			return nil
		})
		if err != nil {
			return errors.NewAPIServerError(err,
				"Failed to create or update validating admission policy",
				false,
				"policyName", policy.Name, "operationResult", opRes)
		}
	}

	// Create (or update) the new set of bindings.
	for _, binding := range newBindings {
		bindingToCreateOrUpdate := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: binding.Name,
			},
		}
		opRes, err := controllerutil.CreateOrUpdate(ctx, m.Client, bindingToCreateOrUpdate, func() error {
			bindingCopy := binding.DeepCopy()
			bindingToCreateOrUpdate.Spec = bindingCopy.Spec
			bindingToCreateOrUpdate.Labels = bindingCopy.Labels
			return nil
		})
		if err != nil {
			return errors.NewAPIServerError(err,
				"Failed to create or update validating admission policy binding",
				false,
				"bindingName", binding.Name, "operationResult", opRes)
		}
	}

	// Delete policies that are no longer enabled/needed.
	for policyName, policy := range currentPolicies {
		if _, newPolicyExists := newPolicies[policyName]; newPolicyExists {
			continue
		}

		err := retry.OnError(policyRWOpBackoff, func(err error) bool {
			// Retry if the error is not a NotFound error.
			return !apierrors.IsNotFound(err)
		}, func() error {
			if err := m.Client.Delete(ctx, policy); err != nil {
				return errors.NewAPIServerError(err, "Failed to delete validating admission policy", false, "policyName", policyName)
			}
			return nil
		})
		if err != nil {
			return errors.Wraps(err, "Failed to clean up stale validating admission policy")
		}
	}

	// Delete bindings that are no longer enabled/needed.
	for bindingName, binding := range currentBindings {
		if _, newBindingExists := newBindings[bindingName]; newBindingExists {
			continue
		}

		err := retry.OnError(policyRWOpBackoff, func(err error) bool {
			// Retry if the error is not a NotFound error.
			return !apierrors.IsNotFound(err)
		}, func() error {
			if err := m.Client.Delete(ctx, binding); err != nil {
				return errors.NewAPIServerError(err, "Failed to delete validating admission policy binding", false, "bindingName", bindingName)
			}
			return nil
		})
		if err != nil {
			return errors.Wraps(err, "Failed to clean up stale validating admission policy binding")
		}
	}

	return err
}

func ParseAndValidateEnabledPolicies(enabledPoliciesRawStr string) ([]string, error) {
	enabledPolicies := []string{}

	policyNameSegs := strings.Split(enabledPoliciesRawStr, ",")
	for _, seg := range policyNameSegs {
		if _, found := AllPolicies[seg]; !found {
			return nil, errors.NewUserError(nil, "No matching validating admission policy is found", "wantPolicyName", seg)
		}
		enabledPolicies = append(enabledPolicies, seg)
	}

	return enabledPolicies, nil
}
