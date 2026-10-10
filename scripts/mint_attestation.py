#!/usr/bin/env python3
"""Crucible attestation: HMAC tokens, commit trailers, nonce ledger, hooks.

Standard library only. Pinned file; the hooks run the snapshot of this file
under <git-common-dir>/crucible/checker/, never the work-tree copy.

Token (.crucible/attestation.token, JSON, 0600): {"payload": {...}, "sig": hex}
with sig = HMAC-SHA256(secret, canonical(payload)). It binds every gated file
(sha256), the parent commit, an expiry and a random nonce, and it admits
exactly one commit (the post-commit hook records the nonce in the ledger).

Trailer: `Crucible-Attestation: v1 level= nonce= tree= parent= verdict= kid=
sig=` where sig = HMAC(secret, "v1|level|nonce|tree|parent|verdict|kid"). No
command mints a trailer: minting is reachable only from the commit-msg hook
and only with a pending token that pre-commit admitted.
"""

from __future__ import annotations

import contextlib
import hmac
import json
import re
import secrets
import signal
import sys
import time
from pathlib import Path

import verify_pins as vp
from verify_pins import KitError, TrustError, say

TOKEN_VERSION = 1
LEVELS = ("verify", "promotion")
VERIFY_VERDICTS = ("PASS",)
PROMOTION_VERDICTS = ("PROCEED_CANARY_RAMP", "PROCEED_NO_BASELINE")
TRAILER_KEY = "Crucible-Attestation"
# A line that looks like the trailer key: any leading whitespace, any case, any
# whitespace before the colon. Never allowed in a user-supplied message.
TRAILER_LINE_RE = re.compile(r"^\s*crucible-attestation\s*:", re.IGNORECASE)
TRAILER_FIELDS = ("level", "nonce", "tree", "parent", "verdict", "kid", "sig")
NONCE_RE = re.compile(r"^[0-9a-f]{32}$")
OID_RE = re.compile(r"^[0-9a-f]{40}$|^[0-9a-f]{64}$")
CLOCK_SKEW_SECONDS = 300
ZERO_OID = "0" * 40
SCISSORS_NOTICE = "Do not modify or remove the line above"


class AttestationError(KitError):
    """A token or trailer check failed."""


# ---------------------------------------------------------------------------
# Tokens
# ---------------------------------------------------------------------------

def sign_payload(payload: dict, secret: bytes) -> dict:
    return {"payload": payload,
            "sig": hmac.new(secret, vp.canonical(payload), "sha256").hexdigest()}


def build_payload(*, level: str, secret: bytes, parent: str | None, files: dict[str, str],
                  verdict: str, report: dict, ttl_seconds: int, tier2: dict | None,
                  now: float | None = None) -> dict:
    now = time.time() if now is None else now
    return {
        "v": TOKEN_VERSION, "level": level, "kid": vp.key_id(secret),
        "nonce": secrets.token_hex(16),
        "issued_at": vp.utc_iso(now), "expires_at": vp.utc_iso(now + ttl_seconds),
        "parent": parent, "files": dict(files), "tree_digest": vp.files_digest(files),
        "verdict": verdict, "report_sha256": vp.sha256_hex(vp.canonical(report)),
        "tier2": tier2,
    }


