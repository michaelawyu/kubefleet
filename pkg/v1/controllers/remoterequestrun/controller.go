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

package remoterequestrun

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	placementv1alpha1 "github.com/kubefleet-dev/kubefleet/apis/kubefleet.dev/placement/v1alpha1"
	fleetErrors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	podLogTailLines = int64(200)
)

type Reconciler struct {
	hubClient        client.Client
	memberCoreClient corev1client.CoreV1Interface
}

func New(hubClient client.Client, memberCoreClient corev1client.CoreV1Interface) *Reconciler {
	return &Reconciler{
		hubClient:        hubClient,
		memberCoreClient: memberCoreClient,
	}
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation started",
		"remoteRequest", req.NamespacedName)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ended",
			"remoteRequest", req.NamespacedName,
			"latencyMilliseconds", latency)
	}()

	remoteReq := &placementv1alpha1.RemoteRequestRun{}
	if err := r.hubClient.Get(ctx, req.NamespacedName, remoteReq); err != nil {
		if apierrors.IsNotFound(err) {
			klog.V(2).InfoS("The RemoteRequestRun object is not found; no further processing is needed",
				"remoteRequest", req.NamespacedName)
			return ctrl.Result{}, nil
		}
		wrappedErr := fleetErrors.NewAPIServerError(err, "", true, "remoteRequest", req.NamespacedName)
		klog.ErrorS(wrappedErr, "Failed to retrieve the RemoteRequestRun object", fleetErrors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}

	completedCond := meta.FindStatusCondition(remoteReq.Status.Conditions, placementv1alpha1.RemoteRequestRunCondTypeCompleted)
	if completedCond != nil && completedCond.ObservedGeneration == remoteReq.Generation {
		klog.V(2).InfoS("The RemoteRequestRun has already been completed; no further processing is needed",
			"remoteRequest", klog.KObj(remoteReq))
		return ctrl.Result{}, nil
	}

	var (
		result interface{}
		runErr error
		errMsg string
	)

	switch {
	case remoteReq.Spec.Resource == "pods" && remoteReq.Spec.SubResource == "":
		result, runErr = r.listPods(ctx, remoteReq.Spec.Namespace, remoteReq.Spec.LabelMatchers)
		if runErr != nil {
			errMsg = fmt.Sprintf("Failed to list pods: %v", runErr)
		}
	case remoteReq.Spec.Resource == "pods" && remoteReq.Spec.SubResource == "logs":
		result, runErr = r.getPodLogs(ctx, remoteReq.Spec.Namespace, remoteReq.Spec.Name)
		if runErr != nil {
			errMsg = fmt.Sprintf("Failed to get pod logs: %v", runErr)
		}
	default:
		errMsg = fmt.Sprintf("Unsupported resource %q or subresource %q", remoteReq.Spec.Resource, remoteReq.Spec.SubResource)
	}

	if errMsg != "" || runErr != nil {
		meta.SetStatusCondition(&remoteReq.Status.Conditions, metav1.Condition{
			Type:               placementv1alpha1.RemoteRequestRunCondTypeCompleted,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: remoteReq.GetGeneration(),
			Reason:             placementv1alpha1.RemoteRequestRunCompletedCondReasonFailed,
			Message:            errMsg,
		})
		if updateErr := r.hubClient.Status().Update(ctx, remoteReq); updateErr != nil {
			wrappedErr := fleetErrors.NewAPIServerError(updateErr,
				"failed to update the status of the RemoteRequestRun",
				false,
				"remoteRequest", klog.KObj(remoteReq))
			klog.ErrorS(wrappedErr, "Failed to update status for RemoteRequestRun after failure", fleetErrors.Args(wrappedErr)...)
			return ctrl.Result{}, wrappedErr
		}
		return ctrl.Result{}, nil
	}

	rawResult, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		wrappedErr := fleetErrors.NewUnexpectedError(marshalErr, "failed to marshal the result",
			"remoteRequest", klog.KObj(remoteReq))
		klog.ErrorS(wrappedErr, "Failed to marshal the result for RemoteRequestRun", fleetErrors.Args(wrappedErr)...)

		meta.SetStatusCondition(&remoteReq.Status.Conditions, metav1.Condition{
			Type:               placementv1alpha1.RemoteRequestRunCondTypeCompleted,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: remoteReq.GetGeneration(),
			Reason:             placementv1alpha1.RemoteRequestRunCompletedCondReasonFailed,
			Message:            fmt.Sprintf("Failed to marshal the result: %v", marshalErr),
		})
		if updateErr := r.hubClient.Status().Update(ctx, remoteReq); updateErr != nil {
			innerErr := fleetErrors.NewAPIServerError(updateErr,
				"failed to update the status of the RemoteRequestRun",
				false,
				"remoteRequest", klog.KObj(remoteReq))
			klog.ErrorS(innerErr, "Failed to update status for RemoteRequestRun after marshal failure", fleetErrors.Args(innerErr)...)
			return ctrl.Result{}, innerErr
		}
		return ctrl.Result{}, nil
	}

	remoteReq.Status.Result = rawResult
	meta.SetStatusCondition(&remoteReq.Status.Conditions, metav1.Condition{
		Type:               placementv1alpha1.RemoteRequestRunCondTypeCompleted,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: remoteReq.GetGeneration(),
		Reason:             placementv1alpha1.RemoteRequestRunCompletedCondReasonSucceeded,
		Message:            "Successfully completed the GET request",
	})
	if updateErr := r.hubClient.Status().Update(ctx, remoteReq); updateErr != nil {
		wrappedErr := fleetErrors.NewAPIServerError(updateErr,
			"failed to update the status of the RemoteRequestRun",
			false,
			"remoteRequest", klog.KObj(remoteReq))
		klog.ErrorS(wrappedErr, "Failed to update status for RemoteRequestRun after success", fleetErrors.Args(wrappedErr)...)
		return ctrl.Result{}, wrappedErr
	}
	return ctrl.Result{}, nil
}

// listPods lists all pods in the given namespace filtered by optional label selectors.
func (r *Reconciler) listPods(ctx context.Context, namespace string, labelMatchers map[string]string) (*corev1.PodList, error) {
	podList := &corev1.PodList{}
	listOpts := metav1.ListOptions{
		LabelSelector: metav1.FormatLabelSelector(&metav1.LabelSelector{
			MatchLabels: labelMatchers,
		}),
	}
	podList, err := r.memberCoreClient.Pods(namespace).List(ctx, listOpts)
	if err != nil {
		return nil, err
	}
	return podList, nil
}

// getPodLogs retrieves the last podLogTailLines lines of logs for a pod.
func (r *Reconciler) getPodLogs(ctx context.Context, namespace, name string) (string, error) {
	tailLines := podLogTailLines
	logStream, err := r.memberCoreClient.Pods(namespace).GetLogs(name, &corev1.PodLogOptions{
		TailLines: &tailLines,
	}).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer logStream.Close()

	var buf bytes.Buffer
	scanner := bufio.NewScanner(logStream)
	for scanner.Scan() {
		buf.WriteString(scanner.Text())
		buf.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return "", err
	}
	return buf.String(), nil
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&placementv1alpha1.RemoteRequestRun{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}
