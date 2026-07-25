import { spawn } from "node:child_process";
import { rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const database = path.join(tmpdir(), `trove-browser-regressions-${process.pid}.db`);

for (const suffix of ["", "-wal", "-shm"]) {
  await rm(`${database}${suffix}`, { force: true });
}

const server = spawn("go", ["run", "./cmd/trove-server"], {
  cwd: root,
  env: {
    ...process.env,
    TROVE_ADDR: "127.0.0.1:18081",
    TROVE_DB: database,
    TROVE_FRESHNESS_ENABLED: "false",
  },
  stdio: "inherit",
});

let shuttingDown = false;
function stop(signal) {
  if (shuttingDown) return;
  shuttingDown = true;
  server.kill(signal);
}

process.on("SIGINT", () => stop("SIGINT"));
process.on("SIGTERM", () => stop("SIGTERM"));
server.on("exit", (code, signal) => {
  if (signal) process.exit(0);
  process.exit(code ?? 1);
});
