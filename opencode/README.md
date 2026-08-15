# Determinism — opencode plugin

Exposes the determinism CLI (deterministic sandbox replay of opencode sessions)
inside opencode as four custom tools, so an agent can inspect and replay past
sessions without leaving the chat.

## Tools

| Tool                | Purpose                                                          |
|---------------------|------------------------------------------------------------------|
| `determinism_list`  | List sessions determinism can replay (id, slug, title).          |
| `determinism_plan`  | Parse a session into an ordered implementation plan (markdown). |
| `determinism_run`   | Replay a session inside a sandbox (dry-run by default).          |
| `determinism_cache` | Manage compressed filesystem/repository snapshots.               |

`determinism_run` mirrors `determinism run`: every bash command, write, edit,
and apply_patch runs in order against copies under the sandbox, so the
original project is never modified. It defaults to `--dry-run`; pass
`dryRun: false` to execute inside the sandbox. Sibling repos outside the
session dir are snapshotted with `cache`, or accessed at their real paths with
`dangerous` (pass `allowDir` to pre-approve; without it the run fails in a
non-interactive context, matching the CLI).

## Installation

```sh
npm install              # installs @opencode-ai/plugin (one-time)
```

Then register the plugin in opencode config (project `opencode.json` or global
`~/.config/opencode/opencode.json`):

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": ["file:///home/burningserenity/Documents/ByteWheel/Determinism/opencode/determinism.ts"]
}
```

Restart opencode. The four `determinism_*` tools will appear alongside the
built-ins.

## Binary discovery

The plugin resolves the `determinism` binary in this order:

1. `$DETERMINISM_BIN` if set and existing;
2. `<repo>/bin/determinism`;
3. otherwise it builds it in the repo (`go build -o bin/determinism ./cmd/determinism`),
   trying the toolchain used for this repo first, then PATH `go`.

## Development

```sh
npx tsc --noEmit --strict --target es2022 --module nodenext \
  --moduleResolution nodenext --types node --skipLibCheck determinism.ts
```

The plugin runs under opencode's Bun runtime; `determinism.ts` must stay
dependency-free apart from `@opencode-ai/plugin` (so `node_modules` can be
recreated with `npm install` from `package.json`).
