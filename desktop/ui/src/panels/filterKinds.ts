// The filter kinds in the words the filter editor and its dialogs use.

import type { FilterKind, FilterTarget } from "../api";

export const KINDS: { kind: FilterKind; label: string }[] = [
  { kind: "contains", label: "Contains" },
  { kind: "prefix", label: "Starts with" },
  { kind: "suffix", label: "Ends with" },
  { kind: "regex", label: "Regex" },
  { kind: "exact", label: "Exact match" },
];

/** kindLabel names a kind; one from a newer version is shown as it is. */
export function kindLabel(kind: string): string {
  return KINDS.find((k) => k.kind === kind)?.label ?? kind;
}

/** targetLabel names what a filter looks at. */
export function targetLabel(target: FilterTarget | string): string {
  return target === "filename" ? "file name" : "text";
}
