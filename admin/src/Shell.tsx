import { useCallback, useState } from "react";
import { DraftsView } from "./Drafts";
import { HomeView } from "./HomeView";
import { SubscriptionsView } from "./Subscriptions";
import { TimelineView } from "./Timeline";
import type { Draft } from "./api";
import { linkBtn, shellStyle } from "./styles";

type Tab = "home" | "drafts" | "timeline" | "subs";

// EditTarget is the cross-tab handoff used when an action in another tab
// (e.g. "edit this draft") needs to drop a record into the home composer.
// HomeView consumes it once and clears it.
export type EditTarget =
  | { kind: "post"; id: string; title: string; body: string }
  | { kind: "draft"; id: string; title: string; body: string };

export function Shell({ onLogout }: { onLogout: () => void }) {
  const [tab, setTab] = useState<Tab>("home");
  const [editTarget, setEditTarget] = useState<EditTarget | null>(null);

  async function logout() {
    await fetch("/admin/api/logout", { method: "POST" });
    onLogout();
  }

  function editDraft(d: Draft) {
    setEditTarget({ kind: "draft", id: d.id, title: d.title ?? "", body: d.body });
    setTab("home");
  }

  // Memoized specifically because it flows into HomeView's effect dep
  // list. Other handlers here (logout, editDraft) intentionally aren't
  // memoized — they're plain event handlers and don't gate any effect.
  const clearEditTarget = useCallback(() => setEditTarget(null), []);

  return (
    <div style={shellStyle}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "1em" }}>
        <h1 style={{ fontSize: "1.2em", margin: 0 }}>repeat</h1>
        <button type="button" onClick={logout} style={linkBtn}>Sign out</button>
      </div>
      <nav style={{ display: "flex", gap: ".5em", borderBottom: "1px solid #ddd", marginBottom: "1.5em" }}>
        <TabBtn active={tab === "home"} onClick={() => setTab("home")}>Home</TabBtn>
        <TabBtn active={tab === "drafts"} onClick={() => setTab("drafts")}>Drafts</TabBtn>
        <TabBtn active={tab === "timeline"} onClick={() => setTab("timeline")}>Timeline</TabBtn>
        <TabBtn active={tab === "subs"} onClick={() => setTab("subs")}>Subscriptions</TabBtn>
      </nav>
      {tab === "home" && (
        <HomeView
          onAuthLost={onLogout}
          editTarget={editTarget}
          onEditConsumed={clearEditTarget}
        />
      )}
      {tab === "drafts" && <DraftsView onAuthLost={onLogout} onEdit={editDraft} />}
      {tab === "timeline" && <TimelineView onAuthLost={onLogout} />}
      {tab === "subs" && <SubscriptionsView onAuthLost={onLogout} />}
    </div>
  );
}

function TabBtn({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        background: "none", border: "none", cursor: "pointer", padding: ".5em .75em",
        borderBottom: active ? "2px solid #333" : "2px solid transparent",
        color: active ? "#111" : "#666",
        fontWeight: active ? 500 : 400,
        marginBottom: -1,
      }}
    >
      {children}
    </button>
  );
}
