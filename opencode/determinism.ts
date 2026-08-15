// Determinism plugin for opencode.
//
// Exposes the determinism CLI (internal/replay sandbox replay of opencode
// sessions) as custom tools:
//
//   - determinism_list  — list sessions determinism can replay
//   - determinism_plan  — build an implementation plan from a session
//   - determinism_run   — replay a session inside a sandbox
//   - determinism_cache — manage compressed filesystem/repository snapshots
//
// The plugin locates the determinism binary via DETERMINISM_BIN, then the
// repo's bin/determinism, and finally builds it from source if missing.
//
// Register this plugin in opencode config:
//
//	{ "plugin": ["file:///path/to/this/repo/opencode/determinism.ts"] }
//
// Type-check with:  cd opencode && npx tsc --noEmit determinism.ts
// (requires:  npm install)
import { type Plugin, tool } from "@opencode-ai/plugin"
import fs from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

const PLUGIN_DIR = path.dirname(fileURLToPath(import.meta.url))
const REPO_DIR = path.resolve(PLUGIN_DIR, "..")
const BIN = path.join(REPO_DIR, "bin", "determinism")

// Build commands tried in order when bin/determinism is missing. The `go` on
// PATH can inherit a broken GOROOT on this machine, so the absolute toolchain
// is tried first, then PATH go with GOROOT unset.
const BUILD_COMMANDS = [
  "GOROOT=/programs/dev-lang/go/current/lib/go /programs/dev-lang/go/current/lib/go/bin/go build -o bin/determinism ./cmd/determinism",
  "env -u GOROOT go build -o bin/determinism ./cmd/determinism",
  "env -u GOROOT /usr/lib/go/bin/go build -o bin/determinism ./cmd/determinism",
]

// ensureBinary returns a working path to the determinism binary, building it
// from source in the repo if it does not exist yet.
async function ensureBinary($: any): Promise<string> {
	const envBin = process.env.DETERMINISM_BIN
	if (envBin && fs.existsSync(envBin)) return envBin
	if (fs.existsSync(BIN)) return BIN

	const shell = $.cwd(REPO_DIR).nothrow().quiet()
	for (const cmd of BUILD_COMMANDS) {
		const out = await shell`${{ raw: cmd }}`
		if (out.exitCode === 0 && fs.existsSync(BIN)) return BIN
	}
	throw new Error(
		`determinism binary missing at ${BIN} and auto-build failed. ` +
			`Build it manually (cd ${REPO_DIR} && go build -o bin/determinism ./cmd/determinism) ` +
			`or set DETERMINISM_BIN to an existing binary.`,
	)
}

interface RunResult {
	ok: boolean
	text: string
}

// runDeterminism executes the CLI with the given arguments in the repo dir and
// captures its output.
async function runDeterminism($: any, args: string[]): Promise<RunResult> {
	const bin = await ensureBinary($)
	const cmd = [bin, ...args].map((a) => $.escape(a)).join(" ")
	const out = await $.cwd(REPO_DIR).nothrow().quiet()`${{ raw: cmd }}`
	const stdout = (out.stdout?.toString() ?? "").trim()
	const stderr = (out.stderr?.toString() ?? "").trim()
	if (out.exitCode !== 0) {
		const parts = [`determinism exited with code ${out.exitCode}`]
		if (stdout) parts.push(stdout)
		if (stderr) parts.push(stderr)
		return { ok: false, text: parts.join("\n") }
	}
	return { ok: true, text: stdout }
}

const sessionArg = tool.schema
	.string()
	.describe(
		"Session reference: a session id (ses_...), slug/name, title substring, initial prompt text, or a path to a chat-text .md transcript.",
	)

const dbArg = tool.schema
	.string()
	.optional()
	.describe("opencode database path (default ~/.local/share/opencode/opencode.db)")

