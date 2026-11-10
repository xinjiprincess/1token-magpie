// Run with Node's test runner and Playwright on the module path; see README.md.
// The owner: the Claude Code provider page says whether its runs are
// Claude Code's interactive mode (TUI) or claude -p (CLI), and what tells
// them apart. Under Claude's accounts a row says which, read from
// /api/claude/mode once as the page opens, with why when -p; "TUI or -p?"
// opens what each is and why the TUI is better, and "Change in Settings"
// goes to the setting. Only switch (nothing served) shows no row. In
// English, Chinese, Japanese and German, Chromium and WebKit, at 1100 and
// 420 wide.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const claude = ({ switchOnly = false } = {}) => ({
  id: "claude", name: "Claude", icon: "claude-color", chat: "", responses: "", anthropic: "", catalog: "", routing: "", switchOnly,
  models: [{ id: "claude-sonnet-5", name: "Claude Sonnet 5", on: true }], agents: [], fallback: [], headers: {}, keyList: [],
  account: {
    agent: "claude", agentName: "Claude Code", user: "work@example.com", plan: "MAX",
    logins: [{ user: "work@example.com", plan: "MAX", active: true, on: true }],
  },
});

function serve(lang, first, mode, reads) {
  const state = { agents: [], profiles: [], settings: { lang, theme: "light" } };
  const providers = () => ({ providers: [first], presets: [], excluded: [], gateway: { running: true, window: true } });
  return async (route) => {
    const url = new URL(route.request().url());
    const json = (data) => route.fulfill({ json: data });
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return json(state);
    if (url.pathname === "/api/providers") return json(providers());
    if (url.pathname === "/api/claude/mode") { reads.n++; return json(mode); }
    if (url.pathname === "/api/settings") return json(state.settings);
    if (url.pathname === "/api/groups") return json({ models: [], groups: [], pools: [] });
    if (url.pathname === "/api/plugins") return json({ plugins: [] });
    if (url.pathname.startsWith("/api/")) return json({});
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

const words = {
  en: { tui: "Running as Claude Code's interactive mode (TUI)", cli: "Running as claude -p (CLI)", off: "turned off in Settings", wsl: "WSL", why: "about half" },
  zh: { tui: "当前走 Claude Code 交互模式（TUI）", cli: "当前走 claude -p（CLI）", off: "设置中关闭", wsl: "WSL", why: "一半" },
  ja: { tui: "Claude Code の対話モード（TUI）で実行中", cli: "claude -p（CLI）で実行中", off: "設定でオフ", wsl: "WSL", why: "約半分" },
  de: { tui: "Läuft im interaktiven Modus von Claude Code (TUI)", cli: "Läuft als claude -p (CLI)", off: "Einstellungen ausgeschaltet", wsl: "WSL", why: "Hälfte" },
};
const strings = [
  "The interactive mode is turned off in Settings",
  "The interactive mode failed to start on this computer lately; magpie tries it again after a while",
  "magpie couldn't find the home folder of Claude Code in WSL",
  "Claude Code isn't installed on this computer",
  "This system has no interactive mode for it",
  "Running as Claude Code's interactive mode (TUI)",
  "Running as claude -p (CLI)",
  "TUI or -p?",
  "Change in Settings",
  "A request with safeguards, a JSON schema, an image known by its URL or a first message over 400KB still runs as -p",
  "TUI: magpie runs Claude Code as a person does, in its interactive screen, and gives it each request there. Anthropic sees Claude Code's own interactive use (entrypoint cli), and the plan counts a request at about half of what -p uses, so the same plan lasts about twice as long.",
  "-p (CLI): claude -p, Claude Code's mode for scripts (entrypoint sdk-cli). It takes everything a request can carry, but the plan counts each request at about twice the interactive mode's. magpie uses it only when the interactive mode is off, can't run here, or a request needs what only -p takes.",
];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const width of [1100, 420]) {
    for (const lang of Object.keys(words)) {
      const w = words[lang];
      const open = async (t, mode, opts = {}) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        t.after(() => browser.close());
        const page = await (await browser.newContext({ viewport: { width, height: 760 }, reducedMotion: "reduce" })).newPage();
        page.setDefaultTimeout(5000);
        page.setDefaultNavigationTimeout(60000);
        const errors = [], reads = { n: 0 };
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", serve(lang, claude(opts), mode, reads));
        await page.goto("http://magpie.test/?view=providers");
        // the first draw waits on the page's first reads: a load's room, not a click's (#4470)
        const row = page.locator(".row.provider", { hasText: "Claude" }).first();
        await row.waitFor({ timeout: 60000 });
        await row.click();
        await page.locator(".editor .acc").first().waitFor();
        return { page, errors, reads };
      };
      const fits = async (page, sel) => {
        const box = await page.locator(sel).boundingBox();
        assert.ok(box.x >= 0 && box.x + box.width <= width, `${sel} fits across ${width}`);
      };

      test(`${engine} ${width} ${lang}: the TUI is said, and what tells it from -p opens`, async (t) => {
        const { page, errors, reads } = await open(t, { tui: true });
        const mode = page.locator(".editor .accts .claude-mode");
        await mode.locator(".mode-name").waitFor();
        assert.equal((await mode.locator(".mode-name").textContent()).trim(), w.tui);
        assert.ok(await mode.locator(".dot.on").count(), "the TUI's dot is lit");
        assert.match(await mode.locator(".note").first().textContent(), /400 ?KB/, "what still runs as -p is said");
        if (lang === "en") {
          const missing = await page.evaluate((ks) => ["zh", "zh-TW", "ja", "de"].flatMap((l) => ks.filter((k) => !I18N[l][k]).map((k) => `${l}: ${k}`)), strings);
          assert.deepEqual(missing, [], "every string has its translations");
        }
        await mode.scrollIntoViewIfNeeded();
        await fits(page, ".editor .accts .claude-mode");
        const more = mode.locator("button.link").first();
        assert.equal(await more.getAttribute("aria-expanded"), "false");
        await more.click();
        await page.locator(".editor .accts .claude-mode p.note").nth(1).waitFor();
        const said = await mode.locator("p.note").allTextContents();
        assert.match(said[0], new RegExp(w.why), "the TUI's saving is said");
        assert.match(said[0], /entrypoint cli/);
        assert.match(said[1], /entrypoint sdk-cli/);
        assert.equal(await mode.locator("button.link").first().getAttribute("aria-expanded"), "true");
        await fits(page, ".editor .accts .claude-mode");
        assert.equal(reads.n, 1, "the mode is read once, not on each draw");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${width} ${lang}: -p says why, and Change in Settings goes to the setting`, async (t) => {
        const { page, errors } = await open(t, { tui: false, why: "off" });
        const mode = page.locator(".editor .accts .claude-mode");
        await mode.locator(".mode-name").waitFor();
        assert.equal((await mode.locator(".mode-name").textContent()).trim(), w.cli);
        assert.equal(await mode.locator(".dot.on").count(), 0);
        assert.match(await mode.locator(".note").first().textContent(), new RegExp(w.off));
        await mode.locator("button.link").nth(1).click();
        await page.locator("#claudeTuiRow").waitFor();
        assert.ok(await page.locator("#setPage-models").isVisible(), "Settings opens on the setting's page");
        assert.deepEqual(errors, []);
      });

      test(`${engine} ${width} ${lang}: Claude Code in WSL whose home is unknown says so; Only switch shows no row`, async (t) => {
        const wsl = await open(t, { tui: false, why: "wsl-home", wsl: true });
        await wsl.page.locator(".editor .accts .claude-mode .mode-name").waitFor();
        assert.match(await wsl.page.locator(".editor .accts .claude-mode .note").first().textContent(), new RegExp(w.wsl));
        const only = await open(t, { tui: true }, { switchOnly: true });
        await only.page.locator(".editor .accts .switch-only").waitFor();
        assert.equal(await only.page.locator(".editor .accts .claude-mode").count(), 0);
        assert.equal(only.reads.n, 0, "nothing is read for a row not shown");
        assert.deepEqual([...wsl.errors, ...only.errors], []);
      });
    }
  }
}
