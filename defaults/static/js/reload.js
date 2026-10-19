(() => {
  let version = document.currentScript?.dataset.version;
  if (!version) return;

  const endpoint = new URL("/reload-version", window.location.href);
  endpoint.searchParams.set("path", window.location.pathname);

  const poll = async () => {
    try {
      const response = await fetch(endpoint, { cache: "no-store" });
      if (response.ok) {
        const currentVersion = await response.text();
        if (currentVersion !== version) {
          version = currentVersion;
          window.location.reload();
          return;
        }
      }
    } catch {
      // A later poll handles transient server or network failures.
    }
    window.setTimeout(poll, 500);
  };

  window.setTimeout(poll, 500);
})();
