"use strict";
const $ = id => document.getElementById(id);
let token = "", overview = null;
const show = data => { $("result").textContent = JSON.stringify(data, null, 2); };
const status = (message, error = false) => { $("status").textContent = message; $("status").className = error ? "error" : ""; };
function reset() {
  token = ""; overview = null;
  $("identity").textContent = "尚未连接";
  for (const id of ["scope", "release", "sources"]) $(id).replaceChildren();
  $("scope-info").textContent = "范围规定必需对象与回归检查。";
  $("release-info").textContent = "请选择发布记录。";
  $("result").textContent = "连接后选择发布记录。";
  document.querySelectorAll("[data-role]").forEach(button => { button.disabled = true; });
}
async function api(path, method = "GET", data) {
  if (!token) throw new Error("请先连接。");
  const headers = {Authorization: `Bearer ${token}`};
  if (data !== undefined && !(data instanceof FormData)) headers["Content-Type"] = "application/json";
  const response = await fetch(`/api/${path}`, {method, headers, body: data instanceof FormData ? data : JSON.stringify(data)});
  const result = await response.json();
  if (!response.ok) throw new Error(`${response.status}: ${typeof result.detail === "string" ? result.detail : JSON.stringify(result.detail)}`);
  return result;
}
function bind(id, event, action) {
  $(id).addEventListener(event, async e => {
    e.preventDefault();
    try { await action(); } catch (error) { status(error.message, true); }
  });
}
function options(id, items, selected) {
  $(id).replaceChildren(...items.map(([value, label]) => new Option(label, value)));
  if (items.some(([value]) => value === selected)) $(id).value = selected;
}
async function refresh() {
  const previous = $("scope").value;
  overview = await api("overview");
  options("scope", overview.scopes.map(s => [s.id, `${s.title} · ${s.id}`]), previous);
  scopeChanged();
}
function scopeChanged() {
  const sid = $("scope").value;
  const scope = overview?.scopes.find(s => s.id === sid);
  $("scope-info").textContent = scope ? `${scope.required_objects.length} 个必需对象 · ${scope.checks.length} 项回归检查` : "暂无范围。可展开下方编辑器创建。";
  const active = overview?.active[sid];
  const releases = overview?.releases.filter(r => r.scope_id === sid) || [];
  options("release", releases.map(r => [r.id, `${r.decision}${r.revoked ? " · 已撤销" : r.id === active ? " · 当前激活" : ""} · ${r.id.slice(0, 12)}`]), $("release").value);
  $("sources").replaceChildren(...(overview?.sources.filter(s => s.scope_id === sid) || []).map(s => {
    const li = document.createElement("li"), button = document.createElement("button");
    li.append(document.createTextNode(s.name)); button.textContent = "查看坐标";
    button.addEventListener("click", async () => { try { show(await api(`sources/${s.id}`)); } catch (e) { status(e.message, true); } });
    li.append(button); return li;
  }));
  $("release-info").textContent = active ? `当前激活：${active.slice(0, 16)}` : "尚无激活版本。";
}
function releasePath(suffix = "") {
  if (!$("release").value) throw new Error("请选择发布记录。");
  return `releases/${$("release").value}${suffix}`;
}
function resolveData() {
  if (!$("scope").value) throw new Error("请选择知识范围。");
  return {scope_id: $("scope").value, valid_time: new Date($("valid-time").value).toISOString()};
}
bind("connect-form", "submit", async () => {
  const entered = $("token").value.trim(); reset(); token = entered; $("token").value = "";
  try {
    const who = await api("me"); await refresh();
    $("identity").textContent = `${who.id} · ${who.roles.join(", ")}`;
    document.querySelectorAll("[data-role]").forEach(button => { button.disabled = !who.roles.includes(button.dataset.role); });
    status("已连接。选择发布记录查看验证结果。");
  } catch (error) { reset(); throw error; }
});
bind("disconnect", "click", () => { reset(); status("已断开，令牌已从页面内存清除。"); });
bind("refresh", "click", async () => { await refresh(); status("记录已刷新。"); });
bind("scope", "change", () => { scopeChanged(); $("result").textContent = "请选择发布记录。"; });
bind("release", "change", async () => { show(await api(releasePath())); status("已加载不可变发布包；操作时会重新检查权限与证据。"); });
bind("resolve", "click", async () => { show(await api("resolve", "POST", resolveData())); status("状态判定完成。"); });
bind("build-form", "submit", async () => {
  const result = await api("build", "POST", resolveData()); await refresh(); $("release").value = result.id; show(result);
  status(`构建完成：${result.bundle.verification.decision}`);
});
bind("upload-form", "submit", async () => {
  if (!$("scope").value) throw new Error("请选择知识范围。");
  const form = new FormData(); form.append("file", $("file").files[0]);
  const result = await api(`scopes/${$("scope").value}/sources?acl=${encodeURIComponent($("source-acl").value)}`, "POST", form);
  await refresh(); show(result); status("证据已导入。业务对象仍需单独提交和审核。");
});
bind("verify", "click", async () => { show(await api(releasePath("/verify"))); status("已完成构建门禁与运行时状态检查。"); });
bind("inspect", "click", async () => { show(await api(releasePath())); status("已加载不可变发布包。"); });
for (const action of ["approve", "activate", "rollback", "revoke"]) bind(action, "click", async () => {
  const data = ["activate", "rollback"].includes(action) ? {expected_active: overview.active[$("scope").value] || null} : undefined;
  const result = await api(releasePath(`/${action}`), "POST", data); await refresh(); show(result); status("发布操作已记录。");
});
bind("context-form", "submit", async () => {
  show(await api(releasePath("/context"), "POST", {object_ids: $("object-ids").value.split(",").map(s => s.trim()).filter(Boolean)}));
  status("已读取固定版本上下文，包含上游依赖。");
});
bind("json-form", "submit", async () => { const result = await api($("json-kind").value, "POST", JSON.parse($("json-input").value)); await refresh(); show(result); status("提交已记录。"); });
bind("decision-form", "submit", async () => { show(await api(`revisions/${encodeURIComponent($("revision-id").value)}/${$("decision").value}`, "POST")); status("审核决定已记录。"); });
const local = new Date(Date.now() - new Date().getTimezoneOffset() * 60000);
$("valid-time").value = local.toISOString().slice(0, 16);
