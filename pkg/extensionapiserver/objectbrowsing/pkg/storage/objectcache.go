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
	"k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"

	objectbrowsingv1beta1 "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1"
	"github.com/kubefleet-dev/kubefleet/pkg/extensionapiserver/objectbrowsing/pkg/storage/inmemdb"
)

type MultiClusterObjectCacheStorage struct {
	db *memdb.MemDB
}

// Verify that MultiClusterObjectCacheStorage implements the required interfaces.
var _ rest.Storage = &MultiClusterObjectCacheStorage{}
var _ rest.SingularNameProvider = &MultiClusterObjectCacheStorage{}
var _ rest.Scoper = &MultiClusterObjectCacheStorage{}
var _ rest.KindProvider = &MultiClusterObjectCacheStorage{}
var _ rest.GroupVersionKindProvider = &MultiClusterObjectCacheStorage{}

var _ rest.CreaterUpdater = &MultiClusterObjectCacheStorage{}
var _ rest.Getter = &MultiClusterObjectCacheStorage{}
var _ rest.Lister = &MultiClusterObjectCacheStorage{}
var _ rest.GracefulDeleter = &MultiClusterObjectCacheStorage{}

func NewObjectCacheStorage(mainDB *memdb.MemDB) (*MultiClusterObjectCacheStorage, error) {
	return &MultiClusterObjectCacheStorage{db: mainDB}, nil
}

// Implement the rest.Storage interface.

// New is a required method for the rest.Storage interface.
//
// It returns an empty object of a specific API type for further manipulation (CREATE/UPDATE ops).
func (c *MultiClusterObjectCacheStorage) New() runtime.Object {
	return &objectbrowsingv1beta1.ClusterAPIObjectWrapper{}
}

// Destroy is a required method for the rest.Storage interface.
//
// It performs cleanup ops as needed on API server shutdown. In the case of KubeFleet multi-cluster object browsing
// extension API server, since we are using in-memory storage and all data are cache objects by nature,
// this method is a no-op.
func (c *MultiClusterObjectCacheStorage) Destroy() {}

// Implement the rest.KindProvider interface.

// Kind is a required method for the rest.KindProvider interface.
//
// It returns the Kind name of the API object that this storage manages.
func (c *MultiClusterObjectCacheStorage) Kind() string {
	return "ClusterAPIObjectWrapper"
}

// Implement the rest.Scoper interface.

// NamespaceScoped is a required method for the rest.Scoper interface.
//
// It reports whether the API object under the storage's management is namespace-scoped
// or cluster-scoped. All wrapped API objects for multi-cluster object browsing are namespace-scoped.
func (c *MultiClusterObjectCacheStorage) NamespaceScoped() bool {
	return true
}

// Implement the rest.SingularNameProvider interface.

// SingularName is a required method for the rest.SingularNameProvider interface.
//
// It returns the singular name of the API object that this storage manages.
func (c *MultiClusterObjectCacheStorage) GetSingularName() string {
	return "clusterapiobjectwrapper"
}

// Implement the rest.GroupVersionKindProvider interface.

// GroupVersionKind is a required method for the rest.GroupVersionKindProvider interface.
//
// It returns the GroupVersionKind of the API object that this storage manages.
func (c *MultiClusterObjectCacheStorage) GroupVersionKind(containingGV schema.GroupVersion) schema.GroupVersionKind {
	return objectbrowsingv1beta1.GroupVersion.WithKind(c.Kind())
}

// Implement the rest.CreaterUpdater interface.

// Create is a required method for the rest.CreaterUpdater interface.
//
// It handles the creation of a new API object.
func (c *MultiClusterObjectCacheStorage) Create(
	ctx context.Context,
	obj runtime.Object,
	createValidation rest.ValidateObjectFunc,
	options *metav1.CreateOptions,
) (runtime.Object, error) {
	txn := c.db.Txn(true)
	defer txn.Abort()

	wrapper, ok := obj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
	if !ok {
		return nil, apierrors.NewBadRequest("object is not a ClusterAPIObjectWrapper")
	}

	isReady, id, err := inmemdb.DefaultIDIndexer.FromObject(wrapper)
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to get index key for the given object: %w", err))
	}
	if !isReady {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to get index key for the given object"))
	}
	existingObj, err := txn.First(inmemdb.ObjectCacheTableName, inmemdb.IDIndexName, string(id))
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to check for existing object: %w", err))
	}
	if existingObj != nil {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "clusterapiobjectwrappers",
		}, wrapper.Name)
	}

	if err := txn.Insert(inmemdb.ObjectCacheTableName, wrapper); err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to insert object into cache: %w", err))
	}

	txn.Commit()
	return wrapper, nil
}

