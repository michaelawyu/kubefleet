# Split Kueue CRDs

## Goal

Move every `CustomResourceDefinition` document from `hack/multitenancydemo/kueue/all.yaml` into a sibling `crds.yaml` file without changing document contents or the ordering of the remaining resources.

## Implementation Plan

- [x] Extract all 11 CRD documents into `hack/multitenancydemo/kueue/crds.yaml` in their current order.
- [x] Remove those CRD documents from `hack/multitenancydemo/kueue/all.yaml`.
- [x] Preserve the Namespace and all post-CRD resources in `all.yaml`.
- [x] Validate that both files parse as YAML and that CRDs exist only in `crds.yaml`.

## Success Criteria

- `crds.yaml` contains exactly the 11 original CRD documents.
- `all.yaml` contains no CRD documents.
- The combined document count is unchanged.
- No YAML document content changes beyond moving the CRDs.

## Result

The 11 CRDs were moved into `crds.yaml`. The updated `all.yaml` contains the original Namespace followed by the original non-CRD resources. An exact reconstruction comparison confirmed that no document content changed during the split.
