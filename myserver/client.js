// myweb client for web apps served by myweb (-web-from or -app). Include
//
//   <script src="/_myweb/client.js"></script>
//
// and, when myweb installs a new version of the app, the page offers to
// reload. To show your own prompt instead, add data-banner="off" and listen
// for the event:
//
//   window.addEventListener("myweb:app-updated", (e) => e.detail /* {name, version, notes} */);
(() => {
  if (window.myweb && window.myweb.client) return;
  const script = document.currentScript;
  const banner = !(script && script.dataset.banner === "off");
  const base = new URL(script ? script.src : "/", location.href);
  const wsURL = (base.protocol === "https:" ? "wss://" : "ws://") + base.host + "/events";

  // the version this page was loaded with: myweb names it in Server-Timing
  let loaded = null;
  try {
    const nav = performance.getEntriesByType("navigation")[0];
    const t = nav && (nav.serverTiming || []).find((e) => e.name === "myweb-app");
    if (t && t.description) loaded = t.description;
  } catch { /* older browsers: the first check below sets it */ }
  let shown = null;

  function notify(detail) {
    if (!detail.version || detail.version === shown) return;
    shown = detail.version;
    window.dispatchEvent(new CustomEvent("myweb:app-updated", { detail }));
    if (banner) show(detail);
  }

  // A socket that was down (e.g. the laptop slept) misses the event, so each
  // (re)connect also compares the installed version with the loaded one.
  async function compare() {
    try {
      const r = await fetch(new URL("/cmd/app", base), {
        method: "POST", credentials: "include", headers: { "Content-Type": "application/json" }, body: "{}",
      });
      if (!r.ok) return;
      const st = (await r.json()).result;
      if (!st || !st.current) return;
      if (loaded === null) loaded = st.current;
      else if (st.current !== loaded) notify({ name: st.name, version: st.current, notes: (st.notes || {})[st.current] || "" });
    } catch { /* not served by myweb, or offline */ }
  }

  let retry = 1000;
  function connect() {
    let ws;
    try { ws = new WebSocket(wsURL); } catch { return; }
    ws.onopen = () => { retry = 1000; compare(); };
    ws.onmessage = (e) => {
      let m; try { m = JSON.parse(e.data); } catch { return; }
      if (m.type === "app-updated" && m.version !== loaded) notify({ name: m.name, version: m.version, notes: m.notes || "" });
    };
    ws.onclose = () => { setTimeout(connect, retry); retry = Math.min(retry * 2, 60000); };
  }

  function show(d) {
    let host = document.getElementById("myweb-update");
    if (host) host.remove();
    host = document.createElement("div");
    host.id = "myweb-update";
    const root = host.attachShadow({ mode: "open" });
    root.innerHTML = `<style>
  :host { all: initial; }
  .box { position: fixed; right: 16px; bottom: 16px; z-index: 2147483647; max-width: min(380px, calc(100vw - 32px));
    font: 14px/1.45 system-ui, -apple-system, "Segoe UI", sans-serif; color: #1d1d22; background: #fff;
    border: 1px solid #e4e4ea; border-radius: 12px; box-shadow: 0 6px 24px rgba(0,0,0,.14); padding: 14px 16px; }
  .notes { color: #6b6b76; font-size: 13px; margin: 6px 0 0; white-space: pre-wrap; max-height: 8.5em; overflow: auto; }
  .row { display: flex; gap: 8px; justify-content: flex-end; margin-top: 12px; }
  button { font: 600 13px system-ui, sans-serif; border-radius: 8px; padding: 6px 14px; cursor: pointer;
    border: 1px solid #e4e4ea; background: #fff; color: #1d1d22; }
  button.primary { background: #5a3fd8; border-color: #5a3fd8; color: #fff; }
  @media (prefers-color-scheme: dark) {
    .box { color: #e6e6ea; background: #1b1b21; border-color: #2c2c35; }
    .notes { color: #8d8d99; }
    button { background: #1b1b21; border-color: #2c2c35; color: #e6e6ea; }
    button.primary { background: #a08cff; border-color: #a08cff; color: #121216; }
  }
</style>
<div class="box" role="status">
  <strong></strong>
  <p class="notes" hidden></p>
  <div class="row"><button class="later">Later</button><button class="primary reload">Reload</button></div>
</div>`;
    root.querySelector("strong").textContent = `${d.name || "This app"} ${d.version} is ready.`;
    if (d.notes) {
      const n = root.querySelector(".notes");
      n.textContent = d.notes;
      n.hidden = false;
    }
    root.querySelector(".reload").onclick = () => location.reload();
    root.querySelector(".later").onclick = () => host.remove();
    (document.body || document.documentElement).appendChild(host);
  }

  window.myweb = Object.assign(window.myweb || {}, { client: true });
  connect();
})();
