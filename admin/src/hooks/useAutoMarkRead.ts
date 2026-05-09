import { useEffect, useRef, useState } from "react";

// useAutoMarkRead returns a ref-callback you attach to a card. When the
// card has been ≥50% visible for `dwellMs`, it counts as "seen". Once it
// then leaves the viewport going up (i.e. user has scrolled past), we fire
// onRead exactly once. Manual mark/unmark is unaffected.
//
// The hook is a no-op when:
//   - `enabled` is false (e.g. the item is already read), or
//   - IntersectionObserver is unavailable (jsdom by default).

interface Options {
  enabled: boolean;
  onRead: () => void;
  dwellMs?: number;
  threshold?: number;
}

export function useAutoMarkRead({
  enabled,
  onRead,
  dwellMs = 800,
  threshold = 0.5,
}: Options) {
  const [el, setEl] = useState<Element | null>(null);
  const dwellTimerRef = useRef<number | null>(null);
  const seenRef = useRef(false);
  const firedRef = useRef(false);

  // Keep the latest onRead callable without retriggering the effect.
  const onReadRef = useRef(onRead);
  useEffect(() => {
    onReadRef.current = onRead;
  }, [onRead]);

  useEffect(() => {
    if (!enabled || !el) return;
    if (typeof IntersectionObserver === "undefined") return;

    const obs = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const visibleEnough = entry.isIntersecting && entry.intersectionRatio >= threshold;
          if (visibleEnough) {
            if (dwellTimerRef.current == null && !seenRef.current) {
              dwellTimerRef.current = window.setTimeout(() => {
                seenRef.current = true;
                dwellTimerRef.current = null;
              }, dwellMs);
            }
            continue;
          }

          if (dwellTimerRef.current != null) {
            clearTimeout(dwellTimerRef.current);
            dwellTimerRef.current = null;
          }
          // Trigger only on upward exit: the card sits above the viewport.
          const aboveViewport = entry.boundingClientRect.bottom <= 0;
          if (seenRef.current && aboveViewport && !firedRef.current) {
            firedRef.current = true;
            obs.disconnect();
            onReadRef.current();
            return;
          }
        }
      },
      { threshold: [0, threshold, 1] },
    );
    obs.observe(el);
    return () => {
      obs.disconnect();
      if (dwellTimerRef.current != null) {
        clearTimeout(dwellTimerRef.current);
        dwellTimerRef.current = null;
      }
    };
  }, [el, enabled, dwellMs, threshold]);

  return setEl;
}
