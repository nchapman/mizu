import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function Login({ onDone, siteTitle }: { onDone: () => void; siteTitle?: string }) {
  const [email, setEmail] = useState("");
  const [pw, setPw] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      // Bypass api() on purpose: a 401 here is a wrong-credentials
      // response, not a session expiry, so we don't want it routed
      // through the global Unauthorized handler.
      const r = await fetch("/admin/api/login", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ email: email.trim(), password: pw }),
      });
      if (!r.ok) {
        // Server returns a generic 401 for both wrong-password and
        // unknown-email — mirror that in the UI so we don't help a
        // visitor enumerate accounts.
        setErr(r.status === 401 ? "Wrong email or password." : (await r.text()) || "Login failed.");
        return;
      }
      onDone();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mx-auto mt-16 max-w-sm px-4">
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
      <h1 className="mb-3 text-lg font-semibold">Sign in</h1>
      <form onSubmit={submit} className="space-y-3 rounded-xl border border-border bg-card p-4 shadow-sm">
        <div className="space-y-1.5">
          <Label htmlFor="login-email">Email</Label>
          <Input
            id="login-email"
            type="email"
            autoFocus
            autoComplete="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="login-password">Password</Label>
          <Input
            id="login-password"
            type="password"
            autoComplete="current-password"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
          />
        </div>
        {err && (
          <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {err}
          </div>
        )}
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={busy || !email || !pw}>
            {busy ? "Signing in…" : "Sign in"}
          </Button>
        </div>
      </form>
    </div>
  );
}
