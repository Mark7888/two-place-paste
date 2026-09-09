import { useState } from "react";
import { api, type Direction, type Status, type SyncResult } from "../api";
import { bytes, relative, when } from "../format";

export function SyncPanel({ status, onChanged }: { status: Status | null; onChanged: () => void }) {
  const [busy, setBusy] = useState<Direction | null>(null);
  const [result, setResult] = useState<SyncResult | null>(null);
  const [error, setError] = useState("");

  const run = async (d: Direction) => {
    setBusy(d);
    setError("");
    try {
      setResult(await api.sync(d));
      onChanged();
    } catch (err) {
      setResult(null);
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };

  if (!status) return <div className="panel">Loading…</div>;
  if (!status.in_group) {
    return (
      <div className="panel">
        <h2>Not in a group yet</h2>
        <p className="muted">
          Open the Pairing tab to join a group with a payload from another device, or to create one
          with a link from the relay&apos;s admin page.
        </p>
      </div>
    );
  }

  const clip = status.clipboard;

  return (
    <>
      <div className="panel">
        <h2>Sync</h2>
        {status.direction_known ? (
          <p className="muted">
            This service last saw the clipboard change {relative(clip.changed_at)}; the group&apos;s
            newest entry is from {relative(status.latest?.created_at)}. Sync now would{" "}
            <strong>{status.suggested_direction}</strong>.
          </p>
        ) : (
          <p className="muted">
            Neither Windows nor macOS records when clipboard content arrived, and this service has not
            seen the clipboard change since it started — so it will not guess a direction. Choose one.
          </p>
        )}
        <div className="row">
          <button
            className="action primary"
            disabled={busy !== null || !status.direction_known}
            onClick={() => void run("auto")}
          >
            {busy === "auto" ? "Syncing…" : "Sync now"}
          </button>
          <button className="action" disabled={busy !== null} onClick={() => void run("upload")}>
            {busy === "upload" ? "Uploading…" : "Upload this clipboard"}
          </button>
          <button className="action" disabled={busy !== null} onClick={() => void run("download")}>
            {busy === "download" ? "Downloading…" : "Copy the latest entry here"}
          </button>
        </div>
        {error ? <div className="notice error" style={{ marginTop: "0.75rem" }}>{error}</div> : null}
        {result ? (
          <div className="notice info" style={{ marginTop: "0.75rem" }}>
            {result.message}
          </div>
        ) : null}
      </div>

      <div className="panel">
        <h2>This clipboard</h2>
        {!clip.available ? (
          <p className="muted">This build cannot reach a clipboard.</p>
        ) : clip.empty ? (
          <p className="muted">Empty.</p>
        ) : (
          <>
            <div className="spread">
              <span>
                {clip.kind}
                {clip.filename ? ` · ${clip.filename}` : ""} · {bytes(clip.bytes)}
              </span>
              <span className="muted">changed {relative(clip.changed_at)}</span>
            </div>
            {clip.preview ? <pre className="preview mono">{clip.preview}</pre> : null}
          </>
        )}
        {clip.error ? <div className="notice error">{clip.error}</div> : null}
      </div>

      <div className="panel">
        <h2>Group</h2>
        <table>
          <tbody>
            <tr>
              <th>Relay</th>
              <td className="mono">{status.server_url}</td>
            </tr>
            <tr>
              <th>Key generation</th>
              <td>epoch {status.epoch}</td>
            </tr>
            <tr>
              <th>Keys stored in</th>
              <td>{status.keystore}</td>
            </tr>
            <tr>
              <th>Latest entry</th>
              <td>
                {status.latest
                  ? `${bytes(status.latest.size)} · ${when(status.latest.created_at)}`
                  : "none yet"}
              </td>
            </tr>
            <tr>
              <th>Last sync</th>
              <td>{relative(status.last_sync)}</td>
            </tr>
          </tbody>
        </table>
        {status.last_error ? <div className="notice error">{status.last_error}</div> : null}
      </div>
    </>
  );
}
