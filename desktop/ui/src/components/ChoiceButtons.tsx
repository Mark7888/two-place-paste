/**
 * A row of buttons where exactly one is the answer, such as On and Off.
 *
 * It is used where a choice has to be made on purpose: the first-run setup
 * asks each question with it, and a value of null means nothing is picked yet,
 * which a checkbox cannot express. It is a radio group to assistive tech, so
 * the arrow keys move between the options and only the picked one is a tab
 * stop, as with native radio buttons.
 */

import { useRef, type KeyboardEvent } from "react";

export interface Choice<T> {
  value: T;
  label: string;
  /** recommended adds a "Recommended" tag under the label. */
  recommended?: boolean;
}

export function ChoiceButtons<T extends string | number | boolean>({
  label,
  options,
  value,
  onChange,
  disabled = false,
}: {
  /** label names the group for assistive tech; the question is shown elsewhere. */
  label: string;
  options: readonly Choice<T>[];
  /** value is the picked option, or null while none is. */
  value: T | null;
  onChange: (value: T) => void;
  disabled?: boolean;
}) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const picked = options.findIndex((o) => o.value === value);

  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const step =
      event.key === "ArrowRight" || event.key === "ArrowDown"
        ? 1
        : event.key === "ArrowLeft" || event.key === "ArrowUp"
          ? -1
          : 0;
    if (step === 0) return;
    event.preventDefault();
    const next = (index + step + options.length) % options.length;
    onChange(options[next].value);
    refs.current[next]?.focus();
  };

  return (
    <div className="choices" role="radiogroup" aria-label={label}>
      {options.map((o, i) => (
        <button
          key={String(o.value)}
          ref={(el) => {
            refs.current[i] = el;
          }}
          type="button"
          role="radio"
          className="choice"
          aria-checked={i === picked}
          // With nothing picked yet the first option takes Tab, as an
          // unanswered radio group does.
          tabIndex={i === (picked === -1 ? 0 : picked) ? 0 : -1}
          disabled={disabled}
          onClick={() => onChange(o.value)}
          onKeyDown={(e) => onKeyDown(e, i)}
        >
          <span className="choice-label">{o.label}</span>
          {o.recommended ? <span className="choice-tag">Recommended</span> : null}
        </button>
      ))}
    </div>
  );
}

/** onOff is the On / Off pair the setup asks most questions with. */
export function onOff(recommended?: boolean): readonly Choice<boolean>[] {
  return [
    { value: true, label: "On", recommended: recommended === true },
    { value: false, label: "Off", recommended: recommended === false },
  ];
}
