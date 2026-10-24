import { forwardRef, useImperativeHandle, useRef, useState, type InputHTMLAttributes } from "react";

type Props = Omit<InputHTMLAttributes<HTMLInputElement>, "type">;

/** PasswordInput is a password field with a Show/Hide toggle inside its right
 *  edge. */
export const PasswordInput = forwardRef<HTMLInputElement, Props>(function PasswordInput(props, ref) {
  const [shown, setShown] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  useImperativeHandle(ref, () => input.current as HTMLInputElement);

  const toggle = () => {
    setShown((s) => !s);
    // Typing should carry on where it left off, not at the start.
    const el = input.current;
    if (!el) return;
    el.focus();
    requestAnimationFrame(() => {
      const end = el.value.length;
      try {
        el.setSelectionRange(end, end);
      } catch {
        // A field that does not support selection is not worth failing over.
      }
    });
  };

  return (
    <div className="with-affix">
      <input ref={input} type={shown ? "text" : "password"} {...props} />
      <button
        type="button"
        className="affix"
        aria-pressed={shown}
        aria-label={shown ? "Hide the password" : "Show the password"}
        onClick={toggle}
      >
        {shown ? "Hide" : "Show"}
      </button>
    </div>
  );
});
