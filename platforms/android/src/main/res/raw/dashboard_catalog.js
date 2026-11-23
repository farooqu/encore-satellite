(() => {
  // Use the signed-in page's own connection. Only display names and routes leave the WebView.
  const hass = document.querySelector('home-assistant')?.hass;
  if (!hass?.connected || !hass.user || !Object.keys(hass.panels || {}).length) return null;
  if (!window.encoreDashboardCatalog) {
    const result = window.encoreDashboardCatalog = { pages: null };
    Promise.all(Object.values(hass.panels).filter(panel => panel.component_name === 'lovelace')
      .map(async panel => {
        const path = panel.url_path;
        const url = '/' + encodeURIComponent(path);
        const title = panel.title || hass.localize(path === 'lovelace' ? 'panel.states' : 'panel.' + path) || path;
        const config = await hass.callWS({ type: 'lovelace/config', url_path: path }).catch(() => null);
        if (!config) return [{ url, title }];
        const views = (config.views || []).flatMap((view, index) => {
          if (view.subview || view.visible === false || (Array.isArray(view.visible) &&
              !view.visible.some(entry => entry.user === hass.user.id))) return [];
          const route = encodeURIComponent(view.path || index);
          return [{ url: `${url}/${route}`, title: `${title} · ${view.title || view.path || `View ${index + 1}`}` }];
        });
        // Generated dashboards have no saved view routes.
        return views.length ? views : [{ url, title }];
      })).then(groups => { result.pages = groups.flat(); })
      .catch(() => { delete window.encoreDashboardCatalog; });
  }
  return window.encoreDashboardCatalog.pages;
})()
