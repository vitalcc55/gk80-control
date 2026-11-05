/** Presentation of the observer's fact; it never grants command permission. */
export function SnapshotStatus({ confirmed }: { confirmed: boolean }) {
  return (
    <p className="snapshot-state" data-confirmed={confirmed}>
      <span className="snapshot-state-dot" aria-hidden="true" />
      {confirmed ? 'Снимок подтверждён' : 'Снимок не подтверждён'}
    </p>
  );
}
