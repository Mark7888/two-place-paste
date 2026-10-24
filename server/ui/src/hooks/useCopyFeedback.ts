import { useCallback, useEffect, useRef, useState } from "react";
import { copyText } from "../lib/copy";

export interface CopyFeedback {
  /** message is "Copied" or "Select and copy" for a moment after a copy,
   *  and null otherwise. */
  message: string | null;
  copy: (text: string) => Promise<void>;
  reset: () => void;
}

/** useCopyFeedback copies a string and says how that went, for a moment. A
 *  browser that refuses both clipboard paths still leaves the value
 *  selectable, so the failure message says to do it by hand. */
export function useCopyFeedback(): CopyFeedback {
  const [message, setMessage] = useState<string | null>(null);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const say = useCallback((text: string, hold: number) => {
    setMessage(text);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setMessage(null), hold);
  }, []);

  const copy = useCallback(
    async (text: string) => {
      try {
        await copyText(text);
        say("Copied", 1600);
      } catch {
        say("Select and copy", 2400);
      }
    },
    [say],
  );

  const reset = useCallback(() => {
    window.clearTimeout(timer.current);
    setMessage(null);
  }, []);

  return { message, copy, reset };
}
