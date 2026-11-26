import { $ } from "./link.js";
export const defaults = {
  facing: "environment",
  height: 1080,
  fps: 30,
  mbps: 6,
  rotation: 0,
  mirror: false,
  audio: false,
};
const profiles = {
  economy: [720, 30, 3],
  standard: [1080, 30, 6],
  motion: [1080, 60, 10],
};
export function clean(input) {
  const s = { ...defaults, ...input };
  for (const [key, values] of Object.entries({
    height: [480, 720, 1080],
    fps: [24, 30, 60],
    rotation: [0, 90, 180, 270],
    facing: ["environment", "user"],
  }))
    if (!values.includes(s[key])) s[key] = defaults[key];
  s.mbps = Math.min(30, Math.max(1, Math.round(Number(s.mbps) || 6)));
  s.mirror = s.mirror === true;
  s.audio = s.audio === true;
  return Object.fromEntries(Object.keys(defaults).map((k) => [k, s[k]]));
}
export function show(s) {
  for (const key of Object.keys(defaults)) {
    if (typeof defaults[key] === "boolean") $(key).checked = s[key];
    else $(key).value = s[key];
  }
  $("profile").value =
    Object.keys(profiles).find(
      (k) => profiles[k].join() === [s.height, s.fps, s.mbps].join(),
    ) || "custom";
}
export function controls(callback) {
  function changed() {
    const s = {};
    for (const [k, v] of Object.entries(defaults))
      s[k] =
        typeof v === "boolean"
          ? $(k).checked
          : typeof v === "number"
            ? Number($(k).value)
            : $(k).value;
    const safe = clean(s);
    show(safe);
    callback(safe);
  }
  for (const k of Object.keys(defaults))
    $(k).addEventListener("change", changed);
  $("profile").onchange = () => {
    const p = profiles[$("profile").value];
    if (p) {
      [$("height").value, $("fps").value, $("mbps").value] = p;
      changed();
    }
  };
}
