#!/usr/bin/env python3
"""Crucible Tier 2 statistics: standard library only, pure, deterministic.

Public functions (none of them mutates its inputs):

  percentile(xs, q)                      numpy "linear" interpolation, q in [0, 1]
  paired_bootstrap_drift_ci(base, cand)  paired percentile bootstrap of the drift
  hierarchical_bootstrap_drift_ci(...)   two-stage paired bootstrap over rounds, then samples
  ols_slope(ys)                          least-squares slope with a Student t p-value
  evaluate_gate(...)                     the Tier 2 verdict (architecture.md, section 8)

Determinism: every random draw comes from random.Random(seed), so a fixed
input and a fixed seed give a fixed result.

Fail-closed rules:
  * Bad samples (too few, unequal lengths, NaN/inf, negative, a baseline
    percentile <= 0) give REJECT_INVALID_SAMPLES, never a pass.
  * Drift is cand/base - 1. If a bootstrap replicate has a baseline percentile
    <= 0 the drift of that replicate is undefined; the CI then reports
    lower = upper = +inf, which makes the tail and typical rules reject-leaning.
    If the point estimate itself has a baseline percentile <= 0, the point is
    +inf too (paired_bootstrap_drift_ci returns (inf, inf, inf)).

CLI (optional):  python3 telemetry_math.py --base base.json --cand cand.json
  Each file is a driver output {"latencies_ns": [...], "memory_bytes": [...]}.
  Prints the evaluate_gate result as JSON. Exit 0 = PROCEED_CANARY_RAMP,
  1 = any reject, 2 = bad input.
"""

from __future__ import annotations

import argparse
import heapq
import json
import math
import random
import sys
from operator import itemgetter

PROCEED = "PROCEED_CANARY_RAMP"
REJECT_LATENCY = "REJECT_LATENCY_REGRESSION"
REJECT_MEMORY_LEAK = "REJECT_MEMORY_LEAK"
REJECT_LEAK = REJECT_MEMORY_LEAK
REJECT_INVALID = "REJECT_INVALID_SAMPLES"

DEFAULT_CONFIG = {
    "p99_drift_max": 0.02,
    "p50_drift_max": 0.02,
    "bootstrap_samples": 1000,
    "ci_level": 0.95,
    "leak_slope_max": 1024.0,
    "leak_alpha": 0.05,
    "min_samples": 1000,
}

MIN_OLS_POINTS = 3
MIN_ROUNDS = 3
ROUNDS_REASON = "need >= 3 rounds for between-process variance"
CI_METHOD_FLAT = "paired bootstrap over samples"
CI_METHOD_ROUNDS = "two-stage paired bootstrap over rounds"
MAX_QUANTUM_RATIO = 0.005
MIN_DISTINCT_FRACTION = 0.20
COARSE_REASON = ("driver resolution too coarse: report per-call means over longer batches")
# Selection (heap) beats a full sort only when few order statistics are needed.
_SELECTION_FRACTION = 0.10


# ---------------------------------------------------------------------------
# Percentiles
# ---------------------------------------------------------------------------

def _linear_position(count: int, q: float) -> tuple[int, int, float]:
    """Return (lower index, upper index, fraction) of the numpy linear method."""
    position = q * (count - 1)
    lower = int(math.floor(position))
    upper = min(lower + 1, count - 1)
    return lower, upper, position - lower


def _interpolate(low_value: float, high_value: float, fraction: float) -> float:
    """Interpolate, staying exact (and nan-free) at equal values and infinities."""
    if fraction == 0.0 or low_value == high_value:
        return low_value
    return low_value + (high_value - low_value) * fraction


def _check_quantile(q: float) -> None:
    if isinstance(q, bool) or not isinstance(q, (int, float)) or not 0.0 <= q <= 1.0:
        raise ValueError(f"q must be a number in [0, 1], got {q!r}")


