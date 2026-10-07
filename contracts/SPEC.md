# mcpm contract

Invariants over the public entry points of `mcpm/internal/builder` and
`mcpm/internal/fetcher`. The contract tests are in
`harness/crucible_contracts_test.go`. Each test calls the real function and
maps its error to a code below.

## Invariants

- INV_PROJECT_DETECTED: dir ∌ {mcp.json, package.json, pyproject.toml, requirements.txt, go.mod} ⇒ builder.DetectAndBuild(dir) fails -> REJECT(ERR_PROJECT_UNDETECTED)
  Source: README.md "How It Works" step 2; internal/builder/builder.go DetectAndBuild.
- INV_MANIFEST_JSON: dir ∋ mcp.json ∧ (¬json.Unmarshal(mcp.json, &Manifest) ∨ trim(Manifest.runCmd) = "") ⇒ builder.DetectAndBuild(dir) fails before buildCmd runs -> REJECT(ERR_MANIFEST_INVALID)
  Source: README.md "Custom (mcp.json)"; internal/builder/builder.go buildFromManifest; required runCmd: maintainer decision, 2026-10-06.
- INV_SERVER_INSTALLED: os.Stat(cwd/.mcp/servers/name) reports not-exist ⇒ fetcher.GetServerPath(name) fails -> REJECT(ERR_SERVER_NOT_FOUND)
  Source: README.md "Update an Installed Server" and "How It Works" step 1; internal/fetcher/git.go GetServerPath.

The accept side of each invariant is also checked:

- A valid `mcp.json` with an empty `buildCmd` returns `requiredEnv` unchanged,
  and `runCmd` and `args` resolved against the server directory `dir`
  (MCP clients start the server from another working directory):
  - `runCmd`: a relative value that contains a path separator (`bin/server`,
    `./server`) becomes `filepath.Join(dir, runCmd)`. A bare name (`node`,
    `python`, `go`) stays a PATH lookup. An absolute value stays.
  - each arg: a non-empty relative arg that does not start with `-` and
    names an existing file or directory in `dir` (checked after `buildCmd`)
    becomes `filepath.Join(dir, arg)`. Every other arg (flags starting with
    `-`, also when a file of that name exists; values; package names such as
    `@scope/pkg`; relative paths that do not exist; absolute paths; `""`)
    stays as written.
  A nil and an empty list are equal.
  Source: README.md "Custom (mcp.json)" example (`"args": ["dist/index.js"]`,
  relative to the repo root); maintainer decision, 2026-10-06.
- `mcp.json` takes precedence over every other marker, so nothing is built.
  Source: internal/builder/builder.go DetectAndBuild ("1. Check for explicit
  mcp.json"). README.md "How It Works" lists `mcp.json` last but states no order.
- An installed server returns `cwd/.mcp/servers/<name>`, and
  `fetcher.ListServers()` returns exactly the directories under
  `.mcp/servers/` (no regular files; an empty list without an error when the
  directory is missing). Source: README.md "List Installed Servers";
  internal/fetcher/git.go ListServers.

## Error codes

| Code | Value | Meaning |
|---|---|---|
| ERR_PROJECT_UNDETECTED | -1 | No mcp.json, package.json, pyproject.toml, requirements.txt or go.mod in the repository root. |
| ERR_MANIFEST_INVALID | -2 | mcp.json exists but does not decode into the Manifest type. |
| ERR_SERVER_NOT_FOUND | -3 | No entry with that name under .mcp/servers/. |

## Gaps

- Go: no entropy check and no source-rule engine. Step 0 runs cmd/gate-ast (McCabe <= 10 per function; 12 pre-adoption functions grandfathered in .crucible/baseline.json, which may only go down).
- main.go is not scanned by the AST gate (a source file in the gate's argv breaks command closure); it has one function with McCabe 1.
- Go: code under test shares the test process; it could write CRUCIBLE_RESULTS or call os.Exit. Not statically checked.
- The functions return plain `fmt.Errorf` errors, not sentinel errors. The tests map an error to a code by its message prefix, so a reworded message fails Tier 1 until a human updates the tests.
- `cmd` rules (argument counts, URL schemes in `parseScheme`, transport detection, `-e KEY=VALUE` parsing) are unexported, and `cmd.Execute` calls `os.Exit`. No contract covers them without a source change.
- Paths that run external programs are not covered: Node, Python and Go builds (so a lost marker is not detected), a non-empty `mcp.json` `buildCmd` (runs through a login shell), `fetcher.Clone`/`Pull` (network), `injector.Register` (runs `claude`, writes `.gemini/settings.json`).
- No source states a rule for server names that are empty, `.`, or contain `/` or `..`; `GetServerPath` accepts them today when the path exists. A permission error from `os.Stat` is also accepted. Not part of this contract.
- `injector.GeminiConfig` keeps the top-level keys other than `mcpServers` through Unmarshal and Marshal. That is a preservation property, not a reject rule; no contract covers it.
- Tier 2 forces a GC before each memory sample, so no timed batch includes a GC cycle. The latency gate therefore does not see a rise in allocations; only the memory gate does.
- Tier 1 runs only `./harness`, and `TestMain` refuses foreign build files in `harness/`. A git-ignored file elsewhere (for example through `.git/info/exclude`, which no pin covers) escapes the local pins; CI on a clean clone is the second line.
