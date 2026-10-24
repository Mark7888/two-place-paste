import { useEffect, useRef, useState } from "react";
import { SEARCH_DELAY } from "./state";

/** SearchBox is a table's search field. Typing searches once it rests for a
 *  moment; Enter searches at once; Escape or the ✕ clears it. */
export function SearchBox({
  value,
  label,
  placeholder,
  onSearch,
}: {
  /** value is the search the table is showing. */
  value: string;
  label: string;
  placeholder?: string;
  onSearch: (text: string) => void;
}) {
  const [text, setText] = useState(value);
  const timer = useRef<number | undefined>(undefined);
  const input = useRef<HTMLInputElement>(null);

  // A search that changes from outside — back and forward, a link — shows
  // here, unless it is only what is being typed, trailing space and all.
  useEffect(() => {
    setText((t) => (t.trim() === value.trim() ? t : value));
  }, [value]);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const run = (t: string) => {
    window.clearTimeout(timer.current);
    onSearch(t);
  };

  const clear = () => {
    setText("");
    run("");
    input.current?.focus();
  };

  return (
    <div className="table-search" role="search">
      <input
        ref={input}
        type="search"
        aria-label={label}
        placeholder={placeholder}
        value={text}
        onChange={(e) => {
          const t = e.target.value;
          setText(t);
          window.clearTimeout(timer.current);
          timer.current = window.setTimeout(() => onSearch(t), SEARCH_DELAY);
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            run(text);
          } else if (e.key === "Escape" && text !== "") {
            e.preventDefault();
            clear();
          }
        }}
      />
      {text !== "" && (
        <button type="button" className="clear" aria-label="Clear search" onClick={clear}>
          ✕
        </button>
      )}
    </div>
  );
}