def percentile(xs, q: float) -> float:
    """Percentile of xs at fraction q (0.99 = p99), numpy "linear" method."""
    _check_quantile(q)
    ordered = sorted(xs)
    if not ordered:
        raise ValueError("percentile of an empty sequence")
    if any(value != value for value in ordered):
        raise ValueError("percentile input contains NaN")
    lower, upper, fraction = _linear_position(len(ordered), q)
    return _interpolate(ordered[lower], ordered[upper], fraction)


def _percentile_unchecked(values: list, q: float) -> float:
    """Percentile of a list known to be finite; picks a fast selection method.

    Needs only the order statistics at positions lower and upper. When that is
    a small tail (p99, p1) a heap selection avoids sorting everything.
    """
    count = len(values)
    lower, upper, fraction = _linear_position(count, q)
    limit = count * _SELECTION_FRACTION
    top_needed = count - lower
    bottom_needed = upper + 1
    if top_needed <= limit:
        top = heapq.nlargest(top_needed, values)  # descending
        low_value = top[-1]
        high_value = top[-2] if upper > lower else top[-1]
    elif bottom_needed <= limit:
        bottom = heapq.nsmallest(bottom_needed, values)  # ascending
        high_value = bottom[-1]
        low_value = bottom[-2] if upper > lower else bottom[-1]
    else:
        ordered = sorted(values)
        low_value, high_value = ordered[lower], ordered[upper]
    return _interpolate(low_value, high_value, fraction)


# ---------------------------------------------------------------------------
# Paired bootstrap
# ---------------------------------------------------------------------------

def _gather(values: list, indices: list) -> list:
    if len(indices) > 1:
        return list(itemgetter(*indices)(values))
    return [values[index] for index in indices]


def _drift(base_value: float, cand_value: float) -> float:
    """cand/base - 1, or +inf (fail closed) when the baseline value is <= 0."""
    if base_value <= 0.0:
        return math.inf
    return cand_value / base_value - 1.0


def _validate_series(name: str, values, *, require_nonempty: bool = True) -> list:
    series = [float(value) for value in values]
    if require_nonempty and not series:
        raise ValueError(f"{name} is empty")
    if any(not math.isfinite(value) for value in series):
        raise ValueError(f"{name} contains NaN or infinite values")
    return series


def _flat_replicates(base: list, cand: list, replicates: int, seed: int):
    """Yield (base_sample, cand_sample): one index vector applied to BOTH arrays."""
    count = len(base)
    rng = random.Random(seed)
    population = range(count)
    for _ in range(replicates):
        indices = rng.choices(population, k=count)
        yield _gather(base, indices), _gather(cand, indices)


def _round_replicates(base_rounds: list, cand_rounds: list, replicates: int, seed: int):
    """Two-stage paired (hierarchical) replicates over rounds.

    Stage 1 draws round indices with replacement; the same index picks the
    paired A and B round (the independent unit is the round = process pair).
    Stage 2 draws sample indices with replacement inside each chosen round,
    one index vector applied to the A and the B series of that round. The
    chosen rounds are concatenated.
    """
    rng = random.Random(seed)
    round_count = len(base_rounds)
    round_population = range(round_count)
    sample_populations = [range(len(series)) for series in base_rounds]
    for _ in range(replicates):
        base_sample: list = []
        cand_sample: list = []
        for chosen in rng.choices(round_population, k=round_count):
            indices = rng.choices(sample_populations[chosen], k=len(base_rounds[chosen]))
            base_sample.extend(_gather(base_rounds[chosen], indices))
            cand_sample.extend(_gather(cand_rounds[chosen], indices))
        yield base_sample, cand_sample


