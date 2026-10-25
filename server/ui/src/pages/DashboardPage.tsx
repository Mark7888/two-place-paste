import { api, type Dashboard, type Health, type HealthState, type Storage } from "../api";
import { Badge } from "../components/Badge";
import { Fact, Facts, SectionHead, StatCard } from "../components/Card";
import { ErrorNotice, Notice, Problems } from "../components/Notice";
import { UsageMeter } from "../components/UsageMeter";
import { useAction } from "../hooks/useAction";
import { useDocumentTitle } from "../hooks/useDocumentTitle";
import { useResource } from "../hooks/useResource";
import { ago, duration, humanBytes, limitText, plural, uptime } from "../lib/format";
import { Link, useRouter } from "../router";

/** DashboardPage is what the relay can see without seeing anything: sizes,
 *  counts and the state of its storage (SPEC §4.4). */
export function DashboardPage() {
  useDocumentTitle("Dashboard");
  const { data, error, reload } = useResource((signal) => api.dashboard(signal), []);
  const { notify } = useRouter();
  const sweep = useAction();

  const sweepNow = () =>
    sweep.run(async () => {
      const { queued } = await api.sweep();
      notify(queued ? "Sweep started. Reload in a moment to see its result." : "A sweep is already waiting to run.");
      reload();
    });

  return (
    <>
      <ErrorNotice error={error?.message ?? sweep.error} />
      {data && (
        <>
          <h1>Dashboard</h1>
          <p className="lede">
            What the relay can see without seeing anything: sizes, counts and the state of its storage. Entry
            contents, types and filenames are encrypted and never reach this server.
          </p>

          <Problems list={data.problems} />
          <Summary data={data} />
          <HealthCard health={data.health} />
          {data.storage && (
            <StorageCard storage={data.storage} canSweep={data.can_sweep} busy={sweep.busy} onSweep={sweepNow} />
          )}
        </>
      )}
    </>
  );
}

function Summary({ data }: { data: Dashboard }) {
  const storage = data.storage;
  return (
    <div className="stats">
      {storage && (
        <StatCard title="Blob storage" wide>
          <p className="figure">
            {humanBytes(storage.used_bytes)} <span className="muted">of {limitText(storage.limit_bytes)}</span>
          </p>
          <UsageMeter used={storage.used_bytes} limit={storage.limit_bytes} label="Blob storage used" />
          <p className="sub">
            {storage.entries} stored {plural(storage.entries, "entry", "entries")}
            {storage.uploads && storage.uploads.count > 0 && (
              <>
                {" "}
                · {storage.uploads.count} {plural(storage.uploads.count, "upload", "uploads")} in progress,{" "}
                {humanBytes(storage.uploads.reserved_bytes)} reserved
              </>
            )}
          </p>
        </StatCard>
      )}
      <StatCard title="Groups">
        <p className="figure">{data.group_count}</p>
        <p className="sub">
          <span className="nowrap">
            {data.unused_tokens} unused {plural(data.unused_tokens, "token", "tokens")}
          </span>
          {data.stale_count > 0 && (
            <>
              {" "}
              · <span className="nowrap">{data.stale_count} stale</span>
            </>
          )}
        </p>
        <p className="sub">
          <Link to="/admin/groups">View groups</Link>
        </p>
      </StatCard>
      <StatCard title="Devices">
        <p className="figure">{data.device_count}</p>
        <p className="sub">across all groups</p>
      </StatCard>
      {data.store && (
        <StatCard title="Store" wide>
          <p className="figure">
            {humanBytes(data.store.used_bytes)} <span className="muted">of {limitText(data.store.limit_bytes)}</span>
          </p>
          <UsageMeter used={data.store.used_bytes} limit={data.store.limit_bytes} label="Store used" />
          <p className="sub">
            {data.store.items} {plural(data.store.items, "item", "items")} · on in {data.store.groups_on} of{" "}
            {data.group_count} {plural(data.group_count, "group", "groups")} · {limitText(data.store.group_limit_bytes)}{" "}
            a group by default
          </p>
        </StatCard>
      )}
      {storage && (
        <StatCard title="Evictions, 24 h">
          <p className="figure">{storage.evictions}</p>
          <p className="sub">
            {storage.evictions > 0 ? `${humanBytes(storage.evicted_bytes)} removed early` : "nothing removed early"}
          </p>
        </StatCard>
      )}
    </div>
  );
}

