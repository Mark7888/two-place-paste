// Empties the directory the Go binary embeds, keeping the .gitkeep that makes
// it exist in a fresh checkout.
//
// Vite's own emptyOutDir would remove that file too, and a checkout without it
// does not compile: //go:embed fails when its directory has no files at all.
import { readdirSync, rmSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = join(dirname(fileURLToPath(import.meta.url)), "..", "..", "internal", "localui", "dist");

for (const entry of readdirSync(dist)) {
  if (entry === ".gitkeep") continue;
  rmSync(join(dist, entry), { recursive: true, force: true });
}
