import * as esbuild from "esbuild";

await esbuild.build({
  entryPoints: ["src/main.ts"],
  bundle: true,
  outfile: "dist/main.js",
  format: "cjs",
  platform: "browser",
  target: "es2020",
  external: [
    "obsidian",
    "electron",
    "@codemirror/*",
    "@lezer/*",
  ],
  logLevel: "info",
});
