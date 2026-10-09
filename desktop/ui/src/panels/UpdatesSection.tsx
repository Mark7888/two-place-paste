import { useCallback, useEffect, useState } from "react";
import {
  api,
  type InstallRequest,
  type ServiceEvent,
  type SettingsPatch,
  type SettingsView,
  type UpdateCandidate,
  type UpdateView,
} from "../api";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";
import { relative, when } from "../format";

// A build's stamp is seconds since this instant (scripts/version.sh).
const STAMP_EPOCH = Date.UTC(2025, 0, 1);

function builtAt(stamp: number): string | undefined {
  return stamp > 0 ? new Date(STAMP_EPOCH + stamp * 1000).toISOString() : undefined;
}

// The service's messages are sentence fragments ("the check failed: …").
function sentence(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}

function shortCommit(sha?: string): string {
  return sha ? sha.slice(0, 7) : "";
}

// A dev build's version already ends in +<commit>; a release's does not.
function commitSuffix(version: string, sha?: string): string {
  const c = shortCommit(sha);
  return c && !version.includes(c) ? ` · ${c}` : "";
}

const channels = [
  { id: "stable", label: "Stable", why: "releases" },
  { id: "beta", label: "Beta", why: "the main branch's newest build" },
  { id: "nightly", label: "Nightly", why: "a commit you choose" },
] as const;

const busyStates = new Set(["checking", "downloading", "installing", "restarting"]);

/**
 * Updates: which channel this installation follows, what it is running, and
 * what that channel offers (docs/plans/versioning-releases-and-updates.md §3).
 *
 * Installing restarts the service, and a restarted service has a new launch
 * token, so this page cannot follow it: after an install it says to reopen
 * TwoPlacePaste from the tray rather than spin forever.
 */
