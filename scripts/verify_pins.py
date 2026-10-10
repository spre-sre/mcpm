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

import collections
import datetime as dt
import fnmatch
import functools
import glob
import hashlib
import json
import os
import posixpath
import re
import secrets
import shlex
import shutil
import stat
import subprocess
import sys
import time
import tomllib
import unicodedata
from pathlib import Path, PurePosixPath
from typing import NamedTuple

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
GIT_TIMEOUT_SECONDS = 120

# Git runs in a clean environment with a hardened command line (architecture
# 16.1). Every GIT_* variable is dropped except these, then two switches are
# forced on. Names are matched ASCII case-insensitively (Windows).
GIT_KEEP_ENV = ("GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM")
GIT_FORCED_ENV = {"GIT_NO_REPLACE_OBJECTS": "1", "GIT_NO_LAZY_FETCH": "1"}
GIT_LOCATION_VARS = ("GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
                     "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE")
GIT_SAFE_CONFIG = (("core.commitGraph", "false"), ("core.fsmonitor", "false"),
                   ("core.untrackedCache", "false"), ("log.showSignature", "false"))
GIT_CONFIG_INJECTION_RE = re.compile(r"^GIT_CONFIG_(COUNT|PARAMETERS|KEY_\d+|VALUE_\d+)$",
                                     re.IGNORECASE)
COMMIT_IDENTITY_PREFIXES = ("GIT_AUTHOR_", "GIT_COMMITTER_")

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


def _glob_segment_regex(segment: str) -> str:
    """The regex of one glob segment: `*` is any run of characters except '/', `?` is one."""
    return "".join("[^/]*" if ch == "*" else "[^/]" if ch == "?" else re.escape(ch)
                   for ch in segment)


def glob_regex(pattern: str) -> re.Pattern:
    """Compile an `ignored_allow` glob (architecture 17.3). Segments split on '/'.
    A segment `**` matches zero or more whole segments (at the end: one or more, so
    `dir/**` names what is below `dir`, not `dir`); `*` and `?` stay inside one segment.
    ASCII case-insensitive: re.ASCII keeps U+017F and U+212A from folding to 's' and 'k'.
    Match with fullmatch."""
    parts = pattern.split("/")
    last = len(parts) - 1
    out = ""
    for index, part in enumerate(parts):
        if part == "**":
            out += r"[^/]+(?:/[^/]+)*" if index == last else r"(?:[^/]+/)*"
        else:
            out += _glob_segment_regex(part) + ("" if index == last else "/")
    return re.compile(out, re.ASCII | re.IGNORECASE)


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
        self.allow = tuple((entry, glob_regex(entry)) for entry in cfg["gate"].get("ignored_allow", []))

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

    @staticmethod
    def ascii_lower(text: str) -> str:
        """Lower-case ASCII letters only: no Unicode case folding (U+017F stays itself)."""
        return "".join(ch.lower() if ch.isascii() else ch for ch in text)

    @staticmethod
    def unicode_fold(text: str) -> str:
        """NFD, then case-folded: the form in which a case-insensitive file system
        (APFS) can equate U+212A with 'k' and U+017F with 's'."""
        return unicodedata.normalize("NFD", text).casefold()

    def _gated_form(self, low: str) -> bool:
        """`low` equals a gated file or directory entry, lies below a directory entry, or is a
        proper directory prefix of any entry (`scripts` when `scripts/verify_pins.py` is machinery)."""
        entries = self.source + self.trusted + self.machinery
        return (self._match(low, entries)
                or any(e.endswith("/") and low == e[:-1] for e in entries)
                or bool(low) and any(e.startswith(low.rstrip("/") + "/") for e in entries))

    def gated_by_config(self, path: str) -> bool:
        """True when `path` is a source or trusted entry (or below a directory entry,
        or the directory itself without its '/') or is machinery. Two forms are tried:
        the ASCII-lower-cased path, and the Unicode-folded path (spec 14.7), because on a
        case-insensitive file system 'go.work' may be stored as 'go.work' with U+212A.
        A non-ASCII name that folds onto no gated entry is not gated (spec 14.6)."""
        for low in (self.ascii_lower(path), self.unicode_fold(path)):
            low = lexical_normalize(low) or low
            if self._gated_form(low):
                return True
        return False

    def allowing_glob(self, path: str) -> str | None:
        """The first `[gate] ignored_allow` glob that admits the git-ignored `path`, or
        None. An entry that ends in '/' is a nested repository, and a path under a gated
        entry is judged by the pins: no glob admits either. A non-ASCII name may be admitted
        only outside every gated directory (spec 14.6): an honest environment directory can
        hold one."""
        if path.endswith("/") or self.gated_by_config(path):
            return None
        return next((entry for entry, regex in self.allow if regex.fullmatch(path)), None)

    def ignored_allowed(self, path: str) -> bool:
        return self.allowing_glob(path) is not None


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


def _lenient_paths(table: dict, key: str) -> list[str]:
    """The string entries of a list that an engine validates ([ast] paths), normalized; this
    reader only needs them to place a linked worktree, so anything else is ignored here."""
    value = table.get(key)
    if not isinstance(value, list):
        return []
    normalized = [(e, lexical_normalize(e)) for e in value if isinstance(e, str)]
    return [e if n is None else n for e, n in normalized]


def _glob_list(table: dict, key: str, where: str) -> list[str]:
    """`[gate] ignored_allow`: normalized like any path entry, plus two glob rules. A
    trailing '/' is refused (a glob names files; `dir/**` names what is below `dir`),
    and `**` must be a whole segment."""
    patterns = []
    for entry in _string_list(table, key, where, required=False):
        pattern = normalize_entry(entry, f"{where}.{key}")
        if pattern.endswith("/"):
            raise ConfigError(f"{where}.{key}: {entry!r} ends with '/'; write {pattern}** for "
                              "the files below a directory")
        if any("**" in part and part != "**" for part in pattern.split("/")):
            raise ConfigError(f"{where}.{key}: {entry!r}: '**' must be a whole path segment")
        patterns.append(pattern)
    return patterns


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
                                                default=DEFAULT_PROTECTED_REFS),
                 "ignored_allow": _glob_list(gate, "ignored_allow", "gate")},
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
                  "baseline": normalize_entry(baseline, "ast.baseline"),
                  "paths": _lenient_paths(ast, "paths")}

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
    return run_git(ws, ["check-ignore", "-q", "--", rel]).returncode == 0


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


def _is_environment(ws: Path, rules: GateRules, rel: str) -> bool:
    """True for a git-ignored repository path that a pinned `ignored_allow` glob admits:
    the environment (an interpreter), exempt from the closure and digested at repin. An
    ignored path no glob admits is an ordinary unpinned closure file (architecture 17.4)."""
    return rules.ignored_allowed(rel) and _git_ignored(ws, rel)


def command_closure(ws: Path, cfg: dict, rules: GateRules) -> dict:
    """Files the gate commands would run or read that are not pinned: every
    argv element of the ast, tier1 and tier2 commands that names a repository
    file or directory, the tier2 driver_paths, every tier1 contract_tests
    entry, the ast baseline, and (directory closure) every non-ignored file
    beside a closure file or below a closure directory. A command must not run
    unpinned code from the repository. A git-ignored path named in a command
    (the environment, such as a local interpreter) is exempt only when a
    `[gate] ignored_allow` glob admits it; it is then listed under "exempt" and its
    digest is recorded at repin. Any other ignored path counts as unpinned."""
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
        if _is_environment(ws, rules, rel):
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

_RECORDED_GIT: dict[str, str] = {}  # realpath of a work tree -> the git recorded in its trust.json


def is_git_variable(name: str) -> bool:
    """True for any spelling of a GIT_* name (Windows names are case-insensitive)."""
    return name[:4].upper() == "GIT_"


def clean_git_env(environ=None) -> dict[str, str]:
    """The environment every trusted git process gets: `environ` (default
    os.environ) without any GIT_* variable except GIT_KEEP_ENV, plus
    GIT_FORCED_ENV (A1.1)."""
    source = os.environ if environ is None else environ
    env = {name: value for name, value in source.items()
           if not is_git_variable(name) or name in GIT_KEEP_ENV}
    env.update(GIT_FORCED_ENV)
    return env


_PATH_GIT_CACHE: dict[str, str] = {}  # PATH value -> path_git() result


def _spawn(argv: list[str], input_bytes: bytes | None, timeout: int,
           env: dict[str, str]) -> subprocess.CompletedProcess:
    """The one subprocess.run of this file: every git process starts here."""
    return subprocess.run(argv, input=input_bytes, capture_output=True, timeout=timeout, env=env)


def path_git() -> str:
    """The absolute path of the git to record and run. Prefers the real binary
    in `git --exec-path` over the `git` found on PATH: on macOS, /usr/bin/git is
    an xcrun shim whose target DEVELOPER_DIR can redirect, so the shim is not a
    stable thing to pin. Falls back to the realpath of the git on PATH."""
    found = shutil.which("git")
    if found is None:
        raise KitError("git was not found on PATH")
    key = os.environ.get("PATH", "")
    if key not in _PATH_GIT_CACHE:
        _PATH_GIT_CACHE[key] = _resolve_path_git(found)
    return _PATH_GIT_CACHE[key]


