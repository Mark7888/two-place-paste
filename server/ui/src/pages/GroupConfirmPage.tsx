import { useEffect } from "react";
import { ApiError, api } from "../api";
import { GroupBadges } from "../components/Badge";
import { ErrorNotice, Problems } from "../components/Notice";
import { Cell, Table } from "../components/Table";
import { useAction } from "../hooks/useAction";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useResource } from "../hooks/useResource";
import { ago, humanBytes, plural } from "../lib/format";
import { groupPath } from "../lib/paths";
import { Link, useRouter } from "../router";

export type ConfirmKind = "purge" | "delete";

const copy = {
  purge: {
    heading: "Purge entries",
    lede:
      "Every entry this group holds is deleted now, before its 24 hours are up. " +
      "Its devices keep working and their own clipboards are not touched; they only lose the shared history.",
    button: "Purge entries",
  },
  delete: {
    heading: "Delete group",
    lede:
      "The group, its devices and every entry it holds are deleted. " +
      "Each device is disconnected and told its group was deleted; to use this server again it needs a new creation token. " +
      "This cannot be undone.",
    button: "Delete group",
  },
} as const;

/** GroupConfirmPage asks before an action that cannot be undone, naming the
 *  group it touches and what it frees (docs/internal/plans/
 *  admin-group-management.md D14). */
export function GroupConfirmPage({ id, kind }: { id: string; kind: ConfirmKind }) {
  const text = copy[kind];
  useDocumentTitle(text.heading);
  const { data, error } = useResource((signal) => api.group(id, signal), [id]);
  const { navigate } = useRouter();
  const action = useAction();

  // Gone already, or never was: nothing to confirm.
  useEffect(() => {
    if (error && !(error instanceof ApiError && error.status === 401)) {
      navigate("/admin/groups", { replace: true, notice: "That group no longer exists." });
    }
  }, [error, navigate]);

  const confirm = () =>
    action.run(async () => {
      if (kind === "purge") {
        const res = await api.purgeGroup(id);
        navigate(groupPath(id), {
          notice: `Purged ${res.entries} ${plural(res.entries, "entry", "entries")}, ${humanBytes(res.bytes)} freed.`,
        });
        return;
      }
      try {
        const res = await api.deleteGroup(id);
        navigate("/admin/groups", { notice: `Deleted the group “${res.name}”.` });
      } catch (err) {
        // Deleted meanwhile, by another tab or another admin: say so where
        // the group would have been listed.
        if (err instanceof ApiError && err.status === 404) {
          navigate("/admin/groups", { notice: err.message });
          return;
        }
        throw err;
      }
    });

  if (!data) return null;
  const g = data.group;
  const used = g.usage?.used_bytes ?? 0;
  return (
    <>
      <ErrorNotice error={action.error} />
      <Problems list={data.problems} />
      <h1>{text.heading}</h1>
      <p className="lede">{text.lede}</p>

      <Table
        columns={[{ label: "Group" }, { label: "Devices" }, { label: "Entries" }, { label: "Blob storage" }, { label: "Last active" }]}
      >
        <tr>
          <Cell label="Group" className="name">
            <span className="stack">
              <span>
                {g.name}
                <GroupBadges group={{ ...g, stale: false, usage: null }} />
              </span>
              <code className="muted">{g.short_id}</code>
            </span>
          </Cell>
          <Cell label="Devices">{g.devices}</Cell>
          <Cell label="Entries">{g.entries}</Cell>
          <Cell label="Blob storage">{g.usage ? humanBytes(g.usage.used_bytes) : ""}</Cell>
          <Cell label="Last active">{ago(Date.now(), g.last_seen, "never")}</Cell>
        </tr>
      </Table>
      <p className="hint spaced">{`${humanBytes(used)} of blob storage freed.`}</p>

      <form
        className="form-row"
        onSubmit={(event) => {
          event.preventDefault();
          void confirm();
        }}
      >
        <button type="submit" className="danger" disabled={action.busy}>
          {text.button}
        </button>
        <Link className="button secondary" to={groupPath(id)}>
          Cancel
        </Link>
      </form>
    </>
  );
}