def check_token(token, secret: bytes, trust_kid: str, consumed: set[str], head: str | None,
                now: float | None = None) -> dict:
    """Authenticate a token and return its payload, or raise AttestationError:
    signature, kid binding, expiry, single use, parent == HEAD."""
    if not isinstance(token, dict) or set(token) != {"payload", "sig"} \
            or not isinstance(token["payload"], dict):
        raise AttestationError("token is malformed")
    payload = token["payload"]
    expected = hmac.new(secret, vp.canonical(payload), "sha256").hexdigest()
    if not hmac.compare_digest(expected, str(token["sig"])):
        raise AttestationError("token signature is invalid (the token was modified or "
                               "signed with another key)")
    try:
        if payload["v"] != TOKEN_VERSION or payload["level"] not in LEVELS:
            raise AttestationError("token has an unknown version or level")
        if payload["kid"] != trust_kid or payload["kid"] != vp.key_id(secret):
            raise AttestationError("token kid is not the pinned key id")
        allowed = PROMOTION_VERDICTS if payload["level"] == "promotion" else VERIFY_VERDICTS
        if payload["verdict"] not in allowed:
            raise AttestationError(f"verdict {payload['verdict']!r} is not valid for a "
                                   f"{payload['level']} token")
        issued, expires = vp.parse_iso(payload["issued_at"]), vp.parse_iso(payload["expires_at"])
        moment = time.time() if now is None else now
        if not issued - CLOCK_SKEW_SECONDS <= moment <= expires:
            raise AttestationError(f"token expired at {payload['expires_at']} "
                                   "(or was issued in the future)")
        if not isinstance(payload["nonce"], str) or not NONCE_RE.match(payload["nonce"]):
            raise AttestationError("token nonce is malformed")
        if payload["nonce"] in consumed:
            raise AttestationError("token nonce was already used for a commit (replay)")
        if payload["parent"] != head:
            raise AttestationError(f"token was issued on parent {str(payload['parent'])[:12]} "
                                   f"but HEAD is {str(head)[:12]} (stale token)")
        files = payload["files"]
        if not isinstance(files, dict) or payload["tree_digest"] != vp.files_digest(files):
            raise AttestationError("token file map does not match its tree digest")
    except (KeyError, TypeError, AttributeError) as exc:
        raise AttestationError(f"token payload is malformed: {exc!r}") from exc
    except KitError as exc:
        raise AttestationError(str(exc)) from exc
    return payload


def check_token_files(payload: dict, staged: dict[str, str]) -> None:
    """The token's gated files must equal the staged gated content exactly."""
    delta = vp.diff_maps(payload["files"], staged)
    if any(delta.values()):
        for line in vp.describe_diff("staged vs verified", delta):
            say(line)
        raise AttestationError("staged gated files differ from the verified tree "
                               "(modified, added or removed since verify)")


def token_path(ws: Path) -> Path:
    return ws / vp.TOKEN_REL


def read_token(ws: Path) -> dict:
    path = token_path(ws)
    if path.is_symlink() or not path.is_file():
        raise AttestationError(f"no attestation token at {vp.TOKEN_REL}; run python3 bin/crucible promote -m MSG (or verify)")
    try:
        return json.loads(vp.read_nofollow(path))
    except (OSError, ValueError, KitError) as exc:
        raise AttestationError(f"token is unreadable: {exc}") from exc


def write_token(ws: Path, token: dict) -> None:
    path = token_path(ws)
    path.parent.mkdir(parents=True, exist_ok=True)
    vp.write_private(path, (json.dumps(token, indent=2, sort_keys=True) + "\n").encode())


def remove_token(ws: Path) -> None:
    token_path(ws).unlink(missing_ok=True)


# ---------------------------------------------------------------------------
# Ledger and pending marker (inside STATE)
# ---------------------------------------------------------------------------

def consumed_nonces(state: Path) -> set[str]:
    return set(_ledger(state)["consumed"])


def _ledger(state: Path) -> dict:
    path = state / vp.LEDGER_FILE
    if not path.exists():
        return {"consumed": [], "promotions": []}
    try:
        data = json.loads(path.read_text())
        return {"consumed": [str(n) for n in data["consumed"]],
                "promotions": [str(c) for c in data.get("promotions", [])]}
    except (OSError, ValueError, KeyError, TypeError) as exc:
        raise TrustError(f"nonce ledger {path} is unreadable: {exc}") from exc


def _write_ledger(state: Path, ledger: dict) -> None:
    vp.write_private(state / vp.LEDGER_FILE, json.dumps(ledger).encode())


def consume_nonce(state: Path, nonce: str, promotion_commit: str | None = None) -> None:
    """Record a used nonce; for a promotion-level commit also record the commit
    (the history of admitted promotions decides whether a baseline-less Tier 2
    can ever be legitimate again)."""
    ledger = _ledger(state)
    ledger["consumed"] = sorted(set(ledger["consumed"]) | {nonce})
    if promotion_commit:
        ledger["promotions"] = sorted(set(ledger["promotions"]) | {promotion_commit})
    _write_ledger(state, ledger)


