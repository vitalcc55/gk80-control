import { Pause } from 'lucide-react';
import { useId } from 'react';
export function StopButton({
  onStop,
  disabled,
  label = 'Остановить испытание',
  disabledReason,
}: {
  onStop: () => void;
  disabled: boolean;
  label?: string;
  disabledReason?: string | undefined;
}) {
  const reasonId = useId();
  return (
    <>
      <button
        className="stop-button full-width"
        type="button"
        disabled={disabled}
        aria-describedby={disabled && disabledReason ? reasonId : undefined}
        onClick={onStop}
      >
        <Pause size={16} aria-hidden="true" />
        {label}
      </button>
      {disabled && disabledReason && (
        <p className="stop-help" id={reasonId}>
          {disabledReason}
        </p>
      )}
    </>
  );
}
