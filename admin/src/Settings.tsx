import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";

import { changeOwnPassword, Unauthorized } from "./api";

// Preferences live in localStorage for now; once we have a real backend
// store this surface keeps working with a tiny adapter swap.
const PREFS_KEY = "mizu:prefs";

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

export function SettingsView({ onAuthLost }: { onAuthLost?: () => void } = {}) {
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

      <Separator className="my-8" />

      <PasswordPanel onAuthLost={onAuthLost} />
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

function PasswordPanel({ onAuthLost }: { onAuthLost?: () => void }) {
  const [oldPw, setOldPw] = useState("");
  const [newPw, setNewPw] = useState("");
  const [newPw2, setNewPw2] = useState("");
  const [err, setErr] = useState("");
  const [ok, setOk] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    setOk(false);
    if (newPw.length < 8) return setErr("New password must be at least 8 characters.");
    if (newPw !== newPw2) return setErr("New passwords don't match.");
    setBusy(true);
    try {
      await changeOwnPassword({ old_password: oldPw, new_password: newPw });
      setOldPw("");
      setNewPw("");
      setNewPw2("");
      setOk(true);
    } catch (e) {
      if (e instanceof Unauthorized) {
        // The server returns 401 for "wrong current password" too;
        // surface that as a form error rather than bouncing to login.
        setErr("Wrong current password.");
        return;
      }
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  // Suppress unused-param warning while still keeping the API symmetric
  // with the other panels — `onAuthLost` is intentionally unused because
  // the only auth-shaped failure here means the current password was
  // wrong, not that the session expired. If real session expiry hits,
  // the next request from any other panel surfaces it.
  void onAuthLost;

  return (
    <section aria-labelledby="password-heading">
      <h3 id="password-heading" className="mb-1 text-base font-semibold">Change password</h3>
      <p className="mb-4 text-sm text-muted-foreground">Update the password for your own account.</p>
      <form onSubmit={submit} className="space-y-3 rounded-md border border-border bg-card p-4">
        <div className="space-y-1.5">
          <Label htmlFor="change-old">Current password</Label>
          <Input
            id="change-old"
            type="password"
            autoComplete="current-password"
            value={oldPw}
            onChange={(e) => setOldPw(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="change-new">New password</Label>
          <Input
            id="change-new"
            type="password"
            autoComplete="new-password"
            value={newPw}
            onChange={(e) => setNewPw(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="change-new-confirm">Confirm new password</Label>
          <Input
            id="change-new-confirm"
            type="password"
            autoComplete="new-password"
            value={newPw2}
            onChange={(e) => setNewPw2(e.target.value)}
          />
        </div>
        {err && (
          <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {err}
          </div>
        )}
        {ok && (
          <div role="status" className="rounded-md border border-emerald-300 bg-emerald-50 px-3 py-2 text-sm text-emerald-900">
            Password changed.
          </div>
        )}
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={busy || !oldPw || !newPw}>
            {busy ? "Saving…" : "Change password"}
          </Button>
        </div>
      </form>
    </section>
  );
}
