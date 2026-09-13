"""Bounded, deterministic data contracts. Meaning and processing are explicit declarations."""

import csv
import io
import json
import re
from decimal import Decimal, InvalidOperation
from itertools import islice
from pathlib import Path
from typing import Annotated, Literal

from pydantic import Field, field_validator, model_validator

from .models import ID, Model, timestamp

Text = Annotated[str, Field(min_length=1, max_length=300)]
MeaningKey = Literal[
    "entity_type",
    "entity_id",
    "identifier_system",
    "measure",
    "unit",
    "currency",
    "period",
    "calendar",
    "grain",
    "population",
    "sampling",
    "coverage",
]


class SnapshotQuery(Model):
    version: str = Field(default="latest", min_length=1, max_length=128)
    as_of: str | None = None

    @field_validator("as_of")
    @classmethod
    def cutoff(cls, value):
        return timestamp(value) if value is not None else None


class DataInput(Model):
    version: Annotated[str, Field(pattern=r"^[a-f0-9]{64}$")]
    path: str = Field(min_length=1, max_length=240)
    role: Literal["data", "mapping", "code", "model"] = "data"


class QualityRule(Model):
    id: ID
    op: Literal["not_null", "unique", "range", "row_count", "distinct_count_change"]
    column: Text | None = None
    minimum: float | None = None
    maximum: float | None = None
    max_change: float | None = Field(default=None, ge=0)

    @model_validator(mode="after")
    def complete(self):
        if (self.op != "row_count") != (self.column is not None):
            raise ValueError("Row count has no column; other quality rules require a column")
        if self.op in ("range", "row_count"):
            if self.minimum is None and self.maximum is None:
                raise ValueError("Supply a minimum or maximum")
            if self.minimum is not None and self.maximum is not None and self.minimum > self.maximum:
                raise ValueError("Minimum exceeds maximum")
        elif self.minimum is not None or self.maximum is not None:
            raise ValueError("Bounds apply only to range and row_count")
        if (self.op == "distinct_count_change") != (self.max_change is not None):
            raise ValueError("distinct_count_change requires max_change; other rules do not use it")
        return self


class DataContract(Model):
    meaning: dict[MeaningKey, Text] = Field(default_factory=dict, max_length=12)
    allowed_uses: list[Text] = Field(default_factory=list, max_length=30)
    rows_pointer: str = Field(default="", max_length=300)
    sheet: int = Field(default=1, ge=1, le=100)
    checks: list[QualityRule] = Field(default_factory=list, max_length=40)

    @model_validator(mode="after")
    def unique_checks(self):
        if len({check.id for check in self.checks}) != len(self.checks):
            raise ValueError("Duplicate data quality check ID")
        if self.rows_pointer and (
            not self.rows_pointer.startswith("/") or re.search(r"~(?![01])", self.rows_pointer)
        ):
            raise ValueError("rows_pointer must be an RFC6901 JSON pointer")
        return self


class DataUse(Model):
    expect: dict[MeaningKey, Text] = Field(default_factory=dict, max_length=12)
    purpose: Text | None = None
    require_quality: bool = True


def _object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("Duplicate JSON key")
        result[key] = value
    return result


def _constant(value):
    raise ValueError("Non-finite JSON number")


def table(path, body, contract, fragments):
    """Read actual bounded records, not the retrieval/120-field projection. No formula execution."""
    suffix = Path(path).suffix.lower()
    if suffix == ".json":
        rows = json.loads(body, object_pairs_hook=_object, parse_constant=_constant)
        json.dumps(rows, allow_nan=False)  # Reject numeric overflow such as 1e999 as well as NaN tokens.
        for part in contract.rows_pointer.split("/")[1:]:
            part = part.replace("~1", "/").replace("~0", "~")
            if isinstance(rows, list):
                if not re.fullmatch(r"0|[1-9][0-9]*", part):
                    raise ValueError("Invalid JSON pointer array index")
                rows = rows[int(part)]
            else:
                rows = rows[part]
        if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows):
            raise ValueError("JSON data contract requires an array of records at rows_pointer")
        columns = sorted({key for row in rows for key in row})
    else:
        if contract.rows_pointer:
            raise ValueError("rows_pointer applies only to JSON")
        if suffix == ".csv":
            reader = csv.reader(io.StringIO(body.decode("utf-8-sig"), newline=""), strict=True)
            cells = list(islice(reader, 20_002))
            if len(cells) > 20_001:
                raise ValueError("Data table exceeds 20,000 records")
        elif suffix == ".xlsx":
            values = {}
            for fragment in fragments:
                match = re.fullmatch(rf"sheet:{contract.sheet}/cell:([A-Z]+)([0-9]+)", fragment["locator"])
                if match:
                    col = 0
                    for char in match[1]:
                        col = col * 26 + ord(char) - 64
                    values[int(match[2]), col] = fragment["text"]
            height = max((row for row, _ in values), default=0)
            width = max((col for _, col in values), default=0)
            if height > 20_001 or width > 512 or height * width > 200_000:
                raise ValueError("Data table exceeds row/column/cell limits")
            cells = [
                [values.get((row, col), "") for col in range(1, width + 1)] for row in range(1, height + 1)
            ]
        else:
            raise ValueError("Data checks support JSON records, CSV and extracted XLSX")
        if not cells:
            raise ValueError("Missing table header")
        columns = cells[0]
        if len(columns) != len(set(columns)) or any(not col.strip() for col in columns):
            raise ValueError("Table headers must be nonempty and unique")
        if any(len(row) != len(columns) for row in cells[1:]):
            raise ValueError("Ragged table rows")
        rows = [dict(zip(columns, row)) for row in cells[1:]]
    if len(rows) > 20_000 or len(columns) > 512 or any(len(col) > 300 for col in columns):
        raise ValueError("Data table exceeds 20,000 records or 512 bounded columns")
    return rows, columns


