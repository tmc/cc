---
date: 2026-05-30T21:00:00Z
notebook_id: 3c3cb8e4-9224-4894-8f3f-a8f3e5fad8d0
conversation_id: 64965feb-ea0d-424e-94eb-ab751dc2cebd
scope: full main history through 85b49f0
commits: 258
artifact_dir: /Users/tmc/tmp/cc-history-audit-20260530
---

# Public History Audit

This audit used the `go-team-history-audit` workflow against the full
`github.com/tmc/cc` history on `main`, through commit `85b49f0`.

The history was too large for one useful patch dump, so it was uploaded to a
fresh NotebookLM notebook as an all-history summary plus six chronological
patch windows. The largest window was uploaded via `nlm source sync`, which
split the source into five NotebookLM chunks.

## Uploaded Sources

- `90f67a59-c9e0-431e-bd63-fe5032a6159f`:
  `cc public history audit 2026-05-30`
- `95cc7c4c-6181-48b8-acb9-4e21f560eb09`:
  `cc public history audit 2026-05-30 (pt2)`
- `e262cfb0-8b6f-46e3-9e7a-98efcc954c1f`:
  `cc public history audit 2026-05-30 (pt3)`
- `bdded605-e0ea-4a9b-a382-a88ab09f6f40`:
  `cc public history audit 2026-05-30 (pt4)`
- `76213fc5-f167-43e3-ac6c-724701458908`:
  `cc public history audit 2026-05-30 (pt5)`

Local uploaded packet:

```sh
/Users/tmc/tmp/cc-history-audit-20260530
```

## Initial Panel Verdict

The first panel response said **do not publish as-is**. Its strongest claims
were:

- AI telemetry appeared throughout the dump.
- `541f141` introduced massive raw session fixture data.
- `.beads/issues.jsonl` was tracked in history.
- Raw fixtures contained local paths and product/payment discussions.
- `3075e69 cc: auto` was a weak message.
- The `examples/xterm-research` to `docs/xterm-research` move was noisy.

## Triage

Local verification changed the verdict:

- The AI telemetry is in `refs/notes`, not commit message bodies. Do not push
  notes to the public remote.
- `3075e69 cc: auto` exists, but the follow-up panel classified it as cosmetic.
- `541f141` really added 89,459 lines across sample-session fixtures.
- `1262521` deleted only `testdata/sample-session.formatted.json.md`; the
  other large sample-session files remain at `HEAD`.
- `.beads/issues.jsonl` was tracked across early commits and deleted in
  `e07fee2`.
- `8665142` and `e92f5d5` are a normal add-then-move for xterm research docs.

Verification commands:

```sh
git notes list | wc -l
git show -s --format=%B 3075e69
git log --stat --oneline -- testdata/sample-session.formatted.json.md \
  testdata/sample-session.formatted.json \
  testdata/sample-session.formatted.md \
  testdata/sample-session.jsonl
git log --all --stat --oneline -- .beads/
git log --summary --oneline -- examples/xterm-research docs/xterm-research
git ls-files 'testdata/sample-session*'
```

## Corrected Verdict

The corrected NotebookLM panel verdict:

- **Mandatory before public**
  - Do not push `refs/notes`.
  - Remove `.beads/` from history.
  - Remove bulky formatted sample-session fixtures from history.

- **Strongly recommended before public**
  - Replace the remaining raw `testdata/sample-session.jsonl` fixture with a
    minimal generic fixture.
  - Remove or genericize local absolute paths in checked-in fixtures where they
    are not explicitly testing path encoding.

- **Not worth rewriting for**
  - `3075e69 cc: auto`.
  - The `xterm-research` add and relocation.

## Cleanup Plan

Work in a sibling clone or throwaway rewrite worktree. Keep this checkout and
the current `origin/main` untouched until the rewritten history has passed
tests and inspection.

1. Make a backup ref:

   ```sh
   git branch backup/pre-public-history-cleanup-$(date -u +%Y%m%dT%H%M%SZ)
   ```

2. Rewrite history to remove private task state:

   ```sh
   git filter-repo --path .beads/ --invert-paths
   ```

3. Rewrite history to remove bulky formatted fixtures:

   ```sh
   git filter-repo \
     --path testdata/sample-session.formatted.json \
     --path testdata/sample-session.formatted.md \
     --path testdata/sample-session.formatted.json.md \
     --invert-paths
   ```

4. Add a normal HEAD commit replacing `testdata/sample-session.jsonl` with a
   minimal generic fixture, or remove it if tests can be adjusted without losing
   coverage.

5. Add a normal HEAD commit genericizing fixture-only absolute paths where they
   are not part of a path parser contract.

6. Verify before publishing:

   ```sh
   git log --all --stat -- .beads/
   git log --all --stat -- testdata/sample-session.formatted.json \
     testdata/sample-session.formatted.md \
     testdata/sample-session.formatted.json.md
   git ls-files 'testdata/sample-session*'
   git grep -n -I -E '/Users/tmc|/Volumes/tmc|nanoclaw|sk_test_|sk_live_'
   go test ./...
   ```

7. Push only branch refs to the future public remote. Do not push notes:

   ```sh
   git push <public-remote> main
   ```

   Do not run:

   ```sh
   git push <public-remote> refs/notes/*
   ```

## Stop Conditions

Do not push rewritten history until:

- the filtered history has been inspected for `.beads/` and bulky fixtures;
- `go test ./...` passes from the rewritten tip;
- a final NotebookLM re-audit of the rewritten history says publishable; and
- the push target and visibility have explicit owner approval.
