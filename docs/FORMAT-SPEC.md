# Determinism — Format Specification

This document specifies every data format Determinism reads or writes. Sources
of truth are the opencode source in `/tmp/opencode-src` (for reference) and
samples from the live `opencode.db`.

## 1. Chat text format

Emitted by `formatTranscript` in `packages/tui/src/util/transcript.ts`.
Line-oriented markdown; determinism parses it with a small state machine.

```
# {title}

**Session ID:** {id}
**Created:** {locale string}
**Updated:** {locale string}

---

## User

{plain text parts, one per paragraph}

---

## Assistant (agent · model · duration)

{plain text parts}

_Thinking:_
{reasoning text}

**Tool: {tool}**

**Input:**
```json
{formatted JSON}
```

**Output:**
```
{raw output}
```

**Error:**
```
{raw error}
```

---
```

Notes:
- Every message block ends with a `---` separator line.
- A `## User` / `## Assistant ...` line opens a new message.
- `**Tool: X**` opens a new tool part.
- Fenced blocks immediately following `**Input:**`, `**Output:**`, or
  `**Error:**` carry that part's payload. Parsing is lenient about which
  block came first (`ParseToolPart` swaps if input doesn't parse as JSON).
- The format has no timestamps; Determinism assigns synthetic monotonic
  timestamps per line to preserve order.

## 2. SQLite database

Path: `~/.local/share/opencode/opencode.db` (may be a directory name in some
configs). Opened **read-only** with `modernc.org/sqlite` (pure Go, no cgo),
DSN `file:<path>?mode=ro&_pragma=query_only(1)`.

### session

| column       | type    |
|--------------|---------|
| id           | TEXT PK (ses_...) |
| project_id   | TEXT    |
| parent_id    | TEXT    | child (subagent) session link |
| slug         | TEXT    | the session "name" (e.g. swift-pixel) |
| directory    | TEXT    | cwd of the session |
| title        | TEXT    | human title |
| version      | TEXT    | schema version |
| time_created | INTEGER | ms epoch |
| time_updated | INTEGER | ms epoch |
| ...          |         | metadata, cost, tokens |

### message

| column       | type    |
|--------------|---------|
| id           | TEXT PK (msg_...) |
| session_id   | TEXT    |
| time_created | INTEGER |
| time_updated | INTEGER |
| data         | TEXT JSON |

`data`:
```json
{ "role": "user"|"assistant", "time": {"created": ms, "completed": ms},
  "modelID": "...", "providerID": "opencode", "agent": "...", "mode": "build" }
```

### part

| column       | type    |
|--------------|---------|
| id           | TEXT PK (part_...) |
| message_id   | TEXT    |
| session_id   | TEXT    |
| time_created | INTEGER |
| time_updated | INTEGER |
| data         | TEXT JSON |

Part `data` variants relevant to replay:

```json
{ "type": "tool", "callID": "tooluse_...", "tool": "bash",
  "state": { "status": "completed",
             "input": { "command": "...", "workdir": "...", "timeout": 600000 },
             "output": "...", "error": "..." } }
```

```json
{ "type": "text", "text": "..." }
{ "type": "reasoning", "reasoning": "..." }
{ "type": "subtask", "prompt": "...", "description": "...", "agent": "explore",
  "model": "...", "command": "..." }
```

Task tool input: `{ "description", "subagent_type", "prompt" }`.
Task tool output begins: `task_id: ses_<child> ...` followed by `<task_result>`.
Child session link also available via `session.parent_id`.

## 3. Tool input schemas (replayable)

| tool        | input fields |
|-------------|--------------|
| bash        | `command`, `workdir?`, `timeout?` (ms) |
| write       | `filePath` or `path`, `content` (older DB rows use `filePath`) |
| edit        | `filePath` or `path`, `oldString`, `newString`, `replaceAll?` |
| apply_patch | `patchText` |
| task        | `description`, `subagent_type`, `prompt` |

## 4. apply_patch patch text

```
*** Begin Patch
*** Add File: rel/path
+content...

*** Update File: rel/path
*** Move to: rel/newpath     (optional)
@@
 context line
-old line
+new line
*** End of File              (optional; append-at-EOF)

*** Delete File: rel/path
*** End Patch
```

Semantics (mirroring `packages/opencode/src/patch/index.ts`):
- `*** Add File:` — lines prefixed `+` form the file content.
- `*** Update File:` — optional `*** Move to:` first, then `@@` context chunks:
  lines prefixed ` ` are context, `-` removed, `+` added. A hunk whose old side
  is empty (and `*** End of File`) appends new lines at EOF.
- `*** Delete File:` — removes the file.
- `*** Begin Patch` / `*** End Patch` are required; a heredoc-wrapped body is
  unwrapped first.

## 5. Plan output (implementation planning)

`determinism plan` writes a markdown table of actions (order, session, kind,
detail) for a session. See `internal/plan/plan.go`.

## 6. Cache format

Entry: `<cache-dir>/<key>.tar.gz` where key = sha256(sessionID \0 workdir \0
initialPrompt) truncated to 32 hex. Tar entries: relative file paths, gzip
compressed. Directories and symlinks are not archived; `.git` and the cache dir
are excluded. Restore rejects absolute paths and `..` traversal.
