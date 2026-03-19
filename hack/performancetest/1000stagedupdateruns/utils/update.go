package utils

import (
	"context"
	"fmt"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/util/retry"
)

func (r *Runner) UpdateResources(ctx context.Context) {
	wg := sync.WaitGroup{}

	// Run the producer.
	wg.Add(1)
	go func() {
		defer wg.Done()

		for i := 0; i < r.maxCRPToUpdateCount; i++ {
			select {
			case r.toPatchResourcesChan <- i:
			case <-ctx.Done():
				close(r.toPatchResourcesChan)
				return
			}
		}

		close(r.toPatchResourcesChan)
	}()

	// Run the workers to add a label to each existing resource.
	for i := 0; i < r.concurrentWorkerCount; i++ {
		wg.Add(1)
		go func(workerIdx int) {
			defer wg.Done()

			for {
				// Read from the channel.
				var resIdx int
				var readOk bool
				select {
				case resIdx, readOk = <-r.toPatchResourcesChan:
					if !readOk {
						println(fmt.Sprintf("worker %d exits", workerIdx))
						return
					}
				case <-ctx.Done():
					return
				}

				if err := r.updateNS(ctx, workerIdx, resIdx); err != nil {
					println(fmt.Sprintf("worker %d: failed to update namespace work-%d: %v", workerIdx, resIdx, err))
					continue
				}

				if err := r.updateConfigMap(ctx, workerIdx, resIdx); err != nil {
					println(fmt.Sprintf("worker %d: failed to update configmap data-%d: %v", workerIdx, resIdx, err))
					continue
				}

				if err := r.updateDeploy(ctx, workerIdx, resIdx); err != nil {
					println(fmt.Sprintf("worker %d: failed to update deployment deploy-%d: %v", workerIdx, resIdx, err))
					continue
				}

				println(fmt.Sprintf("worker %d: successfully updated resources group of idx %d", workerIdx, resIdx))

				r.resourcesPatchedCount.Add(1)
			}
		}(i)
	}

	wg.Wait()

	// Do a sanity check report.
	println(fmt.Sprintf("patched %d out of %d resources in total", r.resourcesPatchedCount.Load(), r.maxCRPToUpdateCount))
}

func (r *Runner) updateNS(ctx context.Context, workerIdx, resIdx int) error {
	// Retrieve the namespace.
	ns := &corev1.Namespace{}
	nsName := fmt.Sprintf(nsNameFmt, resIdx)
	errAfterRetries := retry.OnError(r.retryOpsBackoff, func(err error) bool {
		return err != nil && !errors.IsAlreadyExists(err)
	}, func() error {
		return r.hubClient.Get(ctx, types.NamespacedName{Name: nsName}, ns)
	})
	if errAfterRetries != nil {
		return fmt.Errorf("worker %d: failed to get namespace work-%d after retries: %w", workerIdx, resIdx, errAfterRetries)
	}

	labels := ns.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[randomUUIDLabelKey] = string(uuid.NewUUID())
	ns.SetLabels(labels)

	// Update the namespace with the new label.
	errAfterRetries = retry.OnError(r.retryOpsBackoff, func(err error) bool {
		return err != nil
	}, func() error {
		return r.hubClient.Update(ctx, ns)
	})
	if errAfterRetries != nil {
		return fmt.Errorf("worker %d: failed to update namespace work-%d after retries: %w", workerIdx, resIdx, errAfterRetries)
	}
	return nil
}

func (r *Runner) updateConfigMap(ctx context.Context, workerIdx, resIdx int) error {
	// Retrieve the configmap.
	cm := &corev1.ConfigMap{}
	cmName := fmt.Sprintf(configMapNameFmt, resIdx)
	cmNamespace := fmt.Sprintf(nsNameFmt, resIdx)
	errAfterRetries := retry.OnError(r.retryOpsBackoff, func(err error) bool {
		return err != nil && !errors.IsAlreadyExists(err)
	}, func() error {
		return r.hubClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: cmNamespace}, cm)
	})
	if errAfterRetries != nil {
		return fmt.Errorf("worker %d: failed to get configmap data-%d after retries: %w", workerIdx, resIdx, errAfterRetries)
	}

	labels := cm.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[randomUUIDLabelKey] = string(uuid.NewUUID())
	cm.SetLabels(labels)

	// Update the configmap with the new label.
	errAfterRetries = retry.OnError(r.retryOpsBackoff, func(err error) bool {
		return err != nil
	}, func() error {
		return r.hubClient.Update(ctx, cm)
	})
	if errAfterRetries != nil {
		return fmt.Errorf("worker %d: failed to update configmap data-%d after retries: %w", workerIdx, resIdx, errAfterRetries)
	}
	return nil
}

func (r *Runner) updateDeploy(ctx context.Context, workerIdx, resIdx int) error {
	// Retrieve the deployment.
	deploy := &appsv1.Deployment{}
	deployName := fmt.Sprintf(deployNameFmt, resIdx)
	deployNamespace := fmt.Sprintf(nsNameFmt, resIdx)
	errAfterRetries := retry.OnError(r.retryOpsBackoff, func(err error) bool {
		return err != nil && !errors.IsAlreadyExists(err)
	}, func() error {
		return r.hubClient.Get(ctx, types.NamespacedName{Name: deployName, Namespace: deployNamespace}, deploy)
	})
	if errAfterRetries != nil {
		return fmt.Errorf("worker %d: failed to get deployment deploy-%d after retries: %w", workerIdx, resIdx, errAfterRetries)
	}

	labels := deploy.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[randomUUIDLabelKey] = string(uuid.NewUUID())
	deploy.SetLabels(labels)

	// Update the deployment with the new label.
	errAfterRetries = retry.OnError(r.retryOpsBackoff, func(err error) bool {
		return err != nil
	}, func() error {
		return r.hubClient.Update(ctx, deploy)
	})
	if errAfterRetries != nil {
		return fmt.Errorf("worker %d: failed to update deployment deploy-%d after retries: %w", workerIdx, resIdx, errAfterRetries)
	}
	return nil
}
