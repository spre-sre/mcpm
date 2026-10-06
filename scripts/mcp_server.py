#!/usr/bin/env python3
"""Crucible MCP server: the crucible for external agents, over JSON-RPC 2.0.

Transport: MCP 2024-11-05 over stdio. One JSON object per line on stdin, one
JSON object per line on stdout (flushed). stdout carries protocol messages
only: at startup the real stdout is duplicated into a private stream and file
descriptor 1 is pointed at stderr, so a stray print or a native library can
never corrupt the protocol. Diagnostics go to stderr.

Tools (exactly four), each a fixed subcommand of bin/crucible in JSON mode:
  crucible_status        bin/crucible status --json
  crucible_inspect_spec  bin/crucible spec --json
  crucible_verify        bin/crucible verify --json
  crucible_promote       bin/crucible promote --json --message=<msg>

The CLI in --json mode prints exactly one JSON object on stdout, so the server
reads no report files. Human-only actions (install-hooks, --repin, hook-*) have
no route here: the CLI is only invoked with the four fixed subcommands, as an
argv list (no shell), cwd = the workspace, stdin closed. The server never reads
or prints the signing secret. Standard library only.

Errors: -32700 parse error, -32600 invalid request, -32601 method not found,
-32602 invalid params (unknown tool, arguments not matching the schema, bad
message), -32603 internal error. A gate failure or CLI error is a tool RESULT
with isError true, not a JSON-RPC error.

Test hooks (environment, read at startup): CRUCIBLE_MCP_TIMEOUT_SECONDS
overrides every tool timeout; CRUCIBLE_MCP_GRACE_SECONDS overrides the
SIGTERM-to-SIGKILL grace period.
"""

from __future__ import annotations

import json
import math
import os
import re
import signal
import subprocess
import sys
from pathlib import Path



def _workspace() -> Path:
    """The repository the tools act on: `--workspace <dir>` when bin/crucible
    starts this server from the pinned snapshot, else the directory above this
    file."""
    args = sys.argv[1:]
    if len(args) == 2 and args[0] == "--workspace":
        return Path(args[1]).resolve()
    return Path(__file__).resolve().parent.parent


WS = _workspace()
PROTOCOL_VERSION = "2024-11-05"
SERVER_INFO = {"name": "crucible-mcp", "version": "1.0.0"}
LONG_TIMEOUT = 3 * 3600  # verify and promote
SHORT_TIMEOUT = 120      # status and spec
TERM_GRACE = 120.0       # seconds between SIGTERM and SIGKILL
MAX_LINE_BYTES = 4 * 1024 * 1024
MAX_MESSAGE_CHARS = 4096
OUTPUT_TAIL_CHARS = 8000

PARSE_ERROR, INVALID_REQUEST, METHOD_NOT_FOUND = -32700, -32600, -32601
INVALID_PARAMS, INTERNAL_ERROR = -32602, -32603

# git splits trailer lines on \n only, but refuse every Unicode line break.
_LINE_SPLIT = re.compile(r"[\r\n\v\f\x1c-\x1e\x85  ]")
_TRAILER_LINE = re.compile(r"^\s*crucible-attestation\s*:", re.IGNORECASE)


class RpcError(Exception):
    def __init__(self, code: int, message: str):
        super().__init__(message)
        self.code, self.message = code, message


def log(msg: str) -> None:
    text = f"crucible-mcp: {msg}".encode("utf-8", "backslashreplace").decode("utf-8")
    print(text, file=sys.stderr, flush=True)


def env_float(name: str) -> float | None:
    try:
        value = float(os.environ[name])
    except (KeyError, ValueError):
        return None
    return value if math.isfinite(value) and value > 0 else None


# --------------------------------------------------------------------------
# Tool schemas
# --------------------------------------------------------------------------

_NO_ARGS = {"type": "object", "properties": {}, "required": [],
            "additionalProperties": False}

TOOLS = [
    {"name": "crucible_status",
     "description": "Read-only crucible state: git HEAD and branch, working tree, "
                    "trusted-input pin status, installed hooks, and attestation token "
                    "state. Never changes anything.",
     "inputSchema": _NO_ARGS},
    {"name": "crucible_inspect_spec",
     "description": "Read-only: parse contracts/SPEC.md into structured JSON (invariants, "
                    "error codes, spec digest).",
     "inputSchema": _NO_ARGS},
    {"name": "crucible_verify",
     "description": "Run `bin/crucible verify`: trust check, Step 0 (AST gate, spec check) "
                    "and Tier 1. On success a verify token is issued. Takes minutes. On "
                    "failure the result names the failed stage and reasons.",
     "inputSchema": _NO_ARGS},
    {"name": "crucible_promote",
     "description": "Run `bin/crucible promote`: verify, Tier 2 telemetry gate, then commit "
                    "the gated tree to trunk through the hooks. COMMITS ON SUCCESS. Takes "
                    "minutes. `admitted` says whether a commit landed; isError is true "
                    "unless the CLI exited 0. The message must not contain a "
                    "Crucible-Attestation line: only the commit-msg hook writes that "
                    "trailer.",
     "inputSchema": {"type": "object",
                     "properties": {"message": {
                         "type": "string", "minLength": 1, "maxLength": MAX_MESSAGE_CHARS,
                         "description": "Commit message."}},
                     "required": ["message"], "additionalProperties": False}},
]
TOOL_SCHEMAS = {t["name"]: t["inputSchema"] for t in TOOLS}
# tool -> (CLI subcommand, timeout kind)
COMMANDS = {"crucible_status": ("status", "short"), "crucible_inspect_spec": ("spec", "short"),
            "crucible_verify": ("verify", "long"), "crucible_promote": ("promote", "long")}


