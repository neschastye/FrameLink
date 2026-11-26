import { connection, localSDP, watch } from "./rtc.js";
import { transform } from "./link.js";
export class Viewer {
  constructor(role, video, send, playing) {
    this.role = role;
    this.video = video;
    this.send = send;
    this.playing = playing;
    this.video.addEventListener("playing", () => {
      if (this.pc) {
        video.dataset.playing = "1";
        playing?.(true);
      }
    });
  }
  reset() {
    this.stopWatch?.();
    clearInterval(this.timer);
    this.pc?.close();
    this.pc = null;
    this.id = null;
    this.prev = null;
    this.video.srcObject = null;
    delete this.video.dataset.playing;
    this.playing?.(false);
  }
  signal(type, id, extra = {}) {
    if (id === this.id) this.send({ type, peer: this.role, id, ...extra });
  }
  async message(m) {
    if (m.type === "status") {
      transform(this.video, m.settings);
      return;
    }
    if (m.type === "reset" && m.id === this.id) {
      this.reset();
      return;
    }
    if (m.type === "pair") {
      this.reset();
      this.id = m.id;
      return;
    }
    if (m.type !== "offer" || m.id !== this.id) return;
    const pc = connection(),
      id = m.id;
    this.pc = pc;
    const stream = new MediaStream();
    pc.ontrack = (e) => {
      stream.addTrack(e.track);
      this.video.srcObject = stream;
      this.video.play().catch(() => {});
    };
    this.stopWatch = watch(pc, () => this.signal("retry", id));
    try {
      await pc.setRemoteDescription({ type: "offer", sdp: m.sdp });
      const sdp = await localSDP(pc, await pc.createAnswer());
      if (this.pc !== pc) return;
      this.signal("answer", id, { sdp });
      this.timer = setInterval(() => this.stats(pc, id).catch(() => {}), 2000);
    } catch {
      if (this.pc === pc) this.signal("retry", id);
    }
  }
  async stats(pc, id) {
    const report = await pc.getStats();
    if (pc !== this.pc) return;
    const r = [...report.values()].find(
      (r) => r.type === "inbound-rtp" && (r.kind || r.mediaType) === "video",
    );
    if (!r) return;
    const transport = report.get(r.transportId),
      pair =
        report.get(transport?.selectedCandidatePairId) ||
        [...report.values()].find(
          (p) =>
            p.type === "candidate-pair" &&
            p.nominated &&
            p.state === "succeeded",
        );
    const prev = this.prev;
    this.prev = r;
    const dt = prev?.id === r.id ? (r.timestamp - prev.timestamp) / 1000 : 0;
    const delta = (key) =>
      dt > 0 && Number.isFinite(r[key]) && Number.isFinite(prev[key])
        ? Math.max(0, r[key] - prev[key])
        : null;
    const received = delta("packetsReceived"),
      lost = delta("packetsLost"),
      bytes = delta("bytesReceived"),
      frames = delta("framesDecoded");
    this.signal("metrics", id, {
      metrics: {
        fps: r.framesPerSecond ?? (frames !== null ? frames / dt : null),
        width: r.frameWidth ?? (this.video.videoWidth || null),
        height: r.frameHeight ?? (this.video.videoHeight || null),
        mbps: bytes === null ? null : (bytes * 8) / dt / 1e6,
        rtt: Number.isFinite(pair?.currentRoundTripTime)
          ? pair.currentRoundTripTime * 1000
          : null,
        lost:
          lost !== null && received !== null && lost + received > 0
            ? (100 * lost) / (lost + received)
            : null,
      },
    });
  }
}
