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
    proxy: Object.fromEntries(
      ["/api", "/maven", "/debian", "/keys", "/healthz"].map((p) => [p, "http://localhost:8080"]),
    ),
  },
});
