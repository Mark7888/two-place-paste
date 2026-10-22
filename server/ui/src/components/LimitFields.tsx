import { useState } from "react";
import type { Limit, LimitChange } from "../api";
import { sizeInput } from "../lib/format";

/** useLimitForm holds a storage limit form: the checkbox and the size field,
 *  starting from the stored override. Unticking keeps the size, so the value
 *  is there again when the box is ticked. */
export function useLimitForm(stored: Limit) {
  const [enabled, setEnabled] = useState(stored.enabled);
  const [size, setSize] = useState(() => sizeInput(stored.bytes));
  const change: LimitChange = { enabled, size };
  return { enabled, setEnabled, size, setSize, change };
}
