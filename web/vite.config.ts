import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";
import { mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { brotliCompressSync, constants, gzipSync } from "node:zlib";

const keep = (): Plugin => ({
  name: "keep-dist",
  closeBundle() {
    mkdirSync("dist", { recursive: true });
    writeFileSync("dist/.keep", "placeholder so go:embed all:dist compiles before the UI is built\n");
  },
});

// The text files that precompress writes .br and .gz files of. Images and fonts are already
// compressed.
const textExts = new Set([".html", ".js", ".mjs", ".css", ".svg", ".json", ".webmanifest", ".map", ".txt", ".xml"]);

// precompress writes a .br and a .gz file next to each text file of dist of at least 1 KiB,
// each only when it is smaller than the file. The server sends them to a client that accepts
// the coding (web/embed.go).
const precompress = (): Plugin => ({
  name: "precompress-dist",
  apply: "build",
  closeBundle() {
    for (const rel of readdirSync("dist", { recursive: true, encoding: "utf8" })) {
      const file = join("dist", rel);
      if (!textExts.has(extname(file)) || !statSync(file).isFile()) continue;
      const body = readFileSync(file);
      if (body.length < 1024) continue;
      const br = brotliCompressSync(body, {
        params: {
          [constants.BROTLI_PARAM_MODE]: constants.BROTLI_MODE_TEXT,
          [constants.BROTLI_PARAM_QUALITY]: constants.BROTLI_MAX_QUALITY,
          [constants.BROTLI_PARAM_SIZE_HINT]: body.length,
        },
      });
      if (br.length < body.length) writeFileSync(file + ".br", br);
      const gz = gzipSync(body, { level: constants.Z_BEST_COMPRESSION });
      if (gz.length < body.length) writeFileSync(file + ".gz", gz);
    }
  },
});

export default defineConfig({
  plugins: [tailwindcss(), svelte(), keep(), precompress()],
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
