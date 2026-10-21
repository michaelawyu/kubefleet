# Work Generator Controller (v1) Integration Test Plan

**Date**: October 21, 2026 16:56 AEDT (revised 17:05 and 19:25 AEDT; the latter for the `areWorksInSync` change)
**Task**: Plan the additional integration tests (ITs) for `pkg/v1/controllers/workgenerator`, on top of the existing
envtest suite. Unit tests are out of scope for now. Status: **B1–B4 implemented in `lifecycle_integration_test.go`; `ginkgo -p -race` passes (452 specs)**.

Files in the package (after the renames):

| File | Role |
|---|---|
| [suite_test.go](../../../pkg/v1/controllers/workgenerator/suite_test.go) | envtest suite: manager, field indexes, reconciler (4 workers), namespaces `app`, `fleet-member-member-1/2` |
| [core_ops_integration_test.go](../../../pkg/v1/controllers/workgenerator/core_ops_integration_test.go) | existing ITs (core operations) |
| [utils_integration_test.go](../../../pkg/v1/controllers/workgenerator/utils_integration_test.go) | shared IT helpers, constants, fixtures |
| `lifecycle_integration_test.go` (**new**) | all ITs in this plan (B1–B4) |

## Requirements

1. Do not duplicate what [core_ops_integration_test.go](../../../pkg/v1/controllers/workgenerator/core_ops_integration_test.go)
   already proves; cover the lifecycle paths it leaves out.
2. Follow the conventions of the sibling `pkg/v1` ITs (`stagedupdaterun`, `placementresourcesnapshot`): Ginkgo/Gomega,
   `Ordered` `Context` per scenario, whole-struct `cmp.Diff` comparisons through the existing `*Actual` helpers, `want*`
   naming, random per-test object names (`utils.RandStr()`) so Contexts are independent, `AfterAll` teardown.
3. Cover both `PlacementBinding` (namespace-scoped, `appNamespaceName`) and `ClusterPlacementBinding`
   (`clusterScopedNS`).
4. All new tests go in `lifecycle_integration_test.go`; new shared helpers go in `utils_integration_test.go`.

## Existing coverage (core_ops_integration_test.go)

Four `Describe` blocks: {namespaced, cluster-scoped} × {single snapshot, multiple snapshots}, each with the same
`Context`s:

- generate works (finalizer, works, status waiting → synced/available; multi: status gated on all works);
- update works (new snapshot; multi: shrink, grow, in-place update, delete stale);
- refresh status upon work status changes (unavailable);
- update works with no spec changes (relink, 1s requeue, status refreshed without work re-processing);
- backport failed manifests (apply/availability failures with diff details; multi: from primary + secondary);
- handle inconsistent snapshots (multi only: missing secondary, hash mismatch, recovery via backoff retry).

Deletion is only exercised as `AfterAll` teardown (binding removed → primary work removed → binding gone); nothing
asserts it as a behavior of its own, and suspension and sync strategy changes are never exercised.

## Controller behavior relevant to the new tests

(See `controller.go`, `cleanup.go`, `works.go`.)

1. Binding deleting **or** `spec.suspended` → `cleanupWorks`: no-op without the finalizer; otherwise delete the primary
   work only (secondary works are owned by the primary and rely on K8s GC); if suspended and not deleting, clear
   `status.lastProcessedResourceSnapshotName`; remove the finalizer. Nothing else runs.
2. Otherwise add the finalizer, list works, and call `areWorksInSync` (renamed from `areWorksUpToDate`; returns
   `InSync` / `OutOfSync` / `Erred`). `InSync` only if `lastProcessed == spec.resourceSnapshotName`, there is at least
   one work, all works are linked to that primary, the primary's linked-work-count equals the number of listed works,
   and every work's `syncStrategy` equals the binding's. **Missing works (none, or fewer than the count), a missing
   primary (no work carries the count), and works linked to another snapshot are now `OutOfSync`** (log only, no error),
   so the refresh path regenerates them. Only a malformed or duplicated linked-work-count annotation is `Erred`
   (error returned, no repair).
3. `OutOfSync` → retrieve snapshots, create/update/delete works, report progress
   (`WaitingForSynchronization` / `WaitingForAvailabilityCheck`, counters cleared); requeue after 1s only if no work
   spec changed.
4. `SetupWithManager`: bindings watched with `GenerationChangedPredicate`; works watched with create (always),
   update (**status changes only**) and delete events, mapped to the owner binding via the
   `owner-namespace` label and the `owned-by-placement-binding` annotation (missing either → logged and dropped).

Envtest has **no garbage collector**, so owner-reference cascades never run: tests may assert owner references but not
that secondary works vanish after the primary is deleted.

## Scope handling

The existing file duplicates every `Context` per scope. For the new file, build the `Describe`s from a small table
(`scope{name, bindingNS, bindingNameTemplate, policyNameTemplate}`) in a `for` loop, so each scenario is written once.
The Context bodies still read like the existing ones (same helpers, same step granularity). *(Deviation from the
existing file; see Open questions.)*

## Test plan

