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

package workloadresourcesnapshot

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

func (m *Manager) retrieveWorkloadManifestFrom(
	ctx context.Context, workloadPlacement *experimentalv1beta1.WorkloadPlacement,
) (*experimentalv1beta1.ManifestWithIdentifier, error) {
	deployName := workloadPlacement.Spec.WorkloadRef.Name
	// For now, assume that the referenced object is always a Deployment.
	deploy := &appsv1.Deployment{}
	if err := m.client.Get(ctx, client.ObjectKey{Namespace: workloadPlacement.Namespace, Name: deployName}, deploy); err != nil {
		wrappedErr := errors.NewAPIServerError(err,
			"failed to get Deployment object",
			true,
			"sourcekind", "Deployment", "sourceName", deployName, "sourceNamespace", workloadPlacement.Namespace)
		return nil, wrappedErr
	}
	cleanupManagedFields(deploy)
	deploy.Status = appsv1.DeploymentStatus{}

	deployUnstructuredMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(deploy)
	if err != nil {
		wrappedErr := errors.NewUnexpectedError(err,
			"failed to convert Deployment object to unstructured",
			"sourcekind", "Deployment", "sourceName", deployName, "sourceNamespace", workloadPlacement.Namespace)
		return nil, wrappedErr
	}
	deployUnstructured := &unstructured.Unstructured{Object: deployUnstructuredMap}
	deployJSON, err := deployUnstructured.MarshalJSON()
	if err != nil {
		wrappedErr := errors.NewUnexpectedError(err,
			"failed to marshal Deployment object into JSON",
			"sourcekind", "Deployment", "sourceName", deployName, "sourceNamespace", workloadPlacement.Namespace)
		return nil, wrappedErr
	}

	return &experimentalv1beta1.ManifestWithIdentifier{
		Identifier: experimentalv1beta1.SameNamespacedObjectReference{
			APIGroup:   "apps",
			APIVersion: "v1",
			Kind:       "Deployment",
			Resource:   "deployments",
			Name:       deploy.Name,
		},
		Manifest: runtime.RawExtension{Raw: deployJSON},
	}, nil
}

func (m *Manager) retrieveAdditionalResourceManifestsFrom(
	ctx context.Context, workloadPlacement *experimentalv1beta1.WorkloadPlacement,
) ([]experimentalv1beta1.ManifestWithIdentifier, error) {
	var additionalResManifests []experimentalv1beta1.ManifestWithIdentifier
	for idx := range workloadPlacement.Spec.AdditionalResourceRefs {
		additionalResRef := workloadPlacement.Spec.AdditionalResourceRefs[idx]
		additionalResGVR := &schema.GroupVersionResource{
			Group:    additionalResRef.APIGroup,
			Version:  additionalResRef.APIVersion,
			Resource: additionalResRef.Resource,
		}

		additionalResObj, err := m.dynamicClient.
			Resource(*additionalResGVR).
			Namespace(workloadPlacement.Namespace).
			Get(ctx, additionalResRef.Name, metav1.GetOptions{})
		if err != nil {
			wrappedErr := errors.NewAPIServerError(err,
				"failed to get additional resource object",
				true,
				"sourcekind", additionalResRef.Kind, "sourceName", additionalResRef.Name, "sourceNamespace", workloadPlacement.Namespace)
			return nil, wrappedErr
		}
		cleanupManagedFields(additionalResObj)
		unstructured.RemoveNestedField(additionalResObj.Object, "status")

		additionalResObjJSON, err := additionalResObj.MarshalJSON()
		if err != nil {
			wrappedErr := errors.NewUnexpectedError(err,
				"failed to marshal additional resource object into JSON",
				"sourcekind", additionalResRef.Kind, "sourceName", additionalResRef.Name, "sourceNamespace", workloadPlacement.Namespace)
			return nil, wrappedErr
		}

		additionalResManifests = append(additionalResManifests, experimentalv1beta1.ManifestWithIdentifier{
			Identifier: experimentalv1beta1.SameNamespacedObjectReference{
				APIGroup:   additionalResGVR.Group,
				APIVersion: additionalResGVR.Version,
				Kind:       additionalResRef.Kind,
				Resource:   additionalResGVR.Resource,
				Name:       additionalResRef.Name,
			},
			Manifest: runtime.RawExtension{Raw: additionalResObjJSON},
		})
	}
	return additionalResManifests, nil
}

func cleanupManagedFields(obj metav1.Object) {
	obj.SetCreationTimestamp(metav1.Time{})
	obj.SetDeletionTimestamp(nil)
	obj.SetGeneration(0)
	obj.SetManagedFields(nil)
	obj.SetResourceVersion("")
	obj.SetUID("")
	obj.SetFinalizers(nil)
	obj.SetGenerateName("")
	obj.SetOwnerReferences(nil)
	obj.SetSelfLink("")
}
