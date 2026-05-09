import { useEffect, useState } from "react";

// Hash-based routing keeps the SPA self-contained: no server fallback rules,
// no history API quirks, no router dependency. The cost is a `#` in the URL,
// which is fine for a single-user admin surface.

export type Route = "home" | "subscriptions" | "settings";

const ROUTES: Record<string, Route> = {
  "": "home",
  "/": "home",
  "/home": "home",
  "/subscriptions": "subscriptions",
  "/settings": "settings",
};

function read(): Route {
  const raw = window.location.hash.replace(/^#/, "");
  return ROUTES[raw] ?? "home";
}

export function useRoute(): [Route, (next: Route) => void] {
  const [route, setRoute] = useState<Route>(read);

  useEffect(() => {
    const onHash = () => setRoute(read());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  function navigate(next: Route) {
    const path = next === "home" ? "/" : `/${next}`;
    if (window.location.hash !== `#${path}`) {
      window.location.hash = path;
    }
  }

  return [route, navigate];
}
