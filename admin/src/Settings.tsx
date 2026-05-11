import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";

import { changeOwnPassword, Unauthorized } from "./api";

export type TLSPending = {
  domains?: string[];
  last_checked?: number;
  last_error?: string;
};
export type TLSStatus = { state: string; error?: string; pending?: TLSPending };

type DNSResult = {
  domain: string;
  public_ip: string;
  a_records?: string[];
  aaaa_records?: string[];
  matches: boolean;
  hints?: string[];
};

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

export function SettingsView({
  onAuthLost,
  tlsStatus,
  onTLSChanged,
}: {
  onAuthLost?: () => void;
  tlsStatus?: TLSStatus;
  onTLSChanged?: () => void;
} = {}) {
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

      <HTTPSPanel status={tlsStatus} onChanged={onTLSChanged} />

      <Separator className="my-8" />

      <PasswordPanel onAuthLost={onAuthLost} />
    </div>
  );
}

// HTTPSPanel surfaces the wizard's Domain + Enable HTTPS flow from the
// authenticated Shell so an operator who skipped past those steps (or
// whose DNS hadn't propagated when they ran the wizard) can finish the
// job whenever they want. The backend endpoints — /setup/dns-check and
// /setup/enable-tls — are identical to the wizard; only the surrounding
// chrome differs.
function HTTPSPanel({ status, onChanged }: { status?: TLSStatus; onChanged?: () => void }) {
  const enabled = status?.state === "ready" || status?.state === "issuing";
  const pending = status?.state === "pending";
  const [domain, setDomain] = useState("");
  const [email, setEmail] = useState("");
  const [staging, setStaging] = useState(false);
  const [dns, setDNS] = useState<DNSResult | null>(null);
  const [checking, setChecking] = useState(false);
  const [enabling, setEnabling] = useState<"idle" | "enabling">("idle");
  const [err, setErr] = useState("");

  async function checkDNS() {
    setErr("");
    setChecking(true);
    try {
      const r = await fetch("/admin/api/setup/dns-check", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ domain: domain.trim() }),
      });
      if (r.ok) setDNS(await r.json());
      else setErr((await r.text()) || "DNS check failed.");
    } finally {
      setChecking(false);
    }
  }

  async function enable() {
    setErr("");
    if (!domain.trim()) return setErr("Domain required.");
    if (!email.trim()) return setErr("Contact email required (Let's Encrypt sends renewal warnings here).");
    setEnabling("enabling");
    try {
      const r = await fetch("/admin/api/setup/enable-tls", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ domains: [domain.trim()], email: email.trim(), staging }),
      });
      if (!r.ok) {
        setErr((await r.text()) || "Could not enable HTTPS.");
        return;
      }
      // Server now returns 202 either way: TLS came up immediately, OR
      // DNS wasn't ready and the request was queued for the background
      // poller. Refresh status from /me to surface whichever state we
      // landed in.
      onChanged?.();
    } finally {
      setEnabling("idle");
    }
  }

  async function cancelPending() {
    await fetch("/admin/api/setup/pending-tls", { method: "DELETE" });
    onChanged?.();
  }

  return (
    <section aria-labelledby="https-heading">
      <h3 id="https-heading" className="mb-1 text-base font-semibold">Domain &amp; HTTPS</h3>
      {enabled ? (
        <>
          <p className="mb-4 text-sm text-muted-foreground">
            HTTPS is on. To change the domain or rotate certificates, edit{" "}
            <code className="rounded bg-muted px-1 py-0.5 text-xs">config.yml</code> and restart.
          </p>
          <div className="rounded-md border border-emerald-500/40 bg-emerald-500/5 px-3 py-2 text-sm text-emerald-700 dark:text-emerald-300">
            TLS state: {status?.state}
            {status?.error && <span> · {status.error}</span>}
          </div>
        </>
      ) : pending ? (
        <PendingCard pending={status?.pending} onCancel={cancelPending} onRefresh={() => onChanged?.()} />
      ) : (
        <>
          <p className="mb-4 text-sm text-muted-foreground">
            HTTPS isn't enabled yet. Point your domain's A record at this server, verify with the DNS check, then enable Let's Encrypt.
          </p>
          <div className="space-y-3 rounded-md border border-border bg-card p-4">
            <div className="space-y-1.5">
              <Label htmlFor="https-domain">Domain</Label>
              <Input
                id="https-domain"
                placeholder="blog.example"
                value={domain}
                onChange={(e) => setDomain(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="https-email">Contact email</Label>
              <Input
                id="https-email"
                type="email"
                placeholder="you@example.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={staging}
                onChange={(e) => setStaging(e.target.checked)}
              />
              Use Let's Encrypt staging (untrusted certs, looser rate limits).
            </label>
            <p className="text-xs text-muted-foreground">
              If DNS hasn't propagated yet, we'll keep checking in the background
              and turn HTTPS on the moment it's ready — no need to come back.
            </p>
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={checkDNS}
                disabled={checking || enabling !== "idle" || !domain.trim()}
              >
                {checking ? "Checking…" : "Check DNS"}
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={enable}
                disabled={enabling !== "idle"}
                className="ml-auto"
              >
                {enabling === "enabling" ? "Saving…" : "Enable HTTPS"}
              </Button>
            </div>
            {err && (
              <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
                {err}
              </div>
            )}
            {dns && (
              <div
                className={
                  "rounded-md border px-3 py-2 text-sm " +
                  (dns.matches
                    ? "border-emerald-500/40 bg-emerald-500/5 text-emerald-700 dark:text-emerald-300"
                    : "border-amber-500/40 bg-amber-500/5 text-amber-700 dark:text-amber-300")
                }
              >
                {dns.matches ? (
                  <p>
                    {dns.domain} → {dns.public_ip}. You're good to enable HTTPS.
                  </p>
                ) : (
                  <div className="space-y-1">
                    {dns.public_ip && (
                      <p>This server's public IP appears to be {dns.public_ip}.</p>
                    )}
                    {dns.hints?.map((h, i) => <p key={i}>{h}</p>)}
                  </div>
                )}
              </div>
            )}
          </div>
        </>
      )}
    </section>
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

function PendingCard({
  pending,
  onCancel,
  onRefresh,
}: {
  pending?: TLSPending;
  onCancel: () => void;
  onRefresh: () => void;
}) {
  const domain = pending?.domains?.[0] ?? "(no domain)";
  const lastChecked = pending?.last_checked
    ? new Date(pending.last_checked * 1000).toLocaleTimeString()
    : "—";
  return (
    <>
      <p className="mb-4 text-sm text-muted-foreground">
        Waiting for DNS to propagate. We'll keep checking every minute and
        enable HTTPS the moment your domain points here.
      </p>
      <div className="rounded-md border border-amber-500/40 bg-amber-500/5 p-4 text-sm text-amber-800 dark:text-amber-300">
        <div className="mb-2 font-medium">Watching {domain}</div>
        <div className="text-xs">Last checked at {lastChecked}</div>
        {pending?.last_error && (
          <div className="mt-2 text-xs">{pending.last_error}</div>
        )}
        <div className="mt-3 flex gap-2">
          <Button type="button" size="sm" variant="outline" onClick={onRefresh}>
            Refresh
          </Button>
          <Button type="button" size="sm" variant="outline" onClick={onCancel}>
            Stop waiting
          </Button>
        </div>
      </div>
    </>
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
