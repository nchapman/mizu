import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function Setup({ onDone, siteTitle }: { onDone: () => void; siteTitle?: string }) {
  const [token, setToken] = useState("");
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    if (!token.trim()) return setErr("Setup token required (printed in the server log).");
    if (!email.trim()) return setErr("Email required.");
    if (pw.length < 8) return setErr("Password must be at least 8 characters.");
    if (pw !== pw2) return setErr("Passwords don't match.");
    setBusy(true);
    try {
      // Bypass api() on purpose: pre-auth flows shouldn't go through the
      // global Unauthorized handler — there's no session to drop yet.
      const r = await fetch("/admin/api/setup", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          token: token.trim(),
          email: email.trim(),
          display_name: displayName.trim(),
          password: pw,
        }),
      });
      if (!r.ok) {
        setErr((await r.text()) || "Setup failed.");
        return;
      }
      onDone();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mx-auto mt-16 max-w-md px-4">
      <header className="mb-6 text-center">
        <div className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          mizu
        </div>
        {siteTitle && (
          <div className="mt-1 text-base font-semibold tracking-tight text-foreground">
            {siteTitle}
          </div>
        )}
      </header>
      <h1 className="mb-2 text-lg font-semibold">Welcome to mizu</h1>
      <p className="mb-4 text-sm text-muted-foreground">
        Create your account. The one-time setup token was printed to your server log when
        mizu started — paste it below to prove you're the operator.
      </p>
      <form onSubmit={submit} className="space-y-3 rounded-xl border border-border bg-card p-4 shadow-sm">
        <div className="space-y-1.5">
          <Label htmlFor="setup-token">Setup token</Label>
          <Input
            id="setup-token"
            autoFocus
            value={token}
            onChange={(e) => setToken(e.target.value)}
            spellCheck={false}
            autoCapitalize="off"
            className="font-mono"
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="setup-email">Email</Label>
          <Input
            id="setup-email"
            type="email"
            autoComplete="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="setup-display-name">Display name (optional)</Label>
          <Input
            id="setup-display-name"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="setup-password">New password</Label>
          <Input
            id="setup-password"
            type="password"
            autoComplete="new-password"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="setup-password-confirm">Confirm password</Label>
          <Input
            id="setup-password-confirm"
            type="password"
            autoComplete="new-password"
            value={pw2}
            onChange={(e) => setPw2(e.target.value)}
          />
        </div>
        {err && (
          <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {err}
          </div>
        )}
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={busy}>
            {busy ? "Saving…" : "Create account"}
          </Button>
        </div>
      </form>
    </div>
  );
}
