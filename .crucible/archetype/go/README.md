# Go archetype

Files in this folder: `config.fragment.toml`, `cmd/gate-ast/main.go` (the AST gate,
installed live), `crucible_contracts_test.go.example`,
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
- Command: `go test -count=1 -run TestCrucible ./harness`. `-count=1` disables
  the test cache. The command names the one package that holds the contract tests, not
  `./...`: `./...` would also run the `TestMain` of an ungated package (see "Implicit loaders"). Check that the output lists your tests (`-v` locally): a run
  that selects zero tests passes silently. The fragment narrows only the command it names. A Tier 1 shell
  script that runs `go test ./...` itself is not narrowed: the script must name its packages.


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

### Mutants (G12 level 1)

Every invariant in `contracts/SPEC.md` needs a mutant: a small patch that breaks the code the invariant
guards. The contract tests must then report failures for that invariant. The kit applies each mutant to a
fresh scratch copy of the tree and runs the Tier 1 command there (or `[mutation] command`). A mutant that
the tests do not catch ("survived") means a lazy test. Run the stage with
`python3 bin/crucible mutation --json`.

Before step 1, check the file you will change. It must be tracked (`git ls-files --error-unmatch -- <file>` exits 0; commit it or `git add` it), and it
must have no uncommitted change: `git diff --quiet -- <file>` exits 0. If it has changes, stop and ask the
human; do not stash or discard their work. Step 3 is safe only under this precondition. `git diff` prints
nothing for an untracked file.

Write a mutant:

1. Change one source file as little as you can, to break the rule of one invariant.
2. Save the change, with a command that ignores your git config:
   `git -c color.diff=never diff --no-ext-diff --no-textconv --no-color --src-prefix=a/ --dst-prefix=b/ -U0 -- <file> > contracts/mutants/INV_<NAME>-<kind>.patch`.
3. Restore the file: `git checkout -- <file>` (only under the precondition above).
4. Put these two lines and one blank line at the top of the patch file:

   ```text
   Crucible-Mutant: INV_<NAME>
   Description: <what the change breaks, one line, 200 characters at most>
   ```

5. List the file in `[mutation] mutants` in `.crucible/config.toml`. `contracts/` is a trusted path, so a
   human ratifies the mutants at the repin.

Write up to three kinds for each invariant: the boundary off by one, the wrong error code, the check removed.
One mutant per invariant is the minimum. A mutant file changes one existing source file, with one to three
hunks. Each hunk removes at least one line. The file adds at most ten lines in all. It uses LF line ends and
`--- a/<path>` and `+++ b/<path>` lines. It has no new, deleted, renamed or binary file. When you change the
source later, write the mutant again: the kit applies it byte-exact, and a mismatch is an error.

If no safe mutant exists, waive the invariant: `waived = {INV_<NAME> = "reason"}` in `[mutation]`, and write
the same reason under `## Gaps` in `contracts/SPEC.md`. A human ratifies each waiver at the PIN prompt, which
prints "N of M invariants waived".

The copy holds every file of the work-tree listing. A submodule or a nested repository becomes an empty
directory in the copy, and the report lists it under `skipped_entries`: a test that needs its files fails
there. Run the stage without `--seed`; a seeded run (the order is known in advance) writes no pass record.

An example mutant:

```text
Crucible-Mutant: INV_EXAMPLE_LIMIT
Description: accept one value above the limit

--- a/internal/limits/limits.go
+++ b/internal/limits/limits.go
@@ -5 +5 @@
-const Limit = 100
+const Limit = 101
```

Go notes:
- Each run is a fresh copy, and `go test` builds it. The module and build caches live outside the repository,
  so the builds stay fast.
- A mutant must compile. A compile error gives no results file, so the run is "not judged".
- A panic in the code under test ends the test binary before `TestMain` writes the results. Write a mutant
  that returns a wrong value or a wrong error, not one that panics.
- `-run TestCrucible` in the Tier 1 command applies to every package it names. Check that each package that
  holds a contract test has tests with that prefix.

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

### Build step (`[tier2] build`)

