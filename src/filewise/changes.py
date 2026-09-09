"""Evidence-located changes. Heuristic interpretations never become approval facts."""

import difflib
import json
import re


def interpretation(before, after, locator):
    if before is None:
        return "added", "新增内容"
    if after is None:
        return "removed", "删除内容"
    if "/cell:" in locator and (before.startswith("=") or after.startswith("=")):
        return "formula_changed", "计算公式发生变化，需要复核引用与下游结果"
    numbers = r"[-+]?\d+(?:\.\d+)?"
    old, new = re.findall(numbers, before), re.findall(numbers, after)
    if old != new:
        return "numeric_changed", "数值发生变化，需要检查阈值、单位与引用它的文件"
    normative = r"必须|不得|禁止|应当|允许|MUST|SHALL|MAY|must|shall"
    if re.findall(normative, before) != re.findall(normative, after):
        return "obligation_changed", "约束用语发生变化，需要审核义务或适用条件"
    return "content_changed", "内容发生变化，语义含义待审核"


def fragment_diff(before, after, *, format="text"):
    """Use exact cells for spreadsheets, sequence alignment for movable prose/code."""
    changes = []

    def emit(old, new):
        a, b = old["text"] if old else None, new["text"] if new else None
        locator = (new or old)["locator"]
        kind, summary = interpretation(a, b, locator)
        changes.append(
            {
                "kind": kind,
                "before": a,
                "after": b,
                "before_locator": old["locator"] if old else None,
                "after_locator": new["locator"] if new else None,
                "summary": summary,
                "semantic_status": "needs_review",
            }
        )

    if format == "json":
        try:
            old_values = json_fields("\n".join(f["text"] for f in before)) if before else {}
            new_values = json_fields("\n".join(f["text"] for f in after)) if after else {}
            for pointer in sorted(old_values.keys() | new_values.keys()):
                a, b = old_values.get(pointer), new_values.get(pointer)
                if (
                    pointer in old_values
                    and pointer in new_values
                    and json.dumps(a, sort_keys=True) == json.dumps(b, sort_keys=True)
                ):
                    continue
                if isinstance(a, (dict, list)) and isinstance(b, type(a)) and (a or b):
                    continue
                if pointer not in old_values and isinstance(b, (dict, list)) and b:
                    continue
                if pointer not in new_values and isinstance(a, (dict, list)) and a:
                    continue
                emit(
                    {"locator": "json:" + pointer, "text": json.dumps(a, ensure_ascii=False)}
                    if pointer in old_values
                    else None,
                    {"locator": "json:" + pointer, "text": json.dumps(b, ensure_ascii=False)}
                    if pointer in new_values
                    else None,
                )
            return changes
        except (ValueError, RecursionError):
            # Invalid JSON remains readable text; field checks will fail for missing parsed values.
            pass
    if format in ("xlsx", "csv"):
        old, new = {f["locator"]: f for f in before}, {f["locator"]: f for f in after}
        for locator in sorted(old.keys() | new.keys()):
            if old.get(locator) != new.get(locator):
                emit(old.get(locator), new.get(locator))
    else:
        matcher = difflib.SequenceMatcher(
            a=[f["text"] for f in before],
            b=[f["text"] for f in after],
            # ponytail: coarse alignment on large repetitive text bounds quadratic matching cost.
            autojunk=max(len(before), len(after)) > 2000,
        )
        for tag, i, j, k, end in matcher.get_opcodes():
            if tag == "equal":
                continue
            for offset in range(max(j - i, end - k)):
                emit(
                    before[i + offset] if i + offset < j else None,
                    after[k + offset] if k + offset < end else None,
                )
    return changes


def json_fields(body):
    """Flatten JSON leaves into precise RFC6901-like pointers, including type changes."""
    result = {}

    def visit(value, pointer, depth=0):
        if depth > 64 or len(result) >= 20_000:
            raise ValueError("JSON structure limit exceeded")
        result[pointer] = value
        if isinstance(value, dict):
            for key, item in value.items():
                visit(item, pointer + "/" + key.replace("~", "~0").replace("/", "~1"), depth + 1)
        elif isinstance(value, list):
            for index, item in enumerate(value):
                visit(item, pointer + "/" + str(index), depth + 1)

    visit(json.loads(body), "")
    return result