def missing(value):
    return value is None or (isinstance(value, str) and not value.strip())


def number(value):
    if isinstance(value, bool) or not isinstance(value, (int, float, str)):
        raise ValueError("Not a numeric value")
    result = Decimal(str(value))
    if not result.is_finite():
        raise ValueError("Non-finite number")
    return result


def within(value, rule):
    return (rule.minimum is None or value >= Decimal(str(rule.minimum))) and (
        rule.maximum is None or value <= Decimal(str(rule.maximum))
    )


def quality(path, body, contract, fragments, *, previous=None, baseline=None):
    from .engine import canonical

    tests, issues = [], []
    if not contract.checks:
        return {
            "schema": "filewise/data-quality-v1",
            "decision": "NEEDS_REVIEW",
            "tests": [],
            "issues": [{"reason": "no_data_quality_oracle"}],
        }
    try:
        rows, columns = table(path, body, contract, fragments)
    except (ValueError, KeyError, TypeError, IndexError, RecursionError, csv.Error) as error:
        return {
            "schema": "filewise/data-quality-v1",
            "decision": "BLOCKED",
            "tests": [],
            "issues": [{"reason": "unreadable_data_table", "detail": str(error)[:300]}],
        }
    old_rows = None
    for rule in contract.checks:
        values = [row.get(rule.column) for row in rows]
        bad, actual, known = [], None, True
        if rule.column is not None and rule.column not in columns:
            known = False
        elif rule.op == "row_count":
            actual = len(rows)
            bad = [] if within(Decimal(actual), rule) else [0]
        elif rule.op == "not_null":
            bad = [i for i, value in enumerate(values) if missing(value)]
        elif rule.op == "unique":
            seen = set()
            for i, value in enumerate(values):
                key = canonical(value)
                if missing(value) or key in seen:
                    bad.append(i)
                seen.add(key)
        elif rule.op == "range":
            for i, value in enumerate(values):
                try:
                    if not within(number(value), rule):
                        bad.append(i)
                except (ValueError, InvalidOperation):
                    bad.append(i)
        elif rule.op == "distinct_count_change":
            try:
                if old_rows is None:
                    if previous is None:
                        raise ValueError("No baseline")
                    old_rows, old_columns = table(path, previous[0], contract, previous[1])
                if (
                    rule.column not in old_columns
                    or any(missing(v) for v in values)
                    or any(missing(row.get(rule.column)) for row in old_rows)
                ):
                    raise ValueError("Missing cohort identifiers")
                old_count = len({canonical(row[rule.column]) for row in old_rows})
                new_count = len({canonical(v) for v in values})
                change = abs(new_count - old_count) / old_count if old_count else 0 if not new_count else None
                actual = {
                    "before": old_count,
                    "after": new_count,
                    "relative_change": change,
                    "baseline": baseline,
                }
                bad = [] if change is not None and change <= rule.max_change else [0]
            except (ValueError, KeyError, TypeError, IndexError, RecursionError, csv.Error):
                known = False
        tests.append(
            {
                "id": rule.id,
                "op": rule.op,
                "column": rule.column,
                "passed": known and not bad,
                "known": known,
                "actual": actual,
                "failure_count": len(bad),
                "record_indices": bad[:20] if rule.op not in ("row_count", "distinct_count_change") else [],
                "rule": rule.model_dump(mode="json"),
            }
        )
        if not known or bad:
            issues.append(
                {"reason": "quality_check_failed" if known else "quality_check_unknown", "check": rule.id}
            )
    return {
        "schema": "filewise/data-quality-v1",
        "decision": "BLOCKED" if issues else "PASS" if tests else "NEEDS_REVIEW",
        "profile": {"row_count": len(rows), "columns": columns},
        "tests": tests,
        "issues": issues,
        "baseline": baseline,
        "assurance": "declared_checks_only; no statistical representativeness proof",
    }


def compatible(contract, use):
    issues = []
    for key, expected in use.expect.items():
        actual = (contract or {}).get("meaning", {}).get(key)
        if actual != expected:
            issues.append(
                {
                    "reason": "meaning_unknown" if actual is None else "meaning_mismatch",
                    "field": key,
                    "expected": expected,
                    "actual": actual,
                }
            )
    if use.purpose and use.purpose not in (contract or {}).get("allowed_uses", []):
        issues.append({"reason": "purpose_not_declared", "purpose": use.purpose})
    return issues