// Update is a required method for the rest.CreaterUpdater interface.
//
// It handles the update of an existing API object.
func (c *MultiClusterObjectCacheStorage) Update(
	ctx context.Context,
	name string,
	objectInfo rest.UpdatedObjectInfo,
	createValidation rest.ValidateObjectFunc,
	updateValidation rest.ValidateObjectUpdateFunc,
	forceAllowCreate bool,
	options *metav1.UpdateOptions,
) (runtime.Object, bool, error) {
	txn := c.db.Txn(true)
	defer txn.Abort()

	nsName, found := request.NamespaceFrom(ctx)
	if !found {
		return nil, false, apierrors.NewBadRequest("namespace is required for getting a ClusterAPIObjectWrapper")
	}

	prefix := fmt.Sprintf("%s/%s", nsName, name)
	existing, err := txn.First(inmemdb.ObjectCacheTableName, inmemdb.IDIndexName, prefix)
	if err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	if existing == nil {
		return nil, false, apierrors.NewNotFound(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "clusterapiobjectwrappers",
		}, name)
	}
	existingWrapper, ok := existing.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
	if !ok {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("object with name %s is not a ClusterAPIObjectWrapper", name))
	}

	wrapper, err := objectInfo.UpdatedObject(ctx, existingWrapper)
	if err != nil {
		return nil, false, apierrors.NewBadRequest(fmt.Sprintf("failed to get the updated object: %v", err))
	}
	if err := txn.Insert(inmemdb.ObjectCacheTableName, wrapper); err != nil {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("failed to update object in cache: %w", err))
	}

	txn.Commit()

	return wrapper, true, nil
}

// Implement the rest.Getter interface.

// Get is a required method for the rest.Getter interface.
//
// It handles the retrieval of an existing API object by name.
func (c *MultiClusterObjectCacheStorage) Get(
	ctx context.Context,
	name string,
	options *metav1.GetOptions,
) (runtime.Object, error) {
	txn := c.db.Txn(false)
	defer txn.Abort()

	nsName, found := request.NamespaceFrom(ctx)
	if !found {
		return nil, apierrors.NewBadRequest("namespace is required for getting a ClusterAPIObjectWrapper")
	}

	prefix := fmt.Sprintf("%s/%s", nsName, name)
	obj, err := txn.First(inmemdb.ObjectCacheTableName, inmemdb.IDIndexName, prefix)
	if err != nil {
		return nil, apierrors.NewInternalError(fmt.Errorf("failed to query the in-memory DB: %w", err))
	}
	if obj == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "clusterapiobjectwrappers",
		}, name)
	}

	wrapper, ok := obj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
	if !ok {
		return nil, apierrors.NewInternalError(fmt.Errorf("object with name %s is not a ClusterAPIObjectWrapper", name))
	}

	return wrapper, nil
}

// Implement the rest.Lister interface.

// NewList is a required method for the rest.Lister interface.
//
// It returns an empty list object of a specific API type for further manipulation (LIST ops).
func (c *MultiClusterObjectCacheStorage) NewList() runtime.Object {
	return &objectbrowsingv1beta1.ClusterAPIObjectWrapperList{}
}

// List is a required method for the rest.Lister interface.
//
// It handles the listing of API objects.
func (c *MultiClusterObjectCacheStorage) List(
	ctx context.Context,
	options *internalversion.ListOptions,
) (runtime.Object, error) {
	txn := c.db.Txn(false)
	defer txn.Abort()

	iter, err := txn.Get(inmemdb.ObjectCacheTableName, inmemdb.IDIndexNameWithPrefix, "")
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}

	var wrappers []objectbrowsingv1beta1.ClusterAPIObjectWrapper
	for {
		wrapperObj := iter.Next()
		if wrapperObj == nil {
			break
		}
		wrapper, ok := wrapperObj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
		if !ok {
			return nil, apierrors.NewInternalError(fmt.Errorf("object is not a ClusterAPIObjectWrapper"))
		}
		wrappers = append(wrappers, *wrapper)
	}

	return &objectbrowsingv1beta1.ClusterAPIObjectWrapperList{
		Items: wrappers,
	}, nil
}

// ConvertToTable is a required method for the rest.Lister interface.
//
// It converts the list result into a tabular format for kubectl output. This extension API server does not support
// tabular output, and this method always returns an error.
func (c *MultiClusterObjectCacheStorage) ConvertToTable(ctx context.Context, object runtime.Object, tableOptions runtime.Object) (*metav1.Table, error) {
	return nil, apierrors.NewBadRequest("tabular output is not supported for ClusterAPIObjectWrapper")
}

// Implement the rest.GracefulDeleter interface.

// Delete is a required method for the rest.GracefulDeleter interface.
//
// It handles the deletion of an existing API object by name.
func (c *MultiClusterObjectCacheStorage) Delete(
	ctx context.Context,
	name string,
	deleteValidation rest.ValidateObjectFunc,
	options *metav1.DeleteOptions,
) (runtime.Object, bool, error) {
	txn := c.db.Txn(true)
	defer txn.Abort()

	nsName, found := request.NamespaceFrom(ctx)
	if !found {
		return nil, false, apierrors.NewBadRequest("namespace is required for deleting a ClusterAPIObjectWrapper")
	}
	prefix := fmt.Sprintf("%s/%s", nsName, name)
	obj, err := txn.First(inmemdb.ObjectCacheTableName, inmemdb.IDIndexName, prefix)
	if err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	if obj == nil {
		return nil, false, apierrors.NewNotFound(schema.GroupResource{
			Group:    objectbrowsingv1beta1.GroupVersion.Group,
			Resource: "clusterapiobjectwrappers",
		}, name)
	}

	wrapper, ok := obj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
	if !ok {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("object with name %s is not a ClusterAPIObjectWrapper", name))
	}

	if err := txn.Delete(inmemdb.ObjectCacheTableName, wrapper); err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}

	txn.Commit()
	return wrapper, true, nil
}
