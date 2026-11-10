// 1token account page and first-run login gate. The native Go side owns the
// OAuth flow, the relay key, models, and balance; no provider plugin is used.
magpie.plugin("one-token", (ctx) => {
  const one = (id) => document.getElementById(id);
  const site = "https://www.ciyuancenter.cc";
  const set = (id, value) => { const el = one(id); if (el) el.textContent = value; };
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const contextText = (n) => {
    n = Number(n) || 0;
    if (!n) return "";
    if (n >= 1e6) return `${(n / 1e6).toFixed(n % 1e6 ? 1 : 0)}M`;
    if (n >= 1000) return `${Math.round(n / 1000)}K`;
    return String(n);
  };
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  let current = null;
  let waiting = false;
  let gate = null;

  async function readStatus() {
    try {
      const s = await api("one-token/status");
      return s && typeof s === "object" ? s : { loggedIn: false, models: [] };
    } catch (err) {
      return { loggedIn: false, models: [], error: err?.message || String(err) };
    }
  }

  function modelsHTML(models, signed) {
    if (!signed) return '<p class="ot-model-empty">登录后显示当前令牌可用的聊天模型。</p>';
    if (!models || !models.length) return '<p class="ot-model-empty">当前令牌没有可用的聊天模型；请检查站点开放情况与令牌权限。</p>';
    return models.map((m) => {
      const context = contextText(m.context);
      return `<div class="ot-model-row"><strong>${esc(m.name || m.id)}</strong><code>${esc(m.id)}</code>${context ? `<span>${esc(context)} 上下文</span>` : ""}</div>`;
    }).join("");
  }

  function renderModels(models, signed) {
    const box = one("ot-model-list");
    if (box) box.innerHTML = modelsHTML(models, signed);
  }

  async function waitForLogin(limit = 300) {
    if (waiting) return;
    waiting = true;
    try {
      for (let i = 0; i < limit; i++) {
        await sleep(1000);
        const s = await readStatus();
        current = s;
        if (s.loggedIn) { location.reload(); return; }
        if (s.pending) continue;
        if (s.error) { gateNote(s.error, true); return; }
      }
    } finally {
      waiting = false;
    }
  }

  function gateNote(text, bad = false) {
    const el = one("ot-gate-note");
    if (!el) return;
    el.textContent = text || "";
    el.classList.toggle("bad", !!bad);
  }

  async function gateLogin() {
    try {
      gateNote("正在打开 1token 登录页…");
      await api("one-token/start", {});
      gateNote("请在登录窗口完成授权，完成后会自动返回。");
      await waitForLogin();
    } catch (err) {
      gateNote(err?.message || String(err), true);
    }
  }

  async function gateRegister() {
    try {
      await api("one-token/register", {});
      gateNote("请在注册页完成注册，然后回到这里点「登录 1token」。");
    } catch (err) {
      gateNote(err?.message || String(err), true);
    }
  }

  async function gatePaste() {
    const row = one("ot-gate-paste-row");
    if (row) row.hidden = !row.hidden;
  }

  async function gateSaveKey() {
    const key = one("ot-gate-key")?.value?.trim();
    if (!key) { gateNote("请粘贴 API 令牌。", true); return; }
    try {
      gateNote("正在验证令牌…");
      await api("one-token/token", { key });
      location.reload();
    } catch (err) {
      gateNote(err?.message || String(err), true);
    }
  }

  function installGate() {
    if (!document.body || one("ot-gate")) return;
    gate = document.createElement("div");
    gate.id = "ot-gate";
    gate.innerHTML = `<div class="ot-gate-card">
      <div class="ot-gate-brand"><img src="icons/one-token.svg" alt=""><strong>1token_magpie</strong></div>
      <h1>登录 1token</h1>
      <p>使用 1token 账号登录后，软件会自动同步可用模型和余额。</p>
      <div class="ot-gate-actions">
        <button id="ot-gate-login" class="ot-primary" type="button">登录 1token</button>
        <button id="ot-gate-register" type="button">注册 1token</button>
      </div>
      <button id="ot-gate-paste" class="ot-gate-link" type="button">已有 API 令牌？</button>
      <div id="ot-gate-paste-row" class="ot-gate-paste" hidden>
        <input id="ot-gate-key" type="password" autocomplete="off" placeholder="粘贴 1token API 令牌">
        <button id="ot-gate-save" type="button">保存并使用</button>
      </div>
      <p id="ot-gate-note" class="ot-gate-note" role="status" aria-live="polite"></p>
      <button id="ot-gate-quit" class="ot-gate-link" type="button">退出应用</button>
    </div>`;
    document.body.append(gate);
    one("ot-gate-login").onclick = gateLogin;
    one("ot-gate-register").onclick = gateRegister;
    one("ot-gate-paste").onclick = gatePaste;
    one("ot-gate-save").onclick = gateSaveKey;
    one("ot-gate-key").onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter") gateSaveKey(); };
    one("ot-gate-quit").onclick = () => api("window/quit", {}).catch(() => {});
    void checkGate();
  }

  async function checkGate() {
    const s = await readStatus();
    current = s;
    if (s.loggedIn) {
      gate?.remove();
      gate = null;
      return;
    }
    if (gate) gate.hidden = false;
    if (s.pending) {
      gateNote("请在登录窗口完成授权，完成后会自动返回。");
      void waitForLogin();
    } else if (s.error) {
      gateNote(s.error, true);
    } else {
      gateNote("");
    }
  }

  async function openLogin() {
    try {
      set("ot-updated", "正在打开登录页…");
      await api("one-token/start", {});
      set("ot-updated", "请在登录窗口完成授权…");
      await waitForLogin();
    } catch (err) {
      set("ot-updated", err?.message || String(err));
    }
  }

  async function paint() {
    const s = await readStatus();
    current = s;
    const signed = !!s.loggedIn;
    const models = Array.isArray(s.models) ? s.models : [];
    set("ot-identity", signed ? (s.username || "已登录 1token") : "尚未登录");
    set("ot-connection", signed ? "已连接" : "待连接");
    set("ot-balance", signed ? (s.balance || "—") : "—");
    set("ot-wallet-note", signed ? "API 可用额度，实时来自 1token。" : "登录后查看 API 可用额度。");
    set("ot-login", signed ? "重新登录" : "登录 1token");
    set("ot-models", String(models.length));
    set("ot-chat-title", models.length ? "可用聊天模型" : signed ? "暂无可用的聊天模型" : "登录后同步可用模型");
    set("ot-chat-note", models.length ? "这些模型已进入 Magpie 的模型选择器，Agent 可以直接使用。" : signed ? "请检查站点开放情况与令牌权限。" : "登录后自动同步当前令牌可用的聊天模型。");
    set("ot-account-note", signed ? (s.tokenName ? `令牌：${s.tokenName}` : "1token 账号已登录") : "请先登录 1token 账号。");
    set("ot-updated", s.error ? s.error : signed ? "已同步" : "尚未登录");
    const logout = one("ot-logout");
    if (logout) logout.hidden = !signed;
    renderModels(models, signed);
    return undefined;
  }

  async function refresh() {
    try {
      await api("one-token/refresh", {});
      await paint();
    } catch (err) {
      set("ot-updated", err?.message || String(err));
    }
  }

  async function pasteKey() {
    const key = window.prompt("粘贴 1token API 令牌");
    if (!key) return;
    try {
      await api("one-token/token", { key: key.trim() });
      await paint();
    } catch (err) {
      set("ot-updated", err?.message || String(err));
    }
  }

  async function copyKey() {
    try {
      const r = await api("one-token/key");
      if (!r?.key) { set("ot-updated", "还没有 1token key；请先登录。"); return; }
      await api("copy", { text: r.key });
      set("ot-updated", "已复制 1token key");
    } catch (err) {
      set("ot-updated", err?.message || String(err));
    }
  }

  async function logout() {
    if (!window.confirm("退出 1token 登录？模型会从 Magpie 中移除，账号 key 会保留在旧插件备份里。")) return;
    try {
      await api("one-token/logout", {});
      location.reload();
    } catch (err) {
      set("ot-updated", err?.message || String(err));
    }
  }

  ctx.slots.add("page", {
    key: "one-token", title: "1token 账户", loads: "providers",
    html: `<section class="ot-hero">
      <div class="ot-heading"><div class="ot-brand"><img src="icons/one-token.svg" alt="">1token 桌面版</div><span id="ot-connection" class="ot-badge">待连接</span></div>
      <p>登录 1token 账号，自动同步模型和 API 额度。</p>
      <div class="ot-actions"><button id="ot-login" class="ot-primary" type="button">登录 1token</button><button id="ot-register" type="button">注册 1token</button><button id="ot-copy-key" type="button">复制 1token Key</button><button id="ot-paste-key" type="button">已有 API 令牌</button><button id="ot-refresh" type="button">刷新</button><button id="ot-logout" type="button" hidden>退出登录</button></div>
      <p id="ot-updated" class="ot-updated" role="status" aria-live="polite">尚未登录</p>
    </section>
    <div class="ot-grid">
      <section class="ot-card"><h2>当前账号</h2><strong id="ot-identity">尚未登录</strong><p id="ot-account-note">请先登录 1token 账号。</p></section>
      <section class="ot-card"><h2>API 可用额度</h2><strong id="ot-balance" aria-live="polite">—</strong><p id="ot-wallet-note">登录后查看 API 可用额度。</p><p>视频任务使用独立的视频钱包。</p></section>
    </div>
    <section class="ot-chat"><div class="ot-heading"><h2 id="ot-chat-title">登录后同步可用模型</h2><span class="ot-badge"><span id="ot-models">0</span> 个聊天模型</span></div><p id="ot-chat-note">登录后自动同步当前令牌可用的聊天模型。</p><div id="ot-model-list" class="ot-model-list"></div></section>
    <nav class="ot-shortcuts" aria-label="1token 网页服务">
      <button id="ot-wallet" type="button"><strong>钱包与账单 ↗</strong><span>查看视频余额、冻结金额与收支记录</span></button>
      <button id="ot-tokens" type="button"><strong>生成 / 复制 API 令牌 ↗</strong><span>复制当前 magpie key，或为其他应用新建令牌</span></button>
      <button id="ot-routing" type="button"><strong>统一模型 / 路由组</strong><span>把多个模型合成一个 group，失败自动切换</span></button>
      <button id="ot-logs" type="button"><strong>使用日志 ↗</strong><span>核对调用用量与费用</span></button>
    </nav>
    <section class="ot-steps"><h2>第一次使用</h2><ol><li><strong>登录 / 注册</strong><span>用 1token 账号登录；没有账号可直接注册。</span></li><li><strong>选择模型</strong><span>登录后模型会自动出现在这里和 Magpie 模型选择器。</span></li><li><strong>接入工具</strong><span>去 Agents 页连接 Claude Code、Codex 等。</span></li></ol></section>`,
    mount() {
      one("ot-login").onclick = openLogin;
      one("ot-register").onclick = async () => { try { await api("one-token/register", {}); set("ot-updated", "请在注册页完成注册，然后点登录。"); } catch (err) { set("ot-updated", err?.message || String(err)); } };
      one("ot-copy-key").onclick = copyKey;
      one("ot-paste-key").onclick = pasteKey;
      one("ot-refresh").onclick = refresh;
      one("ot-logout").onclick = logout;
      one("ot-routing").onclick = () => {
        try {
          if (typeof mode !== "undefined" && mode === "panel") api("window/main?view=routing", {}).catch(() => {});
          else if (typeof show === "function") show("routing");
          else api("window/main?view=routing", {}).catch(() => {});
        } catch { /* the main window is enough */ }
      };
      for (const [id, route] of Object.entries({ "ot-wallet": "/wallet", "ot-tokens": "/token", "ot-logs": "/log", "ot-studio": "/studio", "ot-guide": "/docs/c-magpie" })) {
        const el = one(id);
        if (el) el.onclick = () => api("open", { url: site + route }).catch(() => {});
      }
    },
    render: paint, open: paint, refresh,
    leave() { current = null; },
  });

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", installGate);
  else installGate();
});
