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

	"github.com/hashicorp/go-memdb"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"

	objectbrowsingv1beta1 "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/extensionapiserver/objectbrowsing/pkg/storage/inmemdb"
)

type MultiClusterObjectQueryStorage struct {
	db *memdb.MemDB
}

// Verify that MultiClusterObjectQueryStorage implements the required interfaces.
var _ rest.Storage = &MultiClusterObjectQueryStorage{}
var _ rest.SingularNameProvider = &MultiClusterObjectQueryStorage{}
var _ rest.Scoper = &MultiClusterObjectQueryStorage{}
var _ rest.KindProvider = &MultiClusterObjectQueryStorage{}
var _ rest.GroupVersionKindProvider = &MultiClusterObjectQueryStorage{}

var _ rest.Creater = &MultiClusterObjectQueryStorage{}
var _ rest.Getter = &MultiClusterObjectQueryStorage{}
var _ rest.Lister = &MultiClusterObjectQueryStorage{}
var _ rest.GracefulDeleter = &MultiClusterObjectQueryStorage{}

func NewMultiClusterObjectQueryStorage(mainDB *memdb.MemDB) (*MultiClusterObjectQueryStorage, error) {
	return &MultiClusterObjectQueryStorage{
		db: mainDB,
	}, nil
}

// Implement the rest.Storage interface.

// New is a required method for the rest.Storage interface.
//
// It returns an empty object of a specific API type for further manipulation (CREATE/UPDATE ops).
func (s *MultiClusterObjectQueryStorage) New() runtime.Object {
	return &objectbrowsingv1beta1.MultiClusterAPIObjectQuery{}

}

// Destroy is a required method for the rest.Storage interface.
//
// It performs cleanup ops as needed on API server shutdown. In the case of KubeFleet multi-cluster object browsing
// extension API server, since we are using in-memory storage and all data are cache objects by nature,
// this method is a no-op.
func (s *MultiClusterObjectQueryStorage) Destroy() {}

// Implement the rest.KindProvider interface.

// Kind is a required method for the rest.KindProvider interface.
//
// It returns the Kind name of the API object that this storage manages.
func (s *MultiClusterObjectQueryStorage) Kind() string {
	return "MultiClusterAPIObjectQuery"
}

// Implement the rest.Scoper interface.

// NamespaceScoped is a required method for the rest.Scoper interface.
//
// It reports whether the API object under the storage's management is namespace-scoped
// or cluster-scoped. All multi-cluster object queries are cluster-scoped.
func (s *MultiClusterObjectQueryStorage) NamespaceScoped() bool {
	return false
}

// Implement the rest.SingularNameProvider interface.

// SingularName is a required method for the rest.SingularNameProvider interface.
//
// It returns the singular name of the API object that this storage manages.
func (s *MultiClusterObjectQueryStorage) GetSingularName() string {
	return "multiclusterapiobjectquery"
}

// Implement the rest.GroupVersionKindProvider interface.

// GroupVersionKind is a required method for the rest.GroupVersionKindProvider interface.
//
// It returns the GroupVersionKind of the API object that this storage manages.
func (s *MultiClusterObjectQueryStorage) GroupVersionKind(containingGV schema.GroupVersion) schema.GroupVersionKind {
	return objectbrowsingv1beta1.GroupVersion.WithKind(s.Kind())
}

// Implement the rest.CreaterUpdater interface.

