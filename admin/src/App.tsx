import { useCallback, useEffect, useState } from "react";
import "./App.css";
import { Login } from "./Login";
import { Setup } from "./Setup";
import { Shell } from "./Shell";
import { shellStyle } from "./styles";

type Me = { configured: boolean; authenticated: boolean };

export function App() {
  const [me, setMe] = useState<Me | null>(null);
  const [initErr, setInitErr] = useState(false);

  // loadMe flows down through Shell into the composer's upload callback,
  // which uses it as a useCallback dep. A stable identity here means
  // Lexical's PASTE/DROP command listeners don't tear down and re-register
  // on unrelated re-renders.
  const loadMe = useCallback(async () => {
    try {
      setInitErr(false);
      const r = await fetch("/admin/api/me");
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      setMe(await r.json());
    } catch {
      setInitErr(true);
    }
  }, []);
  useEffect(() => {
    loadMe();
  }, [loadMe]);

  if (initErr) {
    return (
      <div style={shellStyle}>
        <p>Could not reach the server.</p>
        <button type="button" onClick={loadMe}>Retry</button>
      </div>
    );
  }
  if (!me) return null;
  if (!me.configured) return <Setup onDone={loadMe} />;
  if (!me.authenticated) return <Login onDone={loadMe} />;
  return <Shell onLogout={loadMe} />;
}
