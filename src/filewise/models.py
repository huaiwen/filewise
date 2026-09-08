"""The public Document IR. All model output is a proposal, never an approval."""

from datetime import datetime, timezone
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field, JsonValue, field_validator, model_validator

ID = Annotated[str, Field(pattern=r"^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$")]
Role = Literal["reader", "editor", "reviewer", "publisher"]


def now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="microseconds")


def timestamp(value: str) -> str:
    dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if dt.tzinfo is None:
        raise ValueError("Timestamp must include a timezone")
    return dt.astimezone(timezone.utc).isoformat(timespec="microseconds")


class Model(BaseModel):
    model_config = ConfigDict(extra="forbid", allow_inf_nan=False)


class Actor(Model):
    id: ID
    roles: set[Role] = Field(min_length=1)


class Evidence(Model):
    source_id: ID
    locator: str = Field(min_length=1, max_length=256)
    quote: str = Field(min_length=1, max_length=4000)


class Check(Model):
    id: ID
    object_id: ID
    field: str = Field(min_length=1, max_length=128)
    op: Literal["eq", "gte", "lte", "contains", "exists"] = "eq"
    expected: JsonValue = None
    reference_object: ID | None = None
    reference_field: str | None = None
    critical: bool = True

    @model_validator(mode="after")
    def complete_reference(self):
        if bool(self.reference_object) != bool(self.reference_field):
            raise ValueError("reference_object and reference_field must be supplied together")
        return self


class Scope(Model):
    id: ID
    title: str = Field(min_length=1, max_length=256)
    description: str = Field(default="", max_length=4000)
    required_objects: list[ID] = Field(min_length=1, max_length=1000)
    checks: list[Check] = Field(default_factory=list, max_length=1000)
    excluded_sources: list[str] = Field(default_factory=list, max_length=100)
    acl: set[Role] = Field(
        default_factory=lambda: {"reader", "editor", "reviewer", "publisher"}, min_length=1
    )

    @model_validator(mode="after")
    def unique_contract(self):
        if len(self.required_objects) != len(set(self.required_objects)):
            raise ValueError("Duplicate required object")
        if len({c.id for c in self.checks}) != len(self.checks):
            raise ValueError("Duplicate check ID")
        return self


class Revision(Model):
    id: ID
    scope_id: ID
    object_id: ID
    kind: Literal["rule", "procedure", "asset", "test", "skill", "record"]
    title: str = Field(min_length=1, max_length=256)
    fields: dict[str, JsonValue] = Field(min_length=1, max_length=100)
    depends_on: list[ID] = Field(default_factory=list, max_length=1000)
    evidence: list[Evidence] = Field(min_length=1, max_length=100)
    valid_from: str
    valid_until: str | None = None
    authority: int = Field(default=100, ge=0, le=1000)
    acl: set[Role] = Field(
        default_factory=lambda: {"reader", "editor", "reviewer", "publisher"}, min_length=1
    )

    _dates = field_validator("valid_from")(timestamp)

    @field_validator("valid_until")
    @classmethod
    def end_date(cls, value):
        return timestamp(value) if value else None

    @model_validator(mode="after")
    def interval(self):
        if self.valid_until and self.valid_until <= self.valid_from:
            raise ValueError("valid_until must be after valid_from")
        if self.object_id in self.depends_on or len(self.depends_on) != len(set(self.depends_on)):
            raise ValueError("Dependencies must be unique and cannot include self")
        return self


class BuildRequest(Model):
    scope_id: ID
    valid_time: str
    transaction_time: str | None = None
    base_release: ID | None = None
    goal: str = Field(default="Review change and update affected knowledge", min_length=1, max_length=2000)

    _valid = field_validator("valid_time")(timestamp)

    @field_validator("transaction_time")
    @classmethod
    def tx_date(cls, value):
        return timestamp(value) if value else None


class ResolveRequest(Model):
    scope_id: ID
    valid_time: str
    transaction_time: str | None = None

    _valid = field_validator("valid_time")(timestamp)

    @field_validator("transaction_time")
    @classmethod
    def tx_date(cls, value):
        return timestamp(value) if value else None


class ImpactRequest(ResolveRequest):
    seeds: list[ID] = Field(min_length=1, max_length=1000)
    direction: Literal["forward", "reverse"] = "forward"


class DiffRequest(Model):
    before_release: ID
    after_release: ID


class ContextRequest(Model):
    object_ids: list[ID] = Field(min_length=1, max_length=100)


class ActivateRequest(Model):
    expected_active: ID | None = None


class SourcePolicy(Model):
    acl: set[Role] = Field(min_length=1)
    revoked: bool = False
