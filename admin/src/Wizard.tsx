import { useEffect, useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

// Wizard is the consumer-grade onboarding flow. It replaces the old
// token-paste setup screen: the operator drops into /admin on a fresh
// install and is walked through account creation, site basics, an
// optional DNS preflight, and optional Let's Encrypt issuance.
//
// State lives in memory — a mid-wizard page reload after the account
// step lands the user in Shell instead. The "Done" step is a soft
// finish line; nothing breaks if the operator never reaches it.

type Step = "welcome" | "account" | "site" | "domain" | "tls" | "done";

type DNSResult = {
  domain: string;
  public_ip: string;
  a_records?: string[];
  aaaa_records?: string[];
  matches: boolean;
  hints?: string[];
};

type WizardProps = {
  onDone: () => void;
  siteTitle?: string;
  setupWindow?: { open: boolean; expires_at?: string };
};

export function Wizard({ onDone, setupWindow }: WizardProps) {
  const [step, setStep] = useState<Step>("welcome");
  const [siteState, setSiteState] = useState({
    title: "",
    author: "",
    base_url: "",
    description: "",
  });
  const [tlsState, setTlsState] = useState({
    domain: "",
    email: "",
    staging: false,
  });

  // Window-closed page is rendered up the tree; here we just render
  // a tiny remaining-time hint on the welcome card.
  return (
    <div className="mx-auto mt-12 max-w-xl px-4">
      <header className="mb-6 text-center">
        <div className="text-xs font-medium uppercase tracking-wider text-muted-foreground">
          mizu setup
        </div>
        <Stepper current={step} />
      </header>
      {step === "welcome" && (
        <Welcome onNext={() => setStep("account")} setupWindow={setupWindow} />
      )}
      {step === "account" && <AccountStep onNext={() => setStep("site")} />}
      {step === "site" && (
        <SiteStep
          value={siteState}
          onChange={setSiteState}
          onNext={() => setStep("domain")}
        />
      )}
      {step === "domain" && (
        <DomainStep
          baseURL={siteState.base_url}
          onSkip={() => setStep("tls")}
          onNext={(domain) => {
            setTlsState((t) => ({ ...t, domain }));
            setStep("tls");
          }}
        />
      )}
      {step === "tls" && (
        <TLSStep
          value={tlsState}
          onChange={setTlsState}
          onSkip={() => setStep("done")}
          onNext={() => setStep("done")}
        />
      )}
      {step === "done" && <Done onClose={onDone} tlsDomain={tlsState.domain} />}
    </div>
  );
}

const ORDER: Step[] = ["welcome", "account", "site", "domain", "tls", "done"];

function Stepper({ current }: { current: Step }) {
  return (
    <div
      aria-label="Setup progress"
      className="mt-3 flex items-center justify-center gap-1.5"
    >
      {ORDER.map((s, i) => {
        const reached = ORDER.indexOf(current) >= i;
        return (
          <span
            key={s}
            className={
              "h-1.5 w-6 rounded-full " +
              (reached ? "bg-foreground" : "bg-muted")
            }
          />
        );
      })}
    </div>
  );
}

function Welcome({
  onNext,
  setupWindow,
}: {
  onNext: () => void;
  setupWindow?: { open: boolean; expires_at?: string };
}) {
  const remaining = useCountdown(setupWindow?.expires_at);
  return (
    <Card>
      <h1 className="mb-2 text-xl font-semibold">Welcome to mizu</h1>
      <p className="mb-2 text-sm text-muted-foreground">
        Let's get this instance set up. We'll create your account, give your
        site a name, and (optionally) hook up HTTPS — all from this browser.
      </p>
      {remaining && (
        <p className="mb-3 text-xs text-muted-foreground">
          Setup window: {remaining} remaining. After that this binary stops
          accepting first-run claims.
        </p>
      )}
      <div className="mt-4 flex justify-end">
        <Button onClick={onNext}>Get started</Button>
      </div>
    </Card>
  );
}

function AccountStep({ onNext }: { onNext: () => void }) {
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    if (!email.trim()) return setErr("Email required.");
    if (pw.length < 8) return setErr("Password must be at least 8 characters.");
    if (pw !== pw2) return setErr("Passwords don't match.");
    setBusy(true);
    try {
      const r = await fetch("/admin/api/setup", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          email: email.trim(),
          display_name: displayName.trim(),
          password: pw,
        }),
      });
      if (!r.ok) {
        setErr((await r.text()) || "Setup failed.");
        return;
      }
      onNext();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <h2 className="mb-2 text-lg font-semibold">Create your account</h2>
      <p className="mb-3 text-sm text-muted-foreground">
        This becomes the admin login. You can invite others later from the
        admin UI.
      </p>
      <form onSubmit={submit} className="space-y-3">
        <Field
          id="setup-email"
          label="Email"
          type="email"
          autoComplete="email"
          autoFocus
          value={email}
          onChange={setEmail}
        />
        <Field
          id="setup-display-name"
          label="Display name (optional)"
          value={displayName}
          onChange={setDisplayName}
        />
        <Field
          id="setup-password"
          label="New password"
          type="password"
          autoComplete="new-password"
          value={pw}
          onChange={setPw}
        />
        <Field
          id="setup-password-confirm"
          label="Confirm password"
          type="password"
          autoComplete="new-password"
          value={pw2}
          onChange={setPw2}
        />
        <ErrorMessage err={err} />
        <div className="flex justify-end">
          <Button type="submit" disabled={busy}>
            {busy ? "Creating…" : "Create account"}
          </Button>
        </div>
      </form>
    </Card>
  );
}

