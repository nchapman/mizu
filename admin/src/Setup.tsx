import { useState } from "react";
import { shellStyle } from "./styles";

export function Setup({ onDone }: { onDone: () => void }) {
  const [token, setToken] = useState("");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    if (!token.trim()) return setErr("Setup token required (printed in the server log).");
    if (pw.length < 8) return setErr("Password must be at least 8 characters.");
    if (pw !== pw2) return setErr("Passwords don't match.");
    setBusy(true);
    try {
      const r = await fetch("/admin/api/setup", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ password: pw, token: token.trim() }),
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
    <div style={shellStyle}>
      <h1 style={{ fontSize: "1.2em" }}>Welcome to repeat</h1>
      <p style={{ color: "#555" }}>
        Set a password to lock down the admin UI. The one-time setup token was printed to your
        server log when repeat started — paste it below to prove you're the operator.
      </p>
      <form onSubmit={submit} style={{ border: "1px solid #ddd", borderRadius: 8, padding: "1em" }}>
        <label style={{ display: "block", marginBottom: ".5em" }}>
          <div style={{ fontSize: ".9em", color: "#555" }}>Setup token</div>
          <input autoFocus value={token} onChange={(e) => setToken(e.target.value)}
            spellCheck={false} autoCapitalize="off"
            style={{ width: "100%", padding: ".4em", fontSize: "1em", fontFamily: "monospace" }} />
        </label>
        <label style={{ display: "block", marginBottom: ".5em" }}>
          <div style={{ fontSize: ".9em", color: "#555" }}>New password</div>
          <input type="password" value={pw} onChange={(e) => setPw(e.target.value)}
            style={{ width: "100%", padding: ".4em", fontSize: "1em" }} />
        </label>
        <label style={{ display: "block", marginBottom: ".5em" }}>
          <div style={{ fontSize: ".9em", color: "#555" }}>Confirm password</div>
          <input type="password" value={pw2} onChange={(e) => setPw2(e.target.value)}
            style={{ width: "100%", padding: ".4em", fontSize: "1em" }} />
        </label>
        {err && <div style={{ color: "#b00", fontSize: ".9em", marginBottom: ".5em" }}>{err}</div>}
        <button type="submit" disabled={busy}>{busy ? "Saving…" : "Set password"}</button>
      </form>
    </div>
  );
}
