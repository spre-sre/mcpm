# Go archetype

Files in this folder: `config.fragment.toml`, `crucible_contracts_test.go.example`,
`telemetry_driver.go.example`. `install_kit.py` copies the examples to
`.crucible/archetype/go/`. They are templates with `TODO(agent)` markers, not
tests. Copy, adapt, and remove the `.example` suffix.

## Tier 1: contract tests

- Framework: the standard `testing` package, run by `go test`.
- Put the file at `harness/crucible_contracts_test.go` in package
  `harness_test`. It imports the project by module path (`go.mod`), so it
  calls only the public API, and `harness/` is already trusted.
- Name tests `TestCrucible_INV_<NAME>_<case>`. The `TestCrucible` prefix is
  what `go test -run TestCrucible` selects; the `INV_<NAME>` part is what
  `verify` looks for in the file.
- For each invariant: the accepted boundary, the first rejected value on each
  side, and seeded random inputs (`rand.New(rand.NewSource(seed))`) that check
  the exact error. Map Go errors to the spec error code in one helper.
- Command: `go test -count=1 -run TestCrucible ./...`. `-count=1` disables
  the test cache. Check that the output lists your tests (`-v` locally): a run
  that selects zero tests passes silently.


### Tier 1 result protocol (architecture.md 11.1)

The CLI runs `[tier1] command` with `CRUCIBLE_RESULTS` (a file path outside the
repository) and `CRUCIBLE_RESULTS_NONCE` (32 hex characters). The contract
tests must write this JSON there:

```json
{"nonce": "<value of CRUCIBLE_RESULTS_NONCE>",
 "invariants": {"INV_<NAME>": {"cases": 2002, "failures": 0}}}
```

Tier 1 passes only if the command exits 0 AND the file exists, carries the
nonce, lists EVERY SPEC.md invariant with `cases > 0` and `failures == 0`, and
lists no unknown invariant. A process that exits early (for example a call to
exit from code under test) therefore fails. The example file in this folder
implements the protocol: copy its `check` helper and `TestMain` and call them for every
assertion. Keep the invariant list in the example in sync with SPEC.md.

Go snippet (the example does exactly this):

```go
func TestMain(m *testing.M) {
	code := m.Run()
	document := map[string]any{"nonce": os.Getenv("CRUCIBLE_RESULTS_NONCE"),
		"invariants": map[string]*tally{ /* every invariant, cases and failures */ }}
	data, _ := json.Marshal(document)
	_ = os.WriteFile(os.Getenv("CRUCIBLE_RESULTS"), data, 0o600)
	os.Exit(code)
}
```

Call `check(t, "INV_<NAME>", got, want, label)` for every assertion; it counts
a case and a failure per invariant.

**Residual.** The code under test runs in the same process as the oracle, so
it can reach the test's state, the environment, files and the exit path. This
kit has no Go source-rule engine. Record it under `## Gaps` in
`contracts/SPEC.md`: "Go: code under test shares the test process; it could
write CRUCIBLE_RESULTS or call os.Exit. Not statically checked."

## Tier 2: telemetry driver

Write `harness/telemetry_driver/main.go` (package main) from
`telemetry_driver.go.example`. `go run ./harness/telemetry_driver` builds it
in whichever tree runs it, so baseline and candidate each use their own code.

The driver contract (reference/architecture.md section 8): the CLI runs
`<tier2.command> --root <tree> --samples <n> --seed <s> --out <file.json>`
with cwd = the tree under test. The driver writes one JSON object
`{"latencies_ns": [floats], "memory_bytes": [floats]}` with exactly `n`
latencies and the same number of memory samples. Rules:

- Build the workload from the seed only (a seeded PRNG), up front, before any
  timing. The seed changes the data, never which code paths run.
- Time only the call to the project's real entry point. Count rejected
  inputs as valid work.
- Batch the calls: one sample times many calls (calibrate the batch once so a
  sample spans at least 200 microseconds) and reports the per-call mean as a
  float number of nanoseconds, with enough decimals to keep the resolution.
  The gate rejects samples whose smallest step exceeds 0.5 % of the median or
  that have under 20 % distinct values ("driver resolution too coarse").
- Take one memory sample per latency sample, after the call, outside the timed
  region.
- Never read the clock, the network or the environment to shape the work.

Memory in Go: `runtime.ReadMemStats(&m)`, sample `m.HeapAlloc` (live heap
bytes) after each call.

## AST gate

None. Set `[ast] command = []` and a non-empty `none_reason`
(`config.fragment.toml` has one). Record the gap in `contracts/SPEC.md` under
`## Gaps`, for example "No complexity ratchet for Go". Do not write a
substitute gate. Existing linters the human may choose to wire later (do not
wire them): `gocyclo`, `gocognit`, `golangci-lint`, `staticcheck`.

## Config

Use `config.fragment.toml`. Set `source_paths` to the project's packages.
`trusted_paths` must hold `contracts/`, `harness/`, `go.mod`, `go.sum`,
`go.work`, the config and the baseline (the module files select dependencies
and `replace` directives, so they load code implicitly). `[environment]
digest_globs` is empty: Go keeps no git-ignored toolchain lockfile. Argv elements that name repo files (none in the
default commands except through `./harness/...` and the test file) must be
pinned.
