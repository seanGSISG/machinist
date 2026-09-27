import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { fileURLToPath, URL } from "node:url";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  // emptyOutDir stays off so dist/.gitkeep survives; the build script clears old output itself.
  build: { assetsInlineLimit: 0, emptyOutDir: false },
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
});