export function UpdatesSection({
  settings,
  onPatch,
  lastEvent,
}: {
  settings: SettingsView;
  /** Resolves to whether the change was saved. */
  onPatch: (patch: SettingsPatch) => Promise<boolean>;
  lastEvent: ServiceEvent | null;
}) {
  const [view, setView] = useState<UpdateView | null>(null);
  const [error, setError] = useState("");
  const [commit, setCommit] = useState(settings.nightly_commit);
  const [confirming, setConfirming] = useState<UpdateCandidate | null>(null);
  const [working, setWorking] = useState(false);

  const load = useCallback(async () => {
    try {
      setView(await api.updateStatus());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (lastEvent?.kind === "update" || lastEvent?.kind === "settings") {
      setError("");
      void load();
    }
  }, [lastEvent, load]);

  useEffect(() => setCommit(settings.nightly_commit), [settings.nightly_commit]);

  // A failed check or install is also the updater's state, which the notice
  // below shows; repeating it here would say the same thing twice. Only an
  // error the state does not carry (a refused request) is shown on its own.
  const run = async (action: () => Promise<UpdateView>) => {
    setError("");
    setWorking(true);
    try {
      setView(await action());
    } catch (err) {
      const next = await api.updateStatus().catch(() => null);
      if (next) setView(next);
      if (!next || next.state !== "error") {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setWorking(false);
    }
  };

  const install = (req: InstallRequest) => {
    setConfirming(null);
    void run(() => api.installUpdate(req));
  };

  // An older or an unsigned build is never one click: the dialog says which,
  // and what it means, before anything is downloaded.
  const startInstall = (c: UpdateCandidate) => {
    if (c.older || c.needs_confirmation) setConfirming(c);
    else install({});
  };

  if (!view) {
    return (
      <section className="group" id="updates">
        <h2>Updates</h2>
        {error ? (
          <div className="notice error">
            <Icon name="alert" />
            <span>{error}</span>
          </div>
        ) : (
          <div className="row">
            <span className="spinner" />
            <span className="muted">Loading…</span>
          </div>
        )}
      </section>
    );
  }

  const busy = working || busyStates.has(view.state);
  const nightly = settings.update_channel === "nightly";
  const avail = view.available;

  return (
    <section className="group" id="updates">
      <h2>Updates</h2>
      <div className="card stack" style={{ gap: "1rem" }}>
        <dl className="facts">
          <dt>Version</dt>
          <dd>
            {view.current.version}
            <span className="muted mono">{commitSuffix(view.current.version, view.current.commit)}</span>
          </dd>
          <dt>Built</dt>
          <dd>{builtAt(view.current.stamp) ? when(builtAt(view.current.stamp)) : "locally"}</dd>
          <dt>Last checked</dt>
          <dd>{relative(view.last_checked)}</dd>
        </dl>

        <label className="field" style={{ maxWidth: "22rem" }}>
          <span className="label">Channel</span>
          <select
            value={settings.update_channel}
            disabled={busy}
            onChange={(e) => void onPatch({ update_channel: e.target.value as SettingsView["update_channel"] })}
          >
            {channels.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label} — {c.why}
              </option>
            ))}
          </select>
        </label>

        {nightly ? (
          <>
            <div className="inline-form">
              <label className="field">
                <span className="label">Commit</span>
                <input
                  type="text"
                  className="mono"
                  value={commit}
                  placeholder="a1b2c3d"
                  spellCheck={false}
                  onChange={(e) => setCommit(e.target.value.trim())}
                />
              </label>
              <button
                className="action"
                disabled={busy || commit === "" || commit === settings.nightly_commit}
                onClick={() =>
                  void (async () => {
                    // A short hash is expanded by the service; once saved,
                    // the commit's build is looked up straight away.
                    if (await onPatch({ nightly_commit: commit })) await run(api.checkUpdate);
                  })()
                }
              >
                Use this commit
              </button>
            </div>
            <p className="muted small">
              Installs that commit&apos;s build and never updates on its own. Builds exist for commits
              on the main branch and the latest commit of each pull request.
            </p>
            <TokenControls view={view} onView={setView} disabled={busy} />
          </>
        ) : null}

        <label className="toggle">
          <input
            type="checkbox"
            checked={settings.auto_update}
            disabled={nightly}
            onChange={(e) => void onPatch({ auto_update: e.target.checked })}
          />
          <span className="text">
            <strong>Install updates automatically</strong>
            <span className="why">
              {nightly
                ? "Nightly installs only the commit you choose."
                : "Checks every few hours and installs while the computer is idle. Off by default."}
            </span>
          </span>
        </label>

        {error ? (
          <div className="notice error">
            <Icon name="alert" />
            <span>{sentence(error)}</span>
          </div>
        ) : null}
        <StateNotice view={view} />
        {avail ? (
          <Candidate
            candidate={avail}
            canInstall={view.can_install && !busy}
            onInstall={() => startInstall(avail)}
          />
        ) : null}
        {!view.can_install && view.cannot_install ? (
          <div className="notice info">
            <Icon name="info" />
            <span>Updates cannot be installed here: {view.cannot_install}.</span>
          </div>
        ) : null}

        <div className="row" style={{ gap: "0.5rem", flexWrap: "wrap" }}>
          <button className="action" disabled={busy} onClick={() => void run(api.checkUpdate)}>
            {view.state === "checking" ? <span className="spinner" /> : <Icon name="refresh" size={15} />}
            Check now
          </button>
          {view.previous ? (
            <button
              className="action"
              disabled={busy || !view.can_install}
              onClick={() => void run(api.rollbackUpdate)}
            >
              Roll back to {view.previous.version}
            </button>
          ) : null}
        </div>
      </div>

      <Modal
        open={confirming !== null}
        title={confirming?.needs_confirmation ? "Install an unsigned build?" : "Install an older build?"}
        onClose={() => setConfirming(null)}
        wide={confirming?.needs_confirmation}
        footer={
          <>
            <button className="action" onClick={() => setConfirming(null)}>
              Cancel
            </button>
            <button
              className={confirming?.needs_confirmation ? "action solid-danger" : "action primary"}
              onClick={() =>
                install({
                  confirm: confirming?.needs_confirmation,
                  allow_older: confirming?.older,
                })
              }
            >
              {confirming?.needs_confirmation ? "Install this commit" : "Install anyway"}
            </button>
          </>
        }
      >
        {confirming ? <ConfirmBody candidate={confirming} current={view.current.version} /> : null}
      </Modal>
    </section>
  );
}

