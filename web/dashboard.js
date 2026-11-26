import { $, Link, linkStatus, read, save } from "./link.js";
import { Viewer } from "./viewer.js";
import { defaults, show, controls } from "./controls.js";
let session = {},
  online = false,
  state = {},
  qrURL = "",
  qrBusy = false,
  toastTimer;
const samples = new Map(),
  preview = $("preview"),
  video = $("video");
preview.checked = read("preview", false) === true;
function toast(text) {
  $("toast").textContent = text;
  $("toast").hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => ($("toast").hidden = true), 2400);
}
async function copy(text) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    toast("Ссылка скопирована");
  } catch {
    window.prompt("Скопируйте ссылку", text);
  }
}
async function updateSession() {
  const opts = { headers: { "X-FrameLink": "dashboard" }, cache: "no-store" };
  const r = await fetch("/api/session", opts);
  if (!r.ok) throw Error("session");
  const info = await r.json();
  session = info;
  $("caPath").textContent = info.ca;
  $("obsAddress").textContent = info.obs.split("#")[0];
  $("camAddress").textContent = info.camera
    ? info.camera.split("#")[0]
    : info.problem || "Нет адреса LAN";
  $("copyCamera").disabled = !info.camera;
  if (info.camera !== qrURL && !qrBusy) {
    qrBusy = true;
    try {
      if (info.camera) {
        const qr = await fetch("/api/qr", opts);
        if (!qr.ok) throw Error("QR");
        const old = $("qr").src;
        $("qr").src = URL.createObjectURL(await qr.blob());
        if (old.startsWith("blob:")) URL.revokeObjectURL(old);
      }
      qrURL = info.camera;
    } finally {
      qrBusy = false;
    }
  }
  $("qr").hidden = !qrURL;
  $("qrWait").hidden = !!qrURL;
  $("qrWait").textContent = info.problem || "Подготовка QR…";
  return info.desk;
}
function render() {
  $("stream").disabled = !online || !state.camera;
  $("stream").classList.toggle("running", !!state.live);
  $("streamLabel").textContent = state.live
    ? "Остановить трансляцию"
    : "Начать трансляцию";
  $("settingsFields").disabled = $("profile").disabled =
    !online || !state.settings;
  $("settingsNote").textContent =
    state.note ||
    (state.camera
      ? "Параметры применяются на подключённом iPhone."
      : "Подключите iPhone для управления.");
  $("camDot").classList.toggle("on", !!state.camera);
  $("obsDot").classList.toggle("on", !!state.obs);
  $("camStatus").textContent = state.live
    ? "Камера активна"
    : state.camera
      ? "iPhone подключён"
      : "Ожидание iPhone";
  $("obsStatus").textContent = state.obs
    ? "Browser Source подключён"
    : "Ожидание Browser Source";
  $("standby").hidden = !!video.dataset.playing;
  $("stageTitle").textContent = preview.checked
    ? "Ожидание видеопотока"
    : "Предпросмотр выключен";
  $("stageHint").textContent = preview.checked
    ? "Кадр появится после запуска камеры."
    : "Включите монитор для проверки кадра.";
}
function renderStats() {
  const now = performance.now();
  const src = ["obs", "desk"].find(
    (role) =>
      state.live &&
      (role === "obs" ? state.obs : preview.checked) &&
      now - (samples.get(role)?.time ?? -Infinity) < 5500,
  );
  const m = samples.get(src)?.metrics || {};
  $("statsSource").textContent =
    src === "obs"
      ? "Приёмник OBS"
      : src === "desk"
        ? "Предпросмотр"
        : "Нет данных";
  $("frameSize").textContent =
    m.width && m.height ? m.width + " × " + m.height : "— × —";
  for (const [k, digits] of [
    ["fps", 0],
    ["mbps", 2],
    ["rtt", 1],
    ["lost", 1],
  ])
    $("metric-" + k).textContent = Number.isFinite(m[k])
      ? m[k].toLocaleString("ru-RU", { maximumFractionDigits: digits })
      : "—";
}
const viewer = new Viewer(
  "desk",
  video,
  (m) => link.send(m),
  () => render(),
);
const link = new Link("desk", updateSession, {
  status(ok, text) {
    online = ok;
    linkStatus(ok, text);
    render();
  },
  open() {
    link.send({ type: "preview", enabled: preview.checked });
  },
  close() {
    state = {};
    samples.clear();
    viewer.reset();
    render();
    renderStats();
  },
  message(m) {
    if (m.type === "status") {
      state = m;
      if (m.settings) show(m.settings);
      if (!m.live) samples.clear();
      if (!m.obs) samples.delete("obs");
      render();
      renderStats();
    }
    if (m.type === "metrics") {
      samples.set(m.peer, { time: performance.now(), metrics: m.metrics });
      renderStats();
    }
    viewer.message(m);
  },
});
controls((settings) =>
  link.send({ type: "control", action: "settings", settings }),
);
$("stream").onclick = () =>
  link.send({ type: "control", action: state.live ? "stop" : "start" });
preview.onchange = () => {
  save("preview", preview.checked);
  link.send({ type: "preview", enabled: preview.checked });
  if (!preview.checked) {
    viewer.reset();
    samples.delete("desk");
  }
  render();
  renderStats();
};
$("copyObs").onclick = () => copy(session.obs);
$("copyCamera").onclick = () => copy(session.camera);
$("certHelp").onclick = () => {
  $("https-help").open = true;
};
setInterval(() => {
  if (!document.hidden) updateSession().catch(() => {});
}, 10000);
setInterval(renderStats, 2500);
show(defaults);
render();
