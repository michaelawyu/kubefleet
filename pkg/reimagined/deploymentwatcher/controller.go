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

package deploymentwatcher

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	controllerName = "DeploymentWatcher"
)

type Reconciler struct {
	HubClient client.Client
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "deployment", req.NamespacedName, "controller", controllerName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends", "deployment", req.NamespacedName, "controller", controllerName, "latency", latency)
	}()

	// Retrieve the Deployment object.
	deploy := appsv1.Deployment{}
	err := r.HubClient.Get(ctx, req.NamespacedName, &deploy)
	switch {
	case apierrors.IsNotFound(err):
		// The Deployment object cannot be found; it may have been deleted already. No need for further
		// reconciliation.
		klog.V(2).InfoS("Deployment cannot be found", "deployment", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{}, nil
	case err != nil:
		// An error occurred while trying to retrieve the Deployment object; retry later.
		wrappedErr := errors.NewAPIServerError()
		klog.ErrorS(err, "Failed to get Deployment object. Requeuing.", "deployment", req.NamespacedName, "controller", controllerName)
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, nil
}
