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

package orasmanifests

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	experimentalv1beta1 "github.com/kubefleet-dev/kubefleet/apis/experimental/v1beta1"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	controllerName = "ORASK8sManifestsController"
)

type Reconciler struct {
	HubClient client.Client

	UseHTTPToConnectToOCIRegistry bool
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "ORASManifests", req.NamespacedName, "controller", controllerName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends", "ORASManifests", req.NamespacedName, "controller", controllerName, "latency", latency)
	}()

	orasManifests := &experimentalv1beta1.ORASManifests{}
	err := r.HubClient.Get(ctx, req.NamespacedName, orasManifests)
	switch {
	case apierrors.IsNotFound(err):
		// The OCI artifact has been deleted; no further action is needed.
		klog.V(2).InfoS("The ORAS manifests object cannot be found", "ORASManifests", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	case err != nil:
		wrappedErr := errors.NewAPIServerError(err, "", true, "ORASK8sManifests", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to retrieve the ORAS manifests object", errors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	if !orasManifests.DeletionTimestamp.IsZero() {
		// The ORAS manifests object has been marked for deletion; no further action is needed.
		/**
		// Perform the cleanup process if the OCI artifact has been marked for deletion and has a cleanup
		// finalizer set up.
		if controllerutil.ContainsFinalizer(ociArtifact, ociArtifactCleanupFinalizer) {
			if err := r.cleanup(ctx, ociArtifact); err != nil {
				wrappedErr := errors.Wraps(err, "", "OCIArtifact", req.NamespacedName, "controller", controllerName)
				klog.ErrorS(wrappedErr, "Failed to complete the cleanup process", errors.Args(wrappedErr)...)
				return ctrl.Result{}, wrappedErr
			}

			// Drop the cleanup finalizer from the OCI artifact.
			controllerutil.RemoveFinalizer(ociArtifact, ociArtifactCleanupFinalizer)
			if err := r.HubClient.Update(ctx, ociArtifact); err != nil {
				wrappedErr := errors.NewAPIServerError(err, "", true, "OCIArtifact", req.NamespacedName, "controller", controllerName)
				klog.ErrorS(wrappedErr, "Failed to drop the cleanup finalizer from the OCI artifact", errors.Args(wrappedErr)...)
				return ctrl.Result{}, wrappedErr
			}
		}
		**/
		return ctrl.Result{}, nil
	}

	originalStatus := orasManifests.Status.DeepCopy()

	tab, err := r.process(ctx, orasManifests)
	if err != nil {
		wrappedErr := errors.Wraps(err, "", "ORASManifests", req.NamespacedName, "controller", controllerName)
		klog.ErrorS(wrappedErr, "Failed to process the ORAS manifests object", errors.Args(wrappedErr)...)

		r.reportErrorStatus(orasManifests, err)
	} else {
		klog.V(2).InfoS("Successfully processed the ORAS manifests object", "ORASManifests", req.NamespacedName, "controller", controllerName)
		r.reportResolvedStatus(orasManifests, tab)
	}

	if equality.Semantic.DeepEqual(originalStatus, &orasManifests.Status) {
		klog.V(2).InfoS("No status change found for the ORAS manifests object; skipping the status update", "ORASManifests", req.NamespacedName, "controller", controllerName)
	} else {
		if err := r.HubClient.Status().Update(ctx, orasManifests); err != nil {
			wrappedErr := errors.NewAPIServerError(err, "", true, "ORASManifests", req.NamespacedName, "controller", controllerName)
			klog.ErrorS(wrappedErr, "Failed to update the status of the ORAS manifests object", errors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the manager. This controller manages the
// ORASManifests API object.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(controllerName).
		For(&experimentalv1beta1.ORASManifests{}).
		Complete(r)
}
