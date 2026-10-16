# Plan — sequential backlog program (subagent-driven)

Owner: Restu Muzakir · Started 2026-10-17 · Status: in progress

This is the plan-of-record for working through the whole open backlog **sequentially**,
one issue (or one slice of a split issue) at a time. Each slice is its own branch,
PR, CI run, lab verification and squash-merge. Each slice is executed as its own
subagent-driven run: a plan with independent tasks, one fresh implementer per task,
a spec-compliance review plus a quality review per task, and one whole-branch review
before merge.

## Order

| # | Slice | Issue | Size |
|---|-------|-------|------|
| 1 | Registry auth UX: `--with-registry-auth`, thread to agent, redaction, docs | #44 | S–M |
| 2 | Placement gaps: `node update --label-add/--label-rm`, surface unschedulable errors, stale docs | #49 (part) | S |
| 3 | Memory balloon (`--balloon`, `--balloon-target-mib`) | #47 (part) | S–M |
| 4 | Alerting: crash-loop + unhealthy-task + disk signals, default rules, notification sink | #90 (= #45 area 3) | M |
| 5 | cgroup v2 from the spec + `--cpuset` | #47 (part) | M |
| 6 | Hugepages + explicit rejection for NUMA → closes #47 | #47 (part) | M |
| 7 | Optional OpenTelemetry tracing, off by default | #91 (= #45 area 4) | M |
| 8 | In-guest per-task metrics via the guest agent | #88 (= #45 area 1) | L |
| 9 | Autoscaling → closes #49 | #49 (part) | L |
| 10 | Cross-node log aggregation → closes #45 (all children) | #89 (= #45 area 2) | L |
| 11 | Cold migration (measured downtime, rollback, HTTP e2e) | #46 (part) | XL |
| 12 | UFFD live migration (sub-second) → closes #46 | #46 (part) | XXL |
| 13 | TUI phase 0: extract `pkg/control` (deferred at the user's request) | #21 (part) | M |
| 14 | Close the tracking epic + refresh `docs/roadmap.md` | #40 | XS |

## Decisions taken at the program level

- **#46 is staged.** The issue's acceptance asks for sub-second downtime, which needs
  UFFD-based lazy paging; the repo has zero UFFD references and no node-to-node
  transport at all. Slice 11 delivers cold migration with a **measured, documented**
  downtime, rollback and an HTTP-reachability e2e; slice 12 delivers UFFD. Recorded
  in the issue when slice 11 lands.
- **#45 is split** into #88 (in-guest metrics), #89 (log aggregation), #90 (alerting)
  and #91 (tracing), as the issue itself requests. #45 closes when all four close.
- **#49's premise is partly stale**: `--constraint` and `--placement-pref` already
  exist (PR #56) and are honoured by the vendored SwarmKit scheduler. What is
  genuinely missing is a way to set node labels, surfacing unschedulable errors, and
  autoscaling. Slice 2 closes the first two; slice 9 the last.
- **#21 is deferred to the end** (user decision), and even then only phase 0
  (`pkg/control` extraction) unless the user asks for the UI. The Bubble Tea
  dependency tree needs an explicit sign-off from the user before phase 1.
- **Branching, not worktrees.** Work happens on a branch in the main checkout plus the
  `/root/sc-verify` checkout on the test server, matching this project's established
  workflow.

## Standing constraints (apply to every slice)

- Tests run on the Linux test server (`root@192.168.18.25`), never on the darwin
  workstation; only `gofmt`/static checks are local.
- No flag may be accepted and silently ignored: it either works or fails with a clear
  error (typed where the CLI must branch).
- Credentials, secrets and tokens never reach logs, JSON output or metrics.
- Tests must be host- and privilege-independent: they pass as root and as an
  unprivileged user, and they do not depend on state left by a previous run.
  See `docs-site/src/content/docs/contributing/testing/unit-tests.md`.
- `./pkg/...` coverage stays ≥ 85%; a PR must not drop a touched package by more
  than 0.1pp.
- Docs live in `docs-site/src/content/docs/` and are verified with the docs pipeline.
- Every slice: independent review before merge, lab e2e where the change is
  observable, and CI green (test + lint + build + coverage gate).
