#!/usr/bin/env python3
"""Crucible trust anchor: config, gated paths, pins, secret, repin, hooks.

Standard library only. This module is a pinned file. The git hooks do not run
the work-tree copy: they run the snapshot taken at the last human repin
(<git-common-dir>/crucible/checker/), so a commit cannot weaken the checker
that judges it.

Trust state lives OUTSIDE the work tree, in STATE = <git-common-dir>/crucible/
(mode 0700): the signing secret (0600), trust.json, the checker snapshot, the
nonce ledger and the pending marker. See reference/architecture.md section 3.

Ratification (section 4): install-hooks and --repin need a human. They refuse
without an interactive TTY on stdin and stdout, refuse when CI is set, print
every pinned file that changed, and require the human to type
`PIN <first 8 hex of the new pin-set digest>`.
"""

from __future__ import annotations

import datetime as dt
import glob
import hashlib
import json
import os
import re
import secrets
import shutil
import stat
import subprocess
import sys
import time
import tomllib
from pathlib import Path

TRUST_VERSION = 1
STATE_DIRNAME = "crucible"
SECRET_FILE = "secret"
TRUST_FILE = "trust.json"
LEDGER_FILE = "ledger.json"
PENDING_FILE = "pending.json"
CHECKER_DIRNAME = "checker"

CONFIG_REL = ".crucible/config.toml"
TOKEN_REL = ".crucible/attestation.token"
PINS_MIRROR_REL = ".crucible/pins.json"
LOCAL_RUN_PREFIX = ".crucible/run/"

# The files hooks execute (copied into STATE/checker/ at repin). bin/crucible
# and the first two scripts are mandatory; the others are copied if present.
CHECKER_REQUIRED = ("bin/crucible", "scripts/verify_pins.py", "scripts/mint_attestation.py")
CHECKER_OPTIONAL = ("scripts/mcp_server.py", "harness/telemetry_math.py")
CHECKER_FILES = CHECKER_REQUIRED + CHECKER_OPTIONAL
CHECKER_CONFIG_REL = CONFIG_REL  # the pinned config copy sits at checker/<this>
# Kit machinery that is always gated, whatever the config says.
MACHINERY = CHECKER_FILES + (".github/workflows/crucible.yml", PINS_MIRROR_REL)

# Installed hook name -> bin/crucible sub-command. Merge commits run
# pre-merge-commit and post-merge instead of pre-commit and post-commit.
HOOKS = {
    "pre-commit": "hook-pre-commit",
    "pre-merge-commit": "hook-pre-commit",
    "commit-msg": "hook-commit-msg",
    "post-commit": "hook-post-commit",
    "post-merge": "hook-post-commit",
    "pre-push": "hook-pre-push",
}

TTL_RANGE = (60, 86400)
EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
GIT_TIMEOUT_SECONDS = 120

TIER2_DEFAULTS = {
    "samples": 5000, "rounds": 10, "p99_drift_max": 0.02, "p50_drift_max": 0.02,
    "bootstrap_samples": 1000, "ci_level": 0.95, "leak_slope_max": 1024.0,
    "leak_alpha": 0.05, "min_samples": 1000, "timeout_seconds": 1800,
}
DEFAULT_BASELINE = ".crucible/baseline.json"
DEFAULT_PROTECTED_REFS = ["refs/heads/main", "refs/heads/master"]
CI_TIMEOUT_DEFAULT = 900
ENV_NAME_RE = re.compile(r"^[A-Z_][A-Z0-9_]*$")
RESERVED_ENV_PREFIX = "CRUCIBLE_"
PYTHON_TOKEN = "@crucible-python"
ISOLATED_FLAG = "-I"


class KitError(Exception):
    """Base class: a check failed; the message says why."""


class ConfigError(KitError):
    """The config is missing or invalid (exit 2)."""


class TrustError(KitError):
    """Machinery is unpinned or changed, or the anchor is wrong (exit 3)."""


class PathViolation(KitError):
    """A path is not plain printable ASCII, or is not a regular file."""


def say(message: str) -> None:
    print(f"crucible: {message}", file=sys.stderr, flush=True)


def plural(count: int, noun: str) -> str:
    """'1 file', '2 files': a count with its noun in the right number."""
    return f"{count} {noun}" if count == 1 else f"{count} {noun}s"


def expand_command(command: list[str]) -> list[str]:
    """Replace the argv token `@crucible-python` by the checker's own
    interpreter run in isolated mode, so a command never depends on a project
    interpreter."""
    out: list[str] = []
    for element in command:
        if element == PYTHON_TOKEN:
            out += [sys.executable, ISOLATED_FLAG]
        else:
            out.append(element)
    return out


# ---------------------------------------------------------------------------
# Time and canonical JSON
# ---------------------------------------------------------------------------

