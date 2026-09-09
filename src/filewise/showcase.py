"""Disposable, synthetic-only workspace demonstration; never attached to serve."""

import json
import secrets
from pathlib import Path

from .engine import Engine, FilewiseError
from .models import Actor
from .projects import Projects


class Showcase:
    def __init__(self, directory):
        from openpyxl import Workbook  # Fail at startup if the document extra is absent.

        self.root = Path(directory) / "files"
        self.root.mkdir(parents=True)
        self.engine = Engine(Path(directory) / "demo.db")
        self.projects = Projects(self.engine)
        self.tokens = {}
        self.identities = {}
        for role in ("editor", "reviewer", "publisher", "agent"):
            actor = Actor(
                id="demo-" + role,
                roles={"reader" if role == "agent" else role},
                audience="agent" if role == "agent" else "operator",
            )
            token = secrets.token_urlsafe(32)
            self.tokens[token] = actor
            self.identities[role] = {"token": token, "actor": actor.model_dump(mode="json")}
        self.editor = self.tokens[self.identities["editor"]["token"]]
        self.book_type = Workbook
        self.write_requirement(100)
        self.write_plan(100)
        (self.root / "delivery.md").write_text(
            "# 交付检查清单\n\n交付前核对 inspection.xlsx 的压力测试结果。\n责任岗位：质量审核员。\n",
            encoding="utf-8",
        )
        (self.root / "README.md").write_text(
            "# 合成液压测试项目\n\n需求 → 检验计划 → 交付清单。所有文件均为演示数据。\n", encoding="utf-8"
        )
        self.projects.create(
            {
                "id": "hydraulic-demo",
                "name": "液压测试 · 合成演示",
                "dependencies": {"inspection.xlsx": ["requirement.json"], "delivery.md": ["inspection.xlsx"]},
                "checks": [
                    {
                        "id": "pressure-match",
                        "file": "inspection.xlsx",
                        "pointer": "sheet:1/cell:B2",
                        "reference_file": "requirement.json",
                        "reference_pointer": "/pressure_kpa",
                    }
                ],
            },
            self.editor,
            self.root,
        )
        snapshot = self.projects.snapshot("hydraulic-demo", self.editor)
        rid = snapshot["release_id"]
        self.projects.approve("hydraulic-demo", rid, self.tokens[self.identities["reviewer"]["token"]])
        self.projects.activate(
            "hydraulic-demo", rid, None, self.tokens[self.identities["publisher"]["token"]]
        )

    def write_requirement(self, pressure):
        (self.root / "requirement.json").write_text(
            json.dumps({"product": "DEMO-001", "pressure_kpa": pressure}, ensure_ascii=False, indent=2)
            + "\n",
            encoding="utf-8",
        )

    def write_plan(self, pressure):
        if getattr(self, "plan_pressure", None) == pressure:
            return
        book = self.book_type()
        sheet = book.active
        sheet.title = "Inspection"
        sheet.append(["检验项目", "压力 / kPa", "责任岗位"])
        sheet.append(["液压测试", pressure, "检验工程师"])
        book.save(self.root / "inspection.xlsx")
        book.close()
        self.plan_pressure = pressure

    def bootstrap(self):
        return {"mode": "synthetic-demo", "project_id": "hydraulic-demo", "identities": self.identities}

    def step(self, action):
        if action == "change":
            self.write_requirement(120)
            self.write_plan(100)
        elif action == "repair":
            self.write_requirement(120)
            self.write_plan(120)
        else:
            raise FilewiseError("Unknown demo step", 404)
        return self.projects.snapshot("hydraulic-demo", self.editor)
