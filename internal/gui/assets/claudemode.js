// The Claude Code provider editor's row saying whether a Claude
// subscription's runs are Claude Code's interactive mode (TUI) or claude
// -p, why -p, and what tells them apart. app.js imports this with the
// row's first read of /api/claude/mode (claudeModeRow), so it weighs
// nothing on the core.
let open = false;
window.claudeModeDraw = function draw(box, m) {
  if (!m) return;
  box.hidden = false;
  const why = {
    off: "The interactive mode is turned off in Settings",
    broken: "The interactive mode failed to start on this computer lately; magpie tries it again after a while",
    "wsl-home": "magpie couldn't find the home folder of Claude Code in WSL",
    missing: "Claude Code isn't installed on this computer",
  }[m.why] || (m.why ? "This system has no interactive mode for it" : "A request with safeguards, a JSON schema, an image known by its URL or a first message over 400KB still runs as -p");
  const head = el("div", "mode-head");
  const more = el("button", "link", t("TUI or -p?"));
  more.setAttribute("aria-expanded", String(open));
  more.onclick = () => { open = !open; draw(box, m); };
  const set = el("button", "link", t("Change in Settings"));
  set.onclick = (e) => openSettingsAt(e, "models", $("#claudeTuiRow"));
  head.append(el("span", "dot" + (m.tui ? " on" : "")),
    el("span", "mode-name", t(m.tui ? "Running as Claude Code's interactive mode (TUI)" : "Running as claude -p (CLI)")), more, set);
  box.replaceChildren(head, el("div", "note", t(why)), ...(open ? [
    "TUI: magpie runs Claude Code as a person does, in its interactive screen, and gives it each request there. Anthropic sees Claude Code's own interactive use (entrypoint cli), and the plan counts a request at about half of what -p uses, so the same plan lasts about twice as long.",
    "-p (CLI): claude -p, Claude Code's mode for scripts (entrypoint sdk-cli). It takes everything a request can carry, but the plan counts each request at about twice the interactive mode's. magpie uses it only when the interactive mode is off, can't run here, or a request needs what only -p takes.",
  ].map((s) => el("p", "note wraps", t(s))) : []));
};
