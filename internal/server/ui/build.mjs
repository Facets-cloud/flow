// Build the flow Mission Control UI: bundle the Preact runtime shim, transpile
// the existing JSX app files ahead-of-time (replacing in-browser Babel), and
// emit the design-system CSS. Outputs land in ../static/assets (committed and
// go:embed'd by the Go server). Run: `node build.mjs`.
import { build, transform } from 'esbuild';
import { readFile, writeFile, access } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const OUT = join(here, '..', 'static', 'assets');
const SRC = join(here, 'src');
const exists = async (p) => { try { await access(p); return true; } catch { return false; } };

// 1) Runtime shim -> minified IIFE (self-contained, safe to fully minify).
await build({
  entryPoints: [join(SRC, 'runtime.js')],
  outfile: join(OUT, 'app.runtime.js'),
  bundle: true,
  format: 'iife',
  minify: true,
  target: 'es2019',
  legalComments: 'none',
});
console.log('built  app.runtime.js');

// 2) Transpile app files: lower JSX to React.createElement, keep them as classic
//    scripts sharing one global scope. minifyIdentifiers is OFF on purpose so
//    cross-file global references (StatusPill, formatAge, ...) are NOT renamed.
const JSX = {
  loader: 'jsx',
  jsx: 'transform',
  jsxFactory: 'React.createElement',
  jsxFragment: 'React.Fragment',
  target: 'es2019',
  minifyWhitespace: true,
  minifySyntax: false,
  minifyIdentifiers: false,
};
// Each source file is wrapped in an IIFE so its top-level declarations stay
// file-local (the original `type="text/babel"` scripts ran in isolated scopes;
// as plain classic scripts they would otherwise collide on `const AGENTS`,
// `useState`, etc.). Cross-file sharing is unchanged — it flows through
// `window.MC` / `window.MC_SCREENS` exactly as the originals do.
//
// `injectHooks` files (primitives, screens) call hooks bare (`useState(...)`)
// without declaring or importing them; the originals relied on those hooks
// being in scope. We make that explicit by destructuring them from the global
// React inside the IIFE. data/main already self-declare hooks from React.
const HOOKS = 'const { useState, useEffect, useMemo, useRef, useContext, useCallback, useReducer, useLayoutEffect, Fragment, createContext } = React;\n';
const files = [
  // [source (relative to this dir), output name, injectHooks]
  ['../static/assets/ebaab09c-07dc-4d0a-b14b-5fa8d1c49925.js', 'app.data.js', false],
  ['../static/assets/dfbb0627-5c41-4bf8-85df-037b2d384519.js', 'app.primitives.js', true],
  ['../static/assets/c906f42d-c4d3-4f33-b4a9-aca5e8a18052.js', 'app.screens.js', true],
  ['src/main.jsx', 'app.main.js', false],
];
for (const [inp, outName, injectHooks] of files) {
  const abs = join(here, inp);
  if (!(await exists(abs))) { console.log('skip   ', outName, '(source absent yet)'); continue; }
  const res = await transform(await readFile(abs, 'utf8'), JSX);
  const body = (injectHooks ? HOOKS : '') + res.code;
  await writeFile(join(OUT, outName), '(function(){\n' + body + '\n})();\n');
  console.log('transpiled', inp, '->', outName, injectHooks ? '(hooks injected)' : '');
}

// 3) Design-system CSS (present once S3 lands).
const cssIn = join(SRC, 'styles', 'app.css');
if (await exists(cssIn)) {
  const res = await transform(await readFile(cssIn, 'utf8'), { loader: 'css', minify: true });
  await writeFile(join(OUT, 'app.css'), res.code);
  console.log('built  app.css');
}
console.log('done.');