Notation: "single" = one primary snapshot; "multi" = primary + 1 secondary (sub-index 1) snapshot; manifest fixtures
reuse `nsSnapshottedResource`/`deploySnapshottedResource`/`configMapSnapshottedResource` and the matching
`*ManifestIdentifier` helpers.

### B1. Binding deletion — `Describe("deleting placement bindings")`

`Context("delete a binding with works")`, single and multi (nested `Context` or table per variant):

1. Create snapshot(s) and a binding on `member-1`; wait for finalizer, works, status waiting; mark works applied and
   available; wait for synced/available.
2. Single: record the primary work UID. Multi: also verify (before the deletion) that the secondary's controller owner
   reference points to the primary work's UID.
3. Delete the binding → **eventually** the primary work is gone and the binding is gone (finalizer removed).
4. Multi only: the secondary work is left to GC (not asserted as removed); the test deletes it itself in `AfterAll`.
5. Isolation (single only): a second binding for another policy on `member-2` created in `BeforeAll` keeps its work and
   its Synced/Available status untouched (`Consistently`).

The earlier "binding whose works are already gone" Context is **dropped**: since missing works are now regenerated,
deleting the primary work and then the binding races with the re-creation. (The NotFound tolerance in `cleanupWorks`
is a better fit for a unit test.)

### B2. Suspension — `Describe("suspending placement bindings")`

Single snapshot only (multi adds nothing but GC noise in envtest); both scopes.

`Context("suspend and resume")`, `Ordered`:

1. Generate works, mark applied/available, wait for synced/available (the baseline, same as existing Contexts).
2. Patch `spec.suspended=true` → eventually: primary work gone; `status.lastProcessedResourceSnapshotName` is nil;
   the cleanup finalizer is removed (new `placementBindingFinalizerRemovedActual`).
3. While suspended: `Consistently` no work for the binding. Create snapshot 1 and point the binding to it (a rollout
   during suspension) → still no works; finalizer still absent.
4. Patch `spec.suspended=false` → finalizer re-added; works generated from snapshot **1**; status
   `wantPlacementBindingStatusWaitingForSync(...)` with `LastProcessedResourceSnapshotName` = snapshot 1 (and the
   counters of the new content).
5. Mark the new work applied/available → status synced/available.
6. `AfterAll`: delete binding, snapshots, works as the existing Contexts do.

`Context("suspend a binding that has not been fully processed")` (optional, cheap): create binding, immediately
suspend before marking works → works removed; resume → works regenerated; status ends at waiting for sync. Covers
suspend racing with the first reconcile; drop if flaky.

`Context("delete a suspended binding")`: suspend, wait for finalizer removal, delete the binding → binding gone
immediately and no work is created in the meantime (`Consistently`).

### B3. Sync strategy changes — `Describe("changing sync strategies of placement bindings")`

Single and multi, both scopes. `Context("update the sync strategy")`, `Ordered`:

1. Generate works with no sync strategy; mark applied/available; status synced/available.
2. Record each work's UID and, for the primary, the linked-work-count annotation.
3. Patch the binding's `syncStrategy` to `ServerSideApply` with `serverSideApplyOptions.forceConflicts=true` (new
   `updatePlacementBindingSyncStrategy` helper). Eventually every work carries the strategy (compared against the
   binding's `spec.syncStrategy` **as read back from the API server**, so CRD defaults on nested fields are
   accounted for); the work set, names, UIDs (updated **in place**), owner references and link annotations are unchanged.
4. The binding status goes back to `wantPlacementBindingStatusWaitingForSync` (spec changed → progress reported, no
   1s requeue path); then mark works applied/available → synced/available again.
5. Patch the strategy back to nil → works revert to a nil strategy in place; status waiting → synced after marking.

Compared with the existing "no spec changes" Contexts, this proves that the sync strategy comparison in
`areWorksInSync` reports `OutOfSync` and triggers the refresh path even when the snapshot name is unchanged.

### B4. Work object events — `Describe("handling work object events")`

Run the `Context`s per scope (the event mapping is identical, but the owner-namespace label is empty for
cluster-scoped bindings).

`Context("work objects that the work generator does not own")`, `Ordered`, single:

1. Baseline binding generated and synced/available.
2. Create work objects in `fleet-member-member-1` that carry (a) no KubeFleet owner metadata, (b) the owner
   label/annotations of a **non-existent** binding. Mark each applied/available (status update event), then delete them.
3. Throughout: `Consistently` the real binding's status stays synced/available and its work is unchanged. After
   the sequence, roll out a new snapshot → works update and status behave as usual (the controller is still healthy).

Covers the create/update/delete handler branches with missing metadata and the not-found binding path.

`Context`s for work objects deleted out of band (the controller now regenerates them without a rollout; the Work delete
event enqueues the owner binding, `areWorksInSync` returns `OutOfSync`, and the refresh path re-creates them). All
`Ordered`, each starting from a baseline that is synced/available. Since a re-created work has an empty status, the
binding status first goes back to `wantPlacementBindingStatusWaitingForSync` (a re-created work counts as a spec
change, so there is no 1s requeue), then to synced/available once the **re-created** work is marked
applied/available (surviving works keep their status).

