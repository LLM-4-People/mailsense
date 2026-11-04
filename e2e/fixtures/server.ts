import { spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

export type Server = {
  baseURL: string;
  token: string;
  dataDir: string;
  proc: ChildProcess;
};

type Descriptor = {
  url: string;
  token: string;
  pid?: number;
};

const READY_TIMEOUT_MS = 30_000;
const SHUTDOWN_TIMEOUT_MS = 10_000;

function binaryPath(): string {
  return path.join(
    __dirname,
    "..",
    ".cache",
    process.platform === "win32" ? "mailsense.exe" : "mailsense",
  );
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitForDescriptor(dataDir: string): Promise<Descriptor> {
  const file = path.join(dataDir, "runtime", "api.json");
  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    try {
      const parsed = JSON.parse(await readFile(file, "utf8")) as Descriptor;
      if (parsed.url && parsed.token) return parsed;
    } catch {
      // The descriptor is written atomically after the listener is up.
    }
    await sleep(100);
  }
  throw new Error(`Mailsense did not publish ${file} within ${READY_TIMEOUT_MS}ms`);
}

async function waitForStatus(baseURL: string, token: string): Promise<void> {
  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(baseURL + "api/status", {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (response.ok) return;
    } catch {
      // Not listening yet.
    }
    await sleep(100);
  }
  throw new Error("Mailsense API did not become ready");
}

// A new server publishes its descriptor as soon as the listener accepts,
// before the derived warm. Fixtures that assert route content still wait for
// the boot contract to say ready; an older server (404) keeps the previous
// status wait.
async function waitForBoot(baseURL: string, token: string): Promise<void> {
  const deadline = Date.now() + READY_TIMEOUT_MS;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(baseURL + "api/boot", {
        headers: { Authorization: `Bearer ${token}` },
      });
      if (response.status === 404) {
        await waitForStatus(baseURL, token);
        return;
      }
      if (response.ok) {
        const view = (await response.json()) as { state?: string };
        if (view.state === "ready") return;
      }
    } catch {
      // Not listening yet, or the warm is still running.
    }
    await sleep(100);
  }
  throw new Error("Mailsense API did not report boot ready");
}

async function waitForExit(proc: ChildProcess, timeout: number): Promise<void> {
  if (proc.exitCode !== null) return;
  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => {
      proc.removeListener("exit", onExit);
      resolve();
    }, timeout);
    const onExit = () => {
      clearTimeout(timer);
      resolve();
    };
    proc.once("exit", onExit);
  });
}

export async function startServer(): Promise<Server> {
  const dataDir = await mkdtemp(path.join(tmpdir(), "mailsense-e2e-"));
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    MAILSENSE_DATA_DIR: dataDir,
    MAILSENSE_LEARNING_TEST_MODE: "true",
    MAILSENSE_UI_LANGUAGE: "en",
    // The host ceilings are host-wide readings of the developer machine:
    // a full disk would refuse the content routes these fixtures probe. The
    // guard is not what any UI spec asserts, so the fixture server turns it
    // off instead of depending on the machine's free space.
    MAILSENSE_RESOURCE_GUARD: "false",
    TZ: "UTC",
  };
  // The desktop opener must never fire during a test run.
  delete env.DISPLAY;
  delete env.WAYLAND_DISPLAY;

  let output = "";
  const proc = spawn(binaryPath(), [], {
    env,
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: true,
  });
  proc.stdout?.on("data", (chunk) => {
    output = (output + String(chunk)).slice(-6000);
  });
  proc.stderr?.on("data", (chunk) => {
    output = (output + String(chunk)).slice(-6000);
  });

  try {
    const descriptor = await waitForDescriptor(dataDir);
    await waitForBoot(descriptor.url, descriptor.token);
    return { baseURL: descriptor.url, token: descriptor.token, dataDir, proc };
  } catch (error) {
    proc.kill("SIGTERM");
    await waitForExit(proc, 3_000);
    await rm(dataDir, { recursive: true, force: true });
    throw new Error(
      `${error instanceof Error ? error.message : String(error)}\n${output}`,
    );
  }
}

export async function stopServer(server: Server): Promise<void> {
  try {
    await fetch(server.baseURL + "api/shutdown", {
      method: "POST",
      headers: {
        Authorization: `Bearer ${server.token}`,
        "Content-Type": "application/json",
      },
      body: "{}",
    });
  } catch {
    // The server may already be gone.
  }
  await waitForExit(server.proc, SHUTDOWN_TIMEOUT_MS);
  if (server.proc.exitCode === null) {
    server.proc.kill("SIGTERM");
    await waitForExit(server.proc, 3_000);
  }
  if (server.proc.exitCode === null) server.proc.kill("SIGKILL");
  await rm(server.dataDir, { recursive: true, force: true });
}
