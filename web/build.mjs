// Bundles the app with esbuild into dist/, with content-hashed file names so
// the Go server can cache assets forever and never cache index.html.
import * as esbuild from "esbuild";
import { mkdir, readFile, rm, writeFile } from "node:fs/promises";
import path from "node:path";

const watch = process.argv.includes("--watch");
const outdir = "dist";

const options = {
  entryPoints: ["src/main.tsx"],
  bundle: true,
  minify: !watch,
  sourcemap: watch ? "inline" : false,
  target: ["es2022"],
  outdir: path.join(outdir, "assets"),
  entryNames: "[name]-[hash]",
  metafile: true,
  jsx: "automatic",
  define: { "process.env.NODE_ENV": JSON.stringify(watch ? "development" : "production") },
  logLevel: "info",
};

async function writeIndex(metafile) {
  const outputs = Object.keys(metafile.outputs).map((p) => path.relative(outdir, p).split(path.sep).join("/"));
  const js = outputs.find((p) => p.endsWith(".js"));
  const css = outputs.find((p) => p.endsWith(".css"));
  const template = await readFile("index.html", "utf8");
  const html = template
    .replace("<!--CSS-->", css ? `<link rel="stylesheet" href="/${css}">` : "")
    .replace("<!--JS-->", `<script type="module" src="/${js}"></script>`);
  await writeFile(path.join(outdir, "index.html"), html);
}

await rm(path.join(outdir, "assets"), { recursive: true, force: true });
await rm(path.join(outdir, "index.html"), { force: true });
await mkdir(outdir, { recursive: true });

if (watch) {
  const ctx = await esbuild.context({
    ...options,
    plugins: [{ name: "index", setup: (b) => b.onEnd((r) => r.metafile && writeIndex(r.metafile)) }],
  });
  await ctx.watch();
} else {
  const result = await esbuild.build(options);
  await writeIndex(result.metafile);
}