- `Context("secondary work deleted")`, multi: delete the secondary → it is re-created with the expected content and
  owner reference to the (unchanged, same-UID) primary; status waiting → synced.
- `Context("primary work deleted")`, multi: delete the primary → a new primary is created (new UID) with the linked-work
  count; the existing secondary is updated in place with its owner reference re-pointed to the new primary's UID;
  status waiting → synced after marking the new primary.
- `Context("all works deleted")`, single: delete the only work → re-created; status waiting → synced. Exercises the
  `len(works) == 0` branch (formerly a transient error).
- `Context("a work object is out of sync with the binding's snapshot link")` is **not** planned: it needs a metadata-only
  change that does not trigger a reconcile (the update handler only reacts to status changes), so it cannot be
  triggered deterministically from outside.

### Helpers to add to utils_integration_test.go

| Helper | Purpose |
|---|---|
| `updatePlacementBindingSuspended(bindingNS, bindingName string, suspended bool)` | `Eventually` + get/update, like `updatePlacementBindingResourceSnapshot` |
| `updatePlacementBindingSyncStrategy(bindingNS, bindingName string, strategy *placementv1alpha1.SyncStrategy)` | same |
| `placementBindingFinalizerRemovedActual(bindingNS, bindingName)` | inverse of `placementBindingFinalizerAddedActual` |
| `placementBindingLastProcessedSnapshotUnsetActual(bindingNS, bindingName)` | status field is nil |
| `deleteWork(workNS, workName)` | delete + `Expect` success/NotFound |
| `wantWork…` re-created variant (or inline) | after a re-creation the work has no status; use `markWorkAsAppliedAndAvailable` on only the re-created work |
| `createForeignWork(workNS, workName string, labels, annotations map[string]string)` | work with arbitrary owner metadata and a minimal spec |
| `noWorksActual(clusterName, bindingNS, bindingName)` | wrapper over the list in `worksGeneratedActual` asserting an empty set (for `Consistently`) |

The `scope` struct and `scopes` table live in `lifecycle_integration_test.go`.

`worksGeneratedActual`, `wantWorkForPlacementResourceSnapshot`, `wantPlacementBindingStatus*`,
`markWorkAsAppliedAndAvailable`, `removePlacementBinding` and the `*RemovedActual` helpers are reused as is. To expect a
sync strategy on works, the test sets `Spec.SyncStrategy` on the `want` works after building them (no signature change).

### Explicitly not planned

- Missing reserved namespace for the target cluster (B5; unlikely in practice, dropped).
- Re-targeting a binding to another cluster, tampering with work specs (documented as unsupported).
- Concurrency/stress of the worker pool, and any unit tests (deferred).

## Behaviors to note (documented only; no action)

- Missing works are now regenerated by the controller (`areWorksInSync` → `OutOfSync`), so the earlier finding
  (indefinite backoff until the next rollout) no longer applies. Content tampering and non-status metadata changes
  are still not repaired until the next rollout or an unrelated trigger, as documented in `controller.go`.
- A genuinely stale cache (works not yet visible) now makes the controller take the refresh path rather than retry
  with an error; `CreateOrUpdate` against the live API server makes this safe (an existing work is updated rather than
  duplicated), but the progress status is reset in the meantime. Not tested (cannot be forced deterministically).
- `Erred` (malformed or duplicated linked-work-count annotation) is still an error without repair; not covered by
  ITs (cannot be induced without tampering and a status-change trigger).
- In envtest the secondary works outlive a suspended/deleted binding (no GC); the tests clean them up explicitly.

## Sequencing

1. Helpers + `scope` table.
2. B1 (deletion), B2 (suspension) — highest-value, simplest.
3. B3 (sync strategy).
4. B4 (work events).

## Running the tests

```bash
# Only the new ITs (needs envtest assets; `make local-unit-test` sets KUBEBUILDER_ASSETS for the whole repo)
export KUBEBUILDER_ASSETS="$($(go env GOPATH)/bin/setup-envtest use -p path)"
go test -race -timeout=30m ./pkg/v1/controllers/workgenerator/... \
  -ginkgo.focus="deleting placement bindings|suspending placement bindings|changing sync strategies|handling work object events"

# The whole package
go test -race -timeout=30m ./pkg/v1/controllers/workgenerator/...
```

Before any PR: `make reviewable`.

## Estimated runtime

Per scope, serial: B1 ~20s, B2 ~35s, B3 ~40s, B4 ~40s (three deletion Contexts plus the `Consistently` windows) ⇒ roughly +2
minutes per scope on top of the existing suite (which already includes 1-minute backoff waits).

## Open questions

1. Scope table loop (default) vs. duplicated Contexts per scope like the existing file?
2. B2 "suspend racing with the first reconcile": keep (may be flaky) or drop?
3. (Resolved by the controller change) Missing works are now regenerated; B4 tests that.

## Status

- [x] Plan approved
- [x] Helpers added
- [x] B1 implemented
- [x] B2 implemented
- [x] B3 implemented
- [x] B4 implemented
- [ ] `make reviewable` clean
