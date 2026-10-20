# Unit Test Plan for the v1 Work Applier Requeue Rate Limiter

**Date**: October 21, 2026 01:21 AEDT
**Task**: Plan unit tests for `pkg/v1/controllers/workapplier/backoff.go`, using
`pkg/controllers/workapplier/backoff_test.go` (old work applier) as the reference.

## Requirements

1. Cover every exported/unexported function in `backoff.go`:
   `NewMultiStageRequeueWithExponentialBackoffRateLimiter`, `When`, `Forget`,
   `computeLinkedWorksObjectGenHash`, `computeProcessingResultHash`,
   `areAllProcessedManifestsAvailable`, `garbageCollect`, and the package-level
   `defaultRequeueRateLimiter` built in `init()`.
2. Cover every branch of the `When` stage switch, the counter-reset conditions, the counter cap,
   and the Forget-triggered GC.
3. Follow repo test conventions: table-driven, no assert libraries, `cmp.Diff`/`cmp.Equal`,
   `want` naming, `"Func(%v) = %v, want %v"` messages, whole-struct comparisons.

## What Changed vs. the Old Implementation (drives what can be ported)

| Area | Old (`pkg/controllers/workapplier`) | New (`pkg/v1/controllers/workapplier`) | Test impact |
|---|---|---|---|
| Type name | `RequeueMultiStageWithExponentialBackoffRateLimiter` | `MultiStageRequeueWithExponentialBackoffRateLimiter` | Rename. |
| Constructor | `float64` seconds; silently resets invalid values to defaults | `int` seconds; returns `(nil, UserError)` on invalid input; adds cross-field rules (slow base <= fast base, initial <= maxSlow <= maxFast <= 3600) | Rewrite the constructor test (error cases instead of "reset to default" cases). |
| `When` inputs | `(work, bundles)` | `(linkedWorks, manifestProcessingStates)` | Port sequences with new inputs. |
| Reset key | primary Work generation | hash of `name,generation` over **all** linked Works (order-sensitive) | New linked-work cases. |
| Skip-to-fast trigger | Work `Available`/`DiffReported` conditions | `areAllProcessedManifestsAvailable` (per-manifest `Available`/`NotTrackable`; vacuously true when empty) | Rewrite the skip test. |
| Counter | unbounded | capped at `maxNumOfRequeuesToTrack - 1` (199) | New test. |
| Forget | deletes entries | also counts forgets; rebuilds maps every `gcTriggeringForgetThreshold` (100000) forgets | New GC tests. |
| Processing result hash | from bundles (+ work) | from `applyRes,availabilityCheckRes` per state only | Port with new fields. |

## Plan

New file: `pkg/v1/controllers/workapplier/backoff_test.go` (package `workapplier`, 2026 header, trailing newline).

### Shared test helpers (file-local; avoid name clashes with `utils_test.go`)

- `newRateLimiterForTest(t, attempts, fixed, slowBase, initSlow, maxSlow, fastBase, maxFast, skip)`:
  calls the constructor and `t.Fatalf` on error.
- `workForBackoffTest(name string, gen int64) *placementv1alpha1.Work` in `memberReservedNSName1`;
  names via `fmt.Sprintf(workNameTemplate, ...)` (there is no `workName` const in the v1 package).
- `stateWith(applyRes ApplyResultType, availRes AvailabilityCheckResultType) *manifestProcessingState`
  (take both results so that `Skipped` availability is paired with a failed apply, e.g. `FailedToApply`,
  as in production, rather than with `Applied`).
- Delay comparison: compare `Seconds()` with `cmpopts.EquateApprox(0, 0.01)` (as the old tests) to absorb
  the `time.Duration` truncation from float multiplication.
- Struct comparison options: `cmp.AllowUnexported(MultiStageRequeueWithExponentialBackoffRateLimiter{})`,
  `cmpopts.IgnoreFields(..., "mu")`, and `cmpopts.EquateEmpty()` where nil/empty maps are equivalent.
- Stateful sequences: each table case builds a **fresh** limiter and walks a `steps` slice
  (`linkedWorks`, `states`, `wantDelaySeconds`) in order; no `t.Parallel()`.