def _bootstrap_drifts(base: list, cand: list, quantiles: tuple, replicates: int,
                      level: float, seed: int, rounds: tuple | None = None) -> dict:
    """Shared engine. Returns {q: (point, lower, upper)} for every q in quantiles.

    Without `rounds`: one index vector per replicate over the flat arrays.
    With rounds = (base_rounds, cand_rounds): the two-stage paired bootstrap.
    The point estimate is always computed on the full concatenated data. All
    quantiles share the same replicates, so the CIs are consistent.
    """
    points = {q: _drift(_percentile_unchecked(base, q), _percentile_unchecked(cand, q))
              for q in quantiles}
    if rounds is None:
        samples = _flat_replicates(base, cand, replicates, seed)
    else:
        samples = _round_replicates(rounds[0], rounds[1], replicates, seed)
    replicate_drifts = {q: [] for q in quantiles}
    undefined = {q: False for q in quantiles}
    for base_sample, cand_sample in samples:
        for q in quantiles:
            base_value = _percentile_unchecked(base_sample, q)
            if base_value <= 0.0:
                undefined[q] = True
            replicate_drifts[q].append(
                _drift(base_value, _percentile_unchecked(cand_sample, q)))
    tail = (1.0 - level) / 2.0
    result = {}
    for q in quantiles:
        if not math.isfinite(points[q]):
            result[q] = (math.inf, math.inf, math.inf)
            continue
        if undefined[q]:
            result[q] = (points[q], math.inf, math.inf)
            continue
        drifts = replicate_drifts[q]
        result[q] = (points[q], percentile(drifts, tail), percentile(drifts, 1.0 - tail))
    return result


def _check_bootstrap_args(q_values: tuple, replicates, level) -> None:
    for q in q_values:
        _check_quantile(q)
    if isinstance(replicates, bool) or not isinstance(replicates, int) or replicates < 1:
        raise ValueError("b must be a positive integer")
    if isinstance(level, bool) or not isinstance(level, (int, float)) or not 0.0 < level < 1.0:
        raise ValueError("level must be in (0, 1)")


def paired_bootstrap_drift_ci(base, cand, q: float = 0.99, b: int = 1000,
                              level: float = 0.95, seed: int = 0):
    """Return (point, lower, upper) for drift = pct(cand, q) / pct(base, q) - 1.

    Each of the b replicates draws ONE index vector (with replacement) and
    applies it to both arrays, so the pairing sample[i] <-> sample[i] holds.
    The CI is the percentile interval of the replicate drifts at `level`.

    Raises ValueError for empty, unequal, NaN or infinite inputs.
    A baseline percentile <= 0 at the point returns (inf, inf, inf); in any
    replicate it returns (point, inf, inf). Either way the gate leans to reject.
    """
    _check_bootstrap_args((q,), b, level)
    base_list = _validate_series("base", base)
    cand_list = _validate_series("cand", cand)
    if len(base_list) != len(cand_list):
        raise ValueError("paired bootstrap needs equal-length arrays")
    return _bootstrap_drifts(base_list, cand_list, (q,), b, level, seed)[q]


def hierarchical_bootstrap_drift_ci(base_rounds, cand_rounds, q: float = 0.99,
                                    b: int = 1000, level: float = 0.95, seed: int = 0):
    """Return (point, lower, upper) with the two-stage paired bootstrap over rounds.

    base_rounds / cand_rounds: lists of per-round lists, same number of rounds
    (>= 2) and equal length per round. See _round_replicates. Raises ValueError
    for bad shapes or non-finite values.
    """
    _check_bootstrap_args((q,), b, level)
    base_list = [_validate_series("base round", r) for r in base_rounds]
    cand_list = [_validate_series("cand round", r) for r in cand_rounds]
    if len(base_list) < 2 or len(base_list) != len(cand_list) or any(
            len(x) != len(y) for x, y in zip(base_list, cand_list)):
        raise ValueError("rounds must pair up: same count (>= 2) and equal lengths")
    flat_base = [v for r in base_list for v in r]
    flat_cand = [v for r in cand_list for v in r]
    return _bootstrap_drifts(flat_base, flat_cand, (q,), b, level, seed,
                             rounds=(base_list, cand_list))[q]


# ---------------------------------------------------------------------------
# Student t / OLS
# ---------------------------------------------------------------------------

