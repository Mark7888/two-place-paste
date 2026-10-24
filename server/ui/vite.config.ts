import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

/** BUILD_PLACEHOLDER is what src/lib/build.ts holds until the build fills in
 *  the real id. */
const BUILD_PLACEHOLDER = "__TPP_BUILD__";

/** buildFiles gives every UI build an id, a digest of what it is made of: the
 *  bundle and everything in public/. Two builds of the same source get the
 *  same id, and any change to the UI gets a new one, wherever it was built —
 *  a release, the Beta channel or a local checkout alike.
 *
 *  The id goes three places. The bundle carries it (src/lib/build.ts), so the
 *  running app knows which build it is. version.json carries it, so the app
 *  can ask the relay which build it serves now and offer to update when the
 *  two differ (src/hooks/useUpdateCheck.ts). And the service worker is built
 *  from sw/sw.js with it as its version, next to the list of files it keeps
 *  for offline use (the bundle and public/, not the page, which it fetches
 *  itself): a new build is a new worker, which drops what the old one kept. */
function buildFiles(): Plugin {
  return {
    name: "tpp-admin-build-files",
    apply: "build",
    generateBundle(_options, bundle) {
      const hash = createHash("sha256");
      const files: string[] = [];
      for (const [name, item] of Object.entries(bundle).sort(([a], [b]) => a.localeCompare(b))) {
        if (name.endsWith(".html")) continue;
        files.push(name);
        hash.update(name).update(item.type === "chunk" ? item.code : item.source);
      }
      for (const name of readdirSync("public").sort()) {
        files.push(name);
        hash.update(name).update(readFileSync(join("public", name)));
      }
      const build = hash.digest("hex").slice(0, 12);

      const app = bundle["assets/app.js"];
      if (app?.type !== "chunk" || !app.code.includes(BUILD_PLACEHOLDER)) {
        this.error("assets/app.js does not carry the build id placeholder (src/lib/build.ts)");
      }
      app.code = app.code.replaceAll(BUILD_PLACEHOLDER, build);

      this.emitFile({ type: "asset", fileName: "version.json", source: `${JSON.stringify({ build })}\n` });
      const worker = readFileSync("sw/sw.js", "utf8")
        .replace("__VERSION__", build)
        .replace("__PRECACHE__", JSON.stringify(files.map((f) => `/admin/${f}`)));
      this.emitFile({ type: "asset", fileName: "sw.js", source: worker });
    },
  };
}

// The relay to develop against: `npm run dev` serves the UI and forwards its
// API calls and QR images there, so the session cookie and the CSRF check
// work exactly as they do in production.
const relay = process.env.TPP_ADMIN_DEV_RELAY ?? "http://127.0.0.1:8080";

// The build lands directly in the Go package that embeds it, so a UI build
// followed by a Go build is all that shipping takes.
//
// emptyOutDir is off on purpose: scripts/clean-dist.mjs empties the directory
// while keeping the .gitkeep that makes //go:embed compile in a checkout with
// no UI build.
export default defineConfig({
  plugins: [react(), buildFiles()],
  base: "/admin/",
  build: {
    outDir: "../web/admin/dist",
    emptyOutDir: false,
    sourcemap: false,
    // The app is one chunk, so there is nothing to preload.
    modulePreload: { polyfill: false },
    // Fixed names, one file each: the relay serves them with no-cache, so a
    // browser revalidates after an upgrade instead of needing a new name.
    rollupOptions: {
      output: {
        entryFileNames: "assets/app.js",
        chunkFileNames: "assets/[name].js",
        assetFileNames: "assets/[name][extname]",
      },
    },
  },
  // `npm test`: the UI's unit and component tests, in a simulated browser.
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["src/test/setup.ts"],
  },
  server: {
    port: 5174,
    strictPort: true,
    proxy: {
      "/admin/api": relay,
      "^/admin/tokens/[^/]+/qr\\.png$": relay,
      "/source": relay,
    },
  },
});