const policyLabels: Record<HealthState, string> = { good: "ok", bad: "unsafe", warn: "check", unknown: "unknown" };
const aofLabels: Record<HealthState, string> = { good: "ok", bad: "off", warn: "unknown", unknown: "unknown" };

/** HealthCard says whether Redis is set up the way SPEC §4.2 needs. A relay
 *  on allkeys-lru or without AOF looks healthy until the day it silently
 *  loses every group, so the card says so up front. */
function HealthCard({ health }: { health: Health }) {
  const redis = health.redis;
  return (
    <>
      <SectionHead title="Health" />
      <div className="card">
        <Facts>
          <Fact label="Version">{health.version}</Fact>
          <Fact label="Running for">{uptime(health.uptime_seconds)}</Fact>
          <Fact label="Go">{health.go_version}</Fact>
          {redis && (
            <>
              <Fact label="Redis eviction policy">
                <code>{redis.policy}</code> <Badge kind={redis.policy_state}>{policyLabels[redis.policy_state]}</Badge>
              </Fact>
              <Fact label="Redis persistence (AOF)">
                <code>{redis.aof}</code> <Badge kind={redis.aof_state}>{aofLabels[redis.aof_state]}</Badge>
              </Fact>
              <Fact label="Redis memory">
                {redis.memory} <span className="muted">of {redis.max_memory}</span>
              </Fact>
            </>
          )}
        </Facts>
        {redis?.policy_note && (
          <Notice kind={redis.policy_state === "bad" ? "error" : "warn"}>{redis.policy_note}</Notice>
        )}
        {redis?.aof_note && <Notice kind={redis.aof_state === "bad" ? "error" : "warn"}>{redis.aof_note}</Notice>}
      </div>
    </>
  );
}

function StorageCard({
  storage,
  canSweep,
  busy,
  onSweep,
}: {
  storage: Storage;
  canSweep: boolean;
  busy: boolean;
  onSweep: () => void;
}) {
  const sweep = storage.last_sweep;
  return (
    <>
      <SectionHead title="Storage">
        {canSweep && (
          <form
            className="inline-form"
            onSubmit={(event) => {
              event.preventDefault();
              onSweep();
            }}
          >
            <button type="submit" className="secondary small" disabled={busy}>
              Sweep now
            </button>
          </form>
        )}
      </SectionHead>
      <div className="card">
        <Facts>
          <Fact label="Backend">{storage.backend}</Fact>
          {storage.location && (
            <Fact label="Location">
              <code className="wrap">{storage.location}</code>
            </Fact>
          )}
          <Fact label="Total limit">{limitText(storage.limit_bytes)}</Fact>
          <Fact label="Per-group limit">{limitText(storage.group_limit_bytes)}</Fact>
          <Fact label="Largest entry">{humanBytes(storage.entry_max_bytes)}</Fact>
          <Fact label="Kept in Redis up to">{humanBytes(storage.inline_max_bytes)}</Fact>
          <Fact label="Last sweep">
            {sweep ? (
              <>
                {ago(Date.now(), sweep.at, "")}, took {duration(sweep.took_ms)}{" "}
                {sweep.error ? <Badge kind="bad">failed</Badge> : <Badge kind="good">ok</Badge>}
              </>
            ) : (
              <span className="muted">not yet</span>
            )}
          </Fact>
        </Facts>
        {sweep?.error && (
          <Notice kind="error">
            <code className="wrap">{sweep.error}</code>
          </Notice>
        )}
      </div>
    </>
  );
}
