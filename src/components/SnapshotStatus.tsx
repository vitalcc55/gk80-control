/** Neutral between observations; sustained uncertainty is shown without granting permission. */
export function SnapshotStatus({ unconfirmed }: { unconfirmed: boolean }) {
  return (
    <p className="snapshot-state" data-confirmed={!unconfirmed}>
      <span className="snapshot-state-dot" aria-hidden="true" />
      {unconfirmed ? 'Снимок не подтверждён' : 'Состояние данных'}
    </p>
  );
}
