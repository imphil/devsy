import { resolve } from "node:path"
import { svelte } from "@sveltejs/vite-plugin-svelte"
import tailwindcss from "@tailwindcss/vite"
import { defineConfig, externalizeDepsPlugin } from "electron-vite"

// Empty in local builds, which makes the analytics module no-op.
// DEVSY_DESKTOP_UPDATE_URL overrides the update feed (see updater.ts); empty
// keeps the publish URL from electron-builder.yml.
const mainDefine = {
  __DEVSY_POSTHOG_API_KEY__: JSON.stringify(
    process.env.DEVSY_POSTHOG_API_KEY ?? "",
  ),
  __DEVSY_UPDATE_URL__: JSON.stringify(
    process.env.DEVSY_DESKTOP_UPDATE_URL ?? "",
  ),
}

export default defineConfig({
  main: {
    plugins: [externalizeDepsPlugin()],
    define: mainDefine,
    build: {
      outDir: "dist/main",
      rollupOptions: {
        input: "src/main/index.ts",
      },
    },
  },
  preload: {
    plugins: [externalizeDepsPlugin()],
    build: {
      outDir: "dist/preload",
      rollupOptions: {
        input: "src/preload/index.ts",
        output: {
          format: "cjs",
          entryFileNames: "[name].js",
        },
      },
    },
  },
  renderer: {
    root: "src/renderer",
    plugins: [tailwindcss(), svelte()],
    resolve: {
      alias: {
        $lib: resolve(__dirname, "src/renderer/src/lib"),
        $shared: resolve(__dirname, "src/shared"),
      },
    },
    build: {
      outDir: "dist/renderer",
      rollupOptions: {
        input: resolve(__dirname, "src/renderer/index.html"),
      },
    },
    server: {
      port: 1420,
      strictPort: true,
    },
  },
})
