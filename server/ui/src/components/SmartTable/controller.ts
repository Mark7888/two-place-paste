import { useRef } from "react";
import type { TableController } from "./types";

/** AttachableController is a TableController a SmartTable can plug into. */
export interface AttachableController extends TableController {
  attach: (refresh: () => void) => () => void;
}

/** useTableController makes a handle for reloading a SmartTable after an
 *  action — a delete, a rename — without losing its search, sort or page:
 *
 *    const table = useTableController();
 *    <SmartTable controller={table} … />
 *    table.refresh();
 */
export function useTableController(): AttachableController {
  const ref = useRef<AttachableController | null>(null);
  if (!ref.current) {
    let current: (() => void) | null = null;
    ref.current = {
      refresh: () => current?.(),
      attach: (refresh) => {
        current = refresh;
        return () => {
          if (current === refresh) current = null;
        };
      },
    };
  }
  return ref.current;
}