// Create is a required method for the rest.CreaterUpdater interface.
//
// It handles the creation of a new API object.
func (s *MultiClusterObjectQueryStorage) Create(
	ctx context.Context,
	obj runtime.Object,
	createValidation rest.ValidateObjectFunc,
	options *metav1.CreateOptions,
) (runtime.Object, error) {
	q, ok := obj.(*objectbrowsingv1beta1.MultiClusterAPIObjectQuery)
	if !ok {
		return nil, apierrors.NewBadRequest("object is not a MultiClusterAPIObjectQuery")
	}

	if len(q.CachedObjectSelectorTerms) != 1 {
		return nil, apierrors.NewBadRequest("exactly one CachedObjectSelectorTerm is required")
	}
	selectorTerm := q.CachedObjectSelectorTerms[0]
	if selectorTerm.Group == nil || selectorTerm.Kind == nil {
		return nil, apierrors.NewBadRequest("group and kind are required in a CachedObjectSelectorTerm")
	}

	txn := s.db.Txn(true)
	defer txn.Abort()

	// Check if a query with the same name already exists.
	isReady, id, err := inmemdb.DefaultIDIndexer.FromObject(q)
	if err != nil || !isReady {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to get index key for the given query object: %w", err))
	}
	existingObj, err := txn.First(inmemdb.MultiClusterObjectQueryTableName, inmemdb.IDIndexName, string(id))
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to check for existing object: %w", err))
	}
	if existingObj != nil {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "multiclusterapiobjectqueries",
		}, q.Name)
	}

	wrappers := make([]objectbrowsingv1beta1.ClusterAPIObjectWrapper, 0, 10)
	if len(q.TargetClusterNames) == 0 {
		// Query objects from all clusters.
		prefix := fmt.Sprintf("%s/%s/", *selectorTerm.Group, *selectorTerm.Kind)
		if selectorTerm.Namespace != nil {
			prefix += fmt.Sprintf("%s/", *selectorTerm.Namespace)

			if selectorTerm.Name != nil {
				prefix += fmt.Sprintf("%s", *selectorTerm.Name)
			}
		}
		iter, err := txn.Get(
			inmemdb.ObjectCacheTableName,
			inmemdb.GroupKindNSNameAndObjectNameIndexNameWithPrefix,
			prefix,
		)
		if err != nil {
			return nil, apierrors.NewInternalError(fmt.Errorf("failed to query object cache with the given selector term: %w", err))
		}
		for obj := iter.Next(); obj != nil; obj = iter.Next() {
			wrapper, ok := obj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
			if !ok {
				return nil, apierrors.NewInternalError(fmt.Errorf("object in cache is not a ClusterAPIObjectWrapper"))
			}
			wrappers = append(wrappers, *wrapper)
		}
	} else {
		// Query objects from specific clusters.
		for _, clusterName := range q.TargetClusterNames {
			prefix := fmt.Sprintf("%s/%s/%s/", clusterName, *selectorTerm.Group, *selectorTerm.Kind)
			if selectorTerm.Namespace != nil {
				prefix += fmt.Sprintf("%s/", *selectorTerm.Namespace)

				if selectorTerm.Name != nil {
					prefix += fmt.Sprintf("%s", *selectorTerm.Name)
				}
			}
			iter, err := txn.Get(
				inmemdb.ObjectCacheTableName,
				inmemdb.PerClusterGroupKindNSNameAndObjectNameIndexNameWithPrefix,
				prefix,
			)
			if err != nil {
				return nil, apierrors.NewInternalError(fmt.Errorf("failed to query object cache with the given selector term: %w", err))
			}
			for obj := iter.Next(); obj != nil; obj = iter.Next() {
				wrapper, ok := obj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
				if !ok {
					return nil, apierrors.NewInternalError(fmt.Errorf("object in cache is not a ClusterAPIObjectWrapper"))
				}
				wrappers = append(wrappers, *wrapper)
			}
		}
	}
	queryStatus := objectbrowsingv1beta1.MultiClusterAPIObjectQueryStatus{
		Conditions: []metav1.Condition{},
		Results:    wrappers,
	}
	meta.SetStatusCondition(&queryStatus.Conditions, metav1.Condition{
		Type:    objectbrowsingv1beta1.MultiClusterAPIObjectQueryCondTypeExecuted,
		Status:  metav1.ConditionTrue,
		Reason:  "QueryExecuted",
		Message: "Query has been executed",
	})
	q.Status = queryStatus

	if err := txn.Insert(inmemdb.MultiClusterObjectQueryTableName, q); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to store the query object in the in-memory DB: %w", err))
	}
	txn.Commit()
	return q, nil
}