def promotion_commits(state: Path) -> list[str]:
    return _ledger(state)["promotions"]


def read_pending(state: Path) -> dict | None:
    path = state / vp.PENDING_FILE
    if not path.is_file():
        return None
    try:
        return json.loads(path.read_text())
    except (OSError, ValueError):
        return None


def write_pending(state: Path, token: dict) -> None:
    vp.write_private(state / vp.PENDING_FILE, json.dumps(token, sort_keys=True).encode())


# ---------------------------------------------------------------------------
# Commit messages and trailers
# ---------------------------------------------------------------------------

def message_has_trailer_line(message: str) -> bool:
    """True when any line looks like the attestation trailer. splitlines()
    splits on more separators than git does, so this errs wide."""
    return any(TRAILER_LINE_RE.match(line) for line in message.splitlines()) \
        or any(TRAILER_LINE_RE.match(line) for line in message.split("\n"))


def reject_trailer_in_message(message: str) -> None:
    if message_has_trailer_line(message):
        raise AttestationError(
            f"the commit message contains a {TRAILER_KEY} line; never write one by hand "
            "(only the commit-msg hook signs and adds it). If this is an amend or a message "
            "copied from an attested commit, remove the old line: the hook signs a fresh one")


def trailer_message(fields: dict) -> bytes:
    return "|".join(["v1", fields["level"], fields["nonce"], fields["tree"], fields["parent"],
                     fields["verdict"], fields["kid"]]).encode()


def trailer_value(secret: bytes, level: str, nonce: str, tree: str, parent: str,
                  verdict: str) -> str:
    fields = {"level": level, "nonce": nonce, "tree": tree, "parent": parent,
              "verdict": verdict, "kid": vp.key_id(secret)}
    sig = hmac.new(secret, trailer_message(fields), "sha256").hexdigest()
    return (f"v1 level={level} nonce={nonce} tree={tree} parent={parent} "
            f"verdict={verdict} kid={fields['kid']} sig={sig}")


def trailer_fields(line: str) -> dict | None:
    """The fields of one `Crucible-Attestation: v1 k=v ...` line, or None when
    it is malformed (wrong version, unknown or duplicate or missing key)."""
    if ":" not in line:
        return None
    parts = line.split(":", 1)[1].split()
    if not parts or parts[0] != "v1":
        return None
    out: dict[str, str] = {}
    for part in parts[1:]:
        key, sep, value = part.partition("=")
        if not sep or key in out or key not in TRAILER_FIELDS or not value:
            return None
        out[key] = value
    return out if set(out) == set(TRAILER_FIELDS) else None


def parse_trailer(message: str, repo: Path) -> dict | None:
    """The fields of the one real trailer, or None. Git decides what the
    trailer block is: this runs `git interpret-trailers --parse`, so
    core.commentChar, the `---` divider and comment blocks are read exactly as
    when the hook inserted it. Text after a divider and earlier paragraphs are
    not trailers. More than one attestation trailer means None, never a guess."""
    proc = vp.run_git(repo, ["interpret-trailers", "--parse"], input_bytes=message.encode())
    if proc.returncode != 0:
        return None
    lines = [line for line in proc.stdout.decode(errors="replace").split("\n")
             if TRAILER_LINE_RE.match(line)]
    return trailer_fields(lines[0]) if len(lines) == 1 else None


def scissors_re(ws: Path) -> re.Pattern:
    out = vp.git(ws, "config", "--get", "core.commentChar", check=False).decode().strip()
    char = r"\S+" if out == "auto" else re.escape(out or "#")
    return re.compile(rf"^{char} -{{24}} >8 -{{24}}$")


def _stripspace(ws: Path, text: str, strip_comments: bool) -> str:
    args = ["stripspace", *(["--strip-comments"] if strip_comments else [])]
    return vp.git(ws, *args, input_bytes=text.encode()).decode(errors="replace")


