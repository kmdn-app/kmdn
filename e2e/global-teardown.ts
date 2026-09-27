import { existsSync, readFileSync } from "node:fs";
import { STATE_FILE, type State } from "./env.ts";

export default async function teardown() {
  if (!existsSync(STATE_FILE)) return;
  const { pids } = JSON.parse(readFileSync(STATE_FILE, "utf8")) as State;
  for (const pid of pids.reverse()) {
    try {
      process.kill(-pid, "SIGTERM"); // the process group (detached)
    } catch {
      try {
        process.kill(pid, "SIGTERM");
      } catch {
        /* already gone */
      }
    }
  }
}
