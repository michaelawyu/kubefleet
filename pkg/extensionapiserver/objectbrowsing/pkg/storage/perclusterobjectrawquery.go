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

package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/go-memdb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	objectbrowsingv1beta1 "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/extensionapiserver/objectbrowsing/pkg/storage/inmemdb"
)

type PerClusterObjectRawQueryStorage struct {
	db *memdb.MemDB
}

// Verify that PerClusterObjectRawQueryStorage implements the required interfaces.
var _ rest.Storage = &PerClusterObjectRawQueryStorage{}
var _ rest.SingularNameProvider = &PerClusterObjectRawQueryStorage{}
var _ rest.Scoper = &PerClusterObjectRawQueryStorage{}
var _ rest.KindProvider = &PerClusterObjectRawQueryStorage{}
var _ rest.GroupVersionKindProvider = &PerClusterObjectRawQueryStorage{}

var _ rest.Creater = &PerClusterObjectRawQueryStorage{}
var _ rest.Updater = &PerClusterObjectRawQueryStorage{}
var _ rest.Getter = &PerClusterObjectRawQueryStorage{}
var _ rest.Lister = &PerClusterObjectRawQueryStorage{}
var _ rest.GracefulDeleter = &PerClusterObjectRawQueryStorage{}

const memberClusterNamespacePrefix = "fleet-member-"

// resourceKindMap maps lowercase plural Kubernetes resource names to their Kind counterparts.
var resourceKindMap = map[string]string{
	"configmaps":             "ConfigMap",
	"cronjobs":               "CronJob",
	"daemonsets":             "DaemonSet",
	"deployments":            "Deployment",
	"events":                 "Event",
	"ingresses":              "Ingress",
	"jobs":                   "Job",
	"namespaces":             "Namespace",
	"networkpolicies":        "NetworkPolicy",
	"nodes":                  "Node",
	"persistentvolumeclaims": "PersistentVolumeClaim",
	"persistentvolumes":      "PersistentVolume",
	"pods":                   "Pod",
	"replicasets":            "ReplicaSet",
	"secrets":                "Secret",
	"serviceaccounts":        "ServiceAccount",
	"services":               "Service",
	"statefulsets":           "StatefulSet",
}

// parseRawQueryPath parses a Kubernetes-style API path into its constituent parts (API group, kind,
// namespace, and object name). Supported formats:
//
//	/api/{version}/{resource}[/{name}]
//	/api/{version}/namespaces/{namespace}/{resource}[/{name}]
//	/apis/{group}/{version}/{resource}[/{name}]
//	/apis/{group}/{version}/namespaces/{namespace}/{resource}[/{name}]
func parseRawQueryPath(rawPath string) (group, kind, namespace, name string, err error) {
	parts := strings.Split(strings.TrimPrefix(rawPath, "/"), "/")
	if len(parts) < 3 {
		return "", "", "", "", fmt.Errorf("path %q has too few segments", rawPath)
	}

	idx := 0
	switch parts[idx] {
	case "api":
		idx += 2 // skip "api" and version
	case "apis":
		idx++ // skip "apis"
		group = parts[idx]
		idx += 2 // skip group and version
	default:
		return "", "", "", "", fmt.Errorf("path %q must start with /api or /apis", rawPath)
	}

	if idx >= len(parts) {
		return "", "", "", "", fmt.Errorf("path %q has no resource segment", rawPath)
	}

	if parts[idx] == "namespaces" {
		idx++ // skip "namespaces"
		if idx >= len(parts) {
			// /api/v1/namespaces — listing all namespaces.
			kind = resourceKindMap["namespaces"]
			return
		}
		peeked := parts[idx]
		idx++
		if idx >= len(parts) {
			// /api/v1/namespaces/{name} — getting a specific namespace.
			kind = resourceKindMap["namespaces"]
			name = peeked
			return
		}
		namespace = peeked
	}

	if idx >= len(parts) {
		return "", "", "", "", fmt.Errorf("path %q has no resource segment after namespace", rawPath)
	}

	resource := parts[idx]
	idx++
	ok := false
	kind, ok = resourceKindMap[resource]
	if !ok {
		return "", "", "", "", fmt.Errorf("unknown resource %q in path %q", resource, rawPath)
	}

	if idx < len(parts) {
		name = parts[idx]
	}
	return
}

