// Thin operator console. No decision logic here: the server decides everything.
const $ = (id) => document.getElementById(id);

async function api(method, path, body) {
  const init = { method, headers: {} };
  if (body) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function tag(text, cls) {
  const el = document.createElement("span");
  el.className = "tag " + (cls || "");
  el.textContent = text;
  return el;
}

function cell(...nodes) {
  const td = document.createElement("td");
  nodes.forEach((n) => td.append(typeof n === "string" ? document.createTextNode(n) : n));
  return td;
}

function btn(label, cls, onClick) {
  const b = document.createElement("button");
  b.className = cls;
  b.textContent = label;
  b.onclick = onClick;
  return b;
}

function statusTag(status) {
  const map = { running: "run", waiting: "", sleeping: "", done: "", terminated: "warn", failed: "bad" };
  return tag(status, map[status] || "");
}

function fmtTime(s) {
  if (!s) return "-";
  return new Date(s).toLocaleString("zh-CN", { hour12: false });
}

async function load() {
  const state = await api("GET", "/api/state");
  renderTasks(state.tasks || []);
  renderApprovals(state.approvals || []);
  renderMetrics(state.metrics || {});
  renderIncidents(state.incidents || []);
  const audit = await api("GET", "/api/audit");
  renderAudit((audit.events || []).slice(-20).reverse());
  $("togglePause").textContent = state.paused ? "恢复调度" : "暂停调度";
  $("togglePause").dataset.paused = state.paused ? "1" : "0";
}

function renderTasks(tasks) {
  const body = $("tasks");
  body.replaceChildren();
  if (!tasks.length) {
    const tr = document.createElement("tr");
    tr.append(cell("暂无任务"));
    body.append(tr);
    return;
  }
  tasks.forEach((t) => {
    const tr = document.createElement("tr");
    tr.append(
      cell(document.createTextNode(t.id)),
      cell(document.createTextNode(t.goal_id)),
      cell(statusTag(t.status)),
      cell(fmtTime(t.next_run)),
      cell("¥" + (t.cost_cny || 0).toFixed(4)),
      cell(
        btn("立即执行", "small", async () => {
          try {
            await api("POST", `/api/tasks/${t.id}/runs`);
            load();
          } catch (e) {
            alert(e.message);
          }
        }),
      ),
    );
    body.append(tr);
  });
}

function renderApprovals(list) {
  const body = $("approvals");
  body.replaceChildren();
  const pending = list.filter((a) => a.state === "pending");
  if (!pending.length) {
    const tr = document.createElement("tr");
    tr.append(cell("没有待确认的动作"));
    body.append(tr);
    return;
  }
  pending.forEach((a) => {
    const tr = document.createElement("tr");
    tr.append(
      cell(document.createTextNode(a.summary || a.intent_id)),
      cell("¥" + (a.amount_cny || 0).toFixed(2)),
      cell(document.createTextNode(a.reject_risk || "-")),
      cell(
        btn("同意", "small", async () => {
          try {
            await api("POST", `/api/approvals/${a.id}/grant`);
            load();
          } catch (e) {
            alert(e.message);
          }
        }),
        document.createTextNode(" "),
        btn("拒绝", "small", async () => {
          try {
            await api("POST", `/api/approvals/${a.id}/reject`, { reason: "用户拒绝" });
            load();
          } catch (e) {
            alert(e.message);
          }
        }),
      ),
    );
    body.append(tr);
  });
}

function renderMetrics(m) {
  const box = $("metrics");
  box.replaceChildren();
  const labels = {
    runs_total: "执行次数",
    runs_passed: "校验通过",
    runs_failed: "校验失败",
    runs_uncertain: "不确定",
    approvals_pending: "待确认",
    notifications: "通知",
    tasks_stopped: "已熔断",
  };
  Object.keys(labels).forEach((k) => {
    const d = document.createElement("div");
    d.className = "metric";
    const b = document.createElement("b");
    b.textContent = String(m[k] || 0);
    const s = document.createElement("span");
    s.textContent = labels[k];
    d.append(b, s);
    box.append(d);
  });
}

function renderIncidents(list) {
  const ul = $("incidents");
  ul.replaceChildren();
  if (!list.length) {
    const li = document.createElement("li");
    li.textContent = "无";
    ul.append(li);
    return;
  }
  list.forEach((i) => {
    const li = document.createElement("li");
    li.textContent = `${i.level} · ${i.detail} · ${fmtTime(i.at)}`;
    ul.append(li);
  });
}

function renderAudit(events) {
  const body = $("audit");
  body.replaceChildren();
  events.forEach((e) => {
    const tr = document.createElement("tr");
    tr.append(
      cell(String(e.seq)),
      cell(fmtTime(e.at)),
      cell(document.createTextNode(e.type)),
      cell(document.createTextNode(e.actor)),
      cell(document.createTextNode(e.payload || "")),
    );
    body.append(tr);
  });
}

$("goalForm").onsubmit = async (ev) => {
  ev.preventDefault();
  const f = new FormData(ev.target);
  try {
    const res = await api("POST", "/api/goals", {
      text: f.get("text"),
      interval_s: Number(f.get("interval_s")) || 60,
      url: f.get("url"),
    });
    $("goalMsg").textContent = res.deduped ? "已存在相同目标，未重复创建" : "已创建";
    ev.target.reset();
    load();
  } catch (e) {
    $("goalMsg").textContent = e.message;
  }
};

$("togglePause").onclick = async () => {
  const paused = $("togglePause").dataset.paused === "1";
  await api("POST", "/api/system/pause", { paused: !paused });
  load();
};

$("verifyAudit").onclick = async () => {
  const res = await api("GET", "/api/audit/verify");
  $("auditMsg").textContent = res.ok ? "审计链完整" : "审计链异常：" + res.detail;
};

load();
setInterval(load, 3000);