def _resolve_path_git(found: str) -> str:
    try:
        proc = _spawn([found, "--exec-path"], None, GIT_TIMEOUT_SECONDS, clean_git_env())
        candidate = os.path.join(proc.stdout.decode().strip(), "git")
        if proc.returncode == 0 and os.path.isfile(candidate) and os.access(candidate, os.X_OK):
            return os.path.realpath(candidate)
    except (OSError, subprocess.SubprocessError):
        pass
    return os.path.realpath(found)


def git_program(ws: Path) -> str:
    """The git to run for `ws`: the one recorded in trust.json once the anchor
    has been bound (bind_recorded_git), otherwise the one on PATH."""
    return _RECORDED_GIT.get(os.path.realpath(ws)) or path_git()


def bind_recorded_git(ws: Path) -> str | None:
    """Make later git calls for `ws` run the git recorded in trust.json (A1.2).
    No trust.json, or one from kit 2.0.0 without a `git` field: PATH stays in
    use. A recorded git that is gone or not executable is a TrustError."""
    key = os.path.realpath(ws)
    state = state_path(ws)
    trust = load_trust(state) if state.is_dir() else None
    recorded = (trust or {}).get("git")
    _RECORDED_GIT.pop(key, None)
    if recorded is None:
        return None
    if not (isinstance(recorded, str) and os.path.isabs(recorded)
            and os.path.isfile(recorded) and os.access(recorded, os.X_OK)):
        raise TrustError(f"the git recorded at the last repin ({recorded!r}) is missing or not "
                         "executable (an OS or package update moved it?); a human must run "
                         "python3 bin/crucible install-hooks --repin")
    _RECORDED_GIT[key] = recorded
    return recorded


def git_binding_report(ws: Path) -> dict:
    """For `status`: the git recorded in trust.json and whether it still runs.
    Never raises: a missing recorded git, an unreadable trust.json or a state
    git cannot locate is reported with its own reason, not fatal. `note` says
    what a human should do when the record is absent."""
    report = {"recorded": None, "usable": True, "error": None, "note": None}
    try:
        state = state_path(ws)
        trust = load_trust(state) if state.is_dir() else None
    except KitError as exc:
        unbind_recorded_git(ws)  # unknown record: no stale binding, PATH applies
        report.update(usable=None, error=str(exc))
        return report
    report["recorded"] = (trust or {}).get("git")
    if trust is not None and report["recorded"] is None:
        report["note"] = "no recorded git; repin to record it (the git on PATH is used)"
    try:
        bind_recorded_git(ws)
    except KitError as exc:
        report.update(usable=False, error=str(exc))
    return report


def unbind_recorded_git(ws: Path) -> None:
    """Forget the recorded git for `ws` (install-hooks records the one on PATH)."""
    _RECORDED_GIT.pop(os.path.realpath(ws), None)


def commit_identity_env() -> dict[str, str]:
    """GIT_AUTHOR_* and GIT_COMMITTER_* from the caller: the only GIT_* names a
    `git commit` of ours takes from the parent environment (A5.2)."""
    return {name: value for name, value in os.environ.items()
            if name.upper().startswith(COMMIT_IDENTITY_PREFIXES)}


def gate_environment(environ, ws: Path | None = None) -> dict[str, str]:
    """The environment a gate command (AST, Tier 1, Tier 2, CI setup) gets
    (A1.4, with ruling P3): `environ` without the git location variables and
    without any inherited GIT_CONFIG_COUNT / _KEY_n / _VALUE_n / _PARAMETERS,
    plus the four safe settings as GIT_CONFIG_COUNT/KEY_n/VALUE_n and
    GIT_NO_LAZY_FETCH=1. There is deliberately no core.hooksPath pin: gate
    commands are agent code, the pin has no security value for them, and it
    broke test suites that commit in scratch repositories. A toolchain that
    calls git (VCS stamping, build scripts) sees the same view as the trust
    layer. Dropping an inherited GIT_CONFIG_PARAMETERS is part of this: it is
    the same injection channel. Other variables are untouched (the general
    allowlist is G3). `ws` is accepted for the caller's convenience and unused."""
    env = {name: value for name, value in environ.items()
           if name.upper() not in GIT_LOCATION_VARS and not GIT_CONFIG_INJECTION_RE.match(name)}
    env["GIT_CONFIG_COUNT"] = str(len(GIT_SAFE_CONFIG))
    for index, (key, value) in enumerate(GIT_SAFE_CONFIG):
        env[f"GIT_CONFIG_KEY_{index}"] = key
        env[f"GIT_CONFIG_VALUE_{index}"] = value
    env["GIT_NO_LAZY_FETCH"] = "1"
    return env


def pinned_hooks_dir(ws: Path) -> Path:
    """<git-common-dir>/hooks: the only hooks directory a trusted git call may use."""
    return state_path(ws).parent / "hooks"


def git_argv(ws: Path, args, *, work_tree: bool = True, hooks: bool = True,
             config=()) -> list[str]:
    """The hardened command line (A1.1): `git --work-tree=<ws> -C <ws> -c ...
    <args>`. `work_tree=False` leaves --work-tree out (the toplevel probe, and
    `git commit`, whose hooks would otherwise inherit GIT_WORK_TREE). `hooks=False`
    leaves the hooksPath pin out (probes that run no hook). `config` adds
    (key, value) pairs after the standard ones."""
    argv = [git_program(ws)]
    if work_tree:
        argv.append(f"--work-tree={ws}")
    argv += ["-C", str(ws)]
    settings = list(GIT_SAFE_CONFIG)
    if hooks:
        settings.append(("core.hooksPath", str(pinned_hooks_dir(ws))))
    for key, value in [*settings, *config]:
        argv += ["-c", f"{key}={value}"]
    return argv + list(args)


def run_git(ws: Path, args, *, input_bytes: bytes | None = None,
            timeout: int = GIT_TIMEOUT_SECONDS, index_file: Path | None = None,
            work_tree: bool = True, hooks: bool = True, config=(),
            extra_env=None) -> subprocess.CompletedProcess:
    """The one place the trust layer starts a git process (A1.1): clean
    environment, hardened argv. `index_file` becomes GIT_INDEX_FILE for this
    call only (A3); `extra_env` is added last. Returns the CompletedProcess;
    KitError when git cannot start or times out."""
    env = clean_git_env()
    env.update(extra_env or {})
    if index_file is not None:
        env["GIT_INDEX_FILE"] = str(index_file)
    argv = git_argv(ws, args, work_tree=work_tree, hooks=hooks, config=config)
    try:
        return _spawn(argv, input_bytes, timeout, env)
    except subprocess.TimeoutExpired as exc:
        raise KitError(f"git {' '.join(args[:3])} timed out") from exc
    except OSError as exc:
        raise KitError(f"cannot run git at {argv[0]}: {exc}") from exc


def git(ws: Path, *args: str, input_bytes: bytes | None = None, check: bool = True,
        timeout: int = GIT_TIMEOUT_SECONDS, **options) -> bytes:
    """Run git through run_git and return stdout. Raises KitError on a
    non-zero exit when `check` is true. `options` go to run_git."""
    proc = run_git(ws, args, input_bytes=input_bytes, timeout=timeout, **options)
    if check and proc.returncode != 0:
        raise KitError(f"git {' '.join(args[:3])} failed: "
                       f"{proc.stderr.decode(errors='replace').strip()}")
    return proc.stdout


def git_ok(ws: Path, *args: str) -> bool:
    """True only for exit 0. Any error (including a git that cannot run) is
    "no", so an ancestry check fails closed (A1.3)."""
    try:
        return run_git(ws, args).returncode == 0
    except KitError:
        return False


def head_commit(ws: Path) -> str | None:
    """The commit HEAD names, or None when HEAD is unborn. HEAD is not peeled
    (`^{commit}` makes rev-parse print nothing for a rewritten loose commit,
    which would read as "no commits"); the commit is read and re-hashed, so a
    forged or deleted HEAD object is a TrustError."""
    proc = run_git(ws, ["rev-parse", "--verify", "-q", "HEAD"])
    oid = proc.stdout.decode().strip() if proc.returncode == 0 else ""
    if not oid:
        return None
    try:
        return read_commit(ws, oid).oid
    except MissingObject as exc:
        raise TrustError(f"HEAD names {oid}, which is missing from the object store") from exc


def repo_toplevel(cwd: Path) -> Path:
    """The work-tree root git finds from `cwd`, asked WITHOUT --work-tree (with
    it git would only echo the directory back)."""
    proc = run_git(cwd, ["rev-parse", "--show-toplevel"], work_tree=False, hooks=False)
    if proc.returncode != 0:
        raise KitError("not inside a git work tree")
    return Path(proc.stdout.decode().strip()).resolve()


