# Cluster-Scoped Work Generator Integration Tests

## Goal

Add cluster-scoped placement binding coverage equivalent to the existing namespace-scoped work generator integration tests.

## Implementation Plan

- [ ] Add cluster-scoped hub-manifest scenarios using `ClusterPlacementBinding` and `ClusterPlacementResourceSnapshot`.
- [ ] Add cluster-scoped ORAS-manifest scenarios with the same extraction and status assertions.
- [ ] Verify Work metadata uses an empty owner namespace while preserving binding, policy, snapshot, and cleanup semantics.
- [ ] Format the updated test file and run the focused work generator integration suite.

## Success Criteria

- Cluster-scoped bindings create the expected Work objects for hub and ORAS manifests.
- Cluster-scoped bindings receive the cleanup finalizer and waiting status conditions.
- Work status updates propagate successful synchronized and available conditions to cluster-scoped bindings.
- Deleting each cluster-scoped binding cleans up its Work objects.
- Existing namespace-scoped scenarios continue to pass.

## Result

Pending implementation.