export const DeterminismPlugin: Plugin = async ({ $ }) => {
	return {
		tool: {
			determinism_list: tool({
				description:
					"List the opencode sessions determinism can replay (id, slug, title). Use it to find a session reference for determinism_plan or determinism_run.",
				args: {
					limit: tool.schema
						.number()
						.int()
						.positive()
						.optional()
						.default(50)
						.describe("Maximum number of sessions to list"),
					db: dbArg,
				},
				async execute(args) {
					const a = ["list", "--limit", String(args.limit ?? 50)]
					if (args.db) a.push("--db", args.db)
					const res = await runDeterminism($, a)
					return res.ok ? res.text : `determinism: ${res.text}`
				},
			}),

			determinism_plan: tool({
				description:
					"Parse a session into an ordered implementation plan (markdown): every bash command, write, edit, and apply_patch in sequence. Optionally write the plan to a file instead of returning it.",
				args: {
					session: sessionArg,
					out: tool.schema
						.string()
						.optional()
						.describe("Optional path to write the plan markdown to"),
				},
				async execute(args) {
					const a = ["plan", args.session]
					if (args.out) a.push("--out", args.out)
					const res = await runDeterminism($, a)
					return res.ok ? res.text : `determinism: ${res.text}`
				},
			}),

			determinism_run: tool({
				description:
					"Replay a session deterministically inside a sandbox: every bash command, write, edit, and apply_patch runs in order against copies under the sandbox, so the original project is never modified. Defaults to --dry-run (print actions, execute nothing); set dryRun=false to execute inside the sandbox. Sibling repos outside the session dir are snapshotted with cache, or accessed at their real paths with dangerous (pass allowDir to pre-approve; without it the run fails in this non-interactive context).",
				args: {
					session: sessionArg,
					dryRun: tool.schema
						.boolean()
						.optional()
						.default(true)
						.describe("Print actions without executing (true by default for safety)"),
					cache: tool.schema
						.boolean()
						.optional()
						.default(false)
						.describe("Snapshot sibling repos into the cache and reference those snapshots"),
					dangerous: tool.schema
						.boolean()
						.optional()
						.default(false)
						.describe("Access sibling repos at their real absolute paths (requires per-sibling approval)"),
					allowDir: tool.schema
						.array(tool.schema.string())
						.optional()
						.describe("Pre-approve a sibling directory for --dangerous (repeatable)"),
					subprompts: tool.schema
						.boolean()
						.optional()
						.default(false)
						.describe("Execute task/subagent subprompt sessions too"),
					strict: tool.schema
						.boolean()
						.optional()
						.default(false)
						.describe("Stop at the first failure instead of failing gracefully"),
					rewriteDir: tool.schema
						.string()
						.optional()
						.describe("Sandbox root directory (default ~/.local/share/determinism/<name>)"),
					cacheDir: tool.schema
						.string()
						.optional()
						.describe("Cache directory (default ~/.cache/determinism)"),
					db: dbArg,
				},
				async execute(args) {
					const a = ["run", args.session]
					if (args.dryRun) a.push("--dry-run")
					if (args.cache) a.push("--cache")
					if (args.dangerous) a.push("--dangerous")
					for (const d of args.allowDir ?? []) a.push("--allow-dir", d)
					if (args.subprompts) a.push("--subprompts")
					if (args.strict) a.push("--strict")
					if (args.rewriteDir) a.push("--rewrite-dir", args.rewriteDir)
					if (args.cacheDir) a.push("--cache-dir", args.cacheDir)
					if (args.db) a.push("--db", args.db)
					const res = await runDeterminism($, a)
					return res.ok ? res.text : `determinism: ${res.text}`
				},
			}),

			determinism_cache: tool({
				description:
					"Manage determinism's compressed filesystem/repository snapshots: save a directory, restore a snapshot, list entries, or show info for one.",
				args: {
					op: tool.schema
						.enum(["save", "restore", "list", "info"])
						.describe("Cache operation to perform"),
					key: tool.schema
						.string()
						.optional()
						.describe("Snapshot key (required for save/restore/info)"),
					dir: tool.schema
						.string()
						.optional()
						.describe("Directory to save from / restore into (required for save/restore)"),
					cacheDir: tool.schema
						.string()
						.optional()
						.describe("Cache directory (default ~/.cache/determinism)"),
				},
				async execute(args) {
					const a = ["cache", args.op]
					if (args.key) a.push(args.key)
					if (args.dir) a.push(args.dir)
					if (args.cacheDir) a.push("--cache-dir", args.cacheDir)
					const res = await runDeterminism($, a)
					return res.ok ? res.text : `determinism: ${res.text}`
				},
			}),
		},
	}
}
