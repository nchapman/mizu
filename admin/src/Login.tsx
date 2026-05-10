import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export function Login({ onDone, siteTitle }: { onDone: () => void; siteTitle?: string }) {
  const [pw, setPw] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      const r = await fetch("/admin/api/login", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ password: pw }),
      });
      if (!r.ok) {
        setErr(r.status === 401 ? "Wrong password." : (await r.text()) || "Login failed.");
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
          repeat
        </div>
        {siteTitle && (
          <div className="mt-1 text-base font-semibold tracking-tight text-foreground">
            {siteTitle}
          </div>
        )}
      </header>
      <h1 className="mb-3 text-lg font-semibold">Sign in</h1>
      <form onSubmit={submit} className="space-y-3 rounded-xl border border-border bg-card p-4 shadow-sm">
        <Input
          type="password"
          autoFocus
          placeholder="Password"
          value={pw}
          onChange={(e) => setPw(e.target.value)}
        />
        {err && (
          <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {err}
          </div>
        )}
        <div className="flex justify-end">
          <Button type="submit" size="sm" disabled={busy || !pw}>
            {busy ? "Signing in…" : "Sign in"}
          </Button>
        </div>
      </form>
    </div>
  );
}
