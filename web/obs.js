import { Link, pageToken, $ } from "./link.js";
import { Viewer } from "./viewer.js";
const viewer = new Viewer("obs", $("video"), (m) => link.send(m));
const link = new Link("obs", () => pageToken("obs"), {
  status() {},
  message: (m) => viewer.message(m),
  close: () => viewer.reset(),
});
