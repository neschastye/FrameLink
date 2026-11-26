import {
  $,
  read,
  save,
  Link,
  linkStatus,
  pageToken,
  transform,
} from "./link.js";
import { defaults, clean, controls, show } from "./controls.js";
import { connection, localSDP, watch, limitSender } from "./rtc.js";

let config = clean(read("camera", defaults)),
  stream = null,
  authorized = false,
  online = false,
  busy = false;
let lens = read("lens", ""),
  notice = "",
  queue = Promise.resolve(),
  wake = null,
  deviceGeneration = 0;
const peers = new Map(),
  video = $("video");
const link = new Link("camera", () => pageToken("camera"), {
  status(ok, text) {
    online = ok;
    linkStatus(ok, text);
    render();
  },
  open: () => publish(),
  close: () => {
    for (const role of [...peers.keys()]) drop(role);
  },
  message: (m) => handle(m),
});
function render() {
  $("stream").disabled =
    busy ||
    (!online && !stream) ||
    !navigator.mediaDevices?.getUserMedia ||
    !window.RTCPeerConnection;
  $("streamLabel").textContent = busy
    ? "Подготовка…"
    : stream
      ? "Остановить трансляцию"
      : "Начать трансляцию";
  $("stream").classList.toggle("running", !!stream);
  $("standby").hidden = !!stream;
  $("camStatus").textContent = stream ? "Камера активна" : "Ожидание запуска";
  const current = stream?.getVideoTracks()[0]?.getSettings() || {};
  $("frameSize").textContent = current.width
    ? current.width + " × " + current.height
    : "— × —";
  $("note").textContent =
    notice || "Оставьте Safari открытым во время трансляции.";
  transform(video, config);
}
function publish() {
  render();
  link.send({
    type: "state",
    enabled: !!stream,
    settings: config,
    note: notice,
  });
}
function explain(error) {
  return (
    {
      NotAllowedError:
        "Доступ отклонён. Разрешите камеру и выбранный микрофон в настройках Safari.",
      NotFoundError: "Устройство захвата не найдено.",
      NotReadableError: "Камера занята или недоступна.",
      OverconstrainedError: "Устройство не поддерживает выбранные параметры.",
    }[error?.name] ||
    "Ошибка камеры: " + String(error?.message || error).slice(0, 130)
  );
}
function serial(fn) {
  queue = queue.then(async () => {
    busy = true;
    render();
    try {
      await fn();
    } catch (e) {
      notice = explain(e);
    } finally {
      busy = false;
      publish();
    }
  });
  return queue;
}
function drop(role) {
  const p = peers.get(role);
  if (!p) return;
  peers.delete(role);
  clearTimeout(p.retry);
  p.stop();
  p.pc.close();
}
async function requestWake() {
  if (!navigator.wakeLock || !stream || document.hidden || wake) return;
  try {
    const lock = await navigator.wakeLock.request("screen");
    if (!stream) {
      await lock.release();
      return;
    }
    wake = lock;
    lock.onrelease = () => {
      if (wake === lock) wake = null;
    };
  } catch {}
}
function stop() {
  ++deviceGeneration;
  for (const role of [...peers.keys()]) drop(role);
  if (stream) {
    for (const track of stream.getTracks()) {
      track.onended = null;
      track.onmute = null;
      track.onunmute = null;
      track.stop();
    }
  }
  stream = null;
  video.srcObject = null;
  delete video.dataset.playing;
  if (wake) {
    wake.release().catch(() => {});
    wake = null;
  }
  $("hardware").replaceChildren();
  $("hardware").hidden = true;
  notice = "";
  publish();
}
async function start() {
  if (stream) return;
  const [w, h] =
    config.height === 480
      ? [854, 480]
      : [Math.round((config.height * 16) / 9), config.height];
  const v = {
    width: { ideal: w },
    height: { ideal: h },
    frameRate: { ideal: config.fps, max: config.fps },
  };
  if (lens) v.deviceId = { exact: lens };
  else v.facingMode = { ideal: config.facing };
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      video: v,
      audio: config.audio,
    });
  } catch (e) {
    if (lens && ["OverconstrainedError", "NotFoundError"].includes(e.name)) {
      lens = "";
      save("lens", lens);
      return start();
    }
    throw e;
  }
  const current = stream,
    track = current.getVideoTracks()[0];
  authorized = true;
  notice = "";
  video.muted = true;
  video.srcObject = current;
  video.dataset.playing = "1";
  video.play().catch(() => {});
  track.onended = () => {
    if (stream === current) {
      stop();
      notice =
        "Safari остановил камеру. Откройте вкладку и нажмите «Начать трансляцию».";
      publish();
    }
  };
  track.onmute = () => {
    if (stream === current) {
      notice = "Видео приостановлено устройством. Вернитесь в Safari.";
      publish();
    }
  };
  track.onunmute = () => {
    if (stream === current) {
      notice = "";
      publish();
    }
  };
  publish();
  requestWake();
  hardware(track, ++deviceGeneration, true).catch(() => {});
}
async function change(next) {
  next = clean(next);
  const reacquire = ["facing", "height", "fps", "audio"].some(
    (k) => next[k] !== config[k],
  );
  if (next.facing !== config.facing) {
    lens = "";
    save("lens", lens);
  }
  config = next;
  save("camera", config);
  show(config);
  if (stream && reacquire) {
    stop();
    await start();
  } else {
    for (const [role, p] of peers) {
      if (p.sender && !(await limitSender(p.sender, config, role === "desk")))
        notice = "Браузер не применил ограничение битрейта.";
    }
  }
  publish();
}
const hardwareFields = [
  ["zoom", "Зум"],
  ["focusMode", "Фокусировка"],
  ["focusDistance", "Дистанция фокусировки"],
  ["exposureMode", "Экспозиция"],
  ["exposureTime", "Выдержка"],
  ["exposureCompensation", "Экспокоррекция"],
];
async function hardware(track, generation, restore = false) {
  const container = $("hardware"),
    caps = track.getCapabilities?.() || {};
  let actual = track.getSettings();
  const storeKey = "hardware." + (actual.deviceId || config.facing);
  const saved = read(storeKey, {}) || {};
  if (restore && track.applyConstraints) {
    const supported = {};
    for (const [key] of hardwareFields) {
      const cap = caps[key],
        value = saved[key];
      if (
        Array.isArray(cap)
          ? cap.includes(value)
          : Number.isFinite(value) &&
            cap &&
            value >= cap.min &&
            value <= cap.max
      )
        supported[key] = value;
    }
    if (Object.keys(supported).length) {
      try {
        await track.applyConstraints({
          ...track.getConstraints(),
          advanced: [supported],
        });
      } catch {}
      actual = track.getSettings();
    }
  }
  const devices = (await navigator.mediaDevices.enumerateDevices?.()) || [];
  if (generation !== deviceGeneration) return;
  container.replaceChildren();
  function field(name, input) {
    const label = document.createElement("label");
    label.textContent = name;
    label.append(input);
    container.append(label);
  }
  const cams = devices.filter((d) => d.kind === "videoinput" && d.deviceId);
  if (cams.length > 1) {
    const select = document.createElement("select");
    cams.forEach((d, i) =>
      select.add(new Option(d.label || "Камера " + (i + 1), d.deviceId)),
    );
    select.value = actual.deviceId || lens;
    field("Объектив", select);
    select.onchange = () =>
      serial(async () => {
        lens = select.value;
        save("lens", lens);
        stop();
        await start();
      });
  }
  for (const [key, label] of hardwareFields) {
    const cap = caps[key];
    if (!cap || !track.applyConstraints) continue;
    if (
      key === "focusDistance" &&
      actual.focusMode &&
      actual.focusMode !== "manual"
    )
      continue;
    if (
      key === "exposureTime" &&
      actual.exposureMode &&
      actual.exposureMode !== "manual"
    )
      continue;
    let input;
    if (Array.isArray(cap) && cap.length > 1) {
      input = document.createElement("select");
      cap.forEach((v) => input.add(new Option(v, v)));
      input.value = actual[key];
    } else if (Number.isFinite(cap.min) && cap.max > cap.min) {
      input = document.createElement("input");
      input.type = "range";
      input.min = cap.min;
      input.max = cap.max;
      input.step = cap.step || "any";
      input.value = actual[key] ?? cap.min;
    } else continue;
    field(label, input);
    input.onchange = () =>
      serial(async () => {
        const value =
          input.type === "range" ? Number(input.value) : input.value;
        const constraints = track.getConstraints();
        const advanced = Object.assign({}, ...(constraints.advanced || []), {
          [key]: value,
        });
        if (key === "focusMode" && value !== "manual")
          delete advanced.focusDistance;
        if (key === "exposureMode" && value !== "manual")
          delete advanced.exposureTime;
        await track.applyConstraints({ ...constraints, advanced: [advanced] });
        if (track.getSettings()[key] !== value) {
          notice = "Устройство не подтвердило параметр «" + label + "».";
        } else {
          saved[key] = value;
          if (key === "focusMode" && value !== "manual")
            delete saved.focusDistance;
          if (key === "exposureMode" && value !== "manual")
            delete saved.exposureTime;
          save(storeKey, saved);
        }
        await hardware(track, generation);
      });
  }
  container.hidden = container.children.length === 0;
}
async function pair(m) {
  drop(m.peer);
  if (!stream) return;
  const pc = connection(),
    p = { pc, id: m.id, sender: null };
  function retry() {
    if (peers.get(m.peer) === p)
      link.send({ type: "retry", id: m.id, peer: m.peer });
  }
  p.stop = watch(pc, retry);
  peers.set(m.peer, p);
  try {
    for (const track of stream.getTracks()) {
      if (track.kind === "audio" && m.peer === "desk") continue;
      const tx = pc.addTransceiver(track, {
        direction: "sendonly",
        streams: [stream],
      });
      if (track.kind === "video") p.sender = tx.sender;
    }
    const sdp = await localSDP(pc, await pc.createOffer());
    if (peers.get(m.peer) === p)
      link.send({ type: "offer", peer: m.peer, id: m.id, sdp });
  } catch {
    if (peers.get(m.peer) === p) p.retry = setTimeout(retry, 1500);
  }
}
async function handle(m) {
  if (m.type === "pair") {
    await pair(m);
    return;
  }
  const p = peers.get(m.peer);
  if (m.type === "reset" && p?.id === m.id) {
    drop(m.peer);
    return;
  }
  if (m.type === "answer" && p?.id === m.id) {
    try {
      await p.pc.setRemoteDescription({ type: "answer", sdp: m.sdp });
      if (
        p.sender &&
        !(await limitSender(p.sender, config, m.peer === "desk")) &&
        peers.get(m.peer) === p
      ) {
        notice = "Браузер не применил ограничение битрейта.";
        publish();
      }
    } catch {
      if (peers.get(m.peer) === p)
        link.send({ type: "retry", id: m.id, peer: m.peer });
    }
  }
  if (m.type === "control") {
    if (m.action === "settings") serial(() => change(m.settings));
    else if (m.action === "stop") serial(stop);
    else if (m.action === "start" && authorized) serial(start);
    else {
      notice = "Для первого запуска нажмите кнопку на iPhone.";
      publish();
    }
  }
}
$("stream").onclick = () => serial(stream ? stop : start);
controls((next) => serial(() => change(next)));
show(config);
if (!navigator.mediaDevices?.getUserMedia || !window.RTCPeerConnection)
  notice = "Для камеры требуется Safari с WebRTC и доверенным HTTPS.";
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) requestWake();
});
addEventListener("pagehide", () => stop());
render();