def _betacf(a: float, b: float, x: float) -> float:
    """Continued fraction for the incomplete beta function (modified Lentz)."""
    tiny = 1e-300
    qab, qap, qam = a + b, a + 1.0, a - 1.0
    c = 1.0
    d = 1.0 - qab * x / qap
    d = 1.0 / (d if abs(d) > tiny else tiny)
    h = d
    for m in range(1, 1000):
        m2 = 2 * m
        step = m * (b - m) * x / ((qam + m2) * (a + m2))
        d = 1.0 + step * d
        d = 1.0 / (d if abs(d) > tiny else tiny)
        c = 1.0 + step / c
        c = c if abs(c) > tiny else tiny
        h *= d * c
        step = -(a + m) * (qab + m) * x / ((a + m2) * (qap + m2))
        d = 1.0 + step * d
        d = 1.0 / (d if abs(d) > tiny else tiny)
        c = 1.0 + step / c
        c = c if abs(c) > tiny else tiny
        change = d * c
        h *= change
        if abs(change - 1.0) < 1e-15:
            break
    return h


def regularized_incomplete_beta(a: float, b: float, x: float) -> float:
    """I_x(a, b) for a, b > 0 and 0 <= x <= 1."""
    if a <= 0.0 or b <= 0.0:
        raise ValueError("a and b must be positive")
    if x <= 0.0:
        return 0.0
    if x >= 1.0:
        return 1.0
    log_front = (math.lgamma(a + b) - math.lgamma(a) - math.lgamma(b)
                 + a * math.log(x) + b * math.log1p(-x))
    front = math.exp(log_front)
    if x < (a + 1.0) / (a + b + 2.0):
        return front * _betacf(a, b, x) / a
    return 1.0 - front * _betacf(b, a, 1.0 - x) / b


def student_t_two_sided_p(t_value: float, degrees_of_freedom: float) -> float:
    """Two-sided p-value of a Student t statistic."""
    if degrees_of_freedom <= 0:
        raise ValueError("degrees of freedom must be positive")
    if math.isnan(t_value):
        return 1.0
    if math.isinf(t_value):
        return 0.0
    x = degrees_of_freedom / (degrees_of_freedom + t_value * t_value)
    return min(1.0, max(0.0, regularized_incomplete_beta(degrees_of_freedom / 2.0, 0.5, x)))


def ols_slope(ys) -> dict:
    """OLS of ys against x = 0..n-1.

    Returns {slope, stderr, t, p_value, n, degenerate}. `degenerate` is True
    when no test is possible (n < 3) or ys is constant; then slope = 0 and
    p_value = 1.0. A perfectly linear non-constant series has stderr 0, t = inf
    and p_value 0.
    """
    values = [float(value) for value in ys]
    count = len(values)
    if any(not math.isfinite(value) for value in values):
        raise ValueError("ols_slope input contains NaN or infinite values")
    flat = {"slope": 0.0, "stderr": 0.0, "t": 0.0, "p_value": 1.0, "n": count,
            "degenerate": True}
    if count < MIN_OLS_POINTS or min(values) == max(values):
        return flat
    mean_x = (count - 1) / 2.0
    mean_y = math.fsum(values) / count
    sum_xx = math.fsum((i - mean_x) ** 2 for i in range(count))
    sum_xy = math.fsum((i - mean_x) * (value - mean_y) for i, value in enumerate(values))
    slope = sum_xy / sum_xx
    residual_ss = math.fsum((value - mean_y - slope * (i - mean_x)) ** 2
                            for i, value in enumerate(values))
    stderr = math.sqrt(max(residual_ss, 0.0) / (count - 2) / sum_xx)
    if stderr == 0.0:
        t_value = math.copysign(math.inf, slope) if slope != 0.0 else 0.0
    else:
        t_value = slope / stderr
    return {"slope": slope, "stderr": stderr, "t": t_value,
            "p_value": student_t_two_sided_p(t_value, count - 2), "n": count,
            "degenerate": False}


