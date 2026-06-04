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

package inmemdb

import (
	"fmt"

	memdb "github.com/hashicorp/go-memdb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	objectbrowsingv1beta1 "github.com/kubefleet-dev/kubefleet/apis/objectbrowsing/v1beta1"
	errors "github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

var (
	DefaultIDIndexer = &ObjectMetadataNSNameAndNameIndexer{}
)

type ObjectMetadataNSNameAndNameIndexer struct{}

var _ memdb.SingleIndexer = &ObjectMetadataNSNameAndNameIndexer{}
var _ memdb.PrefixIndexer = &ObjectMetadataNSNameAndNameIndexer{}

func (idxer *ObjectMetadataNSNameAndNameIndexer) FromObject(obj interface{}) (bool, []byte, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return false, nil, errors.NewUnexpectedError(nil, "object is not a MultiClusterAPIObjectQuery")
	}

	if metaObj.GetNamespace() == "" {
		// The object is cluster-scoped. Use the name as the key, with a null character as a terminator.
		key := metaObj.GetName() + "\000"
		return true, []byte(key), nil
	} else {
		// The object is namespace-scoped. Use "namespace/name" as the key, with a null character as a terminator.
		key := metaObj.GetNamespace() + "/" + metaObj.GetName() + "\000"
		return true, []byte(key), nil
	}
}

func (idxer *ObjectMetadataNSNameAndNameIndexer) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, errors.NewUnexpectedError(nil, "exactly one argument is needed for an indexed query based on namespace and name")
	}

	arg, ok := args[0].(string)
	if !ok {
		return nil, errors.NewUnexpectedError(nil, "argument for an indexed query based on namespace and name should be a string")
	}

	// Add a null character as a terminator.
	key := arg + "\000"
	return []byte(key), nil
}

func (idxer *ObjectMetadataNSNameAndNameIndexer) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	val, err := idxer.FromArgs(args...)
	if err != nil {
		return nil, errors.Wraps(err, "failed to read prefix from given arguments")
	}

	// Strip the null character.
	n := len(val)
	if n > 0 {
		return val[:n-1], nil
	}
	return val, nil
}

type GroupKindNSNameAndObjectNameIndexerForObjectWrapper struct{}

var _ memdb.SingleIndexer = &GroupKindNSNameAndObjectNameIndexerForObjectWrapper{}
var _ memdb.PrefixIndexer = &GroupKindNSNameAndObjectNameIndexerForObjectWrapper{}

func (idxer *GroupKindNSNameAndObjectNameIndexerForObjectWrapper) FromObject(obj interface{}) (bool, []byte, error) {
	wrapper, ok := obj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
	if !ok {
		return false, nil, errors.NewUnexpectedError(nil, "object is not a ClusterAPIObjectWrapper")
	}

	apiGroup := wrapper.Identifier.Group
	kind := wrapper.Identifier.Kind
	namespace := wrapper.Identifier.Namespace
	name := wrapper.Identifier.Name

	key := fmt.Sprintf("%s/%s/%s/%s", apiGroup, kind, namespace, name)

	// Add a null character as a terminator.
	key = key + "\000"
	return true, []byte(key), nil
}

func (idxer *GroupKindNSNameAndObjectNameIndexerForObjectWrapper) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, errors.NewUnexpectedError(nil, "exactly one argument is needed for an indexed query based on group, kind, namespace, and name")
	}

	arg, ok := args[0].(string)
	if !ok {
		return nil, errors.NewUnexpectedError(nil, "argument for an indexed query based on group, kind, namespace, and name should be a string")
	}

	// Add a null character as a terminator.
	key := arg + "\000"
	return []byte(key), nil
}

func (idxer *GroupKindNSNameAndObjectNameIndexerForObjectWrapper) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	val, err := idxer.FromArgs(args...)
	if err != nil {
		return nil, errors.Wraps(err, "failed to read prefix from given arguments")
	}

	// Strip the null character.
	n := len(val)
	if n > 0 {
		return val[:n-1], nil
	}
	return val, nil
}

type PerClusterGroupKindNSNameAndObjectNameIndexerForObjectWrapper struct{}

var _ memdb.SingleIndexer = &PerClusterGroupKindNSNameAndObjectNameIndexerForObjectWrapper{}
var _ memdb.PrefixIndexer = &PerClusterGroupKindNSNameAndObjectNameIndexerForObjectWrapper{}

func (idxer *PerClusterGroupKindNSNameAndObjectNameIndexerForObjectWrapper) FromObject(obj interface{}) (bool, []byte, error) {
	wrapper, ok := obj.(*objectbrowsingv1beta1.ClusterAPIObjectWrapper)
	if !ok {
		return false, nil, errors.NewUnexpectedError(nil, "object is not a ClusterAPIObjectWrapper")
	}

	cluster := wrapper.Identifier.OriginCluster
	apiGroup := wrapper.Identifier.Group
	kind := wrapper.Identifier.Kind
	namespace := wrapper.Identifier.Namespace
	name := wrapper.Identifier.Name

	key := fmt.Sprintf("%s/%s/%s/%s/%s", cluster, apiGroup, kind, namespace, name)

	// Add a null character as a terminator.
	key = key + "\000"
	return true, []byte(key), nil
}

func (idxer *PerClusterGroupKindNSNameAndObjectNameIndexerForObjectWrapper) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, errors.NewUnexpectedError(nil, "exactly one argument is needed for an indexed query based on cluster, group, kind, namespace, and name")
	}

	arg, ok := args[0].(string)
	if !ok {
		return nil, errors.NewUnexpectedError(nil, "argument for an indexed query based on cluster, group, kind, namespace, and name should be a string")
	}

	// Add a null character as a terminator.
	key := arg + "\000"
	return []byte(key), nil
}

func (idxer *PerClusterGroupKindNSNameAndObjectNameIndexerForObjectWrapper) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	val, err := idxer.FromArgs(args...)
	if err != nil {
		return nil, errors.Wraps(err, "failed to read prefix from given arguments")
	}

	// Strip the null character.
	n := len(val)
	if n > 0 {
		return val[:n-1], nil
	}
	return val, nil
}