type SiteState = {
  title: string;
  author: string;
  base_url: string;
  description: string;
};

function SiteStep({
  value,
  onChange,
  onNext,
}: {
  value: SiteState;
  onChange: (v: SiteState) => void;
  onNext: () => void;
}) {
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    if (!value.title.trim()) return setErr("Site title required.");
    if (!value.base_url.trim()) return setErr("Base URL required (e.g. https://blog.example).");
    setBusy(true);
    try {
      const r = await fetch("/admin/api/setup/site", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          title: value.title.trim(),
          author: value.author.trim(),
          base_url: value.base_url.trim(),
          description: value.description.trim(),
        }),
      });
      if (!r.ok) {
        setErr((await r.text()) || "Could not save settings.");
        return;
      }
      onNext();
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <h2 className="mb-2 text-lg font-semibold">About your site</h2>
      <p className="mb-3 text-sm text-muted-foreground">
        These details power your homepage, your RSS feed, and the canonical URLs in webmentions.
      </p>
      <form onSubmit={submit} className="space-y-3">
        <Field
          id="site-title"
          label="Site title"
          autoFocus
          value={value.title}
          onChange={(v) => onChange({ ...value, title: v })}
        />
        <Field
          id="site-author"
          label="Author byline (optional)"
          value={value.author}
          onChange={(v) => onChange({ ...value, author: v })}
        />
        <Field
          id="site-base-url"
          label="Public URL"
          placeholder="https://blog.example"
          value={value.base_url}
          onChange={(v) => onChange({ ...value, base_url: v })}
        />
        <div className="space-y-1.5">
          <Label htmlFor="site-description">Tagline (optional)</Label>
          <Textarea
            id="site-description"
            rows={2}
            value={value.description}
            onChange={(e) => onChange({ ...value, description: e.target.value })}
          />
        </div>
        <ErrorMessage err={err} />
        <div className="flex justify-end">
          <Button type="submit" disabled={busy}>
            {busy ? "Saving…" : "Save and continue"}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function DomainStep({
  baseURL,
  onSkip,
  onNext,
}: {
  baseURL: string;
  onSkip: () => void;
  onNext: (domain: string) => void;
}) {
  const initialDomain = (() => {
    try {
      return new URL(baseURL).hostname;
    } catch {
      return "";
    }
  })();
  const [domain, setDomain] = useState(initialDomain);
  const [result, setResult] = useState<DNSResult | null>(null);
  const [busy, setBusy] = useState(false);

  async function check() {
    setBusy(true);
    try {
      const r = await fetch("/admin/api/setup/dns-check", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ domain: domain.trim() }),
      });
      if (r.ok) setResult(await r.json());
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <h2 className="mb-2 text-lg font-semibold">Point your domain here</h2>
      <p className="mb-3 text-sm text-muted-foreground">
        Optional. If you plan to enable HTTPS in the next step, your domain
        needs an A record pointing at this server first.
      </p>
      <div className="space-y-3">
        <Field
          id="dns-domain"
          label="Domain"
          placeholder="blog.example"
          value={domain}
          onChange={setDomain}
        />
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="outline" onClick={check} disabled={busy || !domain.trim()}>
            {busy ? "Checking…" : "Check DNS"}
          </Button>
          <Button type="button" variant="outline" onClick={onSkip}>
            Skip
          </Button>
          <Button
            type="button"
            onClick={() => onNext(domain.trim())}
            disabled={!result?.matches}
            className="ml-auto"
          >
            DNS looks good — continue
          </Button>
        </div>
        {result && (
          <div
            className={
              "rounded-md border px-3 py-2 text-sm " +
              (result.matches
                ? "border-emerald-500/40 bg-emerald-500/5 text-emerald-700 dark:text-emerald-300"
                : "border-amber-500/40 bg-amber-500/5 text-amber-700 dark:text-amber-300")
            }
          >
            {result.matches ? (
              <p>
                {result.domain} → {result.public_ip}. You're good to go.
              </p>
            ) : (
              <div className="space-y-1">
                {result.public_ip && (
                  <p>This server's public IP appears to be {result.public_ip}.</p>
                )}
                {result.hints?.map((h, i) => <p key={i}>{h}</p>)}
              </div>
            )}
          </div>
        )}
      </div>
    </Card>
  );
}

