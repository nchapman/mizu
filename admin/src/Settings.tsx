import { useEffect, useState } from "react";

import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";

// Preferences live in localStorage for now; once we have a real backend
// store this surface keeps working with a tiny adapter swap.
const PREFS_KEY = "repeat:prefs";

interface Prefs {
  autoMarkRead: boolean;
  openLinksInNewTab: boolean;
}

const defaults: Prefs = { autoMarkRead: true, openLinksInNewTab: true };

function loadPrefs(): Prefs {
  try {
    const raw = localStorage.getItem(PREFS_KEY);
    if (!raw) return defaults;
    return { ...defaults, ...(JSON.parse(raw) as Partial<Prefs>) };
  } catch {
    return defaults;
  }
}

function savePrefs(p: Prefs) {
  try {
    localStorage.setItem(PREFS_KEY, JSON.stringify(p));
  } catch {
    // Storage may be disabled (private mode); silently no-op.
  }
}

export function SettingsView() {
  const [prefs, setPrefs] = useState<Prefs>(loadPrefs);

  useEffect(() => {
    savePrefs(prefs);
  }, [prefs]);

  function toggle<K extends keyof Prefs>(key: K) {
    setPrefs((p) => ({ ...p, [key]: !p[key] }));
  }

  return (
    <div>
      <h2 className="mb-1 text-lg font-semibold">Settings</h2>
      <p className="mb-6 text-sm text-muted-foreground">Reading preferences for this device.</p>

      <Separator className="mb-4" />

      <div className="space-y-4">
        <PrefRow
          id="auto-mark-read"
          label="Auto-mark read on scroll"
          description="When you scroll past a feed item, mark it read automatically."
          checked={prefs.autoMarkRead}
          onChange={() => toggle("autoMarkRead")}
        />
        <PrefRow
          id="new-tab-links"
          label="Open feed links in a new tab"
          description="Following the byline or 'Open original' opens a separate tab."
          checked={prefs.openLinksInNewTab}
          onChange={() => toggle("openLinksInNewTab")}
        />
      </div>
    </div>
  );
}

function PrefRow({
  id,
  label,
  description,
  checked,
  onChange,
}: {
  id: string;
  label: string;
  description: string;
  checked: boolean;
  onChange: () => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0 flex-1">
        <Label htmlFor={id} className="cursor-pointer text-sm">
          {label}
        </Label>
        <p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
      </div>
      <input
        id={id}
        type="checkbox"
        checked={checked}
        onChange={onChange}
        className="mt-1 h-4 w-4 cursor-pointer accent-primary"
      />
    </div>
  );
}
