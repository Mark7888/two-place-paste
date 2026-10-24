import type { ReactNode } from "react";

/** Notice is a one-line message at the top of a screen or inside a card: a
 *  confirmation (ok), a failure (error) or something to look at (warn). */
export function Notice({ kind, children }: { kind: "ok" | "error" | "warn"; children: ReactNode }) {
  const role = kind === "ok" ? "status" : kind === "error" ? "alert" : undefined;
  return (
    <p className={`notice ${kind}`} role={role}>
      {children}
    </p>
  );
}

/** Problems names each part of a screen that could not be read. The rest of
 *  the screen still renders: one failed Redis call should not take a whole
 *  page down exactly when it is needed. */
export function Problems({ list }: { list: readonly string[] | undefined }) {
  return (
    <>
      {list?.map((p) => (
        <Notice key={p} kind="error">
          {p}
        </Notice>
      ))}
    </>
  );
}

/** ErrorNotice shows an error when there is one. */
export function ErrorNotice({ error }: { error: string | null | undefined }) {
  return error ? <Notice kind="error">{error}</Notice> : null;
}
