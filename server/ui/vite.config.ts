import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

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
  plugins: [react()],
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