def pooled_ols_slope(rounds) -> dict:
    """Pooled within-round OLS slope (round fixed effects).

    `rounds` is a list of per-round series. Within each round x = 0..n-1 and
    both x and y are demeaned; the demeaned data of all rounds are pooled into
    one regression through the origin. The degrees of freedom are
    N - rounds - 1, so the standard error and the t p-value are correct. A
    single round gives exactly ols_slope(). A sawtooth across rounds (memory
    reset at each round start) therefore cannot hide a real per-round leak.

    Returns the ols_slope keys plus `rounds`.
    """
    series_list = [[float(value) for value in series] for series in rounds]
    if len(series_list) == 1:
        fit = ols_slope(series_list[0])
        fit["rounds"] = 1
        return fit
    total = sum(len(series) for series in series_list)
    used = [series for series in series_list if len(series) >= 2]
    flat = {"slope": 0.0, "stderr": 0.0, "t": 0.0, "p_value": 1.0, "n": total,
            "degenerate": True, "rounds": len(series_list)}
    if any(not math.isfinite(value) for series in series_list for value in series):
        raise ValueError("pooled_ols_slope input contains NaN or infinite values")
    dof = total - len(series_list) - 1
    if not used or dof < 1:
        return flat
    sum_xx = sum_xy = 0.0
    parts = []
    for series in used:
        count = len(series)
        mean_x = (count - 1) / 2.0
        mean_y = math.fsum(series) / count
        xs = [i - mean_x for i in range(count)]
        ys = [value - mean_y for value in series]
        parts.append((xs, ys))
        sum_xx += math.fsum(x * x for x in xs)
        sum_xy += math.fsum(x * y for x, y in zip(xs, ys))
    if sum_xx == 0.0:
        return flat
    slope = sum_xy / sum_xx
    residual_ss = math.fsum((y - slope * x) ** 2 for xs, ys in parts
                            for x, y in zip(xs, ys))
    if all(max(series) == min(series) for series in used):
        return flat
    stderr = math.sqrt(max(residual_ss, 0.0) / dof / sum_xx)
    if stderr == 0.0:
        t_value = math.copysign(math.inf, slope) if slope != 0.0 else 0.0
    else:
        t_value = slope / stderr
    return {"slope": slope, "stderr": stderr, "t": t_value,
            "p_value": student_t_two_sided_p(t_value, dof), "n": total,
            "degenerate": False, "rounds": len(series_list)}


# ---------------------------------------------------------------------------
# Gate
# ---------------------------------------------------------------------------

def validate_config(cfg) -> dict:
    """Merge cfg over the defaults and validate types and ranges (ValueError)."""
    if cfg is None:
        cfg = {}
    if not isinstance(cfg, dict):
        raise ValueError("cfg must be a dict")
    merged = dict(DEFAULT_CONFIG)
    merged.update({key: cfg[key] for key in DEFAULT_CONFIG if key in cfg})

    def number(name: str) -> float:
        value = merged[name]
        if isinstance(value, bool) or not isinstance(value, (int, float)) \
                or not math.isfinite(value):
            raise ValueError(f"{name} must be a finite number")
        return float(value)

    def whole(name: str, minimum: int) -> int:
        value = merged[name]
        if isinstance(value, bool) or not isinstance(value, int) or value < minimum:
            raise ValueError(f"{name} must be an integer >= {minimum}")
        return value

    for name in ("p99_drift_max", "p50_drift_max", "leak_slope_max"):
        if number(name) < 0.0:
            raise ValueError(f"{name} must be >= 0")
        merged[name] = float(merged[name])
    for name in ("ci_level", "leak_alpha"):
        if not 0.0 < number(name) < 1.0:
            raise ValueError(f"{name} must be in (0, 1)")
        merged[name] = float(merged[name])
    merged["bootstrap_samples"] = whole("bootstrap_samples", 100)
    merged["min_samples"] = whole("min_samples", 10)
    return merged


