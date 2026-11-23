(() => {
  if (window !== window.top || window.encoreDashboardKiosk) return;
  window.encoreDashboardKiosk = true;

  // HA's native kiosk event collapses the layout but still allows its drawer to open.
  // Keep the chrome hidden in this WebView without changing HA's saved user preferences.
  const observers = new WeakSet();
  const styled = new WeakSet();
  const watch = root => {
    if (observers.has(root)) return;
    observers.add(root);
    new MutationObserver(apply).observe(root, { childList: true, subtree: true });
  };
  const shadow = (root, selector) => {
    const next = root?.querySelector(selector)?.shadowRoot;
    if (next) watch(next);
    return next;
  };
  const hide = (root, css) => {
    if (!root || styled.has(root)) return;
    styled.add(root);
    const style = document.createElement('style');
    style.textContent = css;
    root.append(style);
  };
  function apply() {
    const app = shadow(document, 'home-assistant');
    const main = shadow(app, 'home-assistant-main');
    if (!main) return;
    hide(main, 'ha-sidebar { display: none !important }');
    hide(shadow(main, 'ha-drawer'), '.sidebar-shell, wa-drawer { display: none !important }');
    const panel = shadow(main, 'ha-panel-lovelace');
    hide(shadow(panel, 'hui-root'), `
      .header { display: none !important }
      hui-view-container { padding-top: var(--safe-area-inset-top, 0px) !important }
    `);
    const hass = document.querySelector('home-assistant').hass;
    if (hass && !hass.kioskMode) {
      window.dispatchEvent(new CustomEvent('hass-kiosk-mode', { detail: { enable: true } }));
    }
  }
  window.addEventListener('hass-toggle-menu', event => event.stopImmediatePropagation(), true);
  watch(document);
  apply();
})()