`[tier2] build` (optional argv list) runs once in the baseline tree and once in the work tree, before round 1 and not
again. Each build has its own budget, `build_timeout_seconds` (default 900), so build time does not reduce
`[tier2] timeout_seconds` and enters no sample. It runs in the tree, in the scrubbed environment plus the
`[tier2] env` table of that tree's own config, without `CRUCIBLE_*` variables and with no argument appended; the
baseline uses HEAD's own `build` and budget. The work tree builds first. A failing build stops Tier 2 at stage `build`
(`ERROR_TIER2`), a timeout at stage `timeout` (`ERROR_TIMEOUT`), and a baseline build that fails is never a first
adoption. A build that changes the gated tree fails at stage `build`; a process it leaves behind is killed.
`preflight` runs the work-tree build before its smoke run. The files that `build` names are in the command closure
(kind `driver` in the repin review).

Never name a build output in a command. An ignored output admitted by `ignored_allow` becomes an environment digest
that every rebuild changes, which ends in exit 3 at the next run; without the admission it is an unpinned closure
file. A build output inside the work tree is not pinned: use a command that rebuilds, or clear the directory.

Residual risks of a build: a build that starts a daemon with `setsid` (a Gradle daemon, `sccache`) leaves the process
group, so the kill afterwards does not reach it. It could edit a gated file after the tree check, while the timed
rounds run; `promote` sees the change only at admission, where it still fails closed. The candidate build also runs
after HEAD is written to the baseline tree under `$TMPDIR/crucible-tier2-*/baseline`, so candidate build code can edit
that tree before the first baseline sample (the candidate driver has the same power between rounds).

Go (kit 3.2.0 trial, Go 1.24.0, mcpm): `go run` caches the linked executable. Without a build the first driver run
in a fresh tree took 2.4 s and later runs 0.25 s; after `go build -o /dev/null` the first run took 0.9 s, but the build
itself took 1.6 s, so the sum did not change. On a warm cache a whole `telemetry` run took 35.2 to 35.6 s without a
build and 35.2 to 36.3 s with one or two. The fragment therefore leaves `build` unset. If you set it, use a warm-up such as
`go build -o /dev/null ./harness/telemetry_driver`: plain `go build` writes `./telemetry_driver` into the
repository root.


## AST gate

`cmd/gate-ast/main.go` is a native McCabe gate (Go standard library only:
`go/parser`, `go/ast`, `go/token`). `install_kit.py --archetype go` installs it
to `cmd/gate-ast/main.go` (live, not an example). The config fragment runs it
as `[ast] command = ["go", "run", "./cmd/gate-ast", "-json", "-limit", "10",
"./internal", "./cmd", "./pkg"]`. Replace the three scan directories with the
project's `source_paths`. The fragment pins `cmd/gate-ast/` in `trusted_paths`
(a path in both lists is pinned), so only a human can change the gate.

Counting and keys are in reference/architecture.md section 14: each function
declaration is a unit and every function literal inside it counts toward it
(counting version 2). A literal outside any function is its own unit,
`glob.funcN`. The score is 1 plus one for each `if`, `for`, `range`,
non-default `case`, non-default `select` case, `&&` and `||`. The gate skips
`*_test.go`, `vendor/`, `testdata/` and hidden directories. The gate's own code
is scanned and must pass its limit.

Phase 3 (before you change any source file), write the baseline:

```sh
go run ./cmd/gate-ast -snapshot ./internal ./cmd ./pkg
python3 bin/crucible check-ast
```

The snapshot records only functions over the limit and refuses (exit 2) if
`.crucible/baseline.json` exists. A grandfathered function may not exceed its
baseline M; every other function must be at or below the limit. A human lowers
or removes entries after refactors with
`go run ./cmd/gate-ast -tighten ./internal ./cmd ./pkg`. It never raises a
value. The baseline is a pinned trusted input.

A parse error is a violation and exit 2 (fail closed). The gate does not check
entropy or forbidden effects; those checks exist only in the python archetype.
Record in `contracts/SPEC.md` under `## Gaps`: "Go: no entropy check and no
source-rule engine." Linters the human may choose to wire later (do not wire
them): `gocognit`, `golangci-lint`, `staticcheck`.

### Migrating a version-1 baseline (kit v2.0)

Kit v2.0 counts closures and nested scopes toward the enclosing function and
refuses a baseline with `"version": 1`. The migration is human-led; the
baseline is a trusted input. Counting and the trust rules are in
reference/architecture.md section 15. `<kit>` below is the kit's install path:
`claude plugin list --json` → `installPath` of `crucible`, or your clone.

1. Replace the repo's engine with the kit version. `install_kit.py` refuses a
   changed file, so a re-install is not the way:

```sh
cp <kit>/templates/archetypes/go/cmd/gate-ast/main.go cmd/gate-ast/main.go
```