- Never call `When`/`Forget` on the global `defaultRequeueRateLimiter`; the integration suite in the
  same package (`suite_test.go`) uses it.

### P0 tests

1. **`TestNewMultiStageRequeueWithExponentialBackoffRateLimiter`** (rewrite). Table:
   8 args + `wantRateLimiter` + `wantErred`. For errors, assert `got == nil` and
   `kferrors.Category(err) == kferrors.ErrCategoryUser`. For success, `cmp.Diff` the whole struct
   (empty maps, `forgets == 0`, durations converted from seconds).
   Note: the skip flag field is `skipToFastBackoffWhenAllAppliedManifestsAreAvailable`.
   - Valid: typical `(10, 5, 1.2, 2, 15, 1.5, 60, true)`; all lower bounds `(1, 2, 1.05, 2, 2, 1.05, 2, false)`
     (initial == maxSlow == maxFast, slow base == fast base); all upper bounds `(40, 3600, 100, 3600, 3600, 100, 3600, false)`.
   - Invalid (one field off from the typical config each): attempts `0`, `-1`, `41`; fixed `1`, `0`;
     fixed > maxFast (`61` vs `60`); slow base `1.04`; slow base > fast base (`2.0` vs `1.5`); fast base `100.01`;
     slow base `NaN`; initial `1`; initial > maxSlow (`16` vs `15`); maxSlow > maxFast (`61` vs `60`);
     maxFast `3601`.
   - Valid boundary: fixed == maxFast (`60` vs `60`).