def _coerce_series(name: str, values, reasons: list, *, allow_negative: bool,
                   allow_empty: bool = False):
    """Copy values into a float list; record why it is invalid, return None."""
    if values is None and allow_empty:
        return []
    if isinstance(values, (str, bytes, dict)) or not hasattr(values, "__iter__"):
        reasons.append(f"{name} is not a sequence of numbers")
        return None
    try:
        series = [float(value) for value in values]
    except (TypeError, ValueError):
        reasons.append(f"{name} contains a non-numeric value")
        return None
    if any(not math.isfinite(value) for value in series):
        reasons.append(f"{name} contains NaN or infinite values")
        return None
    if not allow_negative and any(value < 0.0 for value in series):
        reasons.append(f"{name} contains negative values")
        return None
    return series


def _coerce_latencies(name: str, values, reasons: list):
    """Latency input: a flat list, or a list of per-round lists.

    Returns (flat list, rounds list or None); (None, None) after a reason.
    """
    if isinstance(values, (str, bytes, dict)) or not hasattr(values, "__iter__"):
        reasons.append(f"{name} is not a sequence of numbers")
        return None, None
    items = list(values)
    nested = [isinstance(item, (list, tuple)) for item in items]
    if items and all(nested):
        rounds = []
        for index, item in enumerate(items):
            series = _coerce_series(f"{name}[{index}]", item, reasons, allow_negative=False)
            if series is None:
                return None, None
            rounds.append(series)
        return [v for r in rounds for v in r], rounds
    if any(nested):
        reasons.append(f"{name} mixes numbers and per-round lists")
        return None, None
    series = _coerce_series(name, items, reasons, allow_negative=False)
    return series, None


def _round_shape_reasons(base_rounds, cand_rounds) -> list:
    """Reasons the two sides do not form valid paired rounds (empty if fine)."""
    if (base_rounds is None) != (cand_rounds is None):
        return ["baseline and candidate latencies must both be flat lists "
                "or both be per-round lists"]
    if base_rounds is None:
        return []
    reasons = []
    if len(base_rounds) != len(cand_rounds):
        reasons.append(f"unequal round counts: baseline {len(base_rounds)}, "
                       f"candidate {len(cand_rounds)}")
    elif len(base_rounds) < MIN_ROUNDS:
        reasons.append(f"{len(base_rounds)} rounds: {ROUNDS_REASON}")
    elif any(len(x) != len(y) for x, y in zip(base_rounds, cand_rounds)):
        reasons.append("unequal sample counts within a round")
    elif any(not r for r in base_rounds):
        reasons.append("a round has no samples")
    return reasons


def _coerce_memory(name: str, values, reasons: list, *, allow_empty: bool = False):
    """Memory input: a flat list (one round) or a list of per-round lists.

    Returns a list of float lists (rounds), or None after recording a reason.
    """
    if values is None and allow_empty:
        return [[]]
    if isinstance(values, (str, bytes, dict)) or not hasattr(values, "__iter__"):
        reasons.append(f"{name} is not a sequence of numbers")
        return None
    items = list(values)
    nested = [isinstance(item, (list, tuple)) for item in items]
    if items and all(nested):
        rounds = []
        for index, item in enumerate(items):
            series = _coerce_series(f"{name}[{index}]", item, reasons, allow_negative=True)
            if series is None:
                return None
            rounds.append(series)
        return rounds
    if any(nested):
        reasons.append(f"{name} mixes numbers and per-round lists")
        return None
    series = _coerce_series(name, items, reasons, allow_negative=True)
    return None if series is None else [series]


