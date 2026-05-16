import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  applyTheme,
  loadThemePref,
  resolveTheme,
  saveThemePref,
  subscribeSystemTheme,
} from "./theme";

function stubMatchMedia(dark: boolean) {
  const listeners = new Set<(e: MediaQueryListEvent) => void>();
  const mql = {
    matches: dark,
    media: "(prefers-color-scheme: dark)",
    addEventListener: (_t: string, cb: (e: MediaQueryListEvent) => void) =>
      listeners.add(cb),
    removeEventListener: (_t: string, cb: (e: MediaQueryListEvent) => void) =>
      listeners.delete(cb),
  };
  vi.stubGlobal("matchMedia", () => mql);
  return {
    fire: () =>
      listeners.forEach((cb) => cb({} as MediaQueryListEvent)),
    listenerCount: () => listeners.size,
  };
}

describe("theme", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.className = "";
    document.documentElement.style.colorScheme = "";
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("loadThemePref", () => {
    it("defaults to 'system' when nothing is stored", () => {
      expect(loadThemePref()).toBe("system");
    });

    it("returns a previously saved value", () => {
      saveThemePref("dark");
      expect(loadThemePref()).toBe("dark");
    });

    it("falls back to 'system' for an unrecognized value", () => {
      localStorage.setItem("mizu:theme", "neon");
      expect(loadThemePref()).toBe("system");
    });
  });

  describe("resolveTheme", () => {
    it("pins to the explicit choice", () => {
      stubMatchMedia(true);
      expect(resolveTheme("light")).toBe("light");
      expect(resolveTheme("dark")).toBe("dark");
    });

    it("follows the OS for 'system'", () => {
      stubMatchMedia(true);
      expect(resolveTheme("system")).toBe("dark");
      stubMatchMedia(false);
      expect(resolveTheme("system")).toBe("light");
    });
  });

  describe("applyTheme", () => {
    it("adds the dark class and sets color-scheme for dark", () => {
      applyTheme("dark");
      expect(document.documentElement.classList.contains("dark")).toBe(true);
      expect(document.documentElement.style.colorScheme).toBe("dark");
    });

    it("removes the dark class for light", () => {
      document.documentElement.classList.add("dark");
      applyTheme("light");
      expect(document.documentElement.classList.contains("dark")).toBe(false);
      expect(document.documentElement.style.colorScheme).toBe("light");
    });

    it("resolves 'system' against the current OS preference", () => {
      stubMatchMedia(true);
      applyTheme("system");
      expect(document.documentElement.classList.contains("dark")).toBe(true);
    });
  });

  describe("subscribeSystemTheme", () => {
    it("invokes the callback on OS-level changes and cleans up", () => {
      const mq = stubMatchMedia(false);
      const cb = vi.fn();
      const unsubscribe = subscribeSystemTheme(cb);
      mq.fire();
      expect(cb).toHaveBeenCalledTimes(1);
      unsubscribe();
      expect(mq.listenerCount()).toBe(0);
    });
  });
});
