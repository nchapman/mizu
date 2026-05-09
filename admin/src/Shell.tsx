import { useCallback, useState } from "react";
import { DraftsView } from "./Drafts";
import { HomeView } from "./HomeView";
import { SettingsView } from "./Settings";
import { StreamView } from "./Stream";
import { SubscriptionsView } from "./Subscriptions";
import { TopBar } from "./TopBar";
import type { Draft } from "./api";
import { useRoute } from "./lib/router";

// EditTarget is the cross-context handoff used when an action elsewhere
// (e.g. "edit this draft") needs to drop a record into the home composer.
// HomeView consumes it once and clears it.
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

  function editDraft(d: Draft) {
    setEditTarget({ kind: "draft", id: d.id, title: d.title ?? "", body: d.body });
    navigate("home");
  }

  // Memoized specifically because it flows into HomeView's effect dep
  // list. Other handlers here (logout, editDraft) intentionally aren't
  // memoized — they're plain event handlers and don't gate any effect.
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
        {route === "drafts" && <DraftsView onAuthLost={onLogout} onEdit={editDraft} />}
        {route === "timeline" && <StreamView onAuthLost={onLogout} />}
        {route === "subscriptions" && <SubscriptionsView onAuthLost={onLogout} />}
        {route === "settings" && <SettingsView />}
      </main>
    </div>
  );
}
