/** Neutral between observations; sustained uncertainty is shown without granting permission. */
export function SnapshotStatus({ unconfirmed }: { unconfirmed: boolean }) {
  return (
    <p className="snapshot-state" data-confirmed={!unconfirmed}>
      <span className="snapshot-state-dot" aria-hidden="true" />
      {unconfirmed ? 'Состояние не подтверждено' : 'Состояние данных'}
    </p>
  );
}