2. **`TestWhenWithFullSequences`** (port + consolidate the 4 old sequence tests into one table; inputs:
   one primary Work, one `NotYetAvailable` state, skip disabled):
   - normal `(2, 5, 1.2, 2, 15, 1.5, 60)`: 5, 5, 2, 2.4, 2.88, 3.456, 4.1472, 4.9766, 5.9720, 7.1664,
     8.5996, 10.3196, 12.3835, 14.8602, 22.2903, 33.4354, 50.1531, 60, 60.
   - no slow backoff `(1, 5, 2, 2, 2, 5, 20)`: 5, 2, 10, 20, 20.
   - no fast backoff `(1, 5, 2, 2, 10, 5, 10)`: 5, 2, 4, 8, 10, 10 (16 > maxSlow, so 8 * 5 = 40, capped).
   - no backoff `(1, 5, 2, 10, 10, 5, 10)`: 5, 10, 10, 10.
   - **new** slow delay lands exactly on maxSlow `(1, 5, 2, 2, 8, 4, 100)`: 5, 2, 4, 8, 32, 100, 100
     (pins the `>` vs `>=` boundaries).
   - **new** default limiter config `(1, 5, 1.2, 2, 15, 1.5, 900)`: 5; 12 slow steps (2 … 14.8602);
     10 fast steps (22.2903 … 856.9121); then 900, 900 (pins the documented default pattern; see #12).

3. **`TestWhenWithLinkedWorksAndProcessingResultChanges`** (port + extend). Config `(1, 5, 2, 10, 30, 5, 200, false)`.
   One fresh limiter per case; warm up to the slow/fast stage, then mutate one input.
   - primary generation bump resets: 5, 10, 20 → (W gen 2) 5, 10.
   - secondary-only generation bump resets: [W, S] 5, 10, 20, 100 → (S gen 2) 5.
   - linked Work added resets: [W] 5, 10 → [W, S] 5.
   - linked Work removed resets: [W, S] 5, 10 → [W] 5.
   - processing result change resets: NotYetAvailable 5, 10, 20, 100, 200 → Available 5, 10.
   - both gen and result change resets: NotYetAvailable 5, 10, 20 → (W gen 2 + Available) 5.
   - **no reset** when only `applyErr`/`availabilityCheckErr`/`diffs` change (same result types):
     5, 10, 20 → 100 (documents the corner-case note on `computeProcessingResultHash`).
   - `Available` ↔ `NotTrackable` flip resets (different hash although both count as available).

4. **`TestWhenWithSkipToFastBackoff`** (rewrite). Config `(1, 5, 2, 10, 30, 5, 200, true)`; fresh limiter per case.
   - all `Available`: 5, 10, 50, 200, 200 (skip never applies in the fixed or initial-slow stage).
   - all `NotTrackable`; mixed `Available` + `NotTrackable`; empty states (vacuously available): same as above.
   - one `Applied`+`NotYetAvailable` / `Applied`+`FailedToCheck` / `FailedToApply`+`Skipped` among
     `Applied`+`Available`: 5, 10, 20, 100, 200 (no skip).
   - skip disabled + all `Available` (config with `false`): 5, 10, 20, 100, 200 (control).
   - unavailable → available mid-sequence: 5, 10, 20 → (reset by hash change) 5, 10, 50, 200.
   - available → unavailable mid-sequence: 5, 10, 50 → 5, 10, 20, 100.

5. **`TestForget`** (port; `lastTrackedGeneration` → `lastTrackedLinkedWorksGenHash`, plus `forgets`).
   Whole-struct `cmp.Diff` after `Forget`, then `When` must return the fixed delay.
   - forget tracked Work: removed from all 4 maps, other entries kept, `forgets` 0 → 1.
   - forget untracked Work: maps unchanged, `forgets` still incremented.
   - GC trigger: seed `forgets = gcTriggeringForgetThreshold - 1`; after `Forget`, entry removed,
     other entries kept, `forgets == 0`.

6. **`TestComputeLinkedWorksObjectGenHash`** (new). Relational table: each case has inputs `a` and `b` plus
   `wantSameHash bool`; assert `err == nil` for both. One golden anchor only: empty list hashes to
   `sha256("[]")` = `4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945` (verified).
   - differ: W gen 0 vs. W gen 1; [W] vs. [W, S]; [W, S] vs. [S, W] (order-sensitive);
     [W, S gen 1] vs. [W, S gen 2]; W vs. renamed W with the same generation.
   - same: identical inputs; (optional) same names/generations in another namespace (namespace is not
     hashed; linked Works always share a namespace, so this only pins an implementation detail).

7. **`TestComputeProcessingResultHash`** (port with `manifestProcessingState`). Same relational style plus
   the `sha256("[]")` anchor for no manifest; `err == nil`.
   - same: `FailedToApply` with vs. without `applyErr`; `Applied`+`FailedToCheck` with vs. without
     `availabilityCheckErr`; `FoundDrifts` with vs. without `diffs`; states differing only in
     `id`/`idStr`/`fromWorkObj`.
   - differ: zero-value state vs. no manifest; `Applied`+`NotYetAvailable` vs. `Applied`+`Available`;
     `Applied`+`Available` vs. `Applied`+`NotTrackable`; `DecodingErred`+`Skipped` vs.
     `FailedToApply`+`Skipped`; assorted multi-manifest list vs. the same list reordered.

8. **`TestAreAllProcessedManifestsAvailable`** (new). Table:
   nil → true; empty → true; `Available` → true; `NotTrackable` → true; `NotYetAvailable` → false;
   `FailedToCheck` → false; `Skipped` → false; zero value `""` → false;
   `Available` + `NotTrackable` → true; `Available` + `NotYetAvailable` → false.

### P1 tests

9. **`TestWhenWithMultipleWorks`**: config `(1, 5, 2, 10, 30, 5, 200, false)`, `NotYetAvailable` states;
   interleave `ns1/A`, `ns1/B`, and `ns2/A` on one limiter (A: 5, 10; B: 5; A: 20; ns2/A: 5).
   Verifies per-`NamespacedName` isolation.

10. **`TestWhenRequeueCounterCap`**:
    - default-config limiter, same inputs, 250 calls → last delay 900s and
      `requeueCounter[key] == maxNumOfRequeuesToTrack - 1`.
    - seeded state (default config, `NotYetAvailable`): counter = 199, last delay = 20s, hashes seeded via
      the two hash helpers for the same inputs → `When` returns 30s (20s ≥ maxSlow 15s, so ×1.5; verified),
      not the fixed/initial delay, and the counter stays 199.
    - (optional) the "195 requeues" comment on `maxNumOfRequeuesToTrack` is accurate (verified), but
      correctness only needs `maxNumOfRequeuesToTrack > attemptsWithFixedDelayUpperBound + 1`, since the
      counter matters only in the fixed/initial stages. Prefer that invariant check over pinning call #195.

11. **`TestGarbageCollect`**: populated maps → identical contents and `forgets == 0`; empty maps →
    still non-nil (explicit `!= nil` checks, no `EquateEmpty`) and a subsequent `When` does not panic.

12. **`TestDefaultRequeueRateLimiter`**: config only. `cmp.Diff` the global's config fields (ignore `mu`,
    the four maps, and `forgets`) against `(1, 5s, 1.2, 2s, 15s, 1.5, 900s, true)`. The config fields are
    never written after `init()`, so this read is race-free. The sequence itself lives in #2.