// Where a fine-grained token is created, and what it needs.
const NEW_TOKEN_URL = "https://github.com/settings/personal-access-tokens/new";

/**
 * The Nightly channel's GitHub token. It goes to the service once, is kept in
 * the OS keystore, and is never shown again: this only ever knows whether one
 * is set and when it expires.
 */
function TokenControls({
  view,
  onView,
  disabled,
}: {
  view: UpdateView;
  onView: (v: UpdateView) => void;
  disabled: boolean;
}) {
  const [token, setToken] = useState("");
  const [replacing, setReplacing] = useState(false);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const t = view.token;

  const save = async () => {
    setError("");
    setSaving(true);
    try {
      onView(await api.setUpdateToken(token));
      setToken("");
      setReplacing(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    setError("");
    try {
      onView(await api.deleteUpdateToken());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const expiresSoon =
    t.expires_at !== undefined && new Date(t.expires_at).getTime() - Date.now() < 14 * 86_400_000;

  return (
    <div className="stack" style={{ gap: "0.6rem" }}>
      {t.set && !replacing ? (
        <div className="row" style={{ gap: "0.5rem", flexWrap: "wrap" }}>
          <span>
            GitHub token {t.source === "environment" ? "from TPP_DESKTOP_GITHUB_TOKEN" : "saved"}
            {t.expires_at ? <span className="muted">, expires {when(t.expires_at)}</span> : null}.
          </span>
          {t.source === "keystore" ? (
            <>
              <button className="action" disabled={disabled} onClick={() => setReplacing(true)}>
                Replace
              </button>
              <button className="action" disabled={disabled} onClick={() => void remove()}>
                Remove
              </button>
            </>
          ) : null}
        </div>
      ) : (
        <>
          <div className="inline-form">
            <label className="field">
              <span className="label">GitHub token</span>
              <input
                type="password"
                value={token}
                autoComplete="off"
                spellCheck={false}
                placeholder="github_pat_…"
                onChange={(e) => setToken(e.target.value.trim())}
              />
            </label>
            <button className="action" disabled={disabled || saving || token === ""} onClick={() => void save()}>
              {saving ? <span className="spinner" /> : null}
              Save token
            </button>
            {replacing ? (
              <button className="action" onClick={() => setReplacing(false)}>
                Cancel
              </button>
            ) : null}
          </div>
          <p className="muted small">
            Nightly downloads builds from GitHub Actions, which needs a token.{" "}
            <a href={NEW_TOKEN_URL} target="_blank" rel="noreferrer">
              Create a fine-grained token
            </a>{" "}
            with access to only Mark7888/two-place-paste and the Actions: Read-only permission. It is
            kept in this computer&apos;s keychain and never shown again.
          </p>
        </>
      )}
      {t.invalid ? (
        <div className="notice warn">
          <Icon name="alert" />
          <span>GitHub rejected the saved token. Replace it to use Nightly.</span>
        </div>
      ) : expiresSoon && !replacing ? (
        <div className="notice warn">
          <Icon name="alert" />
          <span>The token expires {relativeFuture(t.expires_at)}. Replace it before then.</span>
        </div>
      ) : null}
      {error ? (
        <div className="notice error">
          <Icon name="alert" />
          <span>{sentence(error)}</span>
        </div>
      ) : null}
    </div>
  );
}

function relativeFuture(iso?: string): string {
  if (!iso) return "soon";
  const days = Math.round((new Date(iso).getTime() - Date.now()) / 86_400_000);
  if (days <= 0) return "today";
  return days === 1 ? "tomorrow" : `in ${days} days`;
}

function StateNotice({ view }: { view: UpdateView }) {
  switch (view.state) {
    case "up_to_date":
      return (
        <div className="notice ok" role="status">
          <Icon name="check" />
          <span>{view.message ? sentence(view.message) : "This is the newest build on this channel."}</span>
        </div>
      );
    case "error":
      return (
        <div className="notice error" role="status">
          <Icon name="alert" />
          <span>{view.message ? sentence(view.message) : "The last update failed."}</span>
        </div>
      );
    case "downloading":
    case "installing":
      return (
        <div className="notice info" role="status">
          <span className="spinner" />
          <span>{view.state === "downloading" ? "Downloading and verifying…" : "Installing…"}</span>
        </div>
      );
    case "restarting":
      return (
        <div className="notice ok" role="status">
          <Icon name="check" />
          <span>
            Installed. TwoPlacePaste is restarting; open it again from the tray icon in a few
            seconds.
          </span>
        </div>
      );
    case "building":
      return (
        <div className="notice info" role="status">
          <span className="spinner" />
          <span>That commit is still building. Check again when its run has finished.</span>
        </div>
      );
    default:
      return null;
  }
}

function Candidate({
  candidate,
  canInstall,
  onInstall,
}: {
  candidate: UpdateCandidate;
  canInstall: boolean;
  onInstall: () => void;
}) {
  if (candidate.building) return null;
  return (
    <div className={`notice ${candidate.older || !candidate.signed ? "warn" : "info"}`}>
      <Icon name={candidate.older || !candidate.signed ? "alert" : "download"} />
      <span className="stack" style={{ gap: "0.5rem", flex: 1 }}>
        <span>
          <strong>{candidate.version}</strong>
          <span className="mono">{commitSuffix(candidate.version, candidate.commit)}</span>
          {builtAt(candidate.stamp) ? <span>, built {when(builtAt(candidate.stamp))}</span> : null}
          {candidate.older ? <span> — older than the running build</span> : null}
          {!candidate.signed ? <span> — unsigned</span> : null}
        </span>
        <span>
          <button className="action" disabled={!canInstall} onClick={onInstall}>
            <Icon name="download" size={15} />
            Install and restart
          </button>
        </span>
      </span>
    </div>
  );
}

function ConfirmBody({ candidate, current }: { candidate: UpdateCandidate; current: string }) {
  if (!candidate.needs_confirmation) {
    return (
      <p>
        {candidate.version} is older than the running {current}. Settings and keys are kept, but
        anything the newer build added may stop working.
      </p>
    );
  }
  const o = candidate.origin;
  return (
    <>
      <p>
        This build was made by a pull request&apos;s own CI run, which could not sign it. It runs
        with your account&apos;s access to this computer and your clipboard. Install it only if you
        have read the change.
      </p>
      {o ? (
        <dl className="facts">
          <dt>Commit</dt>
          <dd className="mono">
            <a href={o.url} target="_blank" rel="noreferrer">
              {shortCommit(candidate.commit)}
            </a>
          </dd>
          {o.commit_message ? (
            <>
              <dt>Message</dt>
              <dd>{o.commit_message}</dd>
            </>
          ) : null}
          {o.author ? (
            <>
              <dt>Author</dt>
              <dd>{o.author}</dd>
            </>
          ) : null}
          <dt>From</dt>
          <dd>
            {o.repository}
            {o.branch ? <span className="mono"> ({o.branch})</span> : null}
            {o.fork ? <strong> — a fork</strong> : null}
          </dd>
          {o.pull_request ? (
            <>
              <dt>Pull request</dt>
              <dd>
                #{o.pull_request}
                {o.pull_request_title ? ` ${o.pull_request_title}` : ""}
              </dd>
            </>
          ) : null}
          {o.run_url ? (
            <>
              <dt>Build</dt>
              <dd>
                <a href={o.run_url} target="_blank" rel="noreferrer">
                  the CI run
                </a>
              </dd>
            </>
          ) : null}
        </dl>
      ) : null}
      {o?.changes_workflows ? (
        <div className="notice warn">
          <Icon name="alert" />
          <span>
            This pull request changes .github/workflows. Its build ran with those changed workflows,
            so how it was built is part of what you are trusting, not only the app&apos;s code.
          </span>
        </div>
      ) : null}
    </>
  );
}
