import { useState } from "react";
import { shellStyle } from "./styles";

export function Login({ onDone }: { onDone: () => void }) {
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
    <div style={shellStyle}>
      <h1 style={{ fontSize: "1.2em" }}>Sign in</h1>
      <form onSubmit={submit} style={{ border: "1px solid #ddd", borderRadius: 8, padding: "1em" }}>
        <input type="password" autoFocus placeholder="Password" value={pw}
          onChange={(e) => setPw(e.target.value)}
          style={{ width: "100%", padding: ".4em", fontSize: "1em", marginBottom: ".5em" }} />
        {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: ".5em" }}>{err}</div>}
        <button type="submit" disabled={busy || !pw}>{busy ? "Signing in…" : "Sign in"}</button>
      </form>
    </div>
  );
}