// Implement the rest.Lister interface.

// NewList is a required method for the rest.Lister interface.
//
// It returns an empty list object of a specific API type for further manipulation (LIST ops).
func (s *MultiClusterObjectQueryStorage) NewList() runtime.Object {
	return &objectbrowsingv1beta1.MultiClusterAPIObjectQueryList{}
}

// List is a required method for the rest.Lister interface.
//
// It handles the listing of API objects.
func (s *MultiClusterObjectQueryStorage) List(
	ctx context.Context,
	options *internalversion.ListOptions,
) (runtime.Object, error) {
	txn := s.db.Txn(false)
	defer txn.Abort()

	idIdxNameWithPrefix := inmemdb.IDIndexNameWithPrefix
	iter, err := txn.Get(inmemdb.MultiClusterObjectQueryTableName, idIdxNameWithPrefix, "")
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}

	var queries []objectbrowsingv1beta1.MultiClusterAPIObjectQuery
	for {
		queryObj := iter.Next()
		if queryObj == nil {
			break
		}
		q, ok := queryObj.(*objectbrowsingv1beta1.MultiClusterAPIObjectQuery)
		if !ok {
			return nil, apierrors.NewInternalError(fmt.Errorf("object is not a MultiClusterAPIObjectQuery"))
		}
		queries = append(queries, *q)
	}

	return &objectbrowsingv1beta1.MultiClusterAPIObjectQueryList{
		Items: queries,
	}, nil
}

// ConvertToTable is a required method for the rest.Lister interface.
//
// It converts the list result into a tabular format for kubectl output. This extension API server does not support
// tabular output, and this method always returns an error.
func (s *MultiClusterObjectQueryStorage) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return nil, apierrors.NewBadRequest("tabular output is not supported for MultiClusterAPIObjectQuery")
}

// Implement the rest.GracefulDeleter interface.

// Delete is a required method for the rest.GracefulDeleter interface.
//
// It handles the deletion of an existing API object by name.
func (s *MultiClusterObjectQueryStorage) Delete(
	ctx context.Context,
	name string,
	deleteValidation rest.ValidateObjectFunc,
	options *metav1.DeleteOptions,
) (runtime.Object, bool, error) {
	txn := s.db.Txn(true)
	defer txn.Abort()

	iter, err := txn.Get(inmemdb.MultiClusterObjectQueryTableName, inmemdb.IDIndexName, name)
	if err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	firstFound := iter.Next()
	if firstFound == nil {
		return nil, false, apierrors.NewNotFound(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "multiclusterapiobjectqueries",
		}, name)
	}
	q, ok := firstFound.(*objectbrowsingv1beta1.MultiClusterAPIObjectQuery)
	if !ok {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("object with name %s is not a MultiClusterAPIObjectQuery", name))
	}

	if err := txn.Delete(inmemdb.MultiClusterObjectQueryTableName, q); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	txn.Commit()
	return q, true, nil
}

// Implement the rest.Getter interface.

// Get is a required method for the rest.Getter interface.
//
// It handles the retrieval of an existing API object by name.
func (c *MultiClusterObjectQueryStorage) Get(
	ctx context.Context,
	name string,
	options *metav1.GetOptions,
) (runtime.Object, error) {
	txn := c.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(inmemdb.MultiClusterObjectQueryTableName, inmemdb.IDIndexName, name)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	firstFound := iter.Next()
	if firstFound == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "multiclusterapiobjectqueries",
		}, name)
	}

	q, ok := firstFound.(*objectbrowsingv1beta1.MultiClusterAPIObjectQuery)
	if !ok {
		return nil, apierrors.NewInternalError(fmt.Errorf("object in DB is not a MultiClusterAPIObjectQuery"))
	}
	return q, nil
}
