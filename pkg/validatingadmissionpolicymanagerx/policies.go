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
	"fmt"
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kubefleet-dev/kubefleet/pkg/utils"
)

const (
	denyPodsAndReplicaSetsInNonReservedNamespacesPolicyName        = "fleet-deny-pods-and-replicasets-in-non-reserved-namespaces"
	denyPodsAndReplicaSetsInNonReservedNamespacesPolicyBindingName = "fleet-deny-pods-and-replicasets-in-non-reserved-namespaces-binding"
)

type PolicyRenderer interface {
	Policies() []*admissionregistrationv1.ValidatingAdmissionPolicy
	PolicyBindings() []*admissionregistrationv1.ValidatingAdmissionPolicyBinding
}

type denyPodsAndReplicaSetsInNonReservedNamespacesPolicyRenderer struct{}

func NewDenyPodsAndReplicaSetsInNonReservedNamespacesPolicyRenderer() PolicyRenderer {
	return &denyPodsAndReplicaSetsInNonReservedNamespacesPolicyRenderer{}
}

// Policy returns a ValidatingAdmissionPolicy that denies the creation of Pods and ReplicaSets in
// non-reserved namespaces.
func (r *denyPodsAndReplicaSetsInNonReservedNamespacesPolicyRenderer) Policies() []*admissionregistrationv1.ValidatingAdmissionPolicy {
	celExprSegs := []string{}
	reservedNamespacePrefixes := []string{utils.FleetPrefix, utils.KubePrefix}
	for _, prefix := range reservedNamespacePrefixes {
		celExprSegs = append(celExprSegs, fmt.Sprintf(`object.metadata.namespace.startsWith("%s")`, prefix))
	}
	celExpr := fmt.Sprintf("!(%s)", strings.Join(celExprSegs, " || "))

	policy := &admissionregistrationv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: denyPodsAndReplicaSetsInNonReservedNamespacesPolicyName,
		},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicySpec{
			FailurePolicy: ptr.To(admissionregistrationv1.Fail),
			MatchConstraints: &admissionregistrationv1.MatchResources{
				ResourceRules: []admissionregistrationv1.NamedRuleWithOperations{
					{
						RuleWithOperations: admissionregistrationv1.RuleWithOperations{
							Operations: []admissionregistrationv1.OperationType{
								admissionregistrationv1.Create,
							},
							Rule: admissionregistrationv1.Rule{
								APIGroups:   []string{""},
								APIVersions: []string{"v1"},
								Resources:   []string{"pods"},
							},
						},
					},
				},
			},
			Validations: []admissionregistrationv1.Validation{
				{
					Expression: celExpr,
					Message:    "creating pods and replicas is disallowed in the fleet hub cluster",
					Reason:     ptr.To(metav1.StatusReasonForbidden),
				},
			},
		},
	}

	return []*admissionregistrationv1.ValidatingAdmissionPolicy{policy}
}

// PolicyBinding returns a ValidatingAdmissionPolicyBinding that binds the above ValidatingAdmissionPolicy.
func (r *denyPodsAndReplicaSetsInNonReservedNamespacesPolicyRenderer) PolicyBindings() []*admissionregistrationv1.ValidatingAdmissionPolicyBinding {
	binding := &admissionregistrationv1.ValidatingAdmissionPolicyBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: denyPodsAndReplicaSetsInNonReservedNamespacesPolicyBindingName,
		},
		Spec: admissionregistrationv1.ValidatingAdmissionPolicyBindingSpec{
			PolicyName: denyPodsAndReplicaSetsInNonReservedNamespacesPolicyName,
			ValidationActions: []admissionregistrationv1.ValidationAction{
				admissionregistrationv1.Deny,
			},
		},
	}

	return []*admissionregistrationv1.ValidatingAdmissionPolicyBinding{binding}
}
