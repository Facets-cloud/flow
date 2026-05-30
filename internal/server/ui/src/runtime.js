// flow UI runtime shim.
//
// The app code (app.data.js / app.primitives.js / app.screens.js / app.main.js)
// references `React` and `ReactDOM` as GLOBALS (e.g. `React.createElement`,
// `const { useState } = React`, `ReactDOM.createRoot(...)`) and never imports
// them. This shim runs first (as a classic <script>) and points those globals
// at Preact's React-compat layer, so the existing JSX runs on Preact unchanged.
//
// To fall back to real React (if a Preact incompatibility is ever found), swap
// the import below to `react` / `react-dom` and rebuild — no app code changes.
import * as compat from 'preact/compat';

// React 18 client API: provide a createRoot backed by Preact's render so the
// single `ReactDOM.createRoot(el).render(<Root/>)` mount keeps working.
const ReactDOM = {
  ...compat,
  createRoot(container) {
    return {
      render(vnode) {
        compat.render(vnode, container);
      },
      unmount() {
        compat.render(null, container);
      },
    };
  },
};

window.React = compat;
window.ReactDOM = ReactDOM;
