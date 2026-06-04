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
	memdb "github.com/hashicorp/go-memdb"

	"github.com/kubefleet-dev/kubefleet/pkg/utils/errors"
)

const (
	ObjectCacheTableName              = "objectcache"
	MultiClusterObjectQueryTableName  = "multiclusterobjectqueries"
	PerClusterObjectRawQueryTableName = "perclusterobjectrawqueries"

	IDIndexName                                               = "id"
	IDIndexNameWithPrefix                                     = "id_prefix"
	GroupKindNSNameAndObjectNameIndexName                     = "groupKindNSNameAndObjectName"
	GroupKindNSNameAndObjectNameIndexNameWithPrefix           = "groupKindNSNameAndObjectName_prefix"
	PerClusterGroupKindNSNameAndObjectNameIndexName           = "perClusterGroupKindNSNameAndObjectName"
	PerClusterGroupKindNSNameAndObjectNameIndexNameWithPrefix = "perClusterGroupKindNSNameAndObjectName_prefix"
)

var (
	objectBrowsingDBSchema = &memdb.DBSchema{
		Tables: map[string]*memdb.TableSchema{
			ObjectCacheTableName: {
				Name: ObjectCacheTableName,
				Indexes: map[string]*memdb.IndexSchema{
					IDIndexName: {
						Name:    IDIndexName,
						Unique:  true,
						Indexer: &ObjectMetadataNSNameAndNameIndexer{},
					},
					PerClusterGroupKindNSNameAndObjectNameIndexName: {
						Name:    PerClusterGroupKindNSNameAndObjectNameIndexName,
						Unique:  true,
						Indexer: &PerClusterGroupKindNSNameAndObjectNameIndexerForObjectWrapper{},
					},
					GroupKindNSNameAndObjectNameIndexName: {
						Name:    GroupKindNSNameAndObjectNameIndexName,
						Unique:  false,
						Indexer: &GroupKindNSNameAndObjectNameIndexerForObjectWrapper{},
					},
				},
			},
			MultiClusterObjectQueryTableName: {
				Name: MultiClusterObjectQueryTableName,
				Indexes: map[string]*memdb.IndexSchema{
					IDIndexName: {
						Name:    IDIndexName,
						Unique:  true,
						Indexer: &ObjectMetadataNSNameAndNameIndexer{},
					},
				},
			},
			PerClusterObjectRawQueryTableName: {
				Name: PerClusterObjectRawQueryTableName,
				Indexes: map[string]*memdb.IndexSchema{
					IDIndexName: {
						Name:    IDIndexName,
						Unique:  true,
						Indexer: &ObjectMetadataNSNameAndNameIndexer{},
					},
				},
			},
		},
	}
)

func NewDB() (*memdb.MemDB, error) {
	db, err := memdb.NewMemDB(objectBrowsingDBSchema)
	if err != nil {
		return nil, errors.NewUnexpectedError(err, "failed to create in-memory DB for multi-cluster object browsing object cache")
	}
	return db, nil
}