2. Commit `.crucible/pins.json` if it is not committed. The engine reads the
   genesis from the committed file. It exits 2 if a work-tree `pins.json` has
   a different genesis or does not parse.
3. Check the engine folder BEFORE the first migration run (and before every
   verify run). `git status --porcelain --ignored cmd/gate-ast/` must print no
   `??` or `!!` line; a ` M` line for the replaced engine is expected. `ls -A
   cmd/gate-ast/` must show only `main.go`. A planted
   file there can delete itself in an `init()` function before the engine's
   own folder check runs.
4. Run the migration. It writes the version-2 baseline and judges the work
   tree against it. Name the file `./cmd/gate-ast/main.go`, not the directory.
   A file argument compiles that one file, so a planted `.go` file beside it
   is never compiled:

```sh
# Run from the repository root. Use the same flags (-limit, -baseline if set)
# and scan paths as the [ast] command. Keep the GOFLAGS prefix (see below).
GOFLAGS=-trimpath=false go run ./cmd/gate-ast/main.go -json -migrate-baseline ./internal ./cmd ./pkg
```

   Both migration modes refuse a repository with a shallow clone, a grafts
   file, or local git config that redirects git or runs code (for example
   `core.hooksPath` or a `filter.*` driver); they exit 2 and name the key.
   Remove it or work in a full clone (reference/architecture.md 16.2, 16.6).
   The engine reads the worktree config only when `extensions.worktreeConfig`
   is true (reference/architecture.md 22.1).
5. Read every report row. Refactor each violation or accept it. A row marked
   `RAISED` or `identity unverified` needs a decision; the `genesis_line` and
   `head_line` columns show what moved.
6. After ANY source edit, restore the version-1 baseline and run the
   migration again (a second migration on a version-2 baseline exits 2):

```sh
git checkout -- .crucible/baseline.json
```

7. Check the engine folder again (step 3). Then run the verification LAST,
   after the final edit, and BEFORE you commit the migrated baseline. It
   re-measures the work tree, so a later edit makes it exit 1:

```sh
GOFLAGS=-trimpath=false go run ./cmd/gate-ast/main.go -json -verify-migration ./internal ./cmd ./pkg
```

8. Check that `engine_sha256` in the report equals the sha256 of the KIT's
   file, not of the repo copy. The engine hashes its own file, so the repo
   copy always matches:

```sh
shasum -a 256 <kit>/templates/archetypes/go/cmd/gate-ast/main.go
```

   `go run ./cmd/gate-ast` compiles every `.go` file in `cmd/gate-ast/`, but
   the hash covers `main.go` only. Check again, as the last check, that
   `git status --porcelain --ignored cmd/gate-ast/` shows no `??` or `!!` line (a
   ` M` line for the replaced engine is expected) and that
   `ls -A cmd/gate-ast/` shows only `main.go`. The engine also exits 2 in both
   migration modes when it finds any other entry there. That in-process check
   runs after package `init()` functions, so it is a guard only together with
   the file-argument form and the check before the run.

9. Stage the engine, `.crucible/baseline.json` and every source file you
   changed, added or deleted in step 5, then run
   `python3 bin/crucible install-hooks --repin`.

With `-trimpath` the engine cannot read its own source to hash it and exits 2.
`unset GOFLAGS` and an empty `GOFLAGS=` do not help when `-trimpath` was set
with `go env -w`. `GOFLAGS=-trimpath=false` overrides it in both cases, so set
it for the migration. The prefix also replaces any `-overlay` or `-toolexec`
flag set in the Go env file or in the shell, so those cannot change what is
compiled. If you drop the prefix, you drop that guard.

`go run` reports a gate exit 2 as shell status 1 and prints `exit status 2`.
The JSON `ok` field cannot tell exit 2 from exit 1. To see the real exit code,
build the gate and run the binary from the repository root:

```sh
GOFLAGS=-trimpath=false go build -o <tmp>/gate-ast ./cmd/gate-ast/main.go
<tmp>/gate-ast -json -verify-migration ./internal ./cmd ./pkg; echo $?
```

After the baseline is committed, `-verify-migration` exits 2
("already version 2"). To re-check a merged migration, check out the parent
of the migration commit, restore the whole tree from the migration commit
(this also applies files that it deleted), and verify:

```sh
git checkout --detach <migration>~1
git restore --source=<migration> --staged --worktree -- .
git status --porcelain --ignored cmd/gate-ast/ && ls -A cmd/gate-ast/   # no ?? or !! line, then only main.go
GOFLAGS=-trimpath=false go run ./cmd/gate-ast/main.go -json -verify-migration ./internal ./cmd ./pkg
```

## Config

Use `config.fragment.toml`. Set `source_paths` to the project's packages.
`trusted_paths` must hold `contracts/`, `harness/`, `cmd/gate-ast/`, `go.mod`, `go.sum`,
`go.work`, `go.work.sum`, `vendor/`, `.gitignore`, the config and the baseline (the module files select
dependencies and `replace` directives, so they load code implicitly). `[environment]
digest_globs` is empty: Go keeps no git-ignored toolchain lockfile. Argv elements that name repo files (none in the
default commands except through `./harness/...` and the test file) must be
pinned.

### Implicit loaders

Go loads these files and code without a command naming them. The fragment covers each one.

- `go.mod`, `go.sum`, `go.work`, `go.work.sum`: pinned. `GOWORK=off` in every `env` table keeps a local
  workspace out; `go.work` and `go.work.sum` stay pinned for the day a project adopts one on purpose.
  If the repository git-ignores them, enable the commented `digest_globs = ["go.work", "go.work.sum"]`
  in `config.fragment.toml` so a change refuses `verify`.
- `vendor/`: pinned as a directory, because a vendored file is code. But `GOFLAGS=-mod=readonly` (below)
  turns vendor mode OFF: gate commands build from the module cache, and `go mod verify` checks that cache.
  A vendored project sets `GOFLAGS = "-mod=vendor"` in the `env` tables instead; `go mod verify` then does not
  cover `vendor/`, and only the pin does.
- `GOFLAGS`, `GOPROXY`, `GOTOOLCHAIN`, `GOWORK`, `GOENV`: fixed in the `[ast]`, `[tier1]` and `[tier2]` `env`
  tables (`-mod=readonly`, `off`, `local`, `off`, `off`), and in `[ci]` without `GOPROXY`, because
  `go mod download` needs the network. A caller's values never reach a gate command (`[environment] pass`, G3).
  `GOENV=off` makes Go ignore its env file (`go env -w`: `CC`, `CGO_*`, `GOEXPERIMENT`, `GOFLAGS`,
  `GOMODCACHE`, `GOPATH` and the rest), so that file cannot change a gate build. A setting the build needs from
  that file (for example a moved `GOMODCACHE` or `GOPATH`) moves into `[environment] pass` (the caller's
  value) or into the section's `env` table (a fixed value).
  The migration commands below still set `GOFLAGS=-trimpath=false` by hand, because you run them outside the gate.
- Other packages: Tier 1 names `./harness`, the one package with the contract tests, so the `TestMain` and
  `init` of an ungated package never run in Tier 1. A package that the tested code imports is still loaded;
  it is source (gated by `source_paths`) or a dependency (see "Dependencies").
- `.gitignore`: pinned, so a new rule cannot hide a file from the ignored-files check.

### Dependencies

`[tier1] before = [["go", "mod", "verify"]]` runs before the Tier 1 command, in the same environment and without
the `CRUCIBLE_*` variables. It fails Tier 1 (reason `dependency check failed: go exited 1`) when a module in the
module cache differs from `go.sum`, so an edited cache entry cannot pass. `[ci] setup` (`go mod download`) fills the
cache on a clean runner; `GOPROXY=off` keeps Tier 1 and Tier 2 offline. `vendor/` is pinned, but with
`-mod=readonly` the gate builds from the module cache, not from `vendor/`. A vendored project sets
`-mod=vendor` in the `env` tables, and then `go mod verify` does not cover `vendor/`: only the pin does.
The mutation stage does not run `before`: the dependencies are the same in every copy.

