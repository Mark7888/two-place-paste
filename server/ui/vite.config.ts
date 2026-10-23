import { createHash } from "node:crypto";
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

/** serviceWorker builds sw/sw.js into the output as sw.js, with the list of
 *  files it keeps for offline use (the bundle and everything in public/, not
 *  the page, which it fetches itself) and a version that is a digest of those
 *  files. A new build is a new worker, so an installed app picks it up, and
 *  the worker drops what the old one kept. */
function serviceWorker(): Plugin {
  return {
    name: "tpp-admin-service-worker",
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
      const source = readFileSync("sw/sw.js", "utf8")
        .replace("__VERSION__", hash.digest("hex").slice(0, 12))
        .replace("__PRECACHE__", JSON.stringify(files.map((f) => `/admin/${f}`)));
      this.emitFile({ type: "asset", fileName: "sw.js", source });
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
  plugins: [react(), serviceWorker()],
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
