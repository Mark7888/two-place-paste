import { useEffect, useState } from "react";
import {
  ApiError,
  api,
  protectedEntry,
  type Direction,
  type ProtectedEntry,
  type Status,
  type SyncResult,
} from "../api";
import { bytes, relative, useNow, when } from "../format";
import { Icon } from "../components/Icon";
import { Modal } from "../components/Modal";

export function SyncPanel({ status, onChanged }: { status: Status | null; onChanged: () => void }) {
  const [busy, setBusy] = useState<Direction | null>(null);
  const [result, setResult] = useState<SyncResult | null>(null);
  const [error, setError] = useState("");
  const [asking, setAsking] = useState(false);
  // A manual upload the service held back as a protected entry, waiting for
  // "push anyway".
  const [guarded, setGuarded] = useState<ProtectedEntry | null>(null);
  // The masked clipboard's text, fetched only when the eye is pressed.
  const [revealed, setRevealed] = useState<string | null>(null);
  const [revealing, setRevealing] = useState(false);

  // "changed 2 min ago" counts up on its own; without this it is only as fresh
  // as the last push from the service.
  useNow();

  const run = async (d: Direction, planId?: string) => {
    setBusy(d);
    setError("");
    try {
      const res = await api.sync(d, planId);
      setResult(res);
      setAsking(false);
      setGuarded(null);
      onChanged();
    } catch (err) {
      // A protected entry is a question too: push it anyway, or not. A
      // confirmation that finds the clipboard changed comes back as a new
      // question about what is there now.
      const guard = protectedEntry(err);
      if (guard) {
        setResult(null);
        setAsking(false);
        setGuarded(guard);
        return;
      }
      // Any other 409 is the service saying it cannot tell which side is
      // newer. That is a question, not a failure: ask it instead of
      // reporting it.
      if (err instanceof ApiError && err.status === 409) {
        setResult(null);
        setAsking(true);
      } else {
        setResult(null);
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setBusy(null);
    }
  };

  // What was revealed belongs to the clipboard it was revealed for: a new
  // copy hides it again.
  const clipKey = status
    ? `${status.clipboard.changed_at ?? ""}|${status.clipboard.bytes}|${status.clipboard.sensitive ? 1 : 0}`
    : "";
  useEffect(() => setRevealed(null), [clipKey]);

  const reveal = async () => {
    if (revealed !== null) {
      setRevealed(null);
      return;
    }
    setRevealing(true);
    try {
      const res = await api.revealClipboard();
      setRevealed(res.preview);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRevealing(false);
    }
  };

  if (!status) {
    return (
      <div className="row">
        <span className="spinner" />
        <span className="muted">Reading the service…</span>
      </div>
    );
  }

  if (!status.in_group) {
    return (
      <>
        <h1>Sync</h1>
        <p className="lede">This device is not in a group yet.</p>
        <div className="empty">
          Open <strong>Pairing</strong> to join a group with a code from another device, or to
          create one with a link from the relay&apos;s admin page.
        </div>
      </>
    );
  }

  const clip = status.clipboard;
  const known = status.direction_known;

  return (
    <>
      <h1>Sync</h1>
      <p className="lede">
        {known ? (
          <>
            This clipboard changed {relative(clip.changed_at)}; the group&apos;s newest entry is from{" "}
            {relative(status.latest?.created_at)}. Syncing now would{" "}
            <strong>{status.suggested_direction}</strong>.
          </>
        ) : (
          <>
            Neither Windows nor macOS records when clipboard content arrived, and this service has
            not seen this clipboard change since it started. Syncing will ask you which way to go.
          </>
        )}
      </p>

      <section className="group">
        <div className="row">
          <button
            className="action primary"
            disabled={busy !== null}
            onClick={() => void (known ? run("auto") : setAsking(true))}
          >
            {busy === "auto" ? <span className="spinner" /> : <Icon name="sync" size={16} />}
            {busy === "auto" ? "Syncing…" : "Sync now"}
          </button>
          <button className="action" disabled={busy !== null} onClick={() => void run("upload")}>
            <Icon name="upload" size={16} />
            {busy === "upload" ? "Uploading…" : "Upload"}
          </button>
          <button className="action" disabled={busy !== null} onClick={() => void run("download")}>
            <Icon name="download" size={16} />
            {busy === "download" ? "Downloading…" : "Download"}
          </button>
        </div>

        {error ? (
          <div className="notice error" style={{ marginTop: "0.9rem" }}>
            <Icon name="alert" />
            <span>{error}</span>
          </div>
        ) : null}
        {result ? (
          <div
            className={`notice ${result.changed ? "ok" : "info"}`}
            style={{ marginTop: "0.9rem" }}
            role="status"
          >
            <Icon name={result.changed ? "check" : "info"} />
            <span>{result.message}</span>
          </div>
        ) : null}
      </section>

      <section className="group">
        <h2>This clipboard</h2>
        <div className="card">
          {!clip.available ? (
            <p className="muted">This build cannot reach a clipboard.</p>
          ) : clip.empty ? (
            <p className="muted">Empty.</p>
          ) : (
            <>
              <div className="spread">
                <span className="row tight">
                  <Icon
                    name={clip.kind === "image" ? "image" : clip.kind === "file" ? "file" : "text"}
                    size={16}
                  />
                  <strong>{clip.kind}</strong>
                  {clip.filename ? <span className="muted">{clip.filename}</span> : null}
                  <span className="muted">{bytes(clip.bytes)}</span>
                </span>
                <span className="muted small" title={when(clip.changed_at)}>
                  changed {relative(clip.changed_at)}
                </span>
              </div>
              {clip.sensitive ? (
                <div className="masked">
                  <span className="row tight">
                    <Icon name="lock" size={15} />
                    <span>
                      <strong>Sensitive {clip.kind === "text" ? "text" : clip.kind}</strong>
                      {clip.kind === "text" && clip.chars ? (
                        <span className="muted"> · {clip.chars} characters</span>
                      ) : null}
                      {clip.sensitive_reason ? (
                        <span className="muted"> · {clip.sensitive_reason}</span>
                      ) : null}
                    </span>
                  </span>
                  {clip.kind === "text" ? (
                    <button
                      type="button"
                      className="icon-button"
                      aria-label={revealed !== null ? "Hide" : "Show"}
                      title={revealed !== null ? "Hide" : "Show"}
                      disabled={revealing}
                      onClick={() => void reveal()}
                    >
                      <Icon name={revealed !== null ? "eye-off" : "eye"} size={16} />
                    </button>
                  ) : null}
                </div>
              ) : null}
              {clip.sensitive && revealed ? <pre className="preview mono">{revealed}</pre> : null}
              {!clip.sensitive && clip.preview ? (
                <pre className="preview mono">{clip.preview}</pre>
              ) : null}
            </>
          )}
          {status.sensitive_note ? (
            <p className="muted small row tight" style={{ marginTop: "0.6rem" }}>
              <Icon name="lock" size={13} />
              <span>
                Not uploaded automatically: {status.sensitive_note.reason}. Use Upload to push it
                anyway.
              </span>
            </p>
          ) : null}
          {clip.error ? (
            <div className="notice error" style={{ marginTop: "0.6rem" }}>
              <Icon name="alert" />
              <span>{clip.error}</span>
            </div>
          ) : null}
        </div>
      </section>

      <section className="group">
        <h2>Group</h2>
        <div className="card">
          <dl className="facts">
            <dt>Relay</dt>
            <dd className="mono">{status.server_url}</dd>
            <dt>Key generation</dt>
            <dd>epoch {status.epoch}</dd>
            <dt>Keys stored in</dt>
            <dd>{status.keystore}</dd>
            <dt>Latest entry</dt>
            <dd>
              {status.latest
                ? `${bytes(status.latest.size)} · ${when(status.latest.created_at)}`
                : "none yet"}
            </dd>
            <dt>Last sync</dt>
            <dd title={when(status.last_sync)}>{relative(status.last_sync)}</dd>
          </dl>
          {status.last_error ? (
            <div className="notice error" style={{ marginTop: "0.9rem" }}>
              <Icon name="alert" />
              <span>{status.last_error}</span>
            </div>
          ) : null}
        </div>
      </section>

      {/*
        "Push anyway?" Cancel is first and takes the focus: the safe answer is
        the one a stray Enter gives.
      */}
      <Modal
        open={guarded !== null}
        title="This is a protected entry"
        onClose={() => setGuarded(null)}
        footer={
          <>
            <button className="action" autoFocus onClick={() => setGuarded(null)}>
              Cancel
            </button>
            <button
              className="action primary"
              disabled={busy !== null}
              onClick={() => guarded && void run("upload", guarded.planId)}
            >
              {busy === "upload" ? <span className="spinner" /> : <Icon name="upload" size={16} />}
              Push anyway
            </button>
          </>
        }
      >
        <p>
          {guarded?.reason ? <>{guarded.reason}. </> : null}
          Push it to your other devices anyway?
        </p>
        <p className="muted small">
          It is encrypted like every entry, and stays in the group&apos;s history for 24 hours.
        </p>
      </Modal>

      {/*
        The direction chooser. It replaces the old behaviour — a disabled "Sync
        now" and a paragraph explaining why — because a disabled button with an
        explanation is a question the user has to answer anyway, asked in the
        one form they cannot answer.
      */}
      <Modal
        open={asking}
        title="Which way should this sync go?"
        onClose={() => setAsking(false)}
        footer={
          <button className="action" onClick={() => setAsking(false)}>
            Cancel
          </button>
        }
      >
        <p className="muted">
          This service cannot tell which side is newer: Windows and macOS do not record when
          clipboard content arrived, and it has not seen this clipboard change since it started.
          Whichever you pick overwrites the other side, so it is yours to choose.
        </p>
        {/*
          The icon is a sibling of the text rather than part of the title, so
          it forms its own column and the two lines of text line up with each
          other instead of with the glyph.
        */}
        <div className="choice-list">
          <button
            className="action choice"
            disabled={busy !== null}
            onClick={() => void run("upload")}
          >
            <Icon name="upload" size={18} />
            <span className="choice-body">
              <span className="choice-title">Sync up</span>
              <span className="choice-why">
                Send this machine&apos;s clipboard to the group. It becomes the latest entry.
              </span>
            </span>
          </button>
          <button
            className="action choice"
            disabled={busy !== null}
            onClick={() => void run("download")}
          >
            <Icon name="download" size={18} />
            <span className="choice-body">
              <span className="choice-title">Sync down</span>
              <span className="choice-why">
                Copy the group&apos;s latest entry here, replacing this machine&apos;s clipboard.
              </span>
            </span>
          </button>
        </div>
        {status.latest ? (
          <p className="muted small" style={{ marginTop: "0.9rem" }}>
            The group&apos;s newest entry is {bytes(status.latest.size)} from{" "}
            {when(status.latest.created_at)}.
          </p>
        ) : (
          <p className="muted small" style={{ marginTop: "0.9rem" }}>
            The group has no entry yet, so there is nothing to copy down.
          </p>
        )}
      </Modal>
    </>
  );
}