def _read_gitfile(dotgit: Path) -> str:
    """The `gitdir:` value of a `.git` file, parsed as git does: the content must
    start with `gitdir: ` (one space); only the trailing newline or CRLF goes."""
    try:
        text = dotgit.read_bytes().decode("utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise KitError(f"invalid gitfile {dotgit}: {exc}") from exc
    text = text.removesuffix("\n").removesuffix("\r")
    if not text.startswith("gitdir: ") or not text[len("gitdir: "):]:
        raise KitError(f"invalid gitfile {dotgit}: it does not start with 'gitdir: '")
    return text[len("gitdir: "):]


def _linked_common_dir(ws: Path, dotgit: Path) -> Path:
    """The common git dir of a linked worktree or submodule, read from the
    `.git` file and `<gitdir>/commondir`, without starting git. KitError for a
    malformed gitfile, a missing gitdir or an unreadable commondir."""
    gitdir = ws / _read_gitfile(dotgit)  # an absolute value replaces ws
    if not gitdir.is_dir():
        raise KitError(f"invalid gitfile {dotgit}: the git directory {gitdir} does not exist")
    marker = gitdir / "commondir"
    if not marker.is_file():
        return gitdir
    try:
        return gitdir / marker.read_text(encoding="utf-8").strip()
    except (OSError, UnicodeDecodeError) as exc:
        raise KitError(f"cannot read {marker}: {exc}") from exc


def state_path(ws: Path) -> Path:
    """<git-common-dir>/crucible, found without starting git: the recorded git
    is not known yet at that point. An ordinary checkout has a `.git`
    directory; a linked worktree or submodule has a `.git` file that names it.
    Anything else asks git."""
    dotgit = ws / ".git"
    if dotgit.is_dir() and not dotgit.is_symlink():
        return (dotgit / STATE_DIRNAME).resolve()
    if dotgit.is_file():
        return (_linked_common_dir(ws, dotgit) / STATE_DIRNAME).resolve()
    out = Path(git(ws, "rev-parse", "--git-common-dir", work_tree=False, hooks=False)
               .decode().strip())
    return ((out if out.is_absolute() else ws / out) / STATE_DIRNAME).resolve()


# ---------------------------------------------------------------------------
# A2: repository states the hardened command line cannot neutralize
# ---------------------------------------------------------------------------

# Local config keys that redirect git or run code inside it (architecture 16.2).
# commit.gpgSign and log.showSignature are NOT here: the hardened argv and the
# commit override neutralize them, and refusing them would block honest signing.
# trailer.* is here because a local trailer.<t>.command runs during interpret-trailers;
# hook.* because git 2.54 runs hook.<n>.command for hook.<n>.event, even with
# core.hooksPath pinned.
DANGEROUS_CONFIG_RE = re.compile(
    r"^(core\.(worktree|hookspath|fsmonitor|sshcommand)"
    r"|extensions\.(partialclone|worktreeconfig)"
    r"|remote\..+\.promisor"
    r"|filter\..+"
    r"|diff\..+\.(textconv|command)"
    r"|gpg\.program|gpg\..+\.program"
    r"|include\.path|includeif\..+"
    r"|trailer\..+"
    r"|hook\..+)$", re.IGNORECASE)


def is_dangerous_config_key(key: str) -> bool:
    return DANGEROUS_CONFIG_RE.match(key) is not None


def _probe(ws: Path, *args: str) -> subprocess.CompletedProcess:
    """A read-only probe: clean environment, no --work-tree (a redirect has to
    show), no hooks pin (a probe runs no hook)."""
    return run_git(ws, list(args), work_tree=False, hooks=False)


def _probe_text(ws: Path, *args: str) -> str | None:
    proc = _probe(ws, *args)
    return proc.stdout.decode(errors="replace").strip() if proc.returncode == 0 else None


FILTER_COMMAND_KEY_RE = r"^filter\..+\.(clean|process)$"


def config_keys(ws: Path, pattern: str) -> list[str]:
    """The config keys (all scopes, includes followed, clean environment) that match
    `pattern`, sorted and without repeats. Exit 1 is "no match"; any other failure
    is a KitError, so a caller that guards a risk fails closed."""
    proc = _probe(ws, "config", "-z", "--get-regexp", pattern)
    if proc.returncode not in (0, 1):
        raise KitError(f"git config could not be read: {proc.stderr.decode(errors='replace').strip()}")
    return sorted({record.split("\n", 1)[0] for record in split_z(proc.stdout)})


def filter_off_config(ws: Path) -> list[tuple[str, str]]:
    """Config pairs that switch off the clean and process commands of every
    configured filter driver (all config scopes), for the one git call that may
    refresh the index. Probed on git 2.54: `git commit` compares an index entry
    that has no stat data (ours, from `update-index --index-info`) with the
    work-tree file through the attribute's clean filter, so it runs the filter
    although it commits the index entry. With these pairs it runs nothing. What
    is committed is unchanged: the entry that stage_paths wrote raw. A driver
    name may hold dots (`filter.a.b.clean` names driver `a.b`): the variable is
    what follows the last dot."""
    keys = config_keys(ws, FILTER_COMMAND_KEY_RE)
    names = sorted({key[len("filter."):key.rindex(".")] for key in keys})
    for name in names:
        if "=" in name:
            raise KitError(f"filter driver name {name!r} holds '='; git cannot switch it off "
                           "(-c would read it as another key). Rename the driver")
    return [pair for name in names for pair in ((f"filter.{name}.clean", ""),
                                                (f"filter.{name}.process", ""),
                                                (f"filter.{name}.required", "false"))]


def _probe_required(ws: Path, *args: str) -> str:
    """Probe output; a failing probe is a problem that carries git's own message."""
    proc = _probe(ws, *args)
    if proc.returncode != 0:
        raise KitError(f"git {' '.join(args)} failed at {ws}: "
                       f"{proc.stderr.decode(errors='replace').strip()}")
    return proc.stdout.decode(errors="replace").strip()


def _config_keys(ws: Path, scope: str) -> list[str]:
    proc = _probe(ws, "config", scope, "--list", "-z", "--no-includes")
    if proc.returncode != 0:
        raise KitError(f"git config {scope} --list failed: "
                       f"{proc.stderr.decode(errors='replace').strip()}")
    return [record.split("\n", 1)[0] for record in split_z(proc.stdout)]


def local_config_keys(ws: Path) -> list[str]:
    """Every key set in the repository's local config and, when the file exists,
    its worktree config. Includes are not followed (include.path is itself a
    dangerous key)."""
    keys = set(_config_keys(ws, "--local"))
    relative = _probe_text(ws, "rev-parse", "--git-path", "config.worktree")
    if relative is not None and os.path.lexists(ws / relative):
        keys.update(_config_keys(ws, "--worktree"))
    return sorted(keys)


def _grafts_file(ws: Path) -> str | None:
    relative = _probe_text(ws, "rev-parse", "--git-path", "info/grafts")
    if relative is None:
        raise KitError("git rev-parse --git-path info/grafts failed")
    path = ws / relative
    return str(path) if os.path.lexists(path) else None


def _config_remedy(keys: list[str]) -> str:
    """What to do about the dangerous keys found, key by key."""
    lowered = [k.lower() for k in keys]
    steps = []
    if "extensions.worktreeconfig" in lowered:
        steps.append("for extensions.worktreeConfig run `git sparse-checkout disable`, then "
                     "`git config --unset extensions.worktreeConfig`")
    if any(re.fullmatch(r"remote\..+\.promisor", k) for k in lowered):
        steps.append("for remote.*.promisor work in a full clone (unsetting the key leaves "
                     "objects missing)")
    rest = [k for k in lowered if k != "extensions.worktreeconfig"
            and not re.fullmatch(r"remote\..+\.promisor", k)]
    if rest:
        steps.append("for the other keys remove them (git config --unset <key>)")
    return "; ".join(steps)


def state_problems(ws: Path, state: dict) -> list[str]:
    """One sentence per A2 state found in `state` (see repository_state)."""
    problems = []
    top = state["toplevel"]
    if top is not None and top != os.path.realpath(ws):
        problems.append(f"git reports the work tree root {top}, not {ws} (a core.worktree "
                        f"setting or a nested directory redirects git); the gate reads only {ws}")
    if state["shallow"]:
        problems.append("this is a shallow clone (.git/shallow): hidden parents change ancestry "
                        "answers; fetch full history (git fetch --unshallow) or use a full clone")
    if state["grafts"]:
        problems.append(f"{state['grafts']} exists: grafts rewrite history locally; remove it")
    if state["dangerous_config"]:
        problems.append("local git config sets " + ", ".join(state["dangerous_config"])
                        + ": each can run code or redirect git inside the trust layer; "
                        + _config_remedy(state["dangerous_config"]))
    return problems


def repository_state(ws: Path) -> dict:
    """The A2 findings for the repository at `ws`, read with the clean
    environment and no --work-tree: {"toplevel": realpath or None, "shallow":
    bool, "grafts": path or None, "dangerous_config": [keys], "problems":
    [sentences]}. Never raises; a git that cannot answer is a problem."""
    state: dict = {"toplevel": None, "shallow": False, "grafts": None,
                   "dangerous_config": [], "problems": []}
    try:
        state["toplevel"] = os.path.realpath(_probe_required(ws, "rev-parse", "--show-toplevel"))
        state["shallow"] = _probe_required(ws, "rev-parse", "--is-shallow-repository") == "true"
        state["grafts"] = _grafts_file(ws)
        state["dangerous_config"] = [k for k in local_config_keys(ws) if is_dangerous_config_key(k)]
    except KitError as exc:
        state["problems"].append(str(exc))
    state["problems"] += state_problems(ws, state)
    return state


PARTIAL_CLONE_RE = r"^(remote\..+\.promisor|extensions\.partialclone)$"


def refuse_partial_clone_config(ws: Path) -> None:
    """TrustError when any config scope (global, system, GIT_CONFIG_GLOBAL, includes) holds
    remote.<n>.promisor or extensions.partialClone. A1 sets GIT_NO_LAZY_FETCH, but git older
    than 2.44 ignores it, and a promisor remote there can run a program on a lazy fetch.
    A2 reads local config only. A failed read raises KitError (fail closed)."""
    keys = config_keys(ws, PARTIAL_CLONE_RE)
    if keys:
        raise TrustError(f"git config sets {', '.join(keys)}: a promisor remote can run a "
                         "program when git fetches a missing object. Use a full clone / remove "
                         "the promisor setting (any config scope)")


def require_sound_repository(ws: Path, root_hint: bool = False) -> None:
    """TrustError (exit 3) naming every A2 state present. Call it before the
    first git read of a command or hook. `root_hint` adds the install-hooks
    advice for a workspace that is not the repository root."""
    state = repository_state(ws)
    if state["problems"]:
        hint = ""
        redirected = "core.worktree" in (k.lower() for k in state["dangerous_config"])
        if root_hint and not redirected and state["toplevel"] not in (None, os.path.realpath(ws)):
            hint = f"; run install-hooks from the repository root {state['toplevel']}"
        raise TrustError("unsafe local git state: " + "; ".join(state["problems"]) + hint)
    refuse_partial_clone_config(ws)


# ---------------------------------------------------------------------------
# A3: the environment git gives a hook
# ---------------------------------------------------------------------------

# Location variables git never sets for a hook. One in a hook's environment is a
# redirect. (GIT_DIR and GIT_INDEX_FILE are the two git does set; see below.)
HOOK_REFUSED_VARS = ("GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
                     "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE")
# The index file names git uses while it commits: the index itself, the lock of
# `commit -a`/`-i`, and the temporary index of `commit <paths>`.
HOOK_INDEX_NAMES = ("index", "index.lock", "next-index-*.lock")


def location_variables(environ) -> dict[str, str]:
    """The git location variables in `environ`, keyed by upper-case name. Two
    spellings of one name (GIT_DIR and Git_Dir) are ambiguous: TrustError."""
    found: dict[str, str] = {}
    for name, value in environ.items():
        upper = name.upper()
        if upper not in GIT_LOCATION_VARS:
            continue
        if upper in found:
            raise TrustError(f"{upper} is set under two spellings in the hook environment")
        found[upper] = value
    return found


def absolute_git_dir(ws: Path) -> str:
    """The realpath of the repository's git directory, from the clean environment."""
    text = _probe_text(ws, "rev-parse", "--absolute-git-dir")
    if text is None:
        raise KitError(f"git cannot name the git directory of {ws}")
    return os.path.realpath(text)


def hook_index_file(ws: Path, git_dir: str, text: str) -> Path:
    """GIT_INDEX_FILE as git sets it for a hook: resolved against the working
    directory, inside this repository's git directory, and named index,
    index.lock or next-index-*.lock. Anything else is a TrustError."""
    resolved = Path(os.path.join(ws, text))
    parent, name = os.path.realpath(resolved.parent), resolved.name
    if parent != git_dir or not any(fnmatch.fnmatchcase(name, p) for p in HOOK_INDEX_NAMES):
        raise TrustError(f"GIT_INDEX_FILE {text!r} is not git's own index of this repository "
                         f"({git_dir}): a hook trusts only index, index.lock or next-index-*.lock "
                         "there")
    return Path(parent) / name


def validate_hook_environment(ws: Path, environ=None) -> Path | None:
    """A3.3 to A3.5 for the location variables in `environ` (default os.environ).
    Returns the index file the index reads must use, or None for git's default.
    GIT_DIR must name this repository's git directory (it is then dropped: the
    clean environment finds the repository itself); GIT_INDEX_FILE must pass
    hook_index_file; the other location variables are refused."""
    location = location_variables(os.environ if environ is None else environ)
    refused = [name for name in HOOK_REFUSED_VARS if name in location]
    if refused:
        raise TrustError(f"{', '.join(refused)} is set in the hook environment; git does not "
                         "set it for a hook, so it is a redirect")
    git_dir = absolute_git_dir(ws)
    declared = location.get("GIT_DIR")
    if declared is not None and os.path.realpath(os.path.join(ws, declared)) != git_dir:
        raise TrustError(f"GIT_DIR {declared!r} is not this repository's git directory ({git_dir})")
    index = location.get("GIT_INDEX_FILE")
    return None if index is None else hook_index_file(ws, git_dir, index)


def hook_workspace(environ=None) -> tuple[Path, Path | None]:
    """(work tree root, index file or None) for a hook that git started. The
    root is the current directory (git runs hooks at the work-tree root), after
    the A2 checks, which also prove it is the root the clean `rev-parse
    --show-toplevel` reports (A3.1, A3.2). The recorded git is bound first."""
    cwd = Path(os.path.realpath(os.getcwd()))
    bind_recorded_git(cwd)
    require_sound_repository(cwd)
    return cwd, validate_hook_environment(cwd, environ)


# ---------------------------------------------------------------------------
# A4: committed content as verified raw objects
# ---------------------------------------------------------------------------

OID_RE = re.compile(r"^(?:[0-9a-f]{40}|[0-9a-f]{64})$")
OBJECT_KINDS = ("blob", "tree", "commit", "tag")
OBJECT_ALGORITHMS = {40: "sha1", 64: "sha256"}


class MissingObject(KitError):
    """git has no such object or revision path."""


class Commit(NamedTuple):
    oid: str
    tree: str
    parents: tuple
    committer_time: int
    message: bytes


def object_id(kind: str, data: bytes, width: int) -> str:
    """The id git gives an object of `kind` with content `data`, hashed with the
    algorithm that matches an id of `width` hex digits (40: SHA-1, 64: SHA-256)."""
    algorithm = OBJECT_ALGORITHMS.get(width)
    if algorithm is None:
        raise TrustError(f"an object id of {width} hex digits is neither SHA-1 nor SHA-256")
    return hashlib.new(algorithm, f"{kind} {len(data)}\0".encode() + data).hexdigest()


def _split_reply(output: bytes, position: int, spec: str) -> tuple[str, str, int, int]:
    """(oid, kind, size, start of content) of one `cat-file --batch` reply."""
    end = output.find(b"\n", position)
    fields = output[position:end].decode(errors="replace").split(" ") if end >= 0 else []
    if len(fields) != 3 or fields[1] not in OBJECT_KINDS or not fields[2].isdigit() \
            or not OID_RE.match(fields[0]):
        raise MissingObject(f"{spec}: git has no such object")
    return fields[0], fields[1], int(fields[2]), end + 1


def parse_batch(output: bytes, specs: list[str]) -> list[tuple[str, str, bytes]]:
    """Split a `cat-file --batch` reply into (oid, kind, data) per spec and
    re-hash every object (A4.3): an object whose bytes do not hash to its id was
    changed in place, which git itself does not notice on a read."""
    objects, position = [], 0
    for spec in specs:
        oid, kind, size, start = _split_reply(output, position, spec)
        data = output[start:start + size]
        if len(data) != size:
            raise KitError(f"{spec}: short read from git cat-file")
        if object_id(kind, data, len(oid)) != oid:
            raise TrustError(f"object {oid[:12]} ({spec}) does not hash to its id: the object "
                             "store was changed in place (forged or corrupt)")
        objects.append((oid, kind, data))
        position = start + size + 1
    return objects


def read_objects(ws: Path, specs: list[str], *,
                 timeout: int = GIT_TIMEOUT_SECONDS) -> list[tuple[str, str, bytes]]:
    """Raw, re-hashed objects named by `specs` (object ids or `rev:./path`), in
    order, from one `git cat-file --batch`: no filter, no textconv. The batch
    header names the id that the re-hash checks against; `cat-file blob` would
    not. MissingObject for an absent spec; TrustError for a hash mismatch."""
    if not specs:
        return []
    proc = run_git(ws, ["cat-file", "--batch"], timeout=timeout,
                   input_bytes="".join(f"{spec}\n" for spec in specs).encode())
    if proc.returncode != 0:
        raise KitError("git cat-file --batch failed: "
                       f"{proc.stderr.decode(errors='replace').strip()}")
    return parse_batch(proc.stdout, specs)


TYPE_MASK, DIRECTORY_TYPE, GITLINK_TYPE = 0o170000, 0o040000, 0o160000


def parse_tree(data: bytes, width: int) -> list[tuple[str, bytes, str]]:
    """Every record of raw tree `data` (`<mode> <name>\\0<raw oid>`; `width` hex digits per
    id) as (octal mode string, raw name bytes, oid). The one tree parser: a record
    that is cut short, or whose mode is not octal digits, is a TrustError."""
    records, position, raw = [], 0, width // 2
    while position < len(data):
        space = data.find(b" ", position)
        nul = data.find(b"\0", space + 1) if space >= 0 else -1
        if nul < 0 or nul + 1 + raw > len(data):
            raise TrustError("a tree object is malformed")
        mode = data[position:space]
        if not re.fullmatch(rb"[0-7]+", mode):
            raise TrustError("a tree object has a malformed entry mode")
        records.append((mode.decode(), data[space + 1:nul], data[nul + 1:nul + 1 + raw].hex()))
        position = nul + 1 + raw
    return records


def _tree_entry(data: bytes, name: bytes, width: int) -> tuple[int, str] | None:
    """(file type bits, oid) of entry `name` in raw tree `data`, or None when the
    tree has no such entry."""
    for mode, entry, oid in parse_tree(data, width):
        if entry == name:
            return int(mode, 8) & TYPE_MASK, oid
    return None


def _read_many(ws: Path, oids: list[str], kind: str, *,
               timeout: int = GIT_TIMEOUT_SECONDS) -> dict[str, bytes]:
    """oid -> re-hashed content of each object, one batch; each must be a `kind`."""
    try:
        objects = read_objects(ws, oids, timeout=timeout)
    except MissingObject as exc:
        # The authentic tree names these objects: an absence is damage, not "no such path".
        raise TrustError(f"an object that the tree names is missing from the object store: {exc}") from exc
    contents = {}
    for (got, found, data), oid in zip(objects, oids):
        if got != oid or found != kind:
            raise TrustError(f"object {oid[:12]} is a {found}, expected a {kind}")
        contents[oid] = data
    return contents


def _read_kind(ws: Path, oid: str, kind: str) -> bytes:
    """The re-hashed content of object `oid`, which must be a `kind`."""
    return _read_many(ws, [oid], kind)[oid]


def _walk_path(ws: Path, root_tree: str, parts: list[str]) -> tuple[int, str] | None:
    """(mode, oid) of the entry at `parts` below `root_tree`, or None when a
    component is absent or a file stands where a directory is needed. Every
    tree on the way is read and re-hashed (P4): a rewritten tree object could
    point a path at another, correctly hashed, blob."""
    tree, entry = root_tree, None
    for index, part in enumerate(parts):
        if index and entry[0] != DIRECTORY_TYPE:
            return None
        entry = _tree_entry(_read_kind(ws, tree, "tree"), part.encode(), len(root_tree))
        if entry is None:
            return None
        tree = entry[1]
    return entry


def committed_blob(ws: Path, rev: str, path: str) -> bytes | None:
    """The raw bytes of `path` at `rev` (A4.1), or None when `rev` or the path
    is absent (also when a file stands where a directory is needed). `path` is
    relative to the work tree root. The commit, the root tree, every tree on
    the path and the blob are each read through read_objects and re-hashed
    (P4); git's own path lookup (`rev:./path`) is never used, so an object
    rewritten in place is a TrustError, and so is an object that the authentic tree names but the store lacks. A directory or a submodule at `path`
    is a KitError."""
    try:
        commit = read_commit(ws, rev)
    except MissingObject:
        return None  # no such revision
    parts = [part for part in path.split("/") if part]
    entry = _walk_path(ws, commit.tree, parts) if parts else (DIRECTORY_TYPE, commit.tree)
    if entry is None:
        return None
    if entry[0] in (DIRECTORY_TYPE, GITLINK_TYPE):
        raise KitError(f"{rev}:{path} is not a file")
    return _read_kind(ws, entry[1], "blob")


def _commit_parents(lines: list[bytes]) -> list[str]:
    """The `parent` lines that directly follow the `tree` line."""
    parents = []
    for line in lines[1:]:
        if not line.startswith(b"parent "):
            break
        parents.append(line[7:].decode())
    return parents


def parse_commit(oid: str, data: bytes) -> Commit:
    """Header, blank line, message bytes (A4.2). Parents are the `parent` lines
    that follow the `tree` line; continuation lines (gpgsig) start with a space."""
    head, _, message = data.partition(b"\n\n")
    lines = head.split(b"\n")
    committers = [line for line in lines if line.startswith(b"committer ")]
    if not lines[0].startswith(b"tree ") or len(committers) != 1:
        raise KitError(f"commit {oid[:12]} has no tree header or no single committer header")
    tree, parents = lines[0][5:].decode(), _commit_parents(lines)
    when = committers[0].rsplit(b" ", 2)[-2]
    if not OID_RE.match(tree) or not all(OID_RE.match(p) for p in parents) or not when.isdigit():
        raise KitError(f"commit {oid[:12]} has a malformed header")
    return Commit(oid, tree, tuple(parents), int(when), message)


def resolve_commit(ws: Path, rev: str) -> str:
    """The commit id `rev` names. A full id is taken as it is (and read, then
    re-hashed, by read_commit); anything else is resolved by rev-parse."""
    if OID_RE.match(rev):
        return rev
    # No ^{commit} peel: git would parse the object and report a forged one as "missing".
    out = git(ws, "rev-parse", "--verify", "-q", rev, check=False)
    if not out.strip():
        raise MissingObject(f"{rev} does not name a commit")
    return out.decode().strip()


def read_commit(ws: Path, rev: str) -> Commit:
    """The parsed, re-hashed commit `rev` names (A4.2). Never `git log`."""
    oid = resolve_commit(ws, rev)
    got, kind, data = read_objects(ws, [oid])[0]
    if kind != "commit" or got != oid:
        raise TrustError(f"{rev} names {kind} {got[:12]}, not the commit {oid[:12]}")
    return parse_commit(oid, data)


def empty_tree(ws: Path) -> str:
    """The id of the empty tree in this repository's hash algorithm (M3)."""
    out = git(ws, "hash-object", "-t", "tree", "--stdin", input_bytes=b"").decode().strip()
    if not OID_RE.match(out):
        raise KitError("git hash-object did not print an object id")
    return out


REGULAR_TYPE = 0o100000


def file_permissions(mode: str) -> int | None:
    """0o755 or 0o644 for a tree mode of a regular file (type bits 0100000, whatever the
    permission bits: git reads a legacy 100664 as a regular file), executable when any
    execute bit is set; None for any other kind of entry."""
    bits = int(mode, 8)
    if bits & TYPE_MASK != REGULAR_TYPE:
        return None
    return 0o755 if bits & 0o111 else 0o644
SYMLINK_MODE, GITLINK_MODE = "120000", "160000"
HOSTILE_NAMES = (b"", b".", b"..")


def _entry_path(prefix: str, name: bytes) -> str:
    """`prefix/name` for one tree record, KitError for a name that is no plain
    path component (git's fsck refuses these, but a hash-valid tree can hold them)."""
    if name in HOSTILE_NAMES or b"/" in name or b"\0" in name:
        raise KitError(f"unsafe name in the commit tree: {name!r}")
    text = os.fsdecode(name)
    return f"{prefix}/{text}" if prefix else text


def _next_level(level: list[tuple[str, str]], contents: dict[str, bytes], width: int,
                files: list[tuple[str, str, str]]) -> list[tuple[str, str]]:
    """Collect the non-directory records of this level into `files`; return the
    directories (path, tree oid) of the next level."""
    below = []
    for prefix, oid in level:
        names = set()
        for mode, name, entry in parse_tree(contents[oid], width):
            if name in names:       # git reads the first, a writer the last: refuse (P21)
                raise KitError(f"the tree {oid[:12]} names {name!r} more than once")
            names.add(name)
            path = _entry_path(prefix, name)
            if int(mode, 8) & TYPE_MASK == DIRECTORY_TYPE:
                below.append((path, entry))
            else:
                files.append((mode, entry, path))
    return below


def tree_files(ws: Path, root: str, *,
               timeout: int = GIT_TIMEOUT_SECONDS) -> list[tuple[str, str, str]]:
    """(mode, oid, path) of every non-directory entry below the tree `root` (P20).
    The trees are read raw and re-hashed, one batch per directory level, and parsed
    here; `git ls-tree` is not used because git does not verify a loose object's hash.
    A name repeated within one tree is a KitError."""
    files, level = [], [("", root)]
    while level:
        contents = _read_many(ws, list(dict.fromkeys(oid for _, oid in level)), "tree",
                              timeout=timeout)
        level = _next_level(level, contents, len(root), files)
    return files


def tree_entries(ws: Path, commit_oid: str, *,
                 timeout: int = GIT_TIMEOUT_SECONDS) -> list[tuple[str, str, str]]:
    """tree_files of the commit's root tree."""
    return tree_files(ws, read_commit(ws, commit_oid).tree, timeout=timeout)


def gated_tree_map(ws: Path, tree: str, rules: GateRules) -> dict[str, str]:
    """sha256 of each gated file in the tree `tree`, from re-hashed raw objects: the
    same map that staged_tree builds from an index, but taken from one tree id (G14).
    Any non-ASCII path in the tree and any gated entry that is not a regular file
    (mode 100644 or 100755) is a PathViolation."""
    entries = tree_files(ws, tree)
    reject_non_ascii((path for _mode, _oid, path in entries), "the staged tree")
    gated = []
    for mode, oid, path in entries:
        if not rules.is_gated(path):
            continue
        if mode not in ("100644", "100755"):
            raise PathViolation(f"{path}: gated path has mode {mode} (regular files only)")
        gated.append((path, oid))
    blobs = _read_many(ws, list(dict.fromkeys(oid for _path, oid in gated)), "blob")
    return {path: sha256_hex(blobs[oid]) for path, oid in gated}


def safe_member(path: str) -> PurePosixPath:
    """The path as a PurePosixPath, or KitError when it is empty, absolute or
    climbs out of the tree."""
    name = PurePosixPath(path)
    if not path or name.is_absolute() or ".." in name.parts or "\0" in path:
        raise KitError(f"unsafe path in the commit tree: {path!r}")
    return name


def link_stays_inside(path: str, target: str) -> bool:
    """A relative symlink whose target, resolved from the link's own
    directory, stays inside the tree."""
    if not target or target.startswith("/") or "\0" in target:
        return False
    resolved = posixpath.normpath(posixpath.join(posixpath.dirname(path), target))
    return resolved != ".." and not resolved.startswith("../")


NO_FOLLOW = getattr(os, "O_NOFOLLOW", 0)


def _ensure_dirs(destination: Path, parts: tuple, directories: set) -> None:
    """Create the directories above the last of `parts`; each one must be a real
    directory, never a link (lstat), so nothing is created through a link."""
    known = parts[:-1]
    if known in directories:            # this parent chain was already verified
        return
    current = destination
    for part in known:
        current = current / part
        if current in directories:
            continue
        try:
            os.mkdir(current)
        except FileExistsError:
            pass
        if not stat.S_ISDIR(os.lstat(current).st_mode):
            raise KitError(f"{current.relative_to(destination).as_posix()} is not a directory")
        directories.add(current)
    directories.add(known)


def _write_file(target: Path, mode: str, data: bytes, when: int) -> None:
    """Create `target`, which must not exist (O_EXCL: a case-fold collision is an
    error) and is never followed (O_NOFOLLOW); mode and mtime go through the fd."""
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | NO_FOLLOW
    try:
        descriptor = os.open(target, flags, 0o600)
    except FileExistsError as exc:
        raise KitError(f"{target.name} is written twice (a repeated or case-fold name)") from exc
    with os.fdopen(descriptor, "wb") as handle:
        handle.write(data)
        handle.flush()
        os.fchmod(handle.fileno(), file_permissions(mode))
        os.utime(handle.fileno(), (when, when))


def _place(destination: Path, mode: str, path: str, data: bytes | None, when: int,
           directories: set) -> None:
    """Write one non-link entry (file or gitlink directory)."""
    parts = safe_member(path).parts
    _ensure_dirs(destination, parts, directories)
    target = destination.joinpath(*parts)
    if mode == GITLINK_MODE:
        _ensure_dirs(destination, parts + ("",), directories)
    elif file_permissions(mode) is not None:
        _write_file(target, mode, data, when)
    else:
        raise KitError(f"{path}: unsupported tree entry mode {mode}")


def _make_link(destination: Path, path: str, data: bytes, when: int, directories: set) -> Path:
    parts = safe_member(path).parts
    link = os.fsdecode(data)
    if not link_stays_inside(path, link):
        raise KitError(f"symlink {path} -> {link!r} is absolute or leaves the tree")
    _ensure_dirs(destination, parts, directories)
    target = destination.joinpath(*parts)
    try:
        os.symlink(link, target)
    except FileExistsError as exc:
        raise KitError(f"symlink {path} collides with an entry of the same name") from exc
    if os.utime in os.supports_follow_symlinks:
        os.utime(target, (when, when), follow_symlinks=False)
    return target


def _links_stay_inside(destination: Path, links: list[Path]) -> None:
    """Every link, with its chain of links resolved on disk, ends inside the
    destination (the check the standard "data" extraction filter makes): the textual check cannot
    see a chain through other links or a case-insensitive name."""
    root = os.path.realpath(destination)
    for link in links:
        real = os.path.realpath(link)
        if real != root and not real.startswith(root + os.sep):
            raise KitError(f"symlink {link.relative_to(destination).as_posix()} resolves "
                           "outside the tree")


def _stamp_directories(directories: set, when: int) -> None:
    """Set the mtime of every directory, deepest first (writing a file changes
    its directory's mtime). `directories` also holds verified-chain keys (tuples)."""
    real = [d for d in directories if isinstance(d, Path)]
    for directory in sorted(real, key=lambda d: len(d.parts), reverse=True):
        os.utime(directory, (when, when))


def extract_commit(ws: Path, commit: str, destination: Path) -> None:
    """Write the tree of `commit` under `destination` from re-hashed raw objects
    (A4.4, P20, P21), not from `git archive` or `git ls-tree`. Directories and
    regular files (0644 or 0755, created exclusively and never through a link)
    come first, symlinks last; then every link is resolved on disk and must stay
    inside. A gitlink becomes an empty directory; every file and directory gets
    the commit time as its mtime. An unsafe name or path, a repeated name, or an
    absolute or escaping symlink is a KitError. No filter, no textconv, no
    export-ignore or export-subst."""
    info = read_commit(ws, commit)
    entries = tree_entries(ws, info.oid, timeout=600)
    for _mode, _oid, path in entries:
        safe_member(path)                                   # refuse before anything is written
    wanted = list(dict.fromkeys(oid for mode, oid, _ in entries
                                if mode != GITLINK_MODE))
    contents = _read_many(ws, wanted, "blob", timeout=600)
    directories: set = set()
    when = info.committer_time
    for mode, oid, path in entries:
        if mode != SYMLINK_MODE:
            _place(destination, mode, path, contents.get(oid), when, directories)
    links = [_make_link(destination, path, contents[oid], when, directories)
             for mode, oid, path in entries if mode == SYMLINK_MODE]
    _links_stay_inside(destination, links)
    _stamp_directories(directories, when)


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


def work_tree_listing(ws: Path, index_file: Path | None = None) -> list[str]:
    """Every path in the work tree: tracked and untracked, not ignored.
    `index_file` is the index a hook was given (A3.4); None is git's own."""
    return split_z(git(ws, "ls-files", "-z", "-co", "--exclude-standard", index_file=index_file))


def index_listing(ws: Path, index_file: Path | None = None) -> list[str]:
    return split_z(git(ws, "ls-files", "-z", index_file=index_file))


# ---------------------------------------------------------------------------
# G2 (B1): git-ignored files are violations unless a pinned glob admits them
# ---------------------------------------------------------------------------

IGNORED_LIST_LIMIT = 20    # files a message names one by one; more are grouped by directory
IGNORED_GROUP_LIMIT = 20   # directories a grouped message names
IGNORED_JSON_LIMIT = 200   # files a JSON report carries (the count is always complete)
IGNORED_REVIEW_LIMIT = 200  # files per glob that install-hooks prints for the human to review
IGNORED_WARNING = ("delete only files that this task or a gate command created; any other file "
                   "belongs to the human (settings, secrets): stop and ask")


def ignored_listing(ws: Path) -> list[str]:
    """Every git-ignored path that exists in the work tree: untracked files and symlinks
    that .gitignore (any level), .git/info/exclude, the excludes file or the user's
    $XDG_CONFIG_HOME/git/ignore match, found by descending into ignored directories. A
    nested repository or linked worktree is one entry that ends in '/' (its files are not
    listed); the contents of a submodule are not listed at all."""
    return split_z(git(ws, "ls-files", "-z", "-o", "-i", "--exclude-standard"))


def _gitdir_of(dotgit: Path) -> Path | None:
    """The directory a `.git` file names (`gitdir: <path>`), or None when `dotgit` is not a
    plain file of that shape. Nothing is followed by git: the file is read as text."""
    try:
        if dotgit.is_symlink() or not dotgit.is_file():
            return None
        text = dotgit.read_text(errors="replace")
    except OSError:
        return None
    first = text.splitlines()[0] if text.splitlines() else ""
    return Path(first[len("gitdir: "):]) if first.startswith("gitdir: ") else None


def _resolve(base: Path, text: str) -> Path:
    """`text` as a path; a relative one (git 2.48 and later can write them) is taken from `base`."""
    path = Path(text)
    return path if path.is_absolute() else base / path


def _admin_dir(ws: Path, folder: Path) -> Path | None:
    """The administrative directory `<common-dir>/worktrees/<name>` that the entry's `.git` file
    names, or None when the file is missing or odd, the entry or the admin directory is a
    symlink, or the admin directory lies inside the entry (a repository cannot vouch for itself)."""
    named = _gitdir_of(folder / ".git")
    if folder.is_symlink() or named is None:
        return None
    admin = _resolve(folder, str(named))
    worktrees = state_path(ws).parent / "worktrees"
    inside = os.path.realpath(admin).startswith(os.path.realpath(folder) + os.sep)
    sound = (not worktrees.is_symlink() and not admin.is_symlink() and not inside
             and os.path.realpath(admin.parent) == os.path.realpath(worktrees))
    return admin if sound else None


def linked_worktree(ws: Path, entry: str) -> bool:
    """True when the entry (a path that ends in '/') is a linked worktree of THIS
    repository, decided without running git inside it (a nested `.git` can carry config
    that runs code). Its `.git` file must name `<common-dir>/worktrees/<name>`, and that
    directory's `gitdir` file must name the entry's `.git` file back; a relative path is taken
    from the entry (the file) or from the admin directory (the back pointer). Both are
    compared by realpath. A forged file, a worktree of another repository, a pruned one, a
    symlinked admin directory and one inside the entry are not linked worktrees."""
    folder = ws / entry.rstrip("/")
    admin = _admin_dir(ws, folder)
    if admin is None:
        return False
    try:
        back = _resolve(admin, (admin / "gitdir").read_text(errors="replace").strip())
    except OSError:
        return False
    return os.path.realpath(back) == os.path.realpath(folder / ".git")


def _covers(folder: str, *, flat: set[str], deep: set[str]) -> bool:
    return _dir_of(folder) in flat or any(not d or folder == d or folder.startswith(d + "/") for d in deep)


def _closure_cover(ws: Path, cfg: dict | None, rules: GateRules):
    """A predicate "does command closure cover this folder?" built from the sets that
    directory_closure uses: the folders beside a closure file, and everything below the folder
    of a contract test or of a closure directory. Without a config nothing is covered."""
    if not cfg:
        return lambda folder: False
    files = set(command_closure(ws, cfg, rules)["closure_files"])
    dirs = {f for f in files if (ws / f).is_dir()}
    flat = {_dir_of(f) for f in files - dirs}
    deep = {_dir_of(t) for t in cfg["tier1"]["contract_tests"]} | {d.lower() for d in dirs}
    return functools.partial(_covers, flat=flat, deep=deep)


def _under(folder: str, entry: str) -> bool:
    """`folder` is the `[ast] paths` entry or lies below it; "" and "." name the whole repository."""
    low = entry.rstrip("/").lower()
    return low in ("", ".") or folder == low or folder.startswith(low + "/")


def worktree_placed(entry: str, rules: GateRules, cfg: dict | None, covered) -> bool:
    """Spec 14.5 (a) and (b) for a linked worktree: a path segment at or above it starts with
    '.', and it lies outside every gated path, every `[ast] paths` directory and every folder
    that command closure covers. Detection (c) is linked_worktree."""
    folder = rules.ascii_lower(entry.rstrip("/"))
    below_ast = any(_under(folder, p) for p in ((cfg or {}).get("ast") or {}).get("paths", []))
    return (any(part.startswith(".") for part in folder.split("/"))
            and not rules.gated_by_config(entry) and not below_ast and not covered(folder))


def _bucket(ws: Path, path: str, rules: GateRules, cfg: dict | None, cover) -> str:
    """Where `ignored_files` files an entry: local, worktrees (a placed linked worktree),
    misplaced (a linked worktree in the wrong place, also a violation), allowed or violations."""
    if is_local_state(path):
        return "local"
    if path.endswith("/") and linked_worktree(ws, path):
        return "worktrees" if worktree_placed(path, rules, cfg, cover()) else "misplaced"
    return "allowed" if rules.ignored_allowed(path) else "violations"


def ignored_files(ws: Path, rules: GateRules, cfg: dict | None = None) -> dict[str, list[str]]:
    """{"violations", "allowed", "worktrees", "local", "misplaced"}, each sorted. Local state
    (token, run directory) goes to "local"; a linked worktree of this repository that is
    placed as 17.9 requires goes to "worktrees"; one that is not is a violation and also
    listed under "misplaced" (git-ignored or not: detection is not a boundary, so an
    untracked worktree is judged too); an entry is "allowed" when an `ignored_allow` glob admits it,
    else a violation. `cfg` supplies the `[ast] paths` and the command closure for the placement."""
    split: dict[str, list[str]] = {k: [] for k in ("violations", "allowed", "worktrees", "local", "misplaced")}
    cover = functools.cache(lambda: _closure_cover(ws, cfg, rules))
    for path in sorted(set(ignored_listing(ws))):
        split[_bucket(ws, path, rules, cfg, cover)].append(path)
    for path in sorted(p for p in set(work_tree_listing(ws)) if p.endswith("/")):    # not ignored
        if linked_worktree(ws, path):
            split["worktrees" if worktree_placed(path, rules, cfg, cover()) else "misplaced"].append(path)
    split["violations"] = sorted(split["violations"] + split["misplaced"])
    return split


def ignore_rules(ws: Path, paths: list[str]) -> dict[str, str]:
    """path -> "<source>:<line>: <pattern>" for the rule that ignores it, from one
    `git check-ignore -z -v -n --stdin` (shown with ascii() when it is not plain ASCII). A
    path git cannot explain, or a failing git, is absent from the map."""
    if not paths:
        return {}
    stdin = b"".join(p.encode("utf-8", "surrogateescape") + b"\0" for p in paths)
    proc = run_git(ws, ["check-ignore", "-z", "-v", "-n", "--stdin"], input_bytes=stdin)
    if proc.returncode not in (0, 1):
        return {}
    fields = proc.stdout.decode("utf-8", "surrogateescape").split("\0")
    return {path: _shown(f"{source}:{line}: {pattern}")
            for source, line, pattern, path in zip(fields[0::4], fields[1::4], fields[2::4], fields[3::4])
            if pattern}


def closure_names(ws: Path, cfg: dict) -> set[str]:
    """Repository paths that the gate commands name: argv elements of the AST, Tier 1 and
    Tier 2 commands, the driver paths, the contract tests and the baseline. The message
    never tells anyone to delete one of them."""
    names = {cfg["ast"]["baseline"], *cfg["tier1"]["contract_tests"], *cfg["tier2"].get("driver_paths", [])}
    for command in (cfg["ast"]["command"], cfg["tier1"]["command"], cfg["tier2"]["command"]):
        for element in expand_command(command):
            names.update(named_repo_paths(ws, element))
    return names


def _shown(text: str) -> str:
    """`text` as it is when plain ASCII, else with the other characters escaped (\\xe9, \\udcff)."""
    return text if is_plain_ascii(text) else ascii(text)[1:-1]


def _advice_lines(paths: list[str], named: set[str], misplaced: set[str] = frozenset()) -> list[str]:
    """Advice for the entries that get no delete command: a linked worktree in the wrong
    place, a nested repository (an entry that ends in '/') and a file that a gate command
    runs or reads."""
    lines = []
    if any(p in misplaced for p in paths):
        lines.append("a linked worktree of this repository is admitted only under .worktrees/ at the "
                     "repository root (a path segment that starts with '.', outside source, trusted, "
                     "[ast] and test directories): move the worktree under .worktrees/ at the "
                     "repository root (git worktree move), or ask the human; it has no delete command")
    if any(p.endswith("/") and p not in misplaced for p in paths):
        lines.append("a nested repository that is not a linked worktree of this repository has "
                     "no delete command: move it out of the repository, or ask the human")
    if any(p in named for p in paths):
        lines.append("a gate command runs or reads a file listed above: a human must admit it; "
                     "do not delete it")
    return lines


def _delete_lines(paths: list[str], named: set[str], misplaced: set[str] = frozenset()) -> list[str]:
    """An `rm` command for the plain files that no gate command names, then the advice."""
    files = [shlex.quote(p) for p in paths if not p.endswith("/") and p not in named]
    return (["delete them: rm -- " + " ".join(files)] if files else []) + _advice_lines(paths, named, misplaced)


def _rule_text(why: dict[str, str], path: str, misplaced) -> str:
    """The rule that ignores `path`; an untracked worktree that no rule ignores is "not ignored"."""
    return why.get(path) or ("not ignored" if path in misplaced else "rule unknown")


def _admit_exactly(rules: GateRules, path: str) -> bool:
    """True when the glob `path` admits exactly that one file: no wildcard in it, not a nested
    repository and not a gated path (no glob can admit one)."""
    return not path.endswith("/") and not set("*?") & set(path) and not rules.gated_by_config(path)


def _ignored_each(ws: Path, paths: list[str], named: set[str], misplaced: set[str],
                  rules: GateRules) -> list[str]:
    """The message lines for a short list: each file with its rule, the delete command and
    the exact paths a human could admit (never a parent glob)."""
    why = ignore_rules(ws, paths)
    lines = [f"  {path}  ({_rule_text(why, path, misplaced)})" for path in paths]
    lines += _delete_lines(paths, named, misplaced)
    exact = [json.dumps(p) for p in paths if _admit_exactly(rules, p)]
    if exact:
        lines.append("or, for an environment file, a human adds its exact path to [gate] "
                     "ignored_allow in .crucible/config.toml and repins: ignored_allow = ["
                     + ", ".join(exact) + "]")
    return lines


def ignored_groups(violations: list[str], others: set[str]) -> list[tuple[str, list[str]]]:
    """(key, paths) in name order. The key is the shallowest directory above a violation
    that holds no other file (`others`: admitted, local, worktree and non-ignored paths), with
    a trailing '/'; a violation with no such directory is its own key."""
    impure = {"/".join(p.rstrip("/").split("/")[:i]) for p in others
              for i in range(1, len(p.rstrip("/").split("/")))}
    groups: dict[str, list[str]] = {}
    for path in violations:
        parts = path.rstrip("/").split("/")
        key = next(("/".join(parts[:i]) + "/" for i in range(1, len(parts))
                    if "/".join(parts[:i]) not in impure), path)
        groups.setdefault(key, []).append(path)
    return sorted(groups.items())


def _group_command(key: str, members: list[str], named: set[str]) -> str | None:
    """`git clean -fdX -- <dir>` (removes only git-ignored files, skips nested repositories) for
    a directory group that holds no nested repository and no file a gate command names."""
    if not key.endswith("/") or any(m.endswith("/") or m in named for m in members):
        return None
    return shlex.quote(key.rstrip("/"))


def _group_delete_lines(shown: list[tuple[str, list[str]]], named: set[str]) -> list[str]:
    plain = [c for c in (_group_command(k, m, named) for k, m in shown if is_plain_ascii(k)) if c]
    return (["delete them (git clean removes only git-ignored files; -n previews): git clean -fdX -- "
             + " ".join(plain)] if plain else [])


def _ignored_grouped(ws: Path, split: dict, named: set[str]) -> list[str]:
    """The message lines for a long list: one line per directory that holds only violations,
    with its count and the rule of its first file."""
    others = set(work_tree_listing(ws)) | set(index_listing(ws))
    others.update(split["allowed"], split["local"], split["worktrees"])
    groups = ignored_groups(split["violations"], others)
    shown = groups[:IGNORED_GROUP_LIMIT]
    why = ignore_rules(ws, [members[0] for _, members in shown])
    lines = [f"  {_shown(key)}  {plural(len(members), 'file')}  "
             f"(first: {_shown(members[0])}, {_rule_text(why, members[0], split['misplaced'])})"
             for key, members in shown]
    if len(groups) > len(shown):
        lines.append(f"  ... and {len(groups) - len(shown)} more")
    lines += _group_delete_lines(shown, named)
    lines += _advice_lines([m for _, members in shown for m in members], named, set(split["misplaced"]))
    lines.append("the first files are in the JSON result of preflight, verify and status under "
                 "ignored_files; whether to admit any of it is a decision for a human "
                 "(architecture 17)")
    return lines


def _ignored_head(total: int, misplaced: int) -> str:
    """The first line: a misplaced worktree may be untracked and not ignored, so it is counted
    on its own and is never called a git-ignored file."""
    parts = [plural(total - misplaced, "git-ignored file")] if total > misplaced else []
    if misplaced:
        parts.append(plural(misplaced, "misplaced worktree"))
    return (" and ".join(parts) + " in the repository: the pins and the token cannot see "
            "them, but test tools can load them")


def ignored_report(ws: Path, rules: GateRules, cfg: dict | None = None,
                   split: dict | None = None) -> dict | None:
    """None when no git-ignored file is a violation. Otherwise {"count", "files" (the first
    IGNORED_JSON_LIMIT), "truncated", "message"}: the message lists each file with the rule
    that ignores it, grouped by directory with counts when the list is long, and never
    advises deleting a file that a gate command names (`cfg` says which). `split` is the
    result of ignored_files when the caller already has it."""
    split = split or ignored_files(ws, rules, cfg)
    violations = split["violations"]
    if not violations:
        return None
    named = closure_names(ws, cfg) if cfg else set()
    each = len(violations) <= IGNORED_LIST_LIMIT and all(is_plain_ascii(p) for p in violations)
    lines = _ignored_each(ws, violations, named, set(split["misplaced"]), rules) if each else _ignored_grouped(ws, split, named)
    head = _ignored_head(len(violations), len(split["misplaced"]))
    return {"count": len(violations), "files": violations[:IGNORED_JSON_LIMIT],
            "truncated": len(violations) > IGNORED_JSON_LIMIT,
            "message": "\n".join([head, *lines, IGNORED_WARNING])}


def _directory_counts(files: list[str]) -> list[tuple[str, int]]:
    """(first two directory segments, count) for `files`, largest first."""
    counts = collections.Counter("/".join(f.split("/")[:-1][:2]) + "/" if "/" in f else "./" for f in files)
    return sorted(counts.items(), key=lambda item: (-item[1], item[0]))


def _say_admitted(pattern: str, files: list[str]) -> None:
    """One glob's block: its count, the count per directory (at most IGNORED_GROUP_LIMIT
    lines, then how many more), then the first IGNORED_REVIEW_LIMIT files."""
    say(f"  ignored_allow {pattern!r} admits {plural(len(files), 'file')}")
    folders = _directory_counts(files)
    for folder, count in folders[:IGNORED_GROUP_LIMIT]:
        say(f"    under {_shown(folder)}  {plural(count, 'file')}")
    if len(folders) > IGNORED_GROUP_LIMIT:
        say(f"    ... and {len(folders) - IGNORED_GROUP_LIMIT} more directories")
    for path in files[:IGNORED_REVIEW_LIMIT]:
        say(f"    admitted {_shown(path)}")
    if len(files) > IGNORED_REVIEW_LIMIT:
        say(f"    ... and {len(files) - IGNORED_REVIEW_LIMIT} more")


def report_ignored_files(ws: Path, rules: GateRules, cfg: dict | None = None) -> None:
    """For the human at ratification, before the PIN prompt (B2): each `ignored_allow` glob
    with the files it admits, then the ignored files no glob admits, in the words `verify`
    would use. Lists only; it never refuses."""
    split = ignored_files(ws, rules, cfg)
    for pattern, _ in rules.allow:
        _say_admitted(pattern, [p for p in split["allowed"] if rules.allowing_glob(p) == pattern])
    report = ignored_report(ws, rules, cfg, split)
    if report is not None:
        say("  " + report["message"].replace("\n", "\n  "))


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


def staged_changes(ws: Path, index_file: Path | None = None) -> list[str]:
    """Paths whose staged content differs from HEAD (adds, edits, deletes)."""
    base = head_commit(ws) or empty_tree(ws)
    return split_z(git(ws, "diff", "--cached", "--name-only", "--no-renames", "-z", base,
                       index_file=index_file))


def staged_tree(ws: Path, rules: GateRules, index_file: Path | None = None) -> dict[str, str]:
    """sha256 of each gated file as staged in the index (all of them, local
    state included, so an accidental commit of the token shows up)."""
    records = []
    for rec in split_z(git(ws, "ls-files", "-z", "-s", index_file=index_file)):
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
    try:
        objects = read_objects(ws, [oid for _, oid in entries])
    except MissingObject as exc:
        raise PathViolation(f"unexpected object in the index: {exc}") from exc
    tree = {}
    for (rel, oid), (got, kind, data) in zip(entries, objects):
        if got != oid or kind != "blob":
            raise PathViolation(f"{rel}: unexpected object in the index")
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
    bind_recorded_git(ws)
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
        raw = committed_blob(ws, head, PINS_MIRROR_REL)
        data = json.loads(raw) if raw is not None else None
        genesis = data.get("genesis") if isinstance(data, dict) else None
    except TrustError:
        raise  # a forged mirror object is not "no genesis"
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
    return pinned_hooks_dir(ws)


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


def git_change_notes(current: dict | None, git_path: str) -> list[str]:
    """One line when the git on PATH is not the one `current` (trust.json)
    records (a missing record counts as different), otherwise none."""
    if current is None or current.get("git") == git_path:
        return []
    return [f"  git binary: old={current.get('git') or '-'}  new={git_path}"]


def install_hooks(ws: Path, repin: bool = False) -> dict:
    """The human ratification path (section 4). Returns a summary dict."""
    require_human_terminal()
    ws = ws.resolve()
    unbind_recorded_git(ws)  # the human records the git found on PATH right now
    configured = git(ws, "config", "core.hooksPath", check=False, work_tree=False,
                     hooks=False).decode().strip()
    if configured:
        raise KitError(f"core.hooksPath is set to {configured!r}; unset it (hook redirects "
                       "are not allowed)")
    require_sound_repository(ws, root_hint=True)  # A2, including "ws is the work tree root"

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
    git_path = path_git()
    git_notes = git_change_notes(current, git_path)
    changed = (current is None or any(delta.values()) or any(env_delta.values())
               or stale_checker or bool(git_notes))
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
        for note in git_notes or [f"  git binary: {git_path}"]:
            say(note)
        report_ignored_files(ws, rules, cfg)
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
                 "interpreter": current_interpreter(), "git": git_path}
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
        report_ignored_files(ws, rules, cfg)
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