func NewPerClusterObjectRawQueryStorage(mainDB *memdb.MemDB) (*PerClusterObjectRawQueryStorage, error) {
	return &PerClusterObjectRawQueryStorage{
		db: mainDB,
	}, nil
}

// Implement the rest.Storage interface.

// New is a required method for the rest.Storage interface.
//
// It returns an empty object of a specific API type for further manipulation (CREATE ops).
func (s *PerClusterObjectRawQueryStorage) New() runtime.Object {
	return &objectbrowsingv1beta1.PerClusterAPIObjectRawQuery{}
}

// Destroy is a required method for the rest.Storage interface.
//
// It performs cleanup ops as needed on API server shutdown. In the case of KubeFleet multi-cluster object browsing
// extension API server, since we are using in-memory storage and all data are cache objects by nature,
// this method is a no-op.
func (s *PerClusterObjectRawQueryStorage) Destroy() {}

// Implement the rest.KindProvider interface.

// Kind is a required method for the rest.KindProvider interface.
//
// It returns the Kind name of the API object that this storage manages.
func (s *PerClusterObjectRawQueryStorage) Kind() string {
	return "PerClusterAPIObjectRawQuery"
}

// Implement the rest.Scoper interface.

// NamespaceScoped is a required method for the rest.Scoper interface.
//
// It reports whether the API object under the storage's management is namespace-scoped
// or cluster-scoped. All per-cluster object raw queries are namespace-scoped.
func (s *PerClusterObjectRawQueryStorage) NamespaceScoped() bool {
	return true
}

// Implement the rest.SingularNameProvider interface.

// GetSingularName is a required method for the rest.SingularNameProvider interface.
//
// It returns the singular name of the API object that this storage manages.
func (s *PerClusterObjectRawQueryStorage) GetSingularName() string {
	return "perclusterapiobjectrawquery"
}

// Implement the rest.GroupVersionKindProvider interface.

// GroupVersionKind is a required method for the rest.GroupVersionKindProvider interface.
//
// It returns the GroupVersionKind of the API object that this storage manages.
func (s *PerClusterObjectRawQueryStorage) GroupVersionKind(containingGV schema.GroupVersion) schema.GroupVersionKind {
	return objectbrowsingv1beta1.GroupVersion.WithKind(s.Kind())
}

// Implement the rest.Creater interface.

// Create is a required method for the rest.Creater interface.
//
// It handles the creation of a new API object.
func (s *PerClusterObjectRawQueryStorage) Create(
	ctx context.Context,
	obj runtime.Object,
	createValidation rest.ValidateObjectFunc,
	options *metav1.CreateOptions,
) (runtime.Object, error) {
	txn := s.db.Txn(true)
	defer txn.Abort()

	q, ok := obj.(*objectbrowsingv1beta1.PerClusterAPIObjectRawQuery)
	if !ok {
		return nil, apierrors.NewBadRequest("object is not a PerClusterAPIObjectRawQuery")
	}

	isReady, id, err := inmemdb.DefaultIDIndexer.FromObject(q)
	if err != nil || !isReady {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to get index key for the given query object: %w", err))
	}
	existingObj, err := txn.First(inmemdb.PerClusterObjectRawQueryTableName, inmemdb.IDIndexName, string(id))
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to check for existing object: %w", err))
	}
	if existingObj != nil {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "perclusterapiobjectrawqueries",
		}, q.Name)
	}

	if err := txn.Insert(inmemdb.PerClusterObjectRawQueryTableName, q); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to insert object into the in-memory DB: %w", err))
	}

	txn.Commit()
	return q, nil
}

// Implement the rest.Getter interface.

// Get is a required method for the rest.Getter interface.
//
// It handles the retrieval of an existing API object by name.
func (s *PerClusterObjectRawQueryStorage) Get(
	ctx context.Context,
	name string,
	options *metav1.GetOptions,
) (runtime.Object, error) {
	txn := s.db.Txn(false)
	defer txn.Abort()

	nsName, found := request.NamespaceFrom(ctx)
	if !found {
		return nil, apierrors.NewBadRequest("namespace is required for getting a PerClusterAPIObjectRawQuery")
	}

	prefix := fmt.Sprintf("%s/%s", nsName, name)
	obj, err := txn.First(inmemdb.PerClusterObjectRawQueryTableName, inmemdb.IDIndexName, prefix)
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to query the in-memory DB: %w", err))
	}
	if obj == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "perclusterapiobjectrawqueries",
		}, name)
	}

	q, ok := obj.(*objectbrowsingv1beta1.PerClusterAPIObjectRawQuery)
	if !ok {
		return nil, apierrors.NewInternalError(fmt.Errorf("object with name %s is not a PerClusterAPIObjectRawQuery", name))
	}
	return q, nil
}

