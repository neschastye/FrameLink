export const $ = (id) => document.getElementById(id);
export function read(key, fallback) {
  try {
    const value = localStorage.getItem("framelink." + key);
    return value === null ? fallback : JSON.parse(value);
  } catch {
    return fallback;
  }
}
export function save(key, value) {
  try {
    localStorage.setItem("framelink." + key, JSON.stringify(value));
  } catch {}
}
export function pageToken(role) {
  const key = "framelink.token." + role,
    hash = location.hash.slice(1);
  if (/^[A-Za-z0-9_-]{32}$/.test(hash)) {
    try {
      sessionStorage.setItem(key, hash);
      history.replaceState(null, "", location.pathname);
    } catch {}
    return hash;
  }
  try {
    return sessionStorage.getItem(key) || "";
  } catch {
    return "";
  }
}
export class Link {
  constructor(role, credential, events) {
    this.role = role;
    this.credential = credential;
    this.events = events;
    this.delay = 500;
    this.open();
    addEventListener("pagehide", () => {
      this.paused = true;
      clearTimeout(this.timer);
      this.ws?.close();
    });
    addEventListener("pageshow", () => {
      if (this.paused) {
        this.paused = false;
        this.open();
      }
    });
  }
  async open() {
    let key;
    try {
      key = await this.credential();
    } catch {
      this.reconnect();
      return;
    }
    if (this.paused) return;
    if (!key) {
      this.events.status(false, "Откройте ссылку из панели");
      return;
    }
    const url = new URL("/ws", location.href);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("role", this.role);
    const socket = new WebSocket(url, ["framelink.v1", "auth." + key]);
    this.ws = socket;
    socket.onopen = () => {
      if (this.ws !== socket) return;
      this.delay = 500;
      this.events.status(true, "Сервер подключён");
      this.events.open?.();
    };
    socket.onmessage = (e) => {
      if (this.ws === socket) this.events.message(JSON.parse(e.data));
    };
    socket.onclose = (e) => {
      if (this.ws !== socket) return;
      this.events.close?.();
      if (this.paused) return;
      if (e.code === 4009) this.delay = 8000;
      this.events.status(
        false,
        e.code === 4009
          ? "Эта роль уже открыта"
          : this.role === "camera" && this.delay >= 4000
            ? "Проверьте Wi-Fi и QR-код"
            : "Повторное подключение…",
      );
      this.reconnect();
    };
  }
  reconnect() {
    if (this.paused) return;
    clearTimeout(this.timer);
    this.timer = setTimeout(
      () => this.open(),
      this.delay + Math.random() * 200,
    );
    this.delay = Math.min(8000, this.delay * 2);
  }
  send(msg) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(msg));
      return true;
    }
    return false;
  }
}
export function linkStatus(online, text) {
  const el = $("linkState");
  if (el) {
    el.textContent = text;
    el.classList.toggle("online", online);
  }
}
export function transform(video, s) {
  const angle = s?.rotation || 0,
    quarter = angle % 180 !== 0;
  const parent = video.parentElement;
  const update = () => {
    video.style.width =
      (quarter ? parent.clientHeight : parent.clientWidth) + "px";
    video.style.height =
      (quarter ? parent.clientWidth : parent.clientHeight) + "px";
    video.style.left = "50%";
    video.style.top = "50%";
    video.style.right = "auto";
    video.style.bottom = "auto";
    video.style.transform =
      "translate(-50%, -50%) scaleX(" +
      (s?.mirror ? -1 : 1) +
      ") rotate(" +
      angle +
      "deg)";
  };
  update();
  video._resize?.disconnect();
  if (window.ResizeObserver) {
    video._resize = new ResizeObserver(update);
    video._resize.observe(parent);
  }
}
