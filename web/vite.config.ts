import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { mkdirSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const keep = (): Plugin => ({
  name: "keep-dist",
  closeBundle() {
    mkdirSync("dist", { recursive: true });
    writeFileSync("dist/.keep", "placeholder so go:embed all:dist compiles before the UI is built\n");
  },
});

export default defineConfig({
  plugins: [tailwindcss(), svelte(), keep()],
  resolve: {
    alias: { $lib: fileURLToPath(new URL("./src/lib", import.meta.url)) },
  },
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