def sample_resolution(samples) -> dict:
    """Measure timer quantization of one latency series (architecture.md 12.8).

    quantum: smallest positive gap between sorted distinct values (inf if all
    values are equal). ratio: quantum / median (inf if the median is <= 0).
    ok: ratio <= 0.5 % and at least 20 % of the values distinct.
    """
    values = sorted(float(value) for value in samples)
    count = len(values)
    distinct = sorted(set(values))
    gaps = [high - low for low, high in zip(distinct, distinct[1:]) if high > low]
    quantum = min(gaps) if gaps else math.inf
    median = _percentile_unchecked(values, 0.5) if values else 0.0
    ratio = quantum / median if median > 0.0 else math.inf
    fraction = len(distinct) / count if count else 0.0
    return {"quantum": quantum, "median": median, "ratio": ratio,
            "distinct_fraction": fraction,
            "ok": bool(count) and ratio <= MAX_QUANTUM_RATIO
            and fraction >= MIN_DISTINCT_FRACTION}


def _resolution_reasons(base: list, cand: list) -> list:
    reasons = []
    for label, series in (("baseline", base), ("candidate", cand)):
        info = sample_resolution(series)
        if not info["ok"]:
            reasons.append(f"{COARSE_REASON} ({label}: quantum {info['quantum']:.6g} ns, "
                           f"median {info['median']:.6g} ns, "
                           f"{info['distinct_fraction']:.1%} distinct values)")
    return reasons


def _invalid_result(cfg: dict, seed: int, reasons: list, base_count: int,
                    cand_count: int) -> dict:
    return {"verdict": REJECT_INVALID, "reasons": reasons, "notes": [],
            "samples": {"baseline": base_count, "candidate": cand_count,
                        "min_required": cfg["min_samples"]},
            "rounds": None, "ci_method": None, "config": cfg, "seed": seed, "p99": None, "p50": None, "memory": None}


def _quantile_report(base: list, cand: list, q: float, interval: tuple,
                     drift_max: float) -> dict:
    point, lower, upper = interval
    over_budget = point > drift_max
    significant = lower > 0.0
    return {"baseline": _percentile_unchecked(base, q),
            "candidate": _percentile_unchecked(cand, q),
            "drift": point, "ci_lower": lower, "ci_upper": upper,
            "drift_max": drift_max, "over_budget": over_budget,
            "significant": significant, "regression": over_budget and significant}


def _format_drift(report: dict) -> str:
    return (f"drift {report['drift']:+.2%} (CI [{report['ci_lower']:+.2%}, "
            f"{report['ci_upper']:+.2%}], limit +{report['drift_max']:.2%})")


