import { Pause } from 'lucide-react';
export function StopButton({
  onStop,
  disabled,
  label = 'Остановить испытание',
}: {
  onStop: () => void;
  disabled: boolean;
  label?: string;
}) {
  return (
    <button className="stop-button full-width" type="button" disabled={disabled} onClick={onStop}>
      <Pause size={16} aria-hidden="true" />
      {label}
    </button>
  );
}
