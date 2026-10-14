import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import { mkdirSync, writeFileSync } from "node:fs";

const keep = (): Plugin => ({
  name: "keep-dist",
  closeBundle() {
    mkdirSync("dist", { recursive: true });
    writeFileSync("dist/.keep", "placeholder so go:embed all:dist compiles before the UI is built\n");
  },
});

export default defineConfig({
  plugins: [react(), keep()],
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    port: 5173,
    strictPort: true,
    // Everything that the dev server does not serve as UI goes to the backend, including the
    // paths of every repo format.
    proxy: {
      "^/(?!$|index\\.html|src/|@|node_modules/|__vite)": "http://localhost:8080",
    },
  },
});