def stored_message_forms(ws: Path, text: str) -> list[str]:
    """Every form the commit message can take after `git commit` cleans up the
    file the hook saw: raw, stripspace, stripspace --strip-comments, and, for a
    scissors line, the cut text (a user-typed scissors line may be kept or
    cut by the cleanup mode, so both readings are included; the line that
    `commit -v` writes is always a cut)."""
    lines = text.split("\n")
    scissors = scissors_re(ws)
    cut_at = next((i for i, line in enumerate(lines) if scissors.match(line)), None)
    if cut_at is None:
        sources = [text]
    else:
        cut = "\n".join(lines[:cut_at]) + "\n"
        by_git = cut_at + 1 < len(lines) and SCISSORS_NOTICE in lines[cut_at + 1]
        sources = [cut] if by_git else [text, cut]
    forms: list[str] = []
    for source in sources:
        for form in (source, _stripspace(ws, source, False), _stripspace(ws, source, True)):
            if form not in forms:
                forms.append(form)
    return forms


def refuse_trailer_commands(ws: Path) -> None:
    """interpret-trailers runs a configured trailer command. The A2 check reads only
    local config, so read every scope here (global, system, GIT_CONFIG_GLOBAL) and
    refuse before the command can run."""
    keys = vp.config_keys(ws, r"^trailer\..+\.(cmd|command)$")  # a failed read raises: fails closed
    if keys:
        raise AttestationError(
            f"git config sets {', '.join(keys)}; git would run it while adding the trailer. "
            "Remove the trailer command from your git configuration (any scope) and retry")


def insert_trailer(ws: Path, msg_file: Path, value: str) -> None:
    """Add the signed trailer with `git interpret-trailers --in-place
    --if-exists add`, then check that git still finds exactly this trailer in
    every stored form of the message. Otherwise restore the file and raise: a
    commit whose trailer git cannot find would be refused by pre-push for good."""
    refuse_trailer_commands(ws)
    original = msg_file.read_bytes()
    vp.git(ws, "interpret-trailers", "--in-place", "--if-exists", "add",
           "--trailer", f"{TRAILER_KEY}: {value}", str(msg_file))
    expected = trailer_fields(f"{TRAILER_KEY}: {value}")
    for form in stored_message_forms(ws, msg_file.read_text(errors="replace")):
        if parse_trailer(form, ws) != expected:
            msg_file.write_bytes(original)
            raise AttestationError(
                f"git would not find the {TRAILER_KEY} trailer in this message after its "
                "cleanup (a subject that starts with '---', or a scissors line, hides it); "
                "reword the message")


def check_trailer_structure(fields: dict | None, tree: str, parent: str | None) -> str:
    """Structure only (no secret needed): the trailer exists, is well formed,
    and names this tree and first parent. `parent` None means a root commit.
    Returns the level."""
    if not fields:
        raise AttestationError("no Crucible-Attestation trailer (committed with --no-verify, "
                               "or outside the hooks?)")
    if fields["level"] not in LEVELS:
        raise AttestationError("trailer level is unknown")
    if not NONCE_RE.match(fields["nonce"]) or not OID_RE.match(fields["tree"]) \
            or not (OID_RE.match(fields["parent"]) or fields["parent"] == "0") \
            or not re.match(r"^[0-9a-f]{16}$", fields["kid"]) \
            or not re.match(r"^[0-9a-f]{64}$", fields["sig"]):
        raise AttestationError("Crucible-Attestation trailer is malformed")
    if fields["tree"] != tree:
        raise AttestationError(f"trailer attests tree {fields['tree'][:12]} but the commit has "
                               f"tree {tree[:12]}")
    want = parent if parent else "0"
    if fields["parent"] != want:
        raise AttestationError(f"trailer was made on parent {fields['parent'][:12]} but the "
                               f"commit's parent is {want[:12]}")
    return fields["level"]


def check_trailer(fields: dict | None, tree: str, secret: bytes, parent: str | None) -> str:
    """Full check (needs the secret): structure, kid, signature. Returns level."""
    level = check_trailer_structure(fields, tree, parent)
    if fields["kid"] != vp.key_id(secret):
        raise AttestationError("trailer was signed with a different key")
    expected = hmac.new(secret, trailer_message(fields), "sha256").hexdigest()
    if not hmac.compare_digest(expected, fields["sig"]):
        raise AttestationError("trailer signature is invalid (the message was edited)")
    return level


def commit_tree_oid(ws: Path, commit: str) -> str:
    return vp.read_commit(ws, commit).tree


