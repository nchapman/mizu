// Theme preference: "system" follows prefers-color-scheme; "light"/"dark"
// pin the choice. Persisted in localStorage so each browser remembers its
// own selection (there's no server-side prefs store yet).

export type ThemePref = "light" | "dark" | "system";

const KEY = "mizu:theme";

export function loadThemePref(): ThemePref {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    // localStorage may be unavailable (private mode); fall through.
  }
  return "system";
}

export function saveThemePref(pref: ThemePref) {
  try {
    localStorage.setItem(KEY, pref);
  } catch {
    // No-op when storage is disabled.
  }
}

function systemPrefersDark(): boolean {
  return typeof window !== "undefined"
    && window.matchMedia
    && window.matchMedia("(prefers-color-scheme: dark)").matches;
}

export function resolveTheme(pref: ThemePref): "light" | "dark" {
  if (pref === "system") return systemPrefersDark() ? "dark" : "light";
  return pref;
}

export function applyTheme(pref: ThemePref) {
  const resolved = resolveTheme(pref);
  const root = document.documentElement;
  root.classList.toggle("dark", resolved === "dark");
  root.style.colorScheme = resolved;
}

// Wire up a listener so "system" follows OS-level changes live. Returns
// an unsubscribe so React effects can clean up. Callers also re-invoke
// applyTheme whenever the *user* switches the pref.
export function subscribeSystemTheme(onChange: () => void): () => void {
  if (typeof window === "undefined" || !window.matchMedia) return () => {};
  const mq = window.matchMedia("(prefers-color-scheme: dark)");
  mq.addEventListener("change", onChange);
  return () => mq.removeEventListener("change", onChange);
}
