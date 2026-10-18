"use strict";

// Phone control page for mediad. Talks to the same /api routes as the desktop app.

const $ = (id) => document.getElementById(id);
const view = $("view");

const state = {
  tab: "remote",
  items: new Map(), // id -> public item
  shows: [],
  devices: [],
  device: load("device"),
  status: null,
  statusErr: "",
  query: "",
  kind: "movies", // movies | shows
  logs: { after: 0, entries: [] },
  info: null,
  scan: null,
};

function load(key) {
  try { return localStorage.getItem(key) || ""; } catch { return ""; }
}
function save(key, value) {
  try { localStorage.setItem(key, value); } catch { /* private mode */ }
}

// h builds an element. Text goes in as text nodes, so file names cannot inject markup.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k === "style") el.style.cssText = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (v === true) el.setAttribute(k, "");
    else el.setAttribute(k, v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

// mount replaces the page body. Unlike replaceChildren it flattens arrays and skips false/null.
function mount(...kids) {
  view.replaceChildren(...kids.flat(Infinity).filter((k) => k != null && k !== false));
}

async function api(path, opts) {
  const init = opts && opts.body !== undefined
    ? { method: opts.method || "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(opts.body) }
    : { method: (opts && opts.method) || "GET" };
  const res = await fetch(path, init);
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) {
    const err = new Error((data && data.error) || `HTTP ${res.status}`);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

let toastTimer;
function toast(msg, isErr) {
  const t = $("toast");
  t.textContent = msg;
  t.className = "show" + (isErr ? " err" : "");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.className = ""; }, 2800);
}

function clock(sec) {
  sec = Math.max(0, Math.floor(sec || 0));
  const hh = Math.floor(sec / 3600), mm = Math.floor(sec % 3600 / 60), ss = sec % 60;
  const p = (n) => String(n).padStart(2, "0");
  return hh ? `${hh}:${p(mm)}:${p(ss)}` : `${mm}:${p(ss)}`;
}

function itemTitle(it) {
  if (!it) return "";
  if (it.show) return `${it.show.title} · S${it.season}E${it.episode}${it.title ? " · " + it.title : ""}`;
  return it.title + (it.year ? ` (${it.year})` : "");
}

function currentDevice() {
  return state.devices.find((d) => d.id === state.device) || null;
}
const devPath = (suffix) => `/api/cast/${encodeURIComponent(state.device)}/${suffix}`;

// ---- data loading ----

async function loadLibrary() {
  try {
    const snap = await api("/api/library");
    state.items = new Map(snap.items.map((i) => [i.id, i]));
    state.shows = snap.shows;
    state.scan = snap.status;
    if (state.tab === "library" && document.activeElement.id !== "q") renderLibrary();
  } catch (e) { /* server restarting; next poll retries */ }
}

async function loadDevices() {
  try {
    state.devices = await api("/api/cast/devices");
  } catch { return; }
  if (!currentDevice() && state.devices.length) state.device = state.devices[0].id;
  const sel = $("device");
  sel.replaceChildren(...(state.devices.length
    ? state.devices.map((d) => h("option", { value: d.id, selected: d.id === state.device }, d.name))
    : [h("option", { value: "" }, "No Chromecast found")]));
}

async function pollStatus() {
  if (!state.device) { state.status = null; return; }
  try {
    state.status = await api(devPath("status"));
    state.statusErr = "";
  } catch (e) {
    state.status = null;
    state.statusErr = e.message;
  }
  if (state.tab === "remote") renderRemote();
}

// ---- tabs ----

function setTab(tab) {
  state.tab = tab;
  save("tab", tab);
  for (const b of document.querySelectorAll("#tabs button")) b.classList.toggle("on", b.dataset.tab === tab);
  window.scrollTo(0, 0);
  render();
}

function render() {
  if (state.tab === "remote") renderRemote();
  else if (state.tab === "library") renderLibrary();
  else renderServer();
}

// ---- remote ----

// While the user drags the seek slider, polls must not move its thumb.
// seeking is cleared on change, on pointer/touch end or cancel, on blur,
// and by a safety timeout so a lost change event cannot freeze the UI.
let seeking = false;
let seekTimer = 0;

function setSeeking(on) {
  clearTimeout(seekTimer);
  seeking = on;
  if (on) seekTimer = setTimeout(() => { seeking = false; }, 5000);
}

// The Remote view is built once per playing item and device, then updated in place on each poll.
let remoteKey = "";
let remoteRoot = null; // top element mounted in view
let remoteRefs = null; // nodes updated on each poll (only while the playing view is shown)

const isPlayingState = (s) => s.state === "PLAYING" || s.state === "BUFFERING";

function seekBy(delta) {
  const s = state.status;
  if (s) control("seek", Math.max(0, s.position + delta));
}

function togglePlay() {
  const s = state.status;
  if (s) control(isPlayingState(s) ? "pause" : "resume");
}

function toggleMute() {
  const s = state.status;
  if (s) control("mute", !s.muted);
}

function renderRemote() {
  const dev = currentDevice();
  const st = state.status;
  const playing = !!dev && !!st && !!st.itemId && !["IDLE", "DISCONNECTED", ""].includes(st.state);

  if (!playing) {
    const key = !dev ? "nodev" : `idle:${dev.name}:${state.statusErr}`;
    if (key === remoteKey && remoteRoot && remoteRoot.parentNode === view) return;
    remoteKey = key;
    remoteRefs = null;
    if (!dev) {
      remoteRoot = h("div", { class: "empty" }, "No Chromecast found yet.", h("br"), "Make sure the TV is on and on this Wi-Fi.");
      mount(remoteRoot);
      return;
    }
    const msg = state.statusErr ? `Can't reach ${dev.name}: ${state.statusErr}` : `Nothing playing on ${dev.name}.`;
    remoteRoot = h("div", { class: "empty" }, msg);
    mount(
      remoteRoot,
      h("div", { class: "btns" }, h("button", { class: "btn primary", onclick: () => setTab("library") }, "Pick something to play")),
    );
    return;
  }

  const it = state.items.get(st.itemId);
  const sig = it ? `${it.audio.map((a) => a.index).join(",")}|${it.subs.map((s) => s.id).join(",")}` : "none";
  const key = `play:${dev.id}:${st.itemId}:${sig}`;
  if (key !== remoteKey || !remoteRefs || remoteRoot.parentNode !== view) buildRemote(key, it, dev);
  updateRemote(st, it, dev);
}

function buildRemote(key, it, dev) {
  setSeeking(false);
  const audio = it ? it.audio : [];
  const subs = it ? it.subs : [];
  const r = {};
  r.art = h("div", { class: "art" });
  r.artCss = null;
  r.title = h("h3", null);
  r.dim = h("div", { class: "dim" });
  r.cur = h("span", null);
  r.dur = h("span", null);
  r.seek = h("input", {
    class: "seek", type: "range", min: 0, step: 1,
    oninput: (e) => { setSeeking(true); r.cur.textContent = clock(e.target.value); },
    onchange: (e) => { setSeeking(false); control("seek", Number(e.target.value)); },
    onpointerup: () => setSeeking(false),
    onpointercancel: () => setSeeking(false),
    ontouchend: () => setSeeking(false),
    ontouchcancel: () => setSeeking(false),
    onblur: () => setSeeking(false),
  });
  r.play = h("button", { class: "btn primary big", onclick: togglePlay });
  r.volume = h("input", { type: "range", min: 0, max: 100, onchange: (e) => control("volume", Number(e.target.value) / 100) });
  r.mute = h("button", { class: "btn", onclick: toggleMute });
  r.audio = audio.length > 1
    ? h("select", { onchange: (e) => control("audio", Number(e.target.value)) },
      audio.map((a) => h("option", { value: a.index }, audioLabel(a))))
    : null;
  r.sub = subs.length > 0
    ? h("select", { onchange: (e) => control("sub", e.target.value) },
      h("option", { value: "" }, "Off"),
      subs.map((s) => h("option", { value: s.id }, s.label || s.lang || s.id)))
    : null;
  const seekButton = (delta, label) => h("button", { class: "btn", onclick: () => seekBy(delta) }, label);

  const root = h("div", { class: "now" },
    r.art,
    h("div", { class: "body" },
      r.title,
      r.dim,
      r.seek,
      h("div", { class: "times" }, r.cur, r.dur),
      h("div", { class: "transport" },
        seekButton(-30, "−30"),
        seekButton(-10, "−10"),
        r.play,
        seekButton(10, "+10"),
        seekButton(30, "+30"),
      ),
      h("div", { class: "row" },
        h("label", null, "Volume"),
        r.volume,
        r.mute,
      ),
      r.audio && h("div", { class: "row" }, h("label", null, "Audio"), r.audio),
      r.sub && h("div", { class: "row" }, h("label", null, "Subs"), r.sub),
      h("div", { class: "btns" }, h("button", { class: "btn danger", onclick: () => control("stop") }, "Stop")),
    ),
  );
  remoteKey = key;
  remoteRoot = root;
  remoteRefs = r;
  mount(root);
}

// updateRemote writes the latest status into the existing nodes.
// Controls the user is touching or has focused keep their current value.
function updateRemote(st, it, dev) {
  const r = remoteRefs;
  const art = it && (it.backdrop || it.poster);
  const artCss = art ? `background-image:url("${art}")` : "";
  if (r.artCss !== artCss) {
    r.artCss = artCss;
    r.art.style.cssText = artCss;
  }
  r.title.textContent = itemTitle(it) || "Playing";
  r.dim.textContent = `${st.state.toLowerCase()} on ${dev.name}${st.mode ? " · " + st.mode : ""}`;
  if (!seeking) {
    const max = String(Math.max(1, Math.floor(st.duration)));
    if (r.seek.max !== max) r.seek.max = max;
    r.seek.value = Math.floor(st.position || 0);
    r.cur.textContent = clock(st.position);
  }
  r.dur.textContent = clock(st.duration);
  r.play.textContent = isPlayingState(st) ? "❚❚" : "▶";
  if (document.activeElement !== r.volume) r.volume.value = Math.round((st.volume || 0) * 100);
  r.mute.textContent = st.muted ? "Unmute" : "Mute";
  if (r.audio && st.audio != null && document.activeElement !== r.audio) r.audio.value = String(st.audio);
  if (r.sub && document.activeElement !== r.sub) {
    const want = st.sub || "";
    r.sub.value = want;
    if (r.sub.value !== want) r.sub.value = "";
  }
}

function audioLabel(a) {
  const parts = [a.title || a.lang || `Track ${a.index + 1}`];
  if (a.codec) parts.push(a.codec);
  if (a.channels) parts.push(`${a.channels}ch`);
  return parts.join(" · ");
}

async function control(action, value) {
  try {
    await api(devPath("control"), { body: { action, value } });
    setTimeout(pollStatus, 300);
  } catch (e) { toast(e.message, true); }
}

// ---- library ----

function posterOf(x) { return x.poster || x.backdrop || ""; }

function card(x, onclick, sub) {
  const img = posterOf(x);
  const p = x.progress;
  const frac = p && x.duration > 0 && !p.watched ? Math.min(1, p.pos / x.duration) : 0;
  return h("button", { class: "card", onclick },
    h("div", { class: "poster", style: img ? `background-image:url("${img}")` : "" },
      !img && h("div", { class: "fallback" }, x.title),
      frac > 0 && h("div", { class: "bar", style: `width:${Math.round(frac * 100)}%` }),
      p && p.watched && h("div", { class: "tick" }, "✓"),
    ),
    h("div", { class: "t" }, x.show ? x.show.title : x.title),
    sub && h("div", { class: "s" }, sub),
  );
}

function renderLibrary() {
  const q = state.query.trim().toLowerCase();
  const all = [...state.items.values()];
  const matches = (s) => !q || s.toLowerCase().includes(q);

  const resume = all
    .filter((i) => i.progress && !i.progress.watched && i.progress.pos >= 30)
    .sort((a, b) => b.progress.t - a.progress.t)
    .slice(0, 12);
  const movies = all.filter((i) => i.type === "movie" && matches(i.title));
  const shows = state.shows.filter((s) => matches(s.title));

  const search = h("input", {
    type: "search", placeholder: "Search", value: state.query, id: "q",
    oninput: (e) => { state.query = e.target.value; renderLibrary(); const q = $("q"); q.focus(); q.setSelectionRange(q.value.length, q.value.length); },
  });
  const chips = h("div", { class: "chips" },
    ["movies", "shows"].map((k) => h("button", { class: "chip" + (state.kind === k ? " on" : ""), onclick: () => { state.kind = k; renderLibrary(); } }, k === "movies" ? "Movies" : "Shows")));

  const list = state.kind === "movies"
    ? movies.map((m) => card(m, () => openItem(m), m.year))
    : shows.map((s) => card(s, () => openShow(s), s.year));

  mount(
    search,
    !q && resume.length > 0 && [h("h2", null, "Continue watching"), h("div", { class: "shelf" }, resume.map((i) => card(i, () => openItem(i), i.show ? `S${i.season}E${i.episode}` : clock(i.progress.pos))))],
    chips,
    list.length ? h("div", { class: "grid" }, list) : h("div", { class: "empty" }, state.items.size ? "No matches." : "Library is empty or still scanning."),
  );
}

// ---- sheets ----

function closeSheet() {
  const s = $("sheet");
  s.hidden = true;
  s.replaceChildren();
}

function showSheet(...kids) {
  const s = $("sheet");
  s.replaceChildren(h("div", { class: "sheet", onclick: (e) => e.stopPropagation() }, kids));
  s.onclick = closeSheet;
  s.hidden = false;
}

function openShow(show) {
  const eps = show.episodes.map((id) => state.items.get(id)).filter(Boolean);
  const seasons = [...new Set(eps.map((e) => e.season))];
  showSheet(
    h("h3", null, show.title),
    h("div", { class: "meta" }, [show.year, show.genres && show.genres.slice(0, 3).join(", "), show.rating && `★ ${show.rating.toFixed(1)}`].filter(Boolean).join(" · ")),
    show.overview && h("p", null, show.overview),
    seasons.map((s) => [
      h("h2", null, s === 0 ? "Specials" : `Season ${s}`),
      seasonButton(eps.filter((e) => e.season === s)),
      eps.filter((e) => e.season === s).map((e) => h("button", { class: "ep", onclick: () => openItem(e) },
        h("span", { class: "n" }, `E${e.episode}`),
        h("span", { class: "tt" }, e.title || `Episode ${e.episode}`),
        e.progress && e.progress.watched && h("span", { class: "ok" }, "✓"),
        e.progress && !e.progress.watched && e.progress.pos >= 30 && h("span", { class: "dim" }, clock(e.progress.pos)),
      )),
    ]),
  );
}

// Track choices are remembered per show by language, so the next episode opens with the same audio and subtitles.
const maxPrefs = 100;

function loadPrefs() {
  try { return JSON.parse(load("trackPrefs") || "{}"); } catch { return {}; }
}

function rememberTracks(it, choice) {
  if (!it.show) return;
  const prefs = loadPrefs();
  const key = it.show.title;
  const was = prefs[key] || {};
  const pref = {};
  const audio = it.audio.length > 1 && it.audio.find((a) => a.index === choice.audio);
  if (audio && audio.lang) pref.audio = audio.lang;
  else if (was.audio) pref.audio = was.audio;
  if (it.subs.length > 0) {
    const sub = it.subs.find((s) => s.id === choice.sub);
    if (!choice.sub) pref.sub = "";
    else if (sub && sub.lang) pref.sub = sub.lang;
  } else if (was.sub != null) pref.sub = was.sub;
  delete prefs[key]; // re-insert so the oldest shows are dropped first
  prefs[key] = pref;
  for (const k of Object.keys(prefs).slice(0, -maxPrefs)) delete prefs[k];
  save("trackPrefs", JSON.stringify(prefs));
}

function defaultTracks(it) {
  const audio = (it.audio.find((a) => a.default) || it.audio[0] || { index: 0 }).index;
  const choice = { audio, sub: "" };
  const pref = it.show ? loadPrefs()[it.show.title] : null;
  if (!pref) return choice;
  const a = pref.audio && it.audio.find((x) => x.lang === pref.audio);
  if (a) choice.audio = a.index;
  const s = pref.sub && it.subs.find((x) => x.lang === pref.sub);
  if (s) choice.sub = s.id;
  return choice;
}

function openItem(it) {
  const choice = defaultTracks(it);
  const dev = currentDevice();
  const watched = it.progress && it.progress.watched;
  const partial = it.progress && !it.progress.watched && it.progress.pos > 0;
  showSheet(
    it.backdrop && h("div", { class: "backdrop", style: `background-image:url("${it.backdrop}")` }),
    h("h3", null, itemTitle(it)),
    h("div", { class: "meta" }, [it.runtime ? `${it.runtime} min` : it.duration ? `${Math.round(it.duration / 60)} min` : "", it.rating && `★ ${it.rating.toFixed(1)}`, it.playMode === "direct" ? "direct play" : "transcode"].filter(Boolean).join(" · ")),
    it.overview && h("p", null, it.overview),
    it.audio.length > 1 && h("div", { class: "row" }, h("label", null, "Audio"),
      h("select", { onchange: (e) => { choice.audio = Number(e.target.value); } },
        it.audio.map((a) => h("option", { value: a.index, selected: a.index === choice.audio }, audioLabel(a))))),
    it.subs.length > 0 && h("div", { class: "row" }, h("label", null, "Subs"),
      h("select", { onchange: (e) => { choice.sub = e.target.value; } },
        h("option", { value: "" }, "Off"),
        it.subs.map((s) => h("option", { value: s.id, selected: s.id === choice.sub }, s.label || s.lang || s.id)))),
    h("div", { class: "actions" },
      h("button", { class: "btn primary", disabled: !dev, onclick: () => play(it, choice) }, dev ? `Play on ${dev.name}` : "No Chromecast"),
      h("button", { class: "btn", onclick: () => markWatched(it, !watched) }, watched ? "Mark unwatched" : "Mark watched"),
      partial && h("button", { class: "btn", onclick: () => markWatched(it, false) }, "Clear progress"),
    ),
  );
}

function seasonButton(eps) {
  const all = eps.length > 0 && eps.every((e) => e.progress && e.progress.watched);
  return h("button", { class: "btn", onclick: () => markSeason(eps, !all) }, all ? "Mark season unwatched" : "Mark season watched");
}

async function markSeason(eps, watched) {
  try {
    for (const e of eps) await api("/api/progress", { body: { id: e.id, watched } });
    closeSheet();
    await loadLibrary();
  } catch (e) { toast(e.message, true); }
}

async function markWatched(it, watched) {
  try {
    await api("/api/progress", { body: { id: it.id, watched } });
    closeSheet();
    await loadLibrary();
  } catch (e) { toast(e.message, true); }
}

async function play(it, choice, resume) {
  const body = { id: it.id, audio: choice.audio, sub: choice.sub };
  if (resume) body.resume = resume;
  try {
    await api(devPath("play"), { body });
    rememberTracks(it, choice);
    closeSheet();
    setTab("remote");
    pollStatus();
  } catch (e) {
    if (e.status === 409 && e.data && e.data.code === "resume") {
      askResume(it, choice, e.data.position);
    } else {
      toast(e.message, true);
    }
  }
}

function askResume(it, choice, pos) {
  showSheet(
    h("h3", null, itemTitle(it)),
    h("div", { class: "meta" }, `You stopped at ${clock(pos)}.`),
    h("div", { class: "actions" },
      h("button", { class: "btn primary", onclick: () => play(it, choice, "continue") }, `Continue from ${clock(pos)}`),
      h("button", { class: "btn", onclick: () => play(it, choice, "restart") }, "Start over"),
      h("button", { class: "btn", onclick: closeSheet }, "Cancel"),
    ),
  );
}

// ---- server ----

async function loadServer() {
  try {
    const [info, scan] = await Promise.all([api("/api/info"), api("/api/status")]);
    state.info = info;
    state.scan = scan;
    const logs = await api(`/api/logs?after=${state.logs.after}`);
    if (logs.last < state.logs.after) state.logs = { after: 0, entries: [] }; // server restarted
    state.logs.entries = state.logs.entries.concat(logs.entries).slice(-300);
    state.logs.after = logs.last;
  } catch { return; }
  if (state.tab === "server") renderServer();
}

function renderServer() {
  const i = state.info, s = state.scan;
  const log = h("div", { class: "log", id: "log" },
    state.logs.entries.map((e) => h("div", { class: e.level }, `${new Date(e.time).toLocaleTimeString()} ${e.level} ${e.msg}`)));
  const old = $("log");
  const stick = !old || old.scrollTop + old.clientHeight >= old.scrollHeight - 24;
  mount(
    h("h2", null, "Server"),
    i ? h("div", { class: "kv" },
      h("span", null, "Name"), h("span", null, i.name),
      h("span", null, "Address"), h("span", null, i.base),
      h("span", null, "Version"), h("span", null, i.version),
      h("span", null, "TMDB"), h("span", null, i.tmdb ? "on" : "off (using video frames)"),
      h("span", null, "Library"), h("span", null, `${state.items.size} items`),
      h("span", null, "Scan"), h("span", null, s && s.scanning ? `${s.phase} ${s.done}/${s.total}` : "idle"),
    ) : h("div", { class: "empty" }, "Can't reach the server."),
    h("div", { class: "btns" },
      h("button", { class: "btn", onclick: () => scan(false) }, "Rescan"),
      h("button", { class: "btn", onclick: () => scan(true) }, "Refresh metadata"),
    ),
    h("h2", null, "Logs"),
    log,
  );
  if (stick) log.scrollTop = log.scrollHeight;
  else old && (log.scrollTop = old.scrollTop);
}

async function scan(refresh) {
  try {
    await api("/api/scan" + (refresh ? "?refresh" : ""), { body: {} });
    toast(refresh ? "Refreshing metadata…" : "Scanning…");
    setTimeout(loadServer, 800);
  } catch (e) { toast(e.message, true); }
}

// ---- start ----

$("device").addEventListener("change", (e) => {
  state.device = e.target.value;
  save("device", state.device);
  state.status = null;
  pollStatus().then(render);
});
for (const b of document.querySelectorAll("#tabs button")) b.addEventListener("click", () => setTab(b.dataset.tab));

(async function init() {
  api("/api/info").then((i) => { state.info = i; $("name").textContent = i.name; document.title = i.name; }).catch(() => {});
  await Promise.all([loadLibrary(), loadDevices()]);
  const tab = load("tab");
  setTab(["remote", "library", "server"].includes(tab) ? tab : "remote");
  pollStatus();
  setInterval(() => { if (!document.hidden) pollStatus(); }, 1500);
  setInterval(() => { if (!document.hidden) loadDevices(); }, 20000);
  setInterval(() => { if (!document.hidden && state.tab === "server") loadServer(); }, 3000);
  setInterval(() => { if (!document.hidden && state.tab !== "server") loadLibrary(); }, 30000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) { pollStatus(); loadLibrary(); } });
  loadServer();
})();