// Implement the rest.Lister interface.

// NewList is a required method for the rest.Lister interface.
//
// It returns an empty list object of a specific API type for further manipulation (LIST ops).
func (s *PerClusterObjectRawQueryStorage) NewList() runtime.Object {
	return &objectbrowsingv1beta1.PerClusterAPIObjectRawQueryList{}
}

// List is a required method for the rest.Lister interface.
//
// It handles the listing of API objects.
func (s *PerClusterObjectRawQueryStorage) List(
	ctx context.Context,
	options *internalversion.ListOptions,
) (runtime.Object, error) {
	txn := s.db.Txn(false)
	defer txn.Abort()

	nsName, found := request.NamespaceFrom(ctx)
	if !found {
		return nil, apierrors.NewBadRequest("namespace is required for listing PerClusterAPIObjectRawQueries")
	}

	iter, err := txn.Get(inmemdb.PerClusterObjectRawQueryTableName, inmemdb.IDIndexNameWithPrefix, nsName+"/")
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}

	var queries []objectbrowsingv1beta1.PerClusterAPIObjectRawQuery
	for {
		queryObj := iter.Next()
		if queryObj == nil {
			break
		}
		q, ok := queryObj.(*objectbrowsingv1beta1.PerClusterAPIObjectRawQuery)
		if !ok {
			return nil, apierrors.NewInternalError(fmt.Errorf("object is not a PerClusterAPIObjectRawQuery"))
		}
		queries = append(queries, *q)
	}

	return &objectbrowsingv1beta1.PerClusterAPIObjectRawQueryList{
		Items: queries,
	}, nil
}

// ConvertToTable is a required method for the rest.Lister interface.
//
// It converts the list result into a tabular format for kubectl output. This extension API server does not support
// tabular output, and this method always returns an error.
func (s *PerClusterObjectRawQueryStorage) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return nil, apierrors.NewBadRequest("tabular output is not supported for PerClusterAPIObjectRawQuery")
}

// Implement the rest.GracefulDeleter interface.

// Delete is a required method for the rest.GracefulDeleter interface.
//
// It handles the deletion of an existing API object by name.
func (s *PerClusterObjectRawQueryStorage) Delete(
	ctx context.Context,
	name string,
	deleteValidation rest.ValidateObjectFunc,
	options *metav1.DeleteOptions,
) (runtime.Object, bool, error) {
	txn := s.db.Txn(true)
	defer txn.Abort()

	nsName, found := request.NamespaceFrom(ctx)
	if !found {
		return nil, false, apierrors.NewBadRequest("namespace is required for deleting a PerClusterAPIObjectRawQuery")
	}

	prefix := fmt.Sprintf("%s/%s", nsName, name)
	obj, err := txn.First(inmemdb.PerClusterObjectRawQueryTableName, inmemdb.IDIndexName, prefix)
	if err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	if obj == nil {
		return nil, false, apierrors.NewNotFound(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "perclusterapiobjectrawqueries",
		}, name)
	}

	q, ok := obj.(*objectbrowsingv1beta1.PerClusterAPIObjectRawQuery)
	if !ok {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("object with name %s is not a PerClusterAPIObjectRawQuery", name))
	}

	if err := txn.Delete(inmemdb.PerClusterObjectRawQueryTableName, q); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}

	txn.Commit()
	return q, true, nil
}

// Implement the rest.Updater interface.

