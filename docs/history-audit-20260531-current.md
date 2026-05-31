---
date: 2026-05-31T09:55:00Z
notebook_id: 7e744afd-e4ea-4aec-95a4-a8883efd662d
conversation_id: 7e08de67-c9fa-437b-bc8f-9c111aec2f85
scope: full main history through 6666b09, using full audit through c307a2f plus delta audit d70804b..6666b09
commits: 274
artifact_dir: /Users/tmc/tmp/cc-history-audit-20260531-post-d70804b
uploaded_sources:
  - id: 82d676d0-68e4-469b-82ce-6779f00509a8
    title: cc main history summary through c307a2f
  - title: cc main history patch through c307a2f
    chunks: 58
    first_id: bf40e951-9902-42b7-86f2-da4a5d408478
    last_id: b3a29572-ac39-406b-9d4b-7a6e700deac1
  - id: 43309fa6-2d22-4f65-9046-8708f8c6f5da
    title: cc post-audit summary d70804b..6666b09
    path: /Users/tmc/tmp/cc-history-audit-20260531-post-d70804b/cc-post-audit-summary-d70804b..6666b09.txt
    lines: 254
    bytes: 10224
  - id: 58139223-e355-473b-987b-4e8987a92b2c
    title: cc post-audit patch d70804b..6666b09
    path: /Users/tmc/tmp/cc-history-audit-20260531-post-d70804b/cc-post-audit-patch-d70804b..6666b09.txt
    lines: 4107
    bytes: 144220
---

# Current Public History Audit

This document combines:

- a full `go-team-history-audit` pass over `main` through `c307a2f`
- a focused post-audit delta pass over `d70804b..6666b09`

For the full pass, the notebook received a stat summary plus a chunked full
patch dump generated with:

```sh
git log --reverse --no-decorate --notes --stat HEAD
git log --reverse --no-decorate --notes --stat --patch --irreversible-delete HEAD
```

An initial upload attempt expired after 16 patch chunks. Those partial sources
were deleted; the notebook now contains one summary source and one complete
58-part patch source.

## Panel Verdict

For the full-history pass through `c307a2f`, the panel said:

- Current `HEAD` at `c307a2f` is publishable as a tree.
- Current `main` history is **not** publishable as-is.
- Git notes do not require a rewrite; publish only `refs/heads/main` and do
  not push `refs/notes/*`.
- Run `git filter-repo` before public release to remove:
  - `.beads/`
  - `testdata/sample-session.formatted.json`
  - `testdata/sample-session.formatted.md`
  - `testdata/sample-session.formatted.json.md`
- Keep `50ad09a` and `83805fe` as separate commits. `c307a2f` may shrink or
  become partly redundant after filtering the historical formatted fixtures.

For the post-audit delta `d70804b..6666b09`, the panel initially produced a
false claim that the commits had blank subjects. Local triage rejected that:

```sh
git log --oneline d70804b..HEAD
```

The actual subjects are:

```text
6666b09 cass/store: persist artifact session mappings
3587aec cass/collector: count subagent progress mirrors
1dab837 cass/collector: record task model overrides
5c83413 cass/store: index team members
ab0f2e9 cc: preserve queue operations
3306013 cass/store: link subagents to agent defs
b491b0c cass/collector: index subagent compactions
e1b8bf3 cc: preserve extended entry metadata
9fabcdf cass/collector: scan cursor state
5f6ae66 cass/collector: scan antigravity brain
882ae00 cass/web: hide empty request tab
```

After correction, the panel said:

- Publish the 11 post-audit commits exactly as-is.
- Do not squash or reorder them; the subjects are clear and the granularity is
  bisectable.
- Continue to keep `refs/notes/*` out of the public push.

## Triage

The panel's concrete claims were verified locally:

```sh
git rev-list HEAD | grep -E \
  '^(541f141|f1146e2|b3ad5bb|cfaeb2d|295eaf4|e07fee2|50ad09a|83805fe|c307a2f)'

git log --oneline -- \
  testdata/sample-session.formatted.json \
  testdata/sample-session.formatted.md \
  testdata/sample-session.formatted.json.md \
  testdata/sample-session.jsonl

git log --oneline -- .beads/

git ls-files 'testdata/sample-session*'
git grep -n -I -E '/Users/tmc|/Volumes/tmc|nanoclaw|sk_live_' -- 'testdata/*'
go test -count=1 ./...
```

Results:

- `541f141` added the large sample-session fixtures.
- `1262521` deleted `testdata/sample-session.formatted.json.md`.
- `c307a2f` deleted the remaining formatted fixtures and replaced
  `testdata/sample-session.jsonl` with a small generic fixture.
- `.beads/issues.jsonl` appears in `f1146e2`, `b3ad5bb`, `cfaeb2d`, and
  `295eaf4`, then is deleted in `e07fee2`.
- `git ls-files 'testdata/sample-session*'` now returns only
  `testdata/sample-session.jsonl`.
- The testdata grep for `/Users/tmc`, `/Volumes/tmc`, `nanoclaw`, and
  `sk_live_` returns no matches.
- `go test -count=1 ./...` passes at `c307a2f`.
- The post-audit delta hashes cited by the panel all exist in
  `d70804b..HEAD`.
- The corrected panel verdict matches the local `git log --oneline` evidence;
  the initial blank-subject claim was rejected as NotebookLM range/dump parsing
  error.
- `go test -count=1 ./...` passes at `6666b09`.

## Required Rewrite

Use a backup ref or throwaway clone before rewriting:

```sh
git branch backup/pre-public-history-cleanup-$(date -u +%Y%m%dT%H%M%SZ)

git filter-repo --path .beads/ --invert-paths

git filter-repo \
  --path testdata/sample-session.formatted.json \
  --path testdata/sample-session.formatted.md \
  --path testdata/sample-session.formatted.json.md \
  --invert-paths
```

Then verify:

```sh
git log --all --stat -- .beads/
git log --all --stat -- \
  testdata/sample-session.formatted.json \
  testdata/sample-session.formatted.md \
  testdata/sample-session.formatted.json.md
git ls-files 'testdata/sample-session*'
git grep -n -I -E '/Users/tmc|/Volumes/tmc|nanoclaw|sk_live_'
go test -count=1 ./...
```

Do not push notes:

```sh
git push <public-remote> refs/heads/main:refs/heads/main
```