What stays open: the Go toolchain and standard library on the machine, and a `replace` directive that points
outside the module (the `go.mod` pin shows the directive, not the target's content).

Gaps line: `go mod verify` checks the module cache against go.sum before Tier 1; gate commands build from that cache (-mod=readonly), and a project that sets -mod=vendor is covered for vendor/ by the pin only, because go mod verify does not cover vendor/; the Go toolchain, the standard library and the target of a replace directive outside the module are not verified.

### Unit tests

`[tier1] unit_command` (optional) runs the project's own unit tests once per Tier 1 run, after every `before`
command and before the contract command. It gets the scrubbed environment and the `[tier1] env` table, and no
`CRUCIBLE_*` variable; the results directory and the nonce are created only after it has ended, and any process left
in its process group is killed. It shares the Tier 1 `timeout_seconds` budget. A failure stops `verify`,
`preflight` and `ci` at stage `tier1-unit` with the reason `unit tests failed: <argv0> exited <N>`; no token is
issued, and `tier1.unit` in the JSON result holds its `exit_code`, `duration_seconds`, `output_tail`, `output_failures` and `timed_out`.
A repository file named in the command is in the command closure, so it must be pinned; a source directory is the
code under test and stays allowed. The mutation stage does not run it: the contract tests alone must kill each
mutant. A new `unit_command` is a config change, so a human repins; the repin summary shows it as a `unit` line.
Without the key, `preflight` prints one note.

The value in `config.fragment.toml` carries a `TODO(agent)`: write one `./<dir>/...` per `source_paths` directory
that exists. `go test` (Go 1.24.0) behaves as follows:

- A pattern for a missing directory fails: `lstat ./pkg/: no such file or directory`, `FAIL ./pkg/... [setup failed]`, exit 1.
- An existing directory without packages only warns (`matched no packages`) and exits 0 when another pattern matches.
- When all patterns are empty, `go test` prints `no packages to test` and exits 1.

The contract package `./harness` is not named (its `TestMain` needs the results variables), and `./...` is not used
(G7). A pattern that selects no test passes silently.

Gaps line: unit tests run as the same OS user, without the CRUCIBLE_* variables and before the results file and nonce exist, and their process group is killed afterwards; a test can still read a parent process's environment, leave a daemon that escapes the process group, or write outside the repository, and a command that selects zero tests passes silently; a Go pattern for a missing directory fails and an empty directory only warns.

### Environment (`[environment] pass`)

A gate command (the AST gate, Tier 1, Tier 2, the mutation runs, `[ci] setup`) starts from a short
allowlist of the caller's variables: `PATH`, `HOME`, `TMPDIR`, `LANG`, `LC_*`, `TERM`, `USER`,
`LOGNAME`, `SHELL`, `TZ`, `SSL_CERT_FILE`, `SSL_CERT_DIR` and the proxy names (also in lower case).
`GOFLAGS`, `GOWORK`, `GOPROXY`, `GOTOOLCHAIN`, `CGO_*`, `CC` and the rest do not pass, so a caller cannot
change what the gate builds. Go needs nothing more: its caches default to directories under `HOME`, so
`pass` is empty in `config.fragment.toml`. If the project moves a cache, add `GOMODCACHE`, `GOCACHE` or
`GOPATH` to `pass` (`GOENV=off` means a value from `go env -w` no longer applies). A command that needs
another variable may fail loudly or silently change behaviour: add the name to `pass`, or set a
value in the section's `env` table. A human ratifies the list at the repin. `status` shows which `pass`
names are set, never the values.

### Git-ignored files (`[gate] ignored_allow`)

Any git-ignored file in the repository fails the gate (`verify`, `preflight`,
`promote`, `telemetry`, `ci`), because the pins and the token cannot see it and
test tools can load it. Before the FIRST pin you may enable a suggestion that
`config.fragment.toml` carries commented out; name each enabled glob and its
risk in the handoff. The human ratifies it at the PIN prompt, which lists the
files each glob admits. After the pin you never edit the key, and a `preflight`
that names files a gate command writes or runs (the venv, `target/`, `build/`) is a
reason to stop and ask. `.claude/settings.local.json` is the one low-risk
suggestion every fragment carries. A linked worktree of this repository is not a
violation only when it lives under a dot directory at the repository root
(`.worktrees/<name>`, `.claude/worktrees/<name>`), outside source, trusted and
`[ast] paths` directories; anywhere else it is a violation ("move the worktree
under .worktrees/ at the repository root"), because a worktree at `tests/json/` is
imported in place of the standard library and a root-level one is entered by
`go test ./...`. Keep the Tier 1 and Tier 2 commands on explicit files or targets:
a bare collector (pytest with a custom `norecursedirs`, a `make` target that uses
`find`) could still reach `.worktrees/`.

Go needs no admission: the module and build caches live outside the repository
(`go env GOCACHE GOMODCACHE`). A `go test -c` binary, a `*.test` or `*.out` file in
the tree fails the gate; delete it. `go test ./...` skips nested modules and
directories that start with `.` or `_`, so a linked worktree is not loaded.
