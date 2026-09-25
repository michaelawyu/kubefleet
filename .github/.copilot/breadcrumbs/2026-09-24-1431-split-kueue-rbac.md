# Split Kueue Roles

## Goal

Move every `Role` and `ClusterRole` document from `hack/multitenancydemo/kueue/all.yaml` into a sibling `rbac.yaml` file without changing document contents or the ordering of the remaining resources.

## Scope

- Include `Role` and `ClusterRole`.
- Keep `RoleBinding`, `ClusterRoleBinding`, and `ServiceAccount` in `all.yaml`.

## Implementation Plan

- [x] Extract all 3 `Role` and 47 `ClusterRole` documents into `hack/multitenancydemo/kueue/rbac.yaml` in their current order.
- [x] Remove those role documents from `hack/multitenancydemo/kueue/all.yaml`.
- [x] Preserve all other resources in `all.yaml` in their current order.
- [x] Validate that both files parse as YAML and that role definitions exist only in `rbac.yaml`.

## Success Criteria

- `rbac.yaml` contains exactly the 50 original role documents.
- `all.yaml` contains no `Role` or `ClusterRole` documents.
- Bindings and service accounts remain in `all.yaml`.
- The combined document count is unchanged.
- No YAML document content changes beyond moving the role definitions.

## Result

The 50 role definitions were moved into `rbac.yaml`. The 4 `RoleBinding` documents, 2 `ClusterRoleBinding` documents, and all service accounts remain in `all.yaml`. Document-level hashes confirmed that no resource content changed during the split.

## Follow-up: Append Bindings

- [x] Move all 4 `RoleBinding` and 2 `ClusterRoleBinding` documents from `all.yaml`.
- [x] Append the binding documents to the end of the current `rbac.yaml` in their existing relative order.
- [x] Preserve the `Namespace` and `ServiceAccount` documents currently present in `rbac.yaml`.
- [x] Validate that bindings exist only in `rbac.yaml` and that document contents are unchanged.

The binding documents were appended after all existing `rbac.yaml` documents. Their original relative order and contents were preserved.