### P2 (optional)

13. **`TestRateLimiterConcurrentAccess`**: 16 goroutines × 100 iterations of `When`/`Forget` on overlapping
    keys; relies on `-race`; afterwards the four maps have identical key sets.

## Intentionally Not Covered

- `genHashErr`/`resHashErr != nil` reset branches: `json.Marshal([]string)` cannot fail and there is no seam
  to inject an error.
- `init()`'s `klog.Fatalf` path: arguments are constants; P1 #12 covers that construction succeeds.
- Empty `linkedWorks` (index panic): caller contract; the controller always passes the primary Work first.

## Observations (resolved)

1. The unused `default*` constants were removed (by the user).
2. The skip flag was renamed to `skipToFastBackoffWhenAllAppliedManifestsAreAvailable` (by the user).
3. Comment inconsistencies were fixed in `backoff.go`: the stale "reset values" constructor comments,
   the "diff reported" wording in the default limiter setup and struct docs, the counter-reset descriptions
   (now linked-Work based), and the `computeLinkedWorksObjectGenHash` doc comment name.

## Decisions

1. Consolidate the four old sequence tests (plus the default-config sequence) into one table-driven test.
2. Use relational (same/differ) assertions for the hash helpers with a single `sha256("[]")` golden anchor,
   instead of a golden value per case; relationships are the property the rate limiter depends on, and
   per-case goldens would break on any `resource.HashOf` change and could be refreshed blindly.
3. Do not touch the global `defaultRequeueRateLimiter` state in unit tests.
4. Reject `fixedDelaySeconds > maxFastBackoffDelaySeconds` in the constructor (user decision). Previously it
   was accepted and `When` silently clamped the fixed delay to the max (e.g. `(1, 3600, 2, 2, 8, 4, 60)`
   yielded 60, 2, 4). Implemented in `backoff.go`; covered by #1.

## Validation

- `go test -race -count=1 -run 'Test(NewMultiStage|DefaultRequeue|When|Forget|GarbageCollect|Compute|AreAllProcessed|RateLimiterConcurrent)' ./pkg/v1/controllers/workapplier/`:
  passed (13 tests; the `-run` filter skips `TestAPIs`, so no envtest assets are needed).
- Mutation check via `go test -overlay` (working tree untouched): all 9 injected bugs were caught (slow-cap
  `>`→`>=`, primary-only gen hash, no counter cap, no fixed-delay max validation, skip using the slow base,
  no-op GC, Forget keeping a tracker entry, hash including errors, NotYetAvailable counted as available).
- `gofmt -l`: clean.
- `hack/tools/bin/golangci-lint-v2.13.2 run ./pkg/v1/controllers/workapplier/...`: no issues in
  `backoff_test.go`. Pre-existing, unrelated findings remain in `backoff.go` (staticcheck ST1000 package
  comment; QF1001 De Morgan's law suggestions on the attempts and delay validations) and other files.
- `make reviewable` and the envtest integration suite were not run.

## Status

Implemented: `pkg/v1/controllers/workapplier/backoff_test.go` (all P0, P1, and P2 tests; P1 #10 is named
`TestWhenWithRequeueCounterCap`).