def message_has_trailer_line(message: str) -> bool:
    return any(_TRAILER_LINE.match(line) for line in _LINE_SPLIT.split(message))


def validate_arguments(schema: dict, args: object) -> dict:
    """Check arguments against the tool schema. Raises RpcError(-32602)."""
    if not isinstance(args, dict):
        raise RpcError(INVALID_PARAMS, "arguments must be an object")
    props = schema["properties"]
    unknown = sorted(set(args) - set(props))
    if unknown:
        raise RpcError(INVALID_PARAMS, f"unknown argument(s): {', '.join(unknown)}")
    missing = [k for k in schema["required"] if k not in args]
    if missing:
        raise RpcError(INVALID_PARAMS, f"missing required argument(s): {', '.join(missing)}")
    for key, value in args.items():
        if not isinstance(value, str):
            raise RpcError(INVALID_PARAMS, f"argument {key} must be a string")
        try:
            value.encode("utf-8")
        except UnicodeEncodeError:
            raise RpcError(INVALID_PARAMS,
                           f"argument {key} is not valid Unicode text (lone surrogate)")
        if "\0" in value:
            raise RpcError(INVALID_PARAMS, f"argument {key} must not contain NUL")
        if not value.strip():
            raise RpcError(INVALID_PARAMS, f"argument {key} must not be blank")
        if len(value) > props[key]["maxLength"]:
            raise RpcError(INVALID_PARAMS,
                           f"argument {key} is longer than {props[key]['maxLength']} characters")
        if message_has_trailer_line(value):
            raise RpcError(INVALID_PARAMS, "argument message must not contain a "
                                           "Crucible-Attestation line: never write that "
                                           "trailer by hand")
    return args


# --------------------------------------------------------------------------
# CLI runner
# --------------------------------------------------------------------------


def tail(text: str, limit: int = OUTPUT_TAIL_CHARS) -> str:
    return text if len(text) <= limit else "...[truncated]...\n" + text[-limit:]


def strict_constant(name: str):
    raise ValueError(f"{name} is not valid JSON")


def parse_single_object(text: str) -> dict | None:
    """The one JSON object the CLI printed, or None if the output is anything else."""
    stripped = text.strip()
    if not stripped:
        return None
    try:
        value = json.loads(stripped, parse_constant=strict_constant)
        json.dumps(value, allow_nan=False)  # 1e999 parses to inf: refuse it
    except (ValueError, RecursionError):
        return None
    return value if isinstance(value, dict) else None


def run_cli(subcommand: str, extra: list[str], kind: str) -> tuple[int | None, str, str, bool]:
    """Run bin/crucible <subcommand> --json [extra] and return (exit code, stdout,
    stderr, timed_out). argv list, no shell, cwd = workspace, stdin closed. On
    timeout the whole process group gets SIGTERM, then SIGKILL after the grace."""
    timeout = env_float("CRUCIBLE_MCP_TIMEOUT_SECONDS") or \
        (LONG_TIMEOUT if kind == "long" else SHORT_TIMEOUT)
    grace = env_float("CRUCIBLE_MCP_GRACE_SECONDS") or TERM_GRACE
    cmd = [sys.executable, str(WS / "bin" / "crucible"), subcommand, "--json", *extra]
    log(f"run: {subcommand}")
    try:
        proc = subprocess.Popen(cmd, cwd=WS, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, start_new_session=True)
    except OSError as exc:
        return None, "", f"cannot start bin/crucible: {exc}", False
    timed_out = False
    try:
        out, err = proc.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        timed_out = True
        out, err = b"", b""
        for sig, wait in ((signal.SIGTERM, grace), (signal.SIGKILL, 10.0)):
            try:
                os.killpg(proc.pid, sig)
            except ProcessLookupError:
                pass
            try:
                out, err = proc.communicate(timeout=wait)
                break
            except subprocess.TimeoutExpired:
                continue
    return (None if timed_out else proc.returncode,
            out.decode("utf-8", errors="replace"), err.decode("utf-8", errors="replace"),
            timed_out)


