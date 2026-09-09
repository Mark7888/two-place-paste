import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build lands directly in the Go package that embeds it, so a UI build
// followed by a Go build is all that shipping takes.
//
// emptyOutDir is off on purpose: scripts/clean-dist.mjs empties the directory
// while keeping the .gitkeep that makes //go:embed compile in a checkout with
// no UI build.
export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "../internal/localui/dist",
    emptyOutDir: false,
    sourcemap: false,
    // One file each: the page is served from a binary on localhost, and code
    // splitting buys nothing against a loopback socket.
    rollupOptions: {
      output: {
        entryFileNames: "assets/app.js",
        chunkFileNames: "assets/[name].js",
        assetFileNames: "assets/[name][extname]",
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
  },
});
