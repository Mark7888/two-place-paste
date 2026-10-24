import { useEffect } from "react";

/** useDocumentTitle names the browser tab after the screen. */
export function useDocumentTitle(title: string) {
  useEffect(() => {
    document.title = `${title} — TwoPlacePaste`;
  }, [title]);
}
