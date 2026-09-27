# Scope Host Manager Cache

## Goal

Ensure the multi-tenancy demo's host manager watches namespaced resources only in the namespace identified by `VCLUSTER_NAME`, regardless of resource type.

## Implementation Plan

- [x] Replace the Pod-specific `cache.Options.ByObject` configuration with `cache.Options.DefaultNamespaces`.
- [x] Remove imports and comments that are specific to Pod-only cache scoping.
- [x] Add a focused unit test for the host manager cache options.
- [x] Run the targeted unit test and formatting checks.

## Success Criteria

- Every namespaced resource cached by the host manager is restricted to the configured vcluster namespace.
- The host manager does not retain a resource-type-specific namespace override.
- Cluster-scoped resources remain available to the host manager.
- The focused unit test passes.

## Result

The host manager now uses `DefaultNamespaces` to scope every namespaced resource cache to the
vcluster namespace. A focused unit test confirms the namespace configuration and guards against
reintroducing a resource-specific `ByObject` override.
