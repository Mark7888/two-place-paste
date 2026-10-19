// Writes THIRD_PARTY_NOTICES.txt next to the built UI, so the desktop binary
// embeds it and Settings can link to it (LICENSING.md).
//
// It lists the Go modules tppdesktop is built from, for both platforms it
// ships on, and the npm packages the UI bundle is built from. The Go part
// needs `go` on PATH; CI's desktop build sets Go up before this step. Where
// there is no Go — the npm-only CI job, which throws its build away — the
// file is written without the Go part and says so on the console.
import { execFileSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const ui = join(dirname(fileURLToPath(import.meta.url)), "..");
const repo = join(ui, "..", "..");

execFileSync(
  process.execPath,
  [
    join(repo, "scripts", "notices", "notices.mjs"),
    "--title", "TwoPlacePaste desktop app: third-party notices",
    "--intro",
    "TwoPlacePaste is licensed under the Mozilla Public License 2.0. Its source code is at https://github.com/Mark7888/two-place-paste. The desktop app also includes the third-party software below.",
    "--go", `${join(repo, "desktop")}=./cmd/tppdesktop`,
    "--goos", "windows,darwin",
    "--go-optional",
    "--npm", ui,
    "--out", join(ui, "..", "internal", "localui", "dist", "THIRD_PARTY_NOTICES.txt"),
  ],
  { stdio: "inherit" },
);
