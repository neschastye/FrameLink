// SDP is sent once host candidates have been gathered. No STUN or TURN service.
export function connection() {
  return new RTCPeerConnection({ iceServers: [], bundlePolicy: "max-bundle" });
}
export async function localSDP(pc, description) {
  await pc.setLocalDescription(description);
  if (pc.iceGatheringState !== "complete") {
    await new Promise((resolve) => {
      const done = () => {
        clearTimeout(timer);
        pc.removeEventListener("icegatheringstatechange", check);
        pc.removeEventListener("signalingstatechange", check);
        resolve();
      };
      const check = () => {
        if (
          pc.iceGatheringState === "complete" ||
          pc.signalingState === "closed"
        )
          done();
      };
      const timer = setTimeout(done, 4000);
      pc.addEventListener("icegatheringstatechange", check);
      pc.addEventListener("signalingstatechange", check);
      check();
    });
  }
  if (!pc.localDescription?.sdp.includes("a=candidate:"))
    throw Error("ICE: нет локального кандидата");
  return pc.localDescription.sdp;
}
export function watch(pc, retry) {
  let timer;
  const schedule = (ms) => {
    clearTimeout(timer);
    timer = setTimeout(retry, ms);
  };
  schedule(15000);
  const changed = () => {
    const state = pc.connectionState || pc.iceConnectionState;
    if (["connected", "completed", "closed"].includes(state))
      clearTimeout(timer);
    else if (state === "failed") schedule(600);
    else if (state === "disconnected") schedule(4000);
  };
  pc.oniceconnectionstatechange = changed;
  pc.onconnectionstatechange = changed;
  return () => clearTimeout(timer);
}
export async function limitSender(sender, settings, preview) {
  if (!sender.getParameters || !sender.setParameters) return false;
  const apply = async (full) => {
    const p = sender.getParameters();
    if (!p.encodings?.length) return false;
    const e = p.encodings[0];
    e.maxBitrate = Math.min(settings.mbps, preview ? 1.2 : 30) * 1e6;
    if (full && preview) {
      e.maxFramerate = Math.min(15, settings.fps);
      const t = sender.track.getSettings();
      e.scaleResolutionDownBy = Math.max(
        1,
        Math.min(t.width || settings.height, t.height || settings.height) / 360,
      );
    }
    await sender.setParameters(p);
    return true;
  };
  try {
    return await apply(true);
  } catch {
    try {
      return await apply(false);
    } catch {
      return false;
    }
  }
}
