import { useCallback, useState } from "react";
import { HomeView } from "./HomeView";
import { MentionsView } from "./Mentions";
import { SettingsView, type TLSStatus } from "./Settings";
import { SubscriptionsView } from "./Subscriptions";
import { TopBar } from "./TopBar";
import { useRoute } from "./lib/router";

// EditTarget is the cross-context handoff used when an action elsewhere
// (e.g. drafts drawer "Continue") needs to drop a record into the home
// composer. HomeView consumes it once and clears it.
export type EditTarget =
  | { kind: "post"; id: string; title: string; body: string }
  | { kind: "draft"; id: string; title: string; body: string };

export function Shell({
  onLogout,
  tls,
  onTLSChanged,
}: {
  onLogout: () => void;
  tls?: TLSStatus;
  onTLSChanged?: () => void;
}) {
  const [route, navigate] = useRoute();
  const [editTarget, setEditTarget] = useState<EditTarget | null>(null);

  async function logout() {
    await fetch("/admin/api/logout", { method: "POST" });
    onLogout();
  }

  // Memoized specifically because it flows into HomeView's effect dep
  // list.
  const clearEditTarget = useCallback(() => setEditTarget(null), []);

  // Surface an attention dot on the menu when HTTPS is off. "issuing",
  // "pending", and "ready" all count as configured (the operator has
  // expressed intent); only "off" / "error" prompt for action.
  const tlsNeedsAttention = !!tls && tls.state !== "ready" && tls.state !== "issuing" && tls.state !== "pending";

  return (
    <div className="min-h-screen bg-background text-foreground">
      <TopBar
        onNavigate={navigate}
        onLogout={logout}
        needsAttention={tlsNeedsAttention}
      />
      <main className="mx-auto max-w-2xl px-4 pb-16">
        {route === "home" && (
          <HomeView
            onAuthLost={onLogout}
            editTarget={editTarget}
            onEditConsumed={clearEditTarget}
          />
        )}
        {route === "subscriptions" && <SubscriptionsView onAuthLost={onLogout} />}
        {route === "mentions" && <MentionsView onAuthLost={onLogout} />}
        {route === "settings" && (
          <SettingsView
            onAuthLost={onLogout}
            tlsStatus={tls}
            onTLSChanged={onTLSChanged}
          />
        )}
      </main>
    </div>
  );
}