def commit_first_parent(ws: Path, commit: str) -> str | None:
    parents = vp.read_commit(ws, commit).parents
    return parents[0] if parents else None


def commit_message(ws: Path, commit: str) -> str:
    """The commit message as stored, parsed from the raw commit object (A4.2):
    `git log` would re-encode it and could run a configured signature program."""
    return vp.read_commit(ws, commit).message.decode("utf-8", errors="replace")


def verify_commit_trailer(ws: Path, secret: bytes, commit: str) -> str:
    """Full trailer check of one commit against its own tree and first parent."""
    fields = parse_trailer(commit_message(ws, commit), ws)
    return check_trailer(fields, commit_tree_oid(ws, commit), secret,
                         commit_first_parent(ws, commit))


def commit_paths(ws: Path, commit: str) -> list[str]:
    """Paths a commit changes against its first parent (all paths for a root
    commit). -z output is never quoted."""
    parent = commit_first_parent(ws, commit)
    if parent:
        out = vp.git(ws, "diff", "--name-only", "--no-renames", "-z", parent, commit)
    else:
        out = vp.git(ws, "ls-tree", "-r", "--name-only", "-z", commit)
    return vp.split_z(out)


# ---------------------------------------------------------------------------
# Hooks
# ---------------------------------------------------------------------------

def hook_context(ws: Path):
    """(state, trust, secret, cfg, rules): the pinned anchor, or TrustError. The
    hook's environment was checked by vp.hook_workspace before this."""
    return vp.require_anchor(ws)


def hook_pre_commit(ws: Path, index: Path | None = None) -> int:
    """`index` is the validated GIT_INDEX_FILE of the commit (vp.hook_workspace),
    or None: every index read below uses it, so `git commit <paths>` and
    `git commit -a` are judged on the tree git will commit."""
    try:
        state, trust, secret, _cfg, rules = hook_context(ws)
    except KitError as exc:
        say(f"COMMIT REJECTED: {exc}")
        return 1
    (state / vp.PENDING_FILE).unlink(missing_ok=True)  # a marker from an aborted commit is stale
    try:
        changed = vp.staged_changes(ws, index)
        vp.reject_non_ascii(list(changed) + vp.index_listing(ws, index)
                            + vp.work_tree_listing(ws, index), "the index or work tree")
        forbidden = sorted(p for p in changed if vp.is_local_state(p))
        if forbidden:
            raise AttestationError(f"local attestation state must never be committed: {forbidden}")
        gated = sorted(p for p in changed if rules.is_gated(p))
        if not gated:
            say("no gated paths changed; no attestation needed")
            return 0
        say(f"{vp.plural(len(gated), 'gated path')} changed: {', '.join(gated[:6])}"
            + (" ..." if len(gated) > 6 else ""))
        token = read_token(ws)
        payload = check_token(token, secret, trust["kid"], consumed_nonces(state),
                              vp.head_commit(ws))
        staged = vp.staged_tree(ws, rules, index)
        check_token_files(payload, staged)
        vp.require_pins(trust, staged, rules, "staged")
    except (KitError, OSError) as exc:
        say(f"COMMIT REJECTED: {exc}")
        say("run python3 bin/crucible promote -m MSG (or verify, stage exactly the verified files, then commit)")
        return 1
    write_pending(state, token)
    say(f"attestation VALID ({payload['level']}, tree {payload['tree_digest'][:16]}); "
        "commit admitted")
    return 0


