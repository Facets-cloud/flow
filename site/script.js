// Honor prefers-reduced-motion: pause demo videos and surface their poster.
// Also: click-to-play affordance when motion is suppressed.
(function () {
  const mql = window.matchMedia('(prefers-reduced-motion: reduce)');
  const videos = document.querySelectorAll('video.demo-video');

  function applyMotionPreference(reduce) {
    videos.forEach((v) => {
      if (reduce) {
        v.autoplay = false;
        v.loop = false;
        v.removeAttribute('autoplay');
        try { v.pause(); } catch (_) {}
        v.currentTime = 0;
        v.controls = true;
      } else {
        v.autoplay = true;
        v.loop = true;
        v.setAttribute('autoplay', '');
        const playPromise = v.play();
        if (playPromise && typeof playPromise.catch === 'function') {
          playPromise.catch(() => { /* autoplay blocked — controls are always on, user can press play */ });
        }
      }
    });
  }

  applyMotionPreference(mql.matches);
  if (typeof mql.addEventListener === 'function') {
    mql.addEventListener('change', (e) => applyMotionPreference(e.matches));
  } else if (typeof mql.addListener === 'function') {
    mql.addListener((e) => applyMotionPreference(e.matches));
  }
})();

// Copy-to-clipboard for terminal-frame code blocks.
(function () {
  const buttons = document.querySelectorAll('.copy-btn');
  if (!buttons.length) return;

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    // Fallback: textarea + execCommand for non-secure contexts (e.g. file://).
    return new Promise((resolve, reject) => {
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.setAttribute('readonly', '');
      ta.style.position = 'fixed';
      ta.style.left = '-9999px';
      document.body.appendChild(ta);
      ta.select();
      try {
        const ok = document.execCommand('copy');
        document.body.removeChild(ta);
        ok ? resolve() : reject(new Error('execCommand failed'));
      } catch (err) {
        document.body.removeChild(ta);
        reject(err);
      }
    });
  }

  buttons.forEach((btn) => {
    btn.addEventListener('click', () => {
      const frame = btn.closest('.terminal-frame');
      if (!frame) return;
      const code = frame.querySelector('pre.codeblock');
      if (!code) return;

      // Strip a leading "> " prompt sigil so the copied text is the bare command.
      let text = code.textContent.replace(/^>\s+/, '').trimEnd();

      copyText(text).then(
        () => {
          const label = btn.querySelector('.copy-btn__label');
          const original = label ? label.textContent : null;
          btn.classList.add('is-copied');
          btn.setAttribute('aria-label', 'Copied to clipboard');
          if (label) label.textContent = 'Copied';
          window.clearTimeout(btn.__copyTimer);
          btn.__copyTimer = window.setTimeout(() => {
            btn.classList.remove('is-copied');
            btn.setAttribute('aria-label', 'Copy command');
            if (label && original !== null) label.textContent = original;
          }, 1600);
        },
        () => {
          const label = btn.querySelector('.copy-btn__label');
          if (label) label.textContent = 'Copy failed';
          window.setTimeout(() => {
            if (label) label.textContent = 'Copy';
          }, 1600);
        }
      );
    });
  });
})();
