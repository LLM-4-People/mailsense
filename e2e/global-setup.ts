import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import path from "node:path";

// Build once per run. Every worker then starts its own copy against an
// isolated data directory, so settings and caches cannot leak between tests.
export default function globalSetup() {
  const root = path.resolve(__dirname, "..");
  const cache = path.join(__dirname, ".cache");
  mkdirSync(cache, { recursive: true });
  const out = path.join(cache, process.platform === "win32" ? "mailsense.exe" : "mailsense");
  execFileSync("go", ["build", "-trimpath", "-o", out, "./cmd/mailsense"], {
    cwd: root,
    stdio: "inherit",
    env: process.env,
  });
}