def hook_commit_msg(ws: Path, msg_file: Path, index: Path | None = None) -> int:
    """Append the signed trailer when pre-commit admitted a token."""
    try:
        state, trust, secret, _cfg, rules = hook_context(ws)
    except KitError as exc:
        say(f"COMMIT REJECTED: {exc}")
        return 1
    pending = read_pending(state)
    if pending is None:
        return 0  # no gated change: no attestation to record
    text = msg_file.read_text(errors="replace")
    if not [ln for ln in text.splitlines() if ln.strip() and not ln.startswith("#")]:
        return 0  # empty message: let git abort the commit as usual
    try:
        reject_trailer_in_message(text)
    except AttestationError as exc:
        say(f"COMMIT REJECTED: {exc}")
        return 1
    try:
        # Re-check everything against the index as it is now. Git fixes the commit's tree
        # right after pre-commit, so this read guards against an index swapped between
        # pre-commit's re-read and here. The token comes from the pending copy in STATE,
        # not from the work tree.
        head = vp.head_commit(ws)
        payload = check_token(pending, secret, trust["kid"], consumed_nonces(state), head)
        # One index read (G14): write-tree first, then the check and the signature use the
        # tree id it printed. A second read of the index (staged_tree, then write-tree) would
        # let a process that swaps .git/index between them get a trailer for an unverified tree.
        # git smudges a racily clean entry through the clean filter whenever it writes an
        # index, and this hook runs with the clean environment (the caller's -c pairs are
        # gone), so the user's own filter driver is switched off here as well.
        tree = vp.git(ws, "write-tree", index_file=index,
                      config=vp.filter_off_config(ws)).decode().strip()
        staged = vp.gated_tree_map(ws, tree, rules)
        check_token_files(payload, staged)
        vp.require_pins(trust, staged, rules, "staged")
        value = _mint(secret, payload, tree, head)
        insert_trailer(ws, msg_file, value)
    except (KitError, OSError, ValueError) as exc:
        say(f"COMMIT REJECTED: cannot sign the attestation trailer: {exc}")
        return 1
    say(f"signed {TRAILER_KEY} trailer: level={payload['level']} tree={tree[:12]}")
    return 0


def _mint(secret: bytes, payload: dict, tree: str, head: str | None) -> str:
    """Private: build the trailer value for an admitted payload. Called only by
    hook_commit_msg; there is no command that reaches it."""
    return trailer_value(secret, payload["level"], payload["nonce"], tree, head or "0",
                         payload["verdict"])


def hook_post_commit(ws: Path) -> int:
    try:
        state, trust, secret, _cfg, _rules = hook_context(ws)
    except KitError as exc:
        say(f"post-commit: {exc}")
        return 1
    pending = read_pending(state)
    if pending is None:
        return 0
    nonce = pending["payload"]["nonce"]
    promoted = vp.head_commit(ws) if pending["payload"].get("level") == "promotion" else None
    consume_nonce(state, nonce, promoted)
    try:
        if read_token(ws)["payload"]["nonce"] == nonce:
            remove_token(ws)
    except (KitError, KeyError, TypeError):
        pass
    (state / vp.PENDING_FILE).unlink(missing_ok=True)
    head = vp.head_commit(ws) or ""
    try:
        fields = parse_trailer(commit_message(ws, "HEAD"), ws)
        if not fields or fields["tree"] != commit_tree_oid(ws, "HEAD"):
            say("WARNING: HEAD's trailer tree does not match HEAD's tree "
                "(the commit was changed after signing); pre-push will refuse it")
            return 1
    except KitError as exc:
        say(f"WARNING: cannot verify the trailer of {head[:7]}: {exc}")
        return 1
    say(f"attestation consumed by commit {head[:7]} (single use)")
    return 0


def _is_ancestor(ws: Path, older: str, newer: str) -> bool:
    return vp.git_ok(ws, "merge-base", "--is-ancestor", f"{older}^{{commit}}", f"{newer}^{{commit}}")


def check_push_line(ws: Path, genesis: str | None, local_ref: str, local_sha: str,
                    remote_ref: str, remote_sha: str) -> tuple[list[str], list[str]]:
    """(problems, commits to check) for one pre-push line. A remote ref that
    already exists may only move forward (the remote's tip must be an ancestor
    of what is pushed), and a pushed commit set may hold no root commit: a
    rewritten or foreign history is not admitted."""
    problems: list[str] = []
    exclude: list[str] = []
    if set(remote_sha) != {"0"}:
        if not vp.git_ok(ws, "cat-file", "-e", f"{remote_sha}^{{commit}}") \
                or not _is_ancestor(ws, remote_sha, local_sha):
            return [f"{local_ref} -> {remote_ref}: not a fast-forward of the remote tip "
                    f"{remote_sha[:12]} (history rewrite or force push)"], []
        exclude = [f"^{remote_sha}"]
    if genesis:
        exclude = [f"^{genesis}", *exclude]
    commits = vp.git(ws, "rev-list", local_sha, *exclude).decode().split()
    if genesis:
        roots = vp.git(ws, "rev-list", "--max-parents=0", local_sha, *exclude).decode().split()
        if roots:
            problems.append(f"{local_ref}: root commit {roots[0][:12]} is not reachable from the "
                            f"pinned genesis {genesis[:12]}")
    return problems, commits


