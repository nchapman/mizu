import { useCallback, useEffect, useState } from "react";
import "./App.css";
import { Login } from "./Login";
import { Shell } from "./Shell";
import { SetupWindowClosed, Wizard } from "./Wizard";
import { Button } from "@/components/ui/button";

type MeUser = { id: number; email: string; display_name: string };
type SetupWindow = { open: boolean; expires_at?: string };
type Me = {
  configured: boolean;
  authenticated: boolean;
  site_title?: string;
  user?: MeUser;
  setup_window?: SetupWindow;
};

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
      <div className="mx-auto max-w-2xl p-8">
        <p className="mb-4 text-sm text-muted-foreground">Could not reach the server.</p>
        <Button variant="outline" onClick={loadMe}>Retry</Button>
      </div>
    );
  }
  if (!me) return null;
  if (!me.configured) {
    if (me.setup_window && !me.setup_window.open) return <SetupWindowClosed />;
    return <Wizard onDone={loadMe} siteTitle={me.site_title} setupWindow={me.setup_window} />;
  }
  if (!me.authenticated) return <Login onDone={loadMe} siteTitle={me.site_title} />;
  return <Shell onLogout={loadMe} />;
}
