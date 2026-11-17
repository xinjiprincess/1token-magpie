// Model export UI uses safe fixture metadata only; requests never reach a live
// Magpie, a source credential store, Xfriend or an operating-system URL handler.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");
const assets = process.env.ASSET_DIR || path.resolve(__dirname, "../assets");
const engines = process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"];

async function fixture(t, engine, lang) {
  const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
  const page = await browser.newPage({ viewport: { width: 900, height: 800 }, reducedMotion: "reduce" });
  page.setDefaultTimeout(10000);
  const errors = [], posted = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const control = { posted, changed: false, scans: 0 };
  await page.route("**/*", async (route) => {
    const req = route.request(), url = new URL(req.url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs={lang:"${lang}",theme:"light",web:false};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export {};" });
    if (url.pathname === "/api/one-token/status") return json({ loggedIn: true, username: "fixture", models: [] });
    if (url.pathname === "/api/state") return json({ agents: [], profiles: [], settings: { lang, theme: "light" } });
    if (url.pathname === "/api/providers") return json({ providers: [{ id: "go", name: "OpenCode Go", host: "go.example", chat: "https://go.example/v1", models: [], agents: [], key: { set: true, masked: "…test" } }], presets: [], excluded: [], gateway: { running: true, window: true } });
    if (url.pathname === "/api/groups") return json({ groups: [], pools: [] });
    if (url.pathname === "/api/importapps/discovery") return json([]);
    if (url.pathname === "/api/model-transfer/preview") { control.scans++; return json({ fingerprint: "opaque-source-hash", source_id: "source-id", providers: [{ id: "opencode-go", name: "OpenCode Go", kind: "api", auth_state: "ready", credential_present: true, model_count: 2 }, { id: "commandcode-plan", name: "Command Code GOAT", kind: "subscription", auth_state: "needs_reauth", credential_present: false, model_count: 2 }], groups: [{ id: "daily", name: "Daily routing", member_count: 2 }], accounts: [], plugins: [] }); }
    if (url.pathname === "/api/model-transfer/export") { posted.push(req.postDataJSON()); if (control.changed) return route.fulfill({ status: 409, json: { error: "MODEL_TRANSFER_CHANGED" } }); return json({ path: "~/Downloads/fixture.xfriend-models", providers: 1, groups: 0 }); }
    if (url.pathname === "/api/model-transfer/open-xfriend") { posted.push(req.postDataJSON()); return json({ opened: true }); }
    if (url.pathname === "/api/gateway/trace") return new Promise(() => {});
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    try { return await route.fulfill({ body: await fs.readFile(file), contentType }); } catch { return route.fulfill({ status: 404, body: "" }); }
  });
  t.after(async () => { await browser.close(); assert.deepEqual(errors, []); });
  await page.goto("http://magpie.test/?view=providers");
  await page.locator("#providers .row").first().waitFor();
  return { page, control };
}
for (const engine of engines) for (const lang of ["en", "zh"]) {
  test(`${engine} ${lang}: selective encrypted export and empty Xfriend handoff`, async (t) => {
    const { page, control } = await fixture(t, engine, lang);
    assert.equal(control.scans, 0, "model-transfer reads only when the reader asks");
    await page.locator("#exportXfriend").click();
    const dialog = page.locator("#xfriendExportDialog");
    await dialog.locator('[data-kind="provider"]').first().waitFor();
    assert.match(await dialog.innerText(), lang === "zh" ? /重新授权/ : /authorized again/);
    await page.setViewportSize({ width: 360, height: 800 });
    const box = await dialog.boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= 360, "export dialog fits a narrow screen");
    if (process.env.ARTIFACT_DIR) {
      await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
      await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `export-${engine}-${lang}-360.png`) });
    }

    assert.equal(await dialog.locator('input[type="password"]').inputValue(), "");
    await dialog.locator('[data-kind="provider"][value="commandcode-plan"]').uncheck();
    await dialog.locator('[data-kind="group"]').uncheck();
    await dialog.locator('input[type="password"]').fill("fixture-password");
    const include = dialog.locator('label').filter({ hasText: lang === "zh" ? "在加密文件中包含 API Key" : "Include API keys in the encrypted file" }).locator('input');
    await include.check();
    await dialog.getByRole("button", { name: lang === "zh" ? "导出加密文件" : "Export encrypted file", exact: true }).click();
    await dialog.waitFor({ state: "detached" });
    assert.deepEqual(control.posted[0], { selection: { fingerprint: "opaque-source-hash", provider_ids: ["opencode-go"], group_ids: [], include_keys: true }, password: "fixture-password" });
    assert.equal(JSON.stringify(control.posted[0]).includes("api_key"), false);
    await page.locator("#sendXfriend").click();
    await page.waitForFunction(() => document.querySelector("#status")?.textContent.includes("Xfriend"));
    assert.deepEqual(control.posted[1], {});
  });
  test(`${engine} ${lang}: changed source keeps the export open and sends no plaintext`, async (t) => {
    const { page, control } = await fixture(t, engine, lang); control.changed = true;
    await page.locator("#exportXfriend").click();
    const dialog = page.locator("#xfriendExportDialog");
    await dialog.locator('[data-kind="provider"]').first().waitFor();
    await dialog.locator('input[type="password"]').fill("fixture-password");
    await dialog.getByRole("button", { name: lang === "zh" ? "导出加密文件" : "Export encrypted file", exact: true }).click();
    await dialog.locator(".warnbox:not([hidden])").waitFor();
    assert.match(await dialog.innerText(), lang === "zh" ? /源配置已变化/ : /source changed/);
    assert.equal(control.posted.length, 1);
    await page.keyboard.press("Escape");
    await dialog.waitFor({ state: "detached" });
  });
}