function TLSStep({
  value,
  onChange,
  onSkip,
  onNext,
}: {
  value: { domain: string; email: string; staging: boolean };
  onChange: (v: { domain: string; email: string; staging: boolean }) => void;
  onSkip: () => void;
  onNext: () => void;
}) {
  const [err, setErr] = useState("");
  const [status, setStatus] = useState<"idle" | "saving">("idle");

  async function enable() {
    setErr("");
    if (!value.domain.trim()) return setErr("Domain required.");
    if (!value.email.trim()) return setErr("Contact email required (Let's Encrypt sends renewal warnings here).");
    setStatus("saving");
    try {
      const r = await fetch("/admin/api/setup/enable-tls", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          domains: [value.domain.trim()],
          email: value.email.trim(),
          staging: value.staging,
        }),
      });
      if (!r.ok) {
        setErr((await r.text()) || "Could not enable HTTPS.");
        return;
      }
      // The backend always returns 202: either DNS already resolved and
      // TLS is coming up now, or DNS isn't ready and a background poller
      // owns the retry. The Done step explains which case we're in.
      onNext();
    } finally {
      setStatus("idle");
    }
  }

  return (
    <Card>
      <h2 className="mb-2 text-lg font-semibold">Enable HTTPS</h2>
      <p className="mb-3 text-sm text-muted-foreground">
        Optional. We use Let's Encrypt's free certificates. You can skip this
        and run behind your own TLS terminator instead.
      </p>
      <div className="space-y-3">
        <Field
          id="tls-domain"
          label="Domain"
          value={value.domain}
          onChange={(v) => onChange({ ...value, domain: v })}
        />
        <Field
          id="tls-email"
          label="Contact email"
          type="email"
          value={value.email}
          onChange={(v) => onChange({ ...value, email: v })}
        />
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={value.staging}
            onChange={(e) => onChange({ ...value, staging: e.target.checked })}
          />
          Use Let's Encrypt staging (untrusted certs, looser rate limits — handy for testing).
        </label>
        <ErrorMessage err={err} />
        <p className="text-xs text-muted-foreground">
          If DNS hasn't propagated yet, that's fine — we'll keep checking in
          the background and switch HTTPS on the moment it's ready.
        </p>
        <div className="flex flex-wrap gap-2">
          <Button type="button" variant="outline" onClick={onSkip} disabled={status !== "idle"}>
            Skip for now
          </Button>
          <Button type="button" onClick={enable} disabled={status !== "idle"} className="ml-auto">
            {status === "saving" ? "Saving…" : "Enable HTTPS"}
          </Button>
        </div>
      </div>
    </Card>
  );
}

function Done({ onClose, tlsDomain }: { onClose: () => void; tlsDomain: string }) {
  const url = tlsDomain ? `https://${tlsDomain}/admin` : "/admin";
  return (
    <Card>
      <h2 className="mb-2 text-lg font-semibold">You're set</h2>
      <p className="mb-3 text-sm text-muted-foreground">
        That's it. Mizu is ready to publish and read feeds.
      </p>
      <div className="flex justify-end gap-2">
        {tlsDomain ? (
          <a href={url}>
            <Button>Open admin over HTTPS</Button>
          </a>
        ) : (
          <Button onClick={onClose}>Open admin</Button>
        )}
      </div>
    </Card>
  );
}

function Card({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-xl border border-border bg-card p-5 shadow-sm">
      {children}
    </div>
  );
}

type FieldProps = {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
} & Omit<React.InputHTMLAttributes<HTMLInputElement>, "id" | "value" | "onChange">;

function Field({ id, label, value, onChange, ...rest }: FieldProps) {
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} value={value} onChange={(e) => onChange(e.target.value)} {...rest} />
    </div>
  );
}

function ErrorMessage({ err }: { err: string }) {
  if (!err) return null;
  return (
    <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
      {err}
    </div>
  );
}

function useCountdown(expiresAt?: string): string | null {
  // We track "minutes remaining" as the rendered state rather than
  // recomputing from Date.now() inside the body — purity rules want
  // any clock read to flow through useEffect, not the render path.
  const [mins, setMins] = useState<number | null>(() => {
    if (!expiresAt) return null;
    const ms = new Date(expiresAt).getTime() - Date.now();
    return ms <= 0 ? null : Math.round(ms / 60_000);
  });
  useEffect(() => {
    if (!expiresAt) {
      setMins(null);
      return;
    }
    const recompute = () => {
      const ms = new Date(expiresAt).getTime() - Date.now();
      setMins(ms <= 0 ? null : Math.round(ms / 60_000));
    };
    recompute();
    const id = setInterval(recompute, 30_000);
    return () => clearInterval(id);
  }, [expiresAt]);
  if (mins == null) return null;
  return `${mins} minute${mins === 1 ? "" : "s"}`;
}

export function SetupWindowClosed() {
  return (
    <div className="mx-auto mt-16 max-w-md px-4 text-center">
      <div className="mb-3 text-xs font-medium uppercase tracking-wider text-muted-foreground">
        mizu
      </div>
      <h1 className="mb-2 text-lg font-semibold">Setup window has closed</h1>
      <p className="text-sm text-muted-foreground">
        For safety, this binary only accepts first-run setup for the first hour
        after the server starts. Restart the server to reopen the window.
      </p>
    </div>
  );
}