// Update is a required method for the rest.Updater interface.
//
// It handles the update of an existing PerClusterAPIObjectRawQuery. On a successful update, it
// executes the raw query path against the per-cluster object cache and populates the status with
// the results.
func (s *PerClusterObjectRawQueryStorage) Update(
	ctx context.Context,
	name string,
	objInfo rest.UpdatedObjectInfo,
	createValidation rest.ValidateObjectFunc,
	updateValidation rest.ValidateObjectUpdateFunc,
	forceAllowCreate bool,
	options *metav1.UpdateOptions,
) (runtime.Object, bool, error) {
	txn := s.db.Txn(true)
	defer txn.Abort()

	nsName, found := request.NamespaceFrom(ctx)
	if !found {
		return nil, false, apierrors.NewBadRequest("namespace is required for updating a PerClusterAPIObjectRawQuery")
	}

	// Get the existing query.
	prefix := fmt.Sprintf("%s/%s", nsName, name)
	existingObj, err := txn.First(inmemdb.PerClusterObjectRawQueryTableName, inmemdb.IDIndexName, prefix)
	if err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	if existingObj == nil {
		return nil, false, apierrors.NewNotFound(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "perclusterapiobjectrawqueries",
		}, name)
	}
	existingQuery, ok := existingObj.(*objectbrowsingv1beta1.PerClusterAPIObjectRawQuery)
	if !ok {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("object with name %s is not a PerClusterAPIObjectRawQuery", name))
	}

	// Get the updated query.
	updatedObj, err := objInfo.UpdatedObject(ctx, existingQuery)
	if err != nil {
		return nil, false, apierrors.NewBadRequest(fmt.Sprintf("failed to get the updated object: %v", err))
	}
	q, ok := updatedObj.(*objectbrowsingv1beta1.PerClusterAPIObjectRawQuery)
	if !ok {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("updated object is not a PerClusterAPIObjectRawQuery"))
	}

	// Derive the cluster name from the query's namespace by stripping the "fleet-member-" prefix.
	clusterName := strings.TrimPrefix(nsName, memberClusterNamespacePrefix)

	// Parse the raw query path and execute it against the per-cluster object cache.
	apiGroup, kind, namespace, objName, err := parseRawQueryPath(q.RawQueryPath)
	if err != nil {
		return nil, false, apierrors.NewBadRequest(fmt.Sprintf("failed to parse raw query path: %v", err))
	}

	var wrappers []objectbrowsingv1beta1.ClusterAPIObjectWrapper
	if objName != "" {
		// Point lookup for a specific object.
		key := fmt.Sprintf("%s/%s/%s/%s/%s", clusterName, apiGroup, kind, namespace, objName)
		obj, err := txn.First(inmemdb.ObjectCacheTableName, inmemdb.PerClusterGroupKindNSNameAndObjectNameIndexName, key)
		if err != nil {
			return nil, false, apierrors.NewInternalError(fmt.Errorf("failed to query object cache: %w", err))
		}
		if obj != nil {
			wrapper, ok := obj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
			if !ok {
				return nil, false, apierrors.NewInternalError(fmt.Errorf("object in cache is not a ClusterAPIObjectWrapper"))
			}
			wrappers = append(wrappers, *wrapper)
		}
	} else {
		// Prefix scan. Include namespace in prefix only when one is specified.
		var searchPrefix string
		if namespace != "" {
			searchPrefix = fmt.Sprintf("%s/%s/%s/%s/", clusterName, apiGroup, kind, namespace)
		} else {
			searchPrefix = fmt.Sprintf("%s/%s/%s/", clusterName, apiGroup, kind)
		}
		iter, err := txn.Get(inmemdb.ObjectCacheTableName, inmemdb.PerClusterGroupKindNSNameAndObjectNameIndexNameWithPrefix, searchPrefix)
		if err != nil {
			return nil, false, apierrors.NewInternalError(fmt.Errorf("failed to query object cache: %w", err))
		}
		for obj := iter.Next(); obj != nil; obj = iter.Next() {
			wrapper, ok := obj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
			if !ok {
				return nil, false, apierrors.NewInternalError(fmt.Errorf("object in cache is not a ClusterAPIObjectWrapper"))
			}
			wrappers = append(wrappers, *wrapper)
		}
	}

	// Populate the status with results and mark the query as executed.
	now := metav1.Now()
	q.Status.Results = wrappers
	q.Status.Conditions = []metav1.Condition{
		{
			Type:               objectbrowsingv1beta1.PerClusterAPIObjectRawQueryCondTypeExecuted,
			Status:             metav1.ConditionTrue,
			Reason:             "Executed",
			LastTransitionTime: now,
		},
	}

	// Store the updated query.
	if err := txn.Insert(inmemdb.PerClusterObjectRawQueryTableName, q); err != nil {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("failed to update query in the in-memory DB: %w", err))
	}

	txn.Commit()
	return q, false, nil
}