def hook_pre_push(ws: Path, remote: str, lines: list[str]) -> int:
    """Refuse to push gated commits without a valid promotion trailer, to
    update an existing remote ref by anything but a fast-forward, to delete a
    protected ref, or to push a root commit that is not part of the history
    reachable from genesis. Deleting any other ref is allowed."""
    bad: list[str] = []
    checked = 0
    try:
        _state, trust, secret, cfg, rules = hook_context(ws)
        genesis = trust.get("genesis")
        protected = {ref.lower() for ref in cfg["gate"]["protected_refs"]}
        for line in lines:
            parts = line.split()
            if len(parts) != 4:
                continue
            local_ref, local_sha, remote_ref, remote_sha = parts
            if set(local_sha) == {"0"}:  # a deletion
                if remote_ref.lower() in protected:
                    bad.append(f"{remote_ref}: deleting a protected ref is refused (it would "
                               "allow the branch to be recreated at an older commit); "
                               "[gate] protected_refs in .crucible/config.toml lists them")
                continue
            problems, commits = check_push_line(ws, genesis, local_ref, local_sha, remote_ref,
                                                remote_sha)
            bad += problems
            for commit in commits:
                paths = commit_paths(ws, commit)
                try:
                    vp.reject_non_ascii(paths, f"commit {commit[:12]}")
                except vp.PathViolation as exc:
                    bad.append(f"{commit[:12]} ({local_ref}): {exc}")
                    continue
                if not any(rules.is_gated(p) for p in paths):
                    continue
                checked += 1
                try:
                    level = verify_commit_trailer(ws, secret, commit)
                    if level != "promotion":
                        raise AttestationError(f"attestation level is '{level}'; pushing "
                                               "requires a promotion (python3 bin/crucible promote)")
                except KitError as exc:
                    bad.append(f"{commit[:12]} ({local_ref}): {exc}")
    except KitError as exc:
        say(f"PUSH REJECTED: {exc}")
        return 1
    if bad:
        say("PUSH REJECTED: unsafe update, or gated commits without a valid Crucible "
            "promotion attestation:")
        for entry in bad:
            say(f"  {entry}")
        return 1
    say(f"push admitted: {vp.plural(checked, 'gated commit')} checked, all with valid "
        "promotion attestations")
    return 0


# ---------------------------------------------------------------------------
# Signals
# ---------------------------------------------------------------------------

def exit_on_sigterm():
    """Turn the FIRST SIGTERM into SystemExit(143) so `finally` blocks run.
    Later SIGTERMs are ignored by the handler so cleanup is not interrupted.
    Returns a function that restores the previous handler. No-op off the main
    thread."""
    fired: list[int] = []

    def handler(signum, _frame):
        if not fired:
            fired.append(signum)
            sys.exit(128 + signum)

    try:
        previous = signal.signal(signal.SIGTERM, handler)
    except ValueError:
        return lambda: None
    return lambda: signal.signal(signal.SIGTERM, previous)


@contextlib.contextmanager
def defer_sigterm():
    """Hold a SIGTERM that arrives inside the block and raise SystemExit(143)
    when the block ends. The admission commands (`git add`, `git commit`, and
    the hooks inside it) must not be cut short: killing an in-flight git would
    leave index.lock and an unconsumed nonce."""
    held: list[int] = []
    try:
        previous = signal.signal(signal.SIGTERM, lambda signum, _frame: held.append(signum))
    except ValueError:
        yield
        return
    try:
        yield
    finally:
        signal.signal(signal.SIGTERM, previous)
        if held:
            sys.exit(128 + held[0])


if __name__ == "__main__":
    sys.exit("mint_attestation.py is a library; trailers are minted only by the commit-msg hook")
