# Determinism — Implementation Plan

Determinism ingests an opencode AI session and deterministically replays every
command and file operation (write, edit, delete, overwrite) the session
performed. It supports two input sources and several execution modes.

## 1. Goals

- Reproduce, in order, all `bash` commands and all file mutations
  (`write`, `edit`, `apply_patch`) an AI session performed.
- Support the **opencode chat text format** (markdown transcripts) and
  **opencode database reads** from `opencode.db`, with sessions looked up by
  **session name** (slug), id, title, or **initial prompt**.
- Execute **subprompts** (task/subagent child sessions) **gracefully**: fail
  softly and continue if anything inside them fails.
- Execute subprompts from the **prompts that lead up to them** (replaying the
  parent's leading commands/writes first), failing gracefully if any leading
  command or write fails.
- Provide **compressed filesystem/repository caching** behind its own flag so
  prompts that need pre-existing data can be replayed deterministically.

## 2. Scope

| In scope                                     | Out of scope (recorded + skipped)   |
|----------------------------------------------|-------------------------------------|
| bash tool replay                             | read-only tools (read, glob, grep)  |
| write tool replay (create/overwrite)         | webfetch, websearch                 |
| edit tool replay (old->new replace)          | question, todowrite, skill          |
| apply_patch replay (add/update/delete/move)  | summarize, codesearch, rewind       |
| task/subtask subprompt replay (opt-in)       |                                   |
| chat-text parsing                            |                                   |
| SQLite session lookup + load                 |                                   |
| compressed fs/repo cache (opt-in)            |                                   |

## 3. Input sources

### 3.1 Chat text format
The markdown transcript produced by the TUI export. Format
(`packages/tui/src/util/transcript.ts`):

```
# {title}

**Session ID:** {id}
**Created:** ...
**Updated:** ...

---

## User

{text}

---

## Assistant (agent · model · duration)

{text}

**Tool: bash**

**Input:**
```json
{...}
```

**Output:**
```
...
```

---
```

Parsed by `internal/session/chat.go`. Tool inputs/outputs are captured in JSON
fenced blocks; `ParseToolPart` normalizes input/output order.

### 3.2 opencode database
SQLite at `~/.local/share/opencode/opencode.db` (read-only). Tables:
- `session(id, slug, directory, title, parent_id, time_created, time_updated)`
- `message(id, session_id, time_created, data JSON)`
- `part(id, message_id, session_id, time_created, data JSON)`

Part JSON: `{type:"tool", tool:"bash", state:{status,input,output,error}}`,
`{type:"text", text}`, `{type:"reasoning", reasoning}`, `{type:"subtask", prompt,...}`.

Lookup priority: id (`ses_...`) → slug (session name) → title substring →
initial user prompt substring. See `internal/session/db.go`.

## 4. Architecture

```
cmd/determinism       CLI (run, plan, list, cache save/restore/list/info)
internal/session      types + chat parser + DB loader + lookup
internal/replay       engine, tool executors, apply_patch parser, subprompts
internal/cache        compressed gzip-tar filesystem/repo cache
internal/plan         implementation-plan markdown generator
```

Data flow: `reference → loader → session.Session → engine.Replay → Report`.

## 5. Execution model

- Replay is strictly ordered by `time_created` (message then part), so the
  state leading up to each action exists before it runs.
- **Graceful failure (default):** every action is attempted; failures are
  collected in the `Report` and replay continues. `--strict` aborts on first
  failure.
- **bash:** run with `/bin/sh -c` (or `--shell`), cwd resolved from
  `workdir`/session directory, honoring `timeout` (ms). Non-zero exit → failure.
- **write:** `mkdir -p` + write (overwrite). **edit:** single or
  `replaceAll` substitution; "oldString not found" → failure.
- **apply_patch:** parse `*** Begin Patch`/`*** End Patch` with add/update/
  delete/move hunks and apply to the working tree (see §7).
- **subprompts:** see §6.
- **sandboxing (`--rewrite-dir`):** all absolute paths recorded in the session
  are remapped into the sandbox dir before executing write/edit/apply_patch
  and bash `workdir` (see §8.1). Relative-path traversal is rejected. Bash
  command *text* is not rewritten (matches opencode's own advisory-only
  external-path handling).

## 6. Subprompt execution (graceful, behind flag)

Requirement ladder, implemented in `internal/replay/subprompt.go`:

1. **Baseline (no flag):** `task` tool parts and `subtask` parts are recorded
   as *skipped*; nothing is executed. This is the default.
2. **`--subprompts`:** subprompts are executed. Child session resolution:
   a. `task_id: ses_...` parsed from the task tool output (completed tasks), or
   b. `session.parent_id` + title/description match (`Loader.FindChild`) for
      tasks whose output recorded no task_id (e.g. errored tasks).
3. **Leading-up replay:** because replay is linear, every command and
   read/write from the prompts leading up to the subprompt has already run in
   the parent before the subprompt executes, so the child inherits that state.
4. **Graceful failure of the lead-up:** if any earlier command/write in the
   parent failed, the subprompt is still attempted; if the child itself fails,
   the failure is recorded and replay continues. `--strict` upgrades this.

## 7. apply_patch format

From `packages/opencode/src/patch/index.ts`:

```
*** Begin Patch
*** Add File: <path>
+content lines (each prefixed +)

*** Update File: <path>
*** Move to: <newpath>           (optional)
@@
 context
-old
+new

*** Delete File: <path>
*** End Patch
```

Chunks: lines prefixed ` ` (context), `-` (old), `+` (new); `@@` starts a
context chunk; `*** End of File` appends at EOF. `internal/replay/patch.go`
implements parsing and application.

## 8. Compressed filesystem/repository cache (behind its own flag)

`internal/cache/cache.go`:

- Snapshot = gzip-compressed tar of a directory tree.
- `Key(sessionID, workdir, prompt)` → sha256, 32 hex chars.
- **`cache save <key> <dir>`** / **`cache restore <key> <dir>`** /
  **`cache list`** / **`cache info <key>`** subcommands.
- In `run --cache`, before replay the engine restores the snapshot keyed by
  `(session, workdir, initial prompt)` so prompts requiring pre-existing data
  start from that state; after replay the resulting state is snapshotted.
- Skips `.git` and the cache dir itself; symlinks are skipped; unsafe archive
  paths (absolute / `..`) are rejected on restore.

## 8.1 Sandboxing (`--rewrite-dir` / default sandbox)

`internal/replay/rewrite.go` (`Rewriter`):

- Every `run` replays into a sandbox. Location: `--rewrite-dir`/`--dir` if
  given, otherwise `~/.local/share/determinism/<sanitized name>` (Unix);
  `<LocalAppData>/determinism`-style path on Windows.
- `Resolve(p)` maps a recorded path to the sandboxed path:
  - relative paths → under `TargetDir` (the mirror of `SessionDir`);
  - absolute paths at/under `SessionDir` → mirrored under `TargetDir`;
  - sibling paths (under the parent of `SessionDir` but outside it, e.g.
    `../princess-vpn`) → governed by policy: `--cache` extracts a compressed
    snapshot (`cache.SiblingKey(unit)`); `--dangerous` returns the real path
    after per-sibling approval (`--allow-dir` pre-approves); with neither the
    path is rejected gracefully;
  - other outside paths, or traversal that escapes the sandbox → error.
- **Copy-on-first-access:** resolving a session path copies the real file into
  the sandbox mirror (dirs are created empty) so replay commands can read the
  session's source. Skipped for dry runs, for `.git`/build/cache directories,
  non-regular files, and files over 200 MB. The original tree is only ever
  read, never written.
- **Command-text rewriting:** `RewriteCommand` rewrites absolute paths inside
  bash command text (redirects, assignments, quotes, heredocs preserved;
  heredoc bodies untouched) and escaping-relative sibling refs like
  `../princess-vpn/x`, so bash executes against the sandbox. System paths
  (`/dev/null`) are preserved.
- The engine routes every write/edit/apply_patch target and bash `workdir`
  through the rewriter when configured; the replay workdir is the sandbox
  mirror of the session directory.
- `SyncSiblings` re-snapshots sibling repos touched at their real paths when
  `--dangerous` and `--cache` are combined, keeping the cache in real-time
  sync.
- `--dangerous` prompts for approval (stdin) per missing sibling unless
  pre-approved or `--dry-run`; a warning is printed when a session references
  siblings without `--cache`/`--dangerous`.
- Unit-tested (`rewrite_test.go`, `sandbox_test.go`, `siblings_test.go`):
  sandbox writes land in the sandbox, the original tree is never touched,
  out-of-sandbox writes/traversal are rejected, sibling policies behave, and
  bash reads see the mirrored source.

## 9. CLI

```
determinism run <session|name|prompt|file.md> [--dir P] [--rewrite-dir P]
                 [--dry-run] [--subprompts] [--cache] [--cache-dir P]
                 [--dangerous] [--allow-dir P] [--strict] [--db P] [--shell S]
determinism plan <session|...> [--out file.md]      # write implementation plan
determinism list [--limit n]
determinism cache save|restore|list|info ...
```

## 10. Milestones

- M1 — session types, chat parser, DB loader, lookup (name/title/prompt).
- M2 — replay engine: bash/write/edit/apply_patch + graceful failure + Report.
- M3 — subprompt resolution + execution behind `--subprompts`.
- M4 — compressed cache behind `--cache`.
- M5 — CLI, plan generator, tests, README.

See `ROADMAP.md` for status.
