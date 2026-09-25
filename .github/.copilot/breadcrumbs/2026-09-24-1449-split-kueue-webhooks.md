# Split Kueue Webhook Configurations

## Goal

Move every webhook configuration document from `hack/multitenancydemo/kueue/host.yaml` into the existing empty sibling file `webhooks.yaml` without changing document contents or the ordering of the remaining resources.

## Implementation Plan

- [x] Extract the `MutatingWebhookConfiguration` and `ValidatingWebhookConfiguration` documents into `hack/multitenancydemo/kueue/webhooks.yaml` in their current order.
- [x] Remove those two documents from `hack/multitenancydemo/kueue/host.yaml`.
- [x] Preserve all other resources in `host.yaml` in their current order.
- [x] Validate that both files parse as YAML and webhook configurations exist only in `webhooks.yaml`.

## Success Criteria

- `webhooks.yaml` contains exactly the two original webhook configuration documents.
- `host.yaml` contains no webhook configuration documents.
- The eight non-webhook documents remain in `host.yaml`.
- No YAML document content changes beyond moving the webhook configurations.

## Result

The mutating and validating webhook configurations were moved into `webhooks.yaml` in their original order. The source manifest's leading YAML separator style and all document contents were preserved.