def call_tool(params: dict) -> dict:
    name = params.get("name")
    if not isinstance(name, str) or name not in COMMANDS:
        raise RpcError(INVALID_PARAMS, f"unknown tool: {name!r}")
    args = validate_arguments(TOOL_SCHEMAS[name], params.get("arguments", {}))
    subcommand, kind = COMMANDS[name]
    extra = [f"--message={args['message']}"] if name == "crucible_promote" else []
    code, out, err, timed_out = run_cli(subcommand, extra, kind)
    if err.strip():
        log(f"cli stderr: {tail(err, 2000)}")
    if timed_out:
        payload, is_error = {"error": "timeout", "timed_out": True, "exit_code": None,
                             "command": subcommand,
                             "output_tail": tail(out + err)}, True
    else:
        parsed = parse_single_object(out)
        if parsed is None:
            payload, is_error = {"error": "bin/crucible output is not exactly one JSON object",
                                 "exit_code": code, "command": subcommand,
                                 "output_tail": tail(out + err)}, True
        else:
            payload, is_error = parsed, code != 0
    log(f"tool {name} done (isError={is_error})")
    return {"content": [{"type": "text", "text": json.dumps(payload, indent=2,
                                                            allow_nan=False)}],
            "isError": is_error}


# --------------------------------------------------------------------------
# JSON-RPC / MCP
# --------------------------------------------------------------------------


def handle_method(method: str, params: dict) -> dict:
    if method == "initialize":
        return {"protocolVersion": PROTOCOL_VERSION, "capabilities": {"tools": {}},
                "serverInfo": SERVER_INFO}
    if method == "ping":
        return {}
    if method == "tools/list":
        return {"tools": TOOLS}
    if method == "tools/call":
        return call_tool(params)
    raise RpcError(METHOD_NOT_FOUND, f"method not found: {method}")


def error_response(rpc_id, code: int, message: str) -> dict:
    return {"jsonrpc": "2.0", "id": rpc_id, "error": {"code": code, "message": message}}


def valid_id(value) -> bool:
    """A string, an integer (not a boolean, not a float), or null."""
    return value is None or (isinstance(value, (str, int)) and not isinstance(value, bool))


def handle_message(msg) -> dict | None:
    """One decoded JSON value -> the response object, or None (notification)."""
    if not isinstance(msg, dict):
        return error_response(None, INVALID_REQUEST,
                              "batch requests are not supported" if isinstance(msg, list)
                              else "request must be a JSON object")
    has_id = "id" in msg
    rpc_id = msg.get("id")
    if has_id and not valid_id(rpc_id):
        return error_response(None, INVALID_REQUEST, "id must be a string, an integer, or null")
    method = msg.get("method")
    if method is None and has_id and ("result" in msg or "error" in msg):
        return None  # a response to a server request; this server sends none
    if msg.get("jsonrpc") != "2.0" or not isinstance(method, str):
        return error_response(rpc_id if has_id else None, INVALID_REQUEST,
                              "jsonrpc must be '2.0' and method must be a string")
    if not has_id:
        return None  # notification: never answered
    try:
        params = msg.get("params", {})
        if not isinstance(params, dict):
            raise RpcError(INVALID_PARAMS, "params must be an object")
        return {"jsonrpc": "2.0", "id": rpc_id, "result": handle_method(method, params)}
    except RpcError as exc:
        return error_response(rpc_id, exc.code, exc.message)
    except Exception as exc:  # noqa: BLE001 - never crash; keep serving
        log(f"internal error in {method}: {exc!r}")
        return error_response(rpc_id, INTERNAL_ERROR, f"internal error: {type(exc).__name__}")


def reject_float(text: str):
    value = float(text)
    if not math.isfinite(value):
        raise ValueError(f"number {text[:20]} is out of range")
    return value


def handle_line(raw: bytes) -> dict | None:
    try:
        msg = json.loads(raw.decode("utf-8"), parse_constant=strict_constant,
                         parse_float=reject_float)
    except (UnicodeDecodeError, ValueError, RecursionError) as exc:
        return error_response(None, PARSE_ERROR, f"parse error: {exc}")
    return handle_message(msg)


def read_line(stream) -> bytes | None:
    """One line, or None at EOF. An oversized line is drained and refused."""
    raw = stream.readline(MAX_LINE_BYTES + 1)
    if not raw:
        return None
    if len(raw) > MAX_LINE_BYTES and not raw.endswith(b"\n"):
        while raw and not raw.endswith(b"\n"):
            raw = stream.readline(MAX_LINE_BYTES)
        return b"\xff"  # not valid UTF-8: becomes a parse error
    return raw


def main() -> int:
    # Reserve stdout for the protocol: keep a private copy of it and point
    # fd 1 (and sys.stdout) at stderr.
    proto = os.fdopen(os.dup(1), "wb")
    os.dup2(2, 1)
    sys.stdout = sys.stderr
    log(f"serving {WS} (protocol {PROTOCOL_VERSION})")
    stdin = sys.stdin.buffer
    while True:
        try:
            raw = read_line(stdin)
            if raw is None:
                return 0
            if not raw.strip():
                continue
            response = handle_line(raw)
            if response is not None:
                line = json.dumps(response, separators=(",", ":"), ensure_ascii=True,
                                  allow_nan=False)
                proto.write(line.encode("ascii") + b"\n")
                proto.flush()
        except BrokenPipeError:
            return 0
        except Exception as exc:  # noqa: BLE001 - keep serving
            log(f"unexpected error: {exc!r}")


if __name__ == "__main__":
    sys.exit(main())
