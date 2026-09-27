import { tmpdir } from "node:os";
import { join } from "node:path";

export const KMDN_PORT = Number(process.env.KMDN_E2E_PORT ?? 18380);
export const FORGE_PORT = Number(process.env.FORGE_E2E_PORT ?? 18390);
export const KMDN_URL = `http://127.0.0.1:${KMDN_PORT}`;
export const FORGE_URL = `http://127.0.0.1:${FORGE_PORT}`;
export const FORGE_TOKEN = "glpat-e2e-test-token";

/** Where the run keeps its data, logs and state (one per run). */
export const RUN_DIR = process.env.KMDN_E2E_DIR ?? join(tmpdir(), "kmdn-e2e");
export const LOG_FILE = join(RUN_DIR, "kmdn.log");
export const STATE_FILE = join(RUN_DIR, "state.json");

export const ADMIN = { email: "ana@acme.test", name: "Ana Admin" };
export const REVIEWER = { email: "rui@acme.test", name: "Rui Reviewer" };

export type State = {
  repoID: string;
  owner: string;
  name: string;
  pids: number[];
  /** The admin's session from setup (load scripts use it). */
  admin: { cookie: string; csrf: string };
};