def evaluate_gate(base_lat, cand_lat, mem_base, mem_cand, cfg=None, seed: int = 0) -> dict:
    """Decide the Tier 2 verdict. See architecture.md section 8.

    Order of precedence: invalid samples, then memory leak, then latency.
    The result carries every statistic: sample counts, p99 and p50 points and
    paired-bootstrap CIs, both OLS fits, the reasons list, the verdict.
    Raises ValueError only for a bad cfg or seed; bad samples are a verdict.
    """
    config = validate_config(cfg)
    if isinstance(seed, bool) or not isinstance(seed, int):
        raise ValueError("seed must be an integer")

    reasons: list = []
    base, base_rounds = _coerce_latencies("base_lat", base_lat, reasons)
    cand, cand_rounds = _coerce_latencies("cand_lat", cand_lat, reasons)
    if base is not None and cand is not None:
        reasons.extend(_round_shape_reasons(base_rounds, cand_rounds))
    use_rounds = base_rounds is not None and cand_rounds is not None
    memory_base = _coerce_memory("mem_base", mem_base, reasons, allow_empty=True)
    memory_cand = _coerce_memory("mem_cand", mem_cand, reasons)
    base_count = len(base) if base is not None else 0
    cand_count = len(cand) if cand is not None else 0
    if base is not None and base_count < config["min_samples"]:
        reasons.append(f"baseline has {base_count} samples, need {config['min_samples']}")
    if cand is not None and cand_count < config["min_samples"]:
        reasons.append(f"candidate has {cand_count} samples, need {config['min_samples']}")
    if base is not None and cand is not None and base_count != cand_count:
        reasons.append(f"unequal sample counts: baseline {base_count}, candidate {cand_count}")
    if memory_cand is not None:
        points = sum(len(series) for series in memory_cand)
        if points < MIN_OLS_POINTS:
            reasons.append(f"candidate memory has {points} points, "
                           f"need {MIN_OLS_POINTS} for the leak fit")
        elif points - len(memory_cand) - 1 < 1:
            reasons.append(f"candidate memory has {points} points in {len(memory_cand)} "
                           "rounds: too few for the pooled leak fit")
    if base and not reasons:
        for q, label in ((0.99, "p99"), (0.5, "p50")):
            if _percentile_unchecked(base, q) <= 0.0:
                reasons.append(f"baseline {label} is <= 0: drift undefined")
    if base and cand and not reasons:
        reasons.extend(_resolution_reasons(base, cand))
    if reasons:
        return _invalid_result(config, seed, reasons, base_count, cand_count)

    intervals = _bootstrap_drifts(base, cand, (0.99, 0.5), config["bootstrap_samples"],
                                  config["ci_level"], seed,
                                  rounds=(base_rounds, cand_rounds) if use_rounds else None)
    p99 = _quantile_report(base, cand, 0.99, intervals[0.99], config["p99_drift_max"])
    p50 = _quantile_report(base, cand, 0.5, intervals[0.5], config["p50_drift_max"])
    candidate_fit = pooled_ols_slope(memory_cand)
    baseline_fit = pooled_ols_slope(memory_base)
    leak = (candidate_fit["slope"] > config["leak_slope_max"]
            and candidate_fit["p_value"] < config["leak_alpha"])

    notes: list = []
    if leak:
        reasons.append(f"memory leak: slope {candidate_fit['slope']:.1f} per sample > "
                       f"{config['leak_slope_max']:.1f} (p={candidate_fit['p_value']:.3g} "
                       f"< {config['leak_alpha']})")
    for label, report in (("tail (p99)", p99), ("typical (p50)", p50)):
        if report["regression"]:
            reasons.append(f"latency regression in the {label}: {_format_drift(report)}")
        elif report["over_budget"]:
            notes.append(f"{label} {_format_drift(report)} exceeds the budget but the CI "
                         "includes 0: not significant")

    if leak:
        verdict = REJECT_LEAK
    elif p99["regression"] or p50["regression"]:
        verdict = REJECT_LATENCY
    else:
        verdict = PROCEED
    return {"verdict": verdict, "reasons": reasons, "notes": notes,
            "samples": {"baseline": base_count, "candidate": cand_count,
                        "min_required": config["min_samples"]},
            "rounds": len(base_rounds) if use_rounds else None,
            "ci_method": CI_METHOD_ROUNDS if use_rounds else CI_METHOD_FLAT,
            "config": config, "seed": seed, "p99": p99, "p50": p50,
            "memory": {"baseline": baseline_fit, "candidate": candidate_fit, "leak": leak}}


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def _load_driver_output(path: str) -> tuple:
    with open(path, encoding="utf-8") as handle:
        data = json.load(handle)
    return data["latencies_ns"], data["memory_bytes"]


def main(argv: list | None = None) -> int:
    parser = argparse.ArgumentParser(description="Tier 2 gate on two driver output files")
    parser.add_argument("--base", required=True)
    parser.add_argument("--cand", required=True)
    parser.add_argument("--seed", type=int, default=0)
    parser.add_argument("--config", help="JSON file with gate settings (optional)")
    args = parser.parse_args(argv)
    try:
        base_lat, base_mem = _load_driver_output(args.base)
        cand_lat, cand_mem = _load_driver_output(args.cand)
        cfg = None
        if args.config:
            with open(args.config, encoding="utf-8") as handle:
                cfg = json.load(handle)
        result = evaluate_gate(base_lat, cand_lat, base_mem, cand_mem, cfg, args.seed)
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(json.dumps({"verdict": "ERROR", "error": str(error)}))
        return 2
    print(json.dumps(result, indent=2))
    return 0 if result["verdict"] == PROCEED else 1


if __name__ == "__main__":
    sys.exit(main())
