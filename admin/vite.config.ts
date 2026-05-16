/// <reference types="vitest" />
import path from "node:path";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  base: "/admin/",
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    port: 5173,
    proxy: {
      // mizu's always-on HTTPS means :8080 308-redirects to HTTPS, so
      // proxy straight to :8443 and skip self-signed cert verification.
      "/admin/api": {
        target: "https://localhost:8443",
        secure: false,
        changeOrigin: true,
      },
      // Uploaded media is served by the Go binary out of the public dir;
      // proxy it so images inserted into the composer render in dev too.
      "/media": {
        target: "https://localhost:8443",
        secure: false,
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
});