def utc_iso(timestamp: float) -> str:
    return dt.datetime.fromtimestamp(int(timestamp), dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_iso(text: str) -> float:
    try:
        return dt.datetime.strptime(text, "%Y-%m-%dT%H:%M:%SZ").replace(
            tzinfo=dt.timezone.utc).timestamp()
    except (ValueError, TypeError) as exc:
        raise KitError(f"bad timestamp {text!r}") from exc


def canonical(value) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def sha256_hex(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def files_digest(files: dict[str, str]) -> str:
    """sha256 over the canonical file map (also the pin-set digest)."""
    return sha256_hex(canonical(files))


# ---------------------------------------------------------------------------
# Paths
# ---------------------------------------------------------------------------

def is_plain_ascii(path: str) -> bool:
    """True when every character is printable ASCII (0x20..0x7e). Anything
    else (non-ASCII, control characters, undecodable bytes) could alias a
    gated path on a case- or normalization-insensitive filesystem (APFS folds
    U+017F LONG S to 's')."""
    return all(" " <= ch <= "~" for ch in path)


def non_ascii_violations(paths) -> list[str]:
    return sorted(p for p in set(paths) if not is_plain_ascii(p))


def reject_non_ascii(paths, where: str) -> None:
    bad = non_ascii_violations(paths)
    if bad:
        shown = ", ".join(ascii(p) for p in bad[:6]) + (" ..." if len(bad) > 6 else "")
        raise PathViolation(
            f"non-ASCII path in {where}: {shown}. A non-ASCII path could alias a gated "
            "path on a case- or normalization-insensitive filesystem; rename it to "
            "plain ASCII")


def split_z(out: bytes) -> list[str]:
    """Split NUL-separated git output. surrogateescape keeps undecodable bytes
    visible (as non-ASCII) instead of raising."""
    return [p for p in out.decode("utf-8", errors="surrogateescape").split("\0") if p]


def is_local_state(path: str) -> bool:
    low = path.lower()
    return low == TOKEN_REL or low.startswith(LOCAL_RUN_PREFIX)


def lexical_normalize(path: str) -> str | None:
    """The repo-relative spelling of a path: no empty or '.' parts, '..' parts
    resolved, a trailing '/' kept. './x' and 'x' are the same path. None when
    the path climbs out of the root. An absolute path is returned unchanged."""
    if path.startswith("/"):
        return path
    stack: list[str] = []
    for part in path.split("/"):
        if part in ("", "."):
            continue
        if part == "..":
            if not stack:
                return None
            stack.pop()
            continue
        stack.append(part)
    out = "/".join(stack)
    return out + "/" if path.endswith("/") and out else out


def normalize_entry(entry, where: str) -> str:
    """Validate and normalize one configured repo-relative path (case kept)."""
    if not isinstance(entry, str) or not entry or not is_plain_ascii(entry):
        raise ConfigError(f"{where}: entries must be non-empty plain ASCII strings")
    if entry.startswith("/") or "\\" in entry or ".." in entry.split("/"):
        raise ConfigError(f"{where}: {entry!r} must be a relative path without '..'")
    out = lexical_normalize(entry)
    if not out:
        raise ConfigError(f"{where}: {entry!r} names the repository root")
    return out


def _check_entry(entry, where: str) -> str:
    return normalize_entry(entry, where).lower()


class GateRules:
    """Classification of paths as gated, source or pinned (section 2).

    Matching is ASCII case-insensitive (on APFS, SRC/x.py is src/x.py). An
    entry ending in '/' is a directory prefix; any other entry is one file.
    A path in both source_paths and trusted_paths is pinned (fail closed). A
    path that is not plain ASCII is gated and never source (fail closed).
    """

    def __init__(self, cfg: dict):
        self.source = tuple(_check_entry(e, "gate.source_paths") for e in cfg["gate"]["source_paths"])
        self.trusted = tuple(_check_entry(e, "gate.trusted_paths") for e in cfg["gate"]["trusted_paths"])
        self.machinery = tuple(m.lower() for m in MACHINERY)

    @staticmethod
    def _match(low: str, entries) -> bool:
        return any(low.startswith(e) if e.endswith("/") else low == e for e in entries)

    @staticmethod
    def _fold(path: str) -> str:
        """Lower-cased and normalized, so './src/a' and 'src//a' are 'src/a'."""
        low = path.lower()
        return lexical_normalize(low) or low

    def is_source(self, path: str) -> bool:
        if not is_plain_ascii(path):
            return False
        low = self._fold(path)
        return self._match(low, self.source) and not self._match(low, self.trusted) \
            and low not in self.machinery

    def is_gated(self, path: str) -> bool:
        if not is_plain_ascii(path):
            return True
        low = self._fold(path)
        return (self._match(low, self.source) or self._match(low, self.trusted)
                or low in self.machinery)

    def is_pinned(self, path: str) -> bool:
        return self.is_gated(path) and not self.is_source(path)

    def pin_entry(self, path: str) -> bool:
        """A path that goes into the pins map (the mirror cannot pin itself)."""
        return self.is_pinned(path) and self._fold(path) != PINS_MIRROR_REL


# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

def _section(data: dict, name: str, required: bool) -> dict:
    value = data.get(name)
    if value is None:
        if required:
            raise ConfigError(f"[{name}] is missing from {CONFIG_REL}")
        return {}
    if not isinstance(value, dict):
        raise ConfigError(f"[{name}] must be a table")
    return value


def _string_list(table: dict, key: str, where: str, required: bool = True,
                 default=None) -> list[str]:
    if key not in table:
        if required:
            raise ConfigError(f"{where}.{key} is missing")
        return list(default or [])
    value = table[key]
    if not isinstance(value, list) or not all(isinstance(v, str) for v in value):
        raise ConfigError(f"{where}.{key} must be a list of strings")
    return list(value)


def _number(table: dict, key: str, default, where: str, integer: bool = False,
            minimum: float = 0.0):
    value = table.get(key, default)
    if isinstance(value, bool) or not isinstance(value, (int, float)) \
            or (integer and not isinstance(value, int)) or value < minimum:
        raise ConfigError(f"{where}.{key} must be a number >= {minimum}"
                          + (" (integer)" if integer else ""))
    return value


def _env_table(table: dict, where: str) -> dict[str, str]:
    """The per-section `env` table (architecture 12.1): names matching
    [A-Z_][A-Z0-9_]*, string values, and no CRUCIBLE_* name (the CLI owns those)."""
    value = table.get("env", {})
    if not isinstance(value, dict):
        raise ConfigError(f"{where}.env must be a table of NAME = \"value\"")
    for name, item in value.items():
        if not ENV_NAME_RE.match(name):
            raise ConfigError(f"{where}.env: {name!r} is not a valid variable name "
                              "(upper-case letters, digits and '_', not starting with a digit)")
        if name.startswith(RESERVED_ENV_PREFIX):
            raise ConfigError(f"{where}.env: {name} is reserved; the CLI sets every "
                              f"{RESERVED_ENV_PREFIX}* variable itself")
        if not isinstance(item, str) or "\0" in item:
            raise ConfigError(f"{where}.env.{name} must be a string")
    return dict(value)


def _path_list(table: dict, key: str, where: str) -> list[str]:
    """A list of normalized repo-relative paths (config.toml spelling './x' = 'x')."""
    return [normalize_entry(e, f"{where}.{key}") for e in _string_list(table, key, where, required=False)]


def _setup_commands(table: dict) -> list[list[str]]:
    value = table.get("setup", [])
    if not isinstance(value, list) or not all(
            isinstance(cmd, list) and cmd and all(isinstance(a, str) and a for a in cmd)
            for cmd in value):
        raise ConfigError("ci.setup must be a list of non-empty argv lists of strings")
    return [list(cmd) for cmd in value]


def parse_config(text: str) -> dict:
    """Parse and validate config.toml. Fail closed: a missing or malformed
    config is a ConfigError. An EMPTY command is accepted here and fails the
    stage that runs it (tier1 or tier2), so the failure is a gate failure."""
    try:
        data = tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        raise ConfigError(f"{CONFIG_REL} is not valid TOML: {exc}") from exc
    project = _section(data, "project", True)
    gate = _section(data, "gate", True)
    ast = _section(data, "ast", False)
    tier1 = _section(data, "tier1", True)
    tier2 = _section(data, "tier2", True)
    attestation = _section(data, "attestation", False)
    environment = _section(data, "environment", False)
    ci = _section(data, "ci", False)

    cfg = {
        "project": {"name": str(project.get("name", ""))},
        "gate": {"source_paths": _string_list(gate, "source_paths", "gate"),
                 "trusted_paths": _string_list(gate, "trusted_paths", "gate"),
                 "protected_refs": _string_list(gate, "protected_refs", "gate", required=False,
                                                default=DEFAULT_PROTECTED_REFS)},
        "tier1": {"command": _string_list(tier1, "command", "tier1", required=False),
                  "contract_tests": _path_list(tier1, "contract_tests", "tier1"),
                  "timeout_seconds": _number(tier1, "timeout_seconds", 900, "tier1", minimum=1),
                  "env": _env_table(tier1, "tier1")},
        "ci": {"setup": _setup_commands(ci),
               "timeout_seconds": _number(ci, "timeout_seconds", CI_TIMEOUT_DEFAULT, "ci", minimum=1),
               "env": _env_table(ci, "ci")},
        "attestation": {"ttl_seconds": _number(attestation, "ttl_seconds", 3600,
                                               "attestation", integer=True)},
        "environment": {"digest_globs": _string_list(environment, "digest_globs", "environment",
                                                     required=False)},
    }
    for entry in cfg["environment"]["digest_globs"]:
        _check_entry(entry, "environment.digest_globs")
    for ref in cfg["gate"]["protected_refs"]:
        if not ref.startswith("refs/") or not is_plain_ascii(ref) or " " in ref:
            raise ConfigError(f"gate.protected_refs: {ref!r} must be a full ref name "
                              "such as refs/heads/main")
    if not cfg["gate"]["source_paths"]:
        raise ConfigError("gate.source_paths must not be empty")
    ttl = cfg["attestation"]["ttl_seconds"]
    if not TTL_RANGE[0] <= ttl <= TTL_RANGE[1]:
        raise ConfigError(f"attestation.ttl_seconds must be in {TTL_RANGE}")

    none_reason = ast.get("none_reason", "")
    baseline = ast.get("baseline", DEFAULT_BASELINE)
    if not isinstance(none_reason, str) or not isinstance(baseline, str) or not baseline:
        raise ConfigError("ast.none_reason and ast.baseline must be strings")
    # Every other [ast] key belongs to the AST command, not to this CLI.
    cfg["ast"] = {"command": _string_list(ast, "command", "ast", required=False),
                  "none_reason": none_reason, "env": _env_table(ast, "ast"),
                  "baseline": normalize_entry(baseline, "ast.baseline")}

    tier2_cfg = {**TIER2_DEFAULTS, **tier2}
    tier2_cfg["command"] = _string_list(tier2, "command", "tier2", required=False)
    tier2_cfg["driver_paths"] = _path_list(tier2, "driver_paths", "tier2")
    tier2_cfg["env"] = _env_table(tier2, "tier2")
    for key in ("samples", "rounds", "bootstrap_samples", "min_samples"):
        tier2_cfg[key] = _number(tier2_cfg, key, TIER2_DEFAULTS[key], "tier2",
                                 integer=True, minimum=1)
    tier2_cfg["timeout_seconds"] = _number(tier2_cfg, "timeout_seconds", 1800, "tier2", minimum=1)
    if "smoke_samples" in tier2_cfg:
        tier2_cfg["smoke_samples"] = _number(tier2_cfg, "smoke_samples", 1, "tier2",
                                             integer=True, minimum=1)
    cfg["tier2"] = tier2_cfg
    GateRules(cfg)  # validates every path entry
    return cfg


def load_config(path: Path) -> dict:
    try:
        text = path.read_text()
    except OSError as exc:
        raise ConfigError(f"cannot read {path}: {exc}") from exc
    return parse_config(text)


def named_repo_paths(ws: Path, element: str, directories: bool = True) -> list[str]:
    """Repo-relative paths an argv element names: the path as spelled (with
    './' and '..' normalized) and, for a link, what it resolves to when that is
    inside the repository. A path counts when it is an existing regular file or,
    unless `directories` is false, an existing directory. Empty for an absolute
    path elsewhere (such as a system interpreter) or anything that is not
    lexically inside the repository."""
    if not element or element.startswith("-") or "\0" in element:
        return []
    root = ws.resolve()
    full = Path(os.path.normpath(element if os.path.isabs(element) else ws / element))
    try:
        lexical = full.relative_to(root).as_posix()
    except ValueError:
        try:
            lexical = full.relative_to(ws).as_posix()
        except ValueError:
            return []
    if lexical in ("", "."):
        return []
    found: list[str] = []
    try:
        if not (full.is_file() or (directories and full.is_dir())):
            return []
        found = [lexical]
        resolved = full.resolve().relative_to(root).as_posix()
        if resolved != lexical and resolved not in ("", "."):
            found.append(resolved)
        return found
    except (OSError, ValueError):
        return found


def named_repo_files(ws: Path, element: str) -> list[str]:
    """Like named_repo_paths, for regular files only."""
    return named_repo_paths(ws, element, directories=False)


def _git_ignored(ws: Path, rel: str) -> bool:
    proc = subprocess.run(["git", "-C", str(ws), "check-ignore", "-q", "--", rel],
                          capture_output=True, timeout=GIT_TIMEOUT_SECONDS)
    return proc.returncode == 0


def _dir_of(rel: str) -> str:
    return rel.rpartition("/")[0].lower()


def directory_closure(ws: Path, rules: GateRules, closure_files: set[str],
                      contract_tests: set[str], closure_dirs: set[str] = frozenset()) -> set[str]:
    """Unpinned files that sit beside a closure file: every non-ignored file in
    the same directory as a closure file, everything below the directory of a
    contract test (a test framework loads siblings and parents implicitly) and
    everything below a closure directory (a driver named as a directory)."""
    flat = {_dir_of(rel) for rel in closure_files}
    deep = {_dir_of(rel) for rel in contract_tests} | {d.lower() for d in closure_dirs}
    found: set[str] = set()
    for rel in set(work_tree_listing(ws)) | set(index_listing(ws)):
        if is_local_state(rel) or rules.is_pinned(rel):
            continue
        folder = _dir_of(lexical_normalize(rel) or rel)
        if folder in flat or any(not d or folder == d or folder.startswith(d + "/") for d in deep):
            found.add(rel)
    return found


def tier2_driver_set(ws: Path, cfg: dict, rules: GateRules, ignored=None) -> list[str]:
    """The Tier 2 driver paths (architecture 12.2): `[tier2] driver_paths` plus
    every argv element of `[tier2] command` naming an existing file or
    directory in the repository, normalized, and gated (pinned) ones only. A
    path that is git-ignored is the environment, not a driver (an interpreter).
    `ignored(ws, rel)` decides that; a plain archive of a commit has none."""
    ignored = _git_ignored if ignored is None else ignored
    found: list[str] = []
    candidates = list(cfg["tier2"].get("driver_paths", []))
    for element in expand_command(cfg["tier2"]["command"]):
        candidates += named_repo_paths(ws, element)
    for rel in candidates:
        norm = lexical_normalize(rel) or rel
        if norm not in found and rules.is_gated(norm) and not ignored(ws, norm):
            found.append(norm)
    return found


def command_closure(ws: Path, cfg: dict, rules: GateRules) -> dict:
    """Files the gate commands would run or read that are not pinned: every
    argv element of the ast, tier1 and tier2 commands that names a repository
    file or directory, the tier2 driver_paths, every tier1 contract_tests
    entry, the ast baseline, and (directory closure) every non-ignored file
    beside a closure file or below a closure directory. A command must not run
    unpinned code from the repository. Git-ignored paths named in a command
    (the environment, such as a local interpreter) are exempt and listed under
    "exempt"; their digests are recorded at repin."""
    unpinned: set[str] = set()
    exempt: set[str] = set()
    closure_files: set[str] = set()
    closure_dirs: set[str] = set()
    named: list[str] = []
    for command in (cfg["ast"]["command"], cfg["tier1"]["command"], cfg["tier2"]["command"]):
        for element in expand_command(command):
            named += named_repo_paths(ws, element)
    named += [p for p in cfg["tier2"].get("driver_paths", []) if (ws / p).exists()]
    for rel in named:
        rel = lexical_normalize(rel) or rel
        is_dir = (ws / rel).is_dir()
        if is_dir and rules.is_source(rel + "/"):
            continue  # a source directory handed to a command is the code under test
        if rules.is_pinned(rel + "/" if is_dir else rel):
            closure_files.add(rel)
            if is_dir:
                closure_dirs.add(rel)
            continue
        if _git_ignored(ws, rel):
            exempt.add(rel)
        else:
            closure_files.add(rel)
            if is_dir:
                closure_dirs.add(rel)
            unpinned.add(rel)
    tests = set(cfg["tier1"]["contract_tests"])
    for rel in list(tests) + [cfg["ast"]["baseline"]]:
        closure_files.add(rel)
        if not rules.is_pinned(rel):
            unpinned.add(rel)
    closure_files -= closure_dirs  # a directory is covered below, not as a sibling anchor
    unpinned |= directory_closure(ws, rules, closure_files, tests, closure_dirs)
    return {"unpinned": sorted(unpinned), "exempt": sorted(exempt - unpinned),
            "closure_files": sorted(closure_files | closure_dirs)}


def command_closure_violations(ws: Path, cfg: dict, rules: GateRules) -> list[str]:
    return command_closure(ws, cfg, rules)["unpinned"]


# ---------------------------------------------------------------------------
# git helpers
# ---------------------------------------------------------------------------

def git(ws: Path, *args: str, input_bytes: bytes | None = None, check: bool = True,
        timeout: int = GIT_TIMEOUT_SECONDS) -> bytes:
    """Run `git -C ws args` (argv list, with a timeout). Raises KitError on a
    non-zero exit when check is true."""
    try:
        proc = subprocess.run(["git", "-C", str(ws), *args], input=input_bytes,
                              capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        raise KitError(f"git {' '.join(args[:3])} timed out") from exc
    if check and proc.returncode != 0:
        raise KitError(f"git {' '.join(args[:3])} failed: "
                       f"{proc.stderr.decode(errors='replace').strip()}")
    return proc.stdout


def git_ok(ws: Path, *args: str) -> bool:
    try:
        return subprocess.run(["git", "-C", str(ws), *args], capture_output=True,
                              timeout=GIT_TIMEOUT_SECONDS).returncode == 0
    except subprocess.TimeoutExpired:
        return False


def head_commit(ws: Path) -> str | None:
    proc = subprocess.run(["git", "-C", str(ws), "rev-parse", "--verify", "-q", "HEAD^{commit}"],
                          capture_output=True, timeout=GIT_TIMEOUT_SECONDS)
    return proc.stdout.decode().strip() or None if proc.returncode == 0 else None


def repo_toplevel(cwd: Path) -> Path:
    proc = subprocess.run(["git", "-C", str(cwd), "rev-parse", "--show-toplevel"],
                          capture_output=True, text=True, timeout=GIT_TIMEOUT_SECONDS)
    if proc.returncode != 0:
        raise KitError("not inside a git work tree")
    return Path(proc.stdout.strip()).resolve()


def state_path(ws: Path) -> Path:
    common = Path(git(ws, "rev-parse", "--git-common-dir").decode().strip())
    return ((common if common.is_absolute() else ws / common) / STATE_DIRNAME).resolve()


def state_dir(ws: Path, create: bool = True) -> Path:
    """STATE, mode 0700 (forced after creation, whatever the umask)."""
    path = state_path(ws)
    if create:
        path.mkdir(mode=0o700, exist_ok=True)
        if stat.S_IMODE(path.stat().st_mode) != 0o700:
            path.chmod(0o700)
    return path


def read_nofollow(path: Path) -> bytes:
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except OSError as exc:
        raise PathViolation(f"{path}: cannot open without following symlinks: {exc}") from exc
    with os.fdopen(fd, "rb") as handle:
        if not stat.S_ISREG(os.fstat(handle.fileno()).st_mode):
            raise PathViolation(f"{path}: not a regular file")
        return handle.read()


def write_private(path: Path, data: bytes, mode: int = 0o600) -> None:
    """Atomic write, never following a symlink at the target."""
    tmp = path.with_name(f".{path.name}.{secrets.token_hex(4)}.tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, "wb") as handle:
        handle.write(data)
    os.chmod(tmp, mode)
    os.replace(tmp, path)


def work_tree_listing(ws: Path) -> list[str]:
    """Every path in the work tree: tracked and untracked, not ignored."""
    return split_z(git(ws, "ls-files", "-z", "-co", "--exclude-standard"))


def index_listing(ws: Path) -> list[str]:
    return split_z(git(ws, "ls-files", "-z"))


def working_tree(ws: Path, rules: GateRules) -> dict[str, str]:
    """sha256 of each gated work-tree file. Any non-ASCII path anywhere in the
    work tree or index is a violation. Symlinks and non-regular files are
    refused. Local state (token, run dir) is not part of the tree."""
    paths = set(work_tree_listing(ws)) | set(index_listing(ws))
    reject_non_ascii(paths, "the index or work tree")
    tree: dict[str, str] = {}
    for rel in sorted(paths):
        if not rules.is_gated(rel) or is_local_state(rel):
            continue
        try:
            info = (ws / rel).lstat()
        except FileNotFoundError:
            continue  # tracked but deleted in the work tree
        if not stat.S_ISREG(info.st_mode):
            raise PathViolation(f"{rel}: gated path is not a regular file")
        tree[rel] = sha256_hex(read_nofollow(ws / rel))
    return tree


def staged_changes(ws: Path) -> list[str]:
    """Paths whose staged content differs from HEAD (adds, edits, deletes)."""
    base = head_commit(ws) or EMPTY_TREE
    return split_z(git(ws, "diff", "--cached", "--name-only", "--no-renames", "-z", base))


def staged_tree(ws: Path, rules: GateRules) -> dict[str, str]:
    """sha256 of each gated file as staged in the index (all of them, local
    state included, so an accidental commit of the token shows up)."""
    records = []
    for rec in split_z(git(ws, "ls-files", "-z", "-s")):
        meta, rel = rec.split("\t", 1)
        records.append((meta, rel))
    reject_non_ascii((rel for _, rel in records), "the index")
    entries = []
    for meta, rel in records:
        if not rules.is_gated(rel):
            continue
        mode, oid, stage_no = meta.split()
        if stage_no != "0":
            raise PathViolation(f"{rel}: unresolved merge conflict in the index")
        if mode not in ("100644", "100755"):
            raise PathViolation(f"{rel}: gated path has mode {mode} (regular files only)")
        entries.append((rel, oid))
    if not entries:
        return {}
    batch = git(ws, "cat-file", "--batch",
                input_bytes="".join(f"{oid}\n" for _, oid in entries).encode())
    tree, pos = {}, 0
    for rel, oid in entries:
        newline = batch.index(b"\n", pos)
        header = batch[pos:newline].decode().split()
        if len(header) != 3 or header[0] != oid or header[1] != "blob":
            raise PathViolation(f"{rel}: unexpected object in the index")
        size = int(header[2])
        data = batch[newline + 1:newline + 1 + size]
        if len(data) != size:
            raise PathViolation(f"{rel}: short read from git cat-file")
        pos = newline + 1 + size + 1
        tree[rel] = sha256_hex(data)
    return tree


def diff_maps(old: dict[str, str], new: dict[str, str]) -> dict[str, list[str]]:
    return {
        "modified": sorted(p for p in old.keys() & new.keys() if old[p] != new[p]),
        "added": sorted(new.keys() - old.keys()),
        "removed": sorted(old.keys() - new.keys()),
    }


def describe_diff(label: str, delta: dict[str, list[str]]) -> list[str]:
    return [f"  {label}: {kind:8} {path}" for kind in ("modified", "added", "removed")
            for path in delta[kind]]


# ---------------------------------------------------------------------------
# Secret and trust anchor
# ---------------------------------------------------------------------------

def key_id(secret: bytes) -> str:
    return sha256_hex(secret)[:16]


def create_secret(state: Path) -> None:
    path = state / SECRET_FILE
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as handle:
        handle.write(secrets.token_hex(32) + "\n")
    os.chmod(path, 0o600)


def load_secret(state: Path) -> bytes:
    """Read the secret. It is never printed and never read from the environment."""
    path = state / SECRET_FILE
    try:
        info = path.lstat()
    except FileNotFoundError as exc:
        raise TrustError("no signing secret; a human must run python3 bin/crucible install-hooks") from exc
    if not stat.S_ISREG(info.st_mode):
        raise TrustError("the signing secret is not a regular file")
    if info.st_mode & 0o077:
        raise TrustError("the signing secret is readable by other users; chmod 600 it")
    value = read_nofollow(path).decode().strip()
    if len(value) < 32:
        raise TrustError("the signing secret is too short")
    return value.encode()


def load_trust(state: Path) -> dict | None:
    path = state / TRUST_FILE
    if not path.is_file():
        return None
    try:
        trust = json.loads(path.read_text())
        if (trust.get("version") != TRUST_VERSION or not isinstance(trust.get("kid"), str)
                or not isinstance(trust.get("repo_root"), str)
                or not isinstance(trust.get("pins"), dict)
                or not isinstance(trust.get("checker_digest"), str)):
            raise ValueError("missing or malformed fields")
        return trust
    except (OSError, ValueError, AttributeError) as exc:
        raise TrustError(f"trust anchor {path} is unreadable: {exc}") from exc


def checker_dir(state: Path) -> Path:
    return state / CHECKER_DIRNAME


def checker_snapshot_digest(state: Path) -> str:
    """sha256 over the canonical map of every file in the checker snapshot."""
    root = checker_dir(state)
    files = {}
    if root.is_dir():
        for path in sorted(root.rglob("*")):
            if path.is_symlink():
                raise TrustError(f"checker snapshot holds a symlink: {path}")
            if path.is_file():
                files[path.relative_to(root).as_posix()] = sha256_hex(path.read_bytes())
    return files_digest(files)


def snapshot_config(state: Path) -> dict:
    """The pinned config, from the checker snapshot (never the work tree)."""
    return load_config(checker_dir(state) / CHECKER_CONFIG_REL)


def require_anchor(ws: Path, *, check_checker: bool = True) -> tuple[Path, dict, bytes, dict, GateRules]:
    """The anchor the checks rely on: (state, trust, secret, pinned config,
    rules). TrustError unless the anchor exists, the work tree is the pinned
    root, the key is the pinned key, and the checker snapshot is intact."""
    if "GIT_WORK_TREE" in os.environ:
        raise TrustError("GIT_WORK_TREE / --work-tree redirects are not allowed")
    state = state_dir(ws, create=False)
    trust = load_trust(state) if state.is_dir() else None
    if trust is None:
        raise TrustError("no trust anchor pinned; a human must run python3 bin/crucible install-hooks")
    if str(ws.resolve()) != trust["repo_root"]:
        raise TrustError(f"work tree {ws} is not the pinned repository root {trust['repo_root']}")
    secret = load_secret(state)
    if key_id(secret) != trust["kid"]:
        raise TrustError("the signing secret is not the pinned key (kid mismatch)")
    if check_checker and checker_snapshot_digest(state) != trust["checker_digest"]:
        raise TrustError("the checker snapshot differs from the pinned digest; "
                         "a human must run python3 bin/crucible install-hooks --repin")
    try:
        cfg = snapshot_config(state)
    except ConfigError as exc:
        raise TrustError(f"pinned config snapshot is unusable: {exc}") from exc
    return state, trust, secret, cfg, GateRules(cfg)


def pin_differences(trust: dict, tree: dict[str, str], rules: GateRules) -> dict[str, list[str]]:
    """Differences between the pinned set in trust.json and `tree` (any map of
    gated paths). The mirror file must match its pinned digest exactly."""
    current = {p: h for p, h in tree.items() if rules.pin_entry(p)}
    delta = diff_maps(trust["pins"], current)
    mirror = tree.get(PINS_MIRROR_REL)
    if mirror is None:
        delta["removed"] = sorted(delta["removed"] + [PINS_MIRROR_REL])
    elif mirror != trust.get("mirror_sha256"):
        delta["modified"] = sorted(delta["modified"] + [PINS_MIRROR_REL])
    return delta


def require_pins(trust: dict, tree: dict[str, str], rules: GateRules, label: str) -> None:
    delta = pin_differences(trust, tree, rules)
    if any(delta.values()):
        for line in describe_diff(f"{label} vs pinned", delta):
            say(line)
        raise TrustError(f"{label} trusted inputs differ from the pinned set; a human must "
                         "review them and run python3 bin/crucible install-hooks --repin")


def environment_digests(ws: Path, cfg: dict, rules: GateRules) -> dict[str, str]:
    """sha256 of the content (at the resolved realpath) of every closure-exempt
    git-ignored file the commands name, and of every file matching
    [environment] digest_globs. These files are the environment the commands
    run in (interpreter, site hooks); a change must reach a human."""
    rels = set(command_closure(ws, cfg, rules)["exempt"])
    for pattern in cfg["environment"]["digest_globs"]:
        for match in glob.glob(pattern, root_dir=str(ws), recursive=True, include_hidden=True):
            rel = match.replace(os.sep, "/")
            if (ws / rel).is_file():
                rels.add(rel)
    reject_non_ascii(rels, "the environment digest set")
    digests: dict[str, str] = {}
    for rel in sorted(rels):
        try:
            digests[rel] = sha256_hex((ws / rel).resolve(strict=True).read_bytes())
        except OSError as exc:
            raise PathViolation(f"{rel}: cannot read environment file: {exc}") from exc
    return digests


def require_environment(ws: Path, trust: dict, cfg: dict, rules: GateRules) -> None:
    """TrustError (exit 3) when the environment differs from the digests
    recorded at repin: modified, added or removed paths are listed."""
    delta = diff_maps(trust.get("environment") or {}, environment_digests(ws, cfg, rules))
    if any(delta.values()):
        for line in describe_diff("environment vs pinned", delta):
            say(line)
        listed = ", ".join(f"{kind} {path}" for kind in ("modified", "added", "removed")
                           for path in delta[kind])
        raise TrustError(f"the environment the commands run in changed ({listed}); a human must "
                         "review it and run python3 bin/crucible install-hooks --repin")


def committed_genesis(ws: Path) -> str | None:
    """The genesis recorded in the mirror committed at HEAD, when it is a commit
    that is an ancestor of HEAD; otherwise None (architecture 12.5). A clone
    that pins later keeps the repository's genesis, so every clone writes the
    same mirror bytes."""
    head = head_commit(ws)
    if head is None:
        return None
    try:
        data = json.loads(git(ws, "show", f"{head}:{PINS_MIRROR_REL}", check=True))
        genesis = data.get("genesis") if isinstance(data, dict) else None
    except (KitError, ValueError, UnicodeDecodeError):
        return None
    if not isinstance(genesis, str) or not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", genesis):
        return None
    if not git_ok(ws, "merge-base", "--is-ancestor", f"{genesis}^{{commit}}", head):
        return None
    return genesis


def mirror_text(trust_like: dict) -> str:
    return json.dumps({"version": TRUST_VERSION, "genesis": trust_like["genesis"],
                       "pins": trust_like["pins"]}, indent=2, sort_keys=True) + "\n"


# ---------------------------------------------------------------------------
# Hooks
# ---------------------------------------------------------------------------

def _sh_dquote(text: str) -> str:
    return '"' + re.sub(r'(["\\$`])', r"\\\1", text) + '"'


def current_interpreter() -> str:
    """The absolute realpath of the interpreter running this code."""
    return os.path.realpath(sys.executable)


def hook_script(state: Path, subcommand: str, interpreter: str | None = None) -> str:
    """A 3-line stub that execs the absolute interpreter recorded at repin, in
    isolated mode, on the pinned checker snapshot (never PATH or the environment)."""
    checker = checker_dir(state) / "bin" / "crucible"
    return ("#!/bin/sh\n# Installed by crucible install-hooks. Runs the pinned checker snapshot.\n"
            f'exec {_sh_dquote(interpreter or current_interpreter())} {ISOLATED_FLAG} '
            f'{_sh_dquote(str(checker))} {subcommand} "$@"\n')


def hooks_directory(ws: Path) -> Path:
    path = Path(git(ws, "rev-parse", "--git-path", "hooks").decode().strip())
    return path if path.is_absolute() else ws / path


def hook_states(ws: Path, state: Path) -> dict[str, str]:
    directory = hooks_directory(ws)
    try:
        interpreter = (load_trust(state) or {}).get("interpreter")
    except TrustError:
        interpreter = None
    result = {}
    for name, sub in HOOKS.items():
        path = directory / name
        if not path.is_file():
            result[name] = "MISSING"
        elif path.read_text(errors="replace") != hook_script(state, sub, interpreter) \
                or not os.access(path, os.X_OK):
            result[name] = "DIFFERENT"
        else:
            result[name] = "OK"
    return result


def write_hooks(ws: Path, state: Path, interpreter: str | None = None) -> list[str]:
    directory = hooks_directory(ws)
    directory.mkdir(parents=True, exist_ok=True)
    installed = []
    for name, sub in HOOKS.items():
        path = directory / name
        script = hook_script(state, sub, interpreter)
        if path.exists() or path.is_symlink():
            if path.is_file() and path.read_text(errors="replace") == script:
                path.chmod(0o755)
                continue
            backup = path.with_name(f"{name}.pre-crucible.{secrets.token_hex(3)}")
            path.rename(backup)
            say(f"existing {name} hook moved to {backup.name}")
        path.write_text(script)
        path.chmod(0o755)
        installed.append(name)
    return installed


# ---------------------------------------------------------------------------
# Checker snapshot
# ---------------------------------------------------------------------------

def take_checker_snapshot(ws: Path, state: Path, config_bytes: bytes,
                          expected: dict[str, str]) -> str:
    """Copy the checker files and the pinned config into STATE/checker/.
    Every copied file must hash to its pin (guards against a change between
    pin computation and copy). Returns the snapshot digest."""
    target = checker_dir(state)
    staging = state / f".checker-{secrets.token_hex(4)}"
    staging.mkdir(mode=0o700)
    try:
        for rel in CHECKER_FILES:
            source = ws / rel
            if not source.exists() and not source.is_symlink():
                if rel in CHECKER_REQUIRED:
                    raise ConfigError(f"kit file {rel} is missing from the work tree")
                continue
            data = read_nofollow(source)
            if rel in expected and sha256_hex(data) != expected[rel]:
                raise TrustError(f"{rel} changed while the checker snapshot was taken")
            out = staging / rel
            out.parent.mkdir(parents=True, exist_ok=True)
            out.write_bytes(data)
            out.chmod(0o755 if rel == "bin/crucible" else 0o644)
        cfg_out = staging / CHECKER_CONFIG_REL
        cfg_out.parent.mkdir(parents=True, exist_ok=True)
        cfg_out.write_bytes(config_bytes)
        cfg_out.chmod(0o644)
        if target.exists():
            shutil.rmtree(target)
        staging.rename(target)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    return checker_snapshot_digest(state)


# ---------------------------------------------------------------------------
# Human ratification
# ---------------------------------------------------------------------------

def require_human_terminal() -> None:
    """Refuse unless a human is at an interactive terminal: stdin AND stdout
    must be TTYs and CI must be unset. An agent shell has no TTY."""
    if os.environ.get("CI"):
        raise KitError("CI is set: pinning needs a human at an interactive terminal")
    if not (sys.stdin.isatty() and sys.stdout.isatty()):
        raise KitError("this step needs a human at an interactive terminal "
                       "(stdin and stdout must both be TTYs)")


def install_hooks(ws: Path, repin: bool = False) -> dict:
    """The human ratification path (section 4). Returns a summary dict."""
    require_human_terminal()
    ws = ws.resolve()
    if "GIT_WORK_TREE" in os.environ:
        raise KitError("GIT_WORK_TREE is set; run from the repository root without it")
    configured = subprocess.run(["git", "-C", str(ws), "config", "core.hooksPath"],
                                capture_output=True, text=True,
                                timeout=GIT_TIMEOUT_SECONDS).stdout.strip()
    if configured:
        raise KitError(f"core.hooksPath is set to {configured!r}; unset it (hook redirects "
                       "are not allowed)")
    if repo_toplevel(ws) != ws:
        raise KitError(f"run install-hooks from the repository root {repo_toplevel(ws)}")

    config_bytes = read_nofollow(ws / CONFIG_REL) if (ws / CONFIG_REL).exists() else b""
    if not config_bytes:
        raise ConfigError(f"{CONFIG_REL} is missing")
    cfg = parse_config(config_bytes.decode())
    rules = GateRules(cfg)
    tree = working_tree(ws, rules)
    pins = {p: h for p, h in tree.items() if rules.pin_entry(p)}
    pin_digest = files_digest(pins)
    environment = environment_digests(ws, cfg, rules)

    state = state_dir(ws)
    current = load_trust(state)
    delta = diff_maps(current["pins"], pins) if current else \
        {"modified": [], "added": sorted(pins), "removed": []}
    env_delta = diff_maps((current or {}).get("environment") or {}, environment)
    if current:
        stale_checker = checker_snapshot_digest(state) != current["checker_digest"]
    else:
        stale_checker = False
    changed = current is None or any(delta.values()) or any(env_delta.values()) or stale_checker
    summary = {"pinned": False, "pin_digest": pin_digest, "differences": delta,
               "environment_differences": env_delta,
               "hooks_installed": [], "kid": None, "genesis": (current or {}).get("genesis")}

    if changed and (current is None or repin):
        old = current["pins"] if current else {}
        for kind in ("modified", "added", "removed"):
            for path in delta[kind]:
                say(f"  pin {kind:8} {path}  old={old.get(path, '-')[:12]}  "
                    f"new={pins.get(path, '-')[:12]}")
        old_env = (current or {}).get("environment") or {}
        for kind in ("modified", "added", "removed"):
            for path in env_delta[kind]:
                say(f"  environment {kind:8} {path}  old={old_env.get(path, '-')[:12]}  "
                    f"new={environment.get(path, '-')[:12]}")
        if stale_checker:
            say("  the stored checker snapshot is damaged and will be replaced")
        word = f"PIN {pin_digest[:8]}"
        answer = input(f"Pin {plural(len(pins), 'trusted input')}? Type '{word}' to confirm: ")
        if answer.strip() != word:
            raise KitError("pin not confirmed; nothing changed")
        if not (state / SECRET_FILE).exists():
            create_secret(state)
        secret = load_secret(state)
        genesis = (current or {}).get("genesis") or committed_genesis(ws) or head_commit(ws)
        digest = take_checker_snapshot(ws, state, config_bytes, pins)
        trust = {"version": TRUST_VERSION, "kid": key_id(secret), "repo_root": str(ws),
                 "pinned_at": utc_iso(time.time()), "genesis": genesis,
                 "pins": pins, "checker_digest": digest, "environment": environment,
                 "interpreter": current_interpreter()}
        mirror = mirror_text(trust)
        trust["mirror_sha256"] = sha256_hex(mirror.encode())
        (ws / PINS_MIRROR_REL).parent.mkdir(parents=True, exist_ok=True)
        write_private(ws / PINS_MIRROR_REL, mirror.encode(), 0o644)
        write_private(state / TRUST_FILE, (json.dumps(trust, indent=2, sort_keys=True) + "\n").encode())
        summary.update(pinned=True, kid=trust["kid"], genesis=genesis)
        say(f"pinned {plural(len(pins), 'trusted input')} (digest {pin_digest[:16]}), "
            f"{plural(len(environment), 'environment file')}, kid {trust['kid']}, "
            f"genesis {(genesis or 'none')[:12]}")
    elif changed:
        for line in describe_diff("work tree vs pinned", delta):
            say(line)
        for line in describe_diff("environment vs pinned", env_delta):
            say(line)
        say("trusted inputs differ from the pinned set; the gate rejects them until a human "
            "runs python3 bin/crucible install-hooks --repin")
        summary["kid"] = current["kid"]
    else:
        summary["kid"] = current["kid"]
        say("pins are up to date")
    recorded = (load_trust(state) or {}).get("interpreter")
    summary["hooks_installed"] = write_hooks(ws, state, recorded or current_interpreter())
    return summary


if __name__ == "__main__":
    sys.exit("verify_pins.py is a library; run python3 bin/crucible")
