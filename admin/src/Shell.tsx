import { useCallback, useState } from "react";
import { HomeView } from "./HomeView";
import { MentionsView } from "./Mentions";
import { SettingsView } from "./Settings";
import { SubscriptionsView } from "./Subscriptions";
import { TopBar } from "./TopBar";
import { useRoute } from "./lib/router";

// EditTarget is the cross-context handoff used when an action elsewhere
// (e.g. drafts drawer "Continue") needs to drop a record into the home
// composer. HomeView consumes it once and clears it.
export type EditTarget =
  | { kind: "post"; id: string; title: string; body: string }
  | { kind: "draft"; id: string; title: string; body: string };

export function Shell({ onLogout }: { onLogout: () => void }) {
  const [route, navigate] = useRoute();
  const [editTarget, setEditTarget] = useState<EditTarget | null>(null);

  async function logout() {
    await fetch("/admin/api/logout", { method: "POST" });
    onLogout();
  }

  // Memoized specifically because it flows into HomeView's effect dep
  // list.
  const clearEditTarget = useCallback(() => setEditTarget(null), []);

  return (
    <div className="min-h-screen bg-background text-foreground">
      <TopBar onNavigate={navigate} onLogout={logout} />
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
        {route === "settings" && <SettingsView onAuthLost={onLogout} />}
      </main>
    </div>
  );
}
