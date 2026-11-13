import { Icon } from "./Icon";

/**
 * OutdatedNotice says the relay no longer serves this version of the app
 * (SPEC §5.3). It is not a revocation: the group and its keys are untouched,
 * and once the app is updated it connects as before. It stays until then,
 * because nothing syncs meanwhile.
 */
export function OutdatedNotice({ onCheck }: { onCheck: () => void }) {
  return (
    <div className="notice warn" role="status">
      <Icon name="alert" />
      <span className="grow">
        The relay needs a newer version of TwoPlacePaste. Update this app to keep syncing; your group
        and its devices are unaffected. If no update is offered, your update channel has no version
        for this relay yet: try the Beta channel, or ask your relay operator.
      </span>
      <button type="button" className="action" onClick={onCheck}>
        <Icon name="refresh" size={15} />
        Check for updates
      </button>
    </div>
  );
}
