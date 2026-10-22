import { useId, type ReactNode } from "react";

/** StatCard is one figure of a summary row, such as "Groups 12". */
export function StatCard({ title, wide, children }: { title: string; wide?: boolean; children: ReactNode }) {
  const id = useId();
  return (
    <section className={wide ? "card stat wide" : "card stat"} aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      {children}
    </section>
  );
}

/** SectionHead titles a part of a screen, with an optional count or action
 *  at its far end. */
export function SectionHead({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="section-head">
      <h2>{title}</h2>
      {children}
    </div>
  );
}

/** Facts is a list of label/value pairs, two columns where there is room. */
export function Facts({ children }: { children: ReactNode }) {
  return <dl className="facts">{children}</dl>;
}

export function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

/** Empty stands in for a list with nothing in it. */
export function Empty({ children }: { children: ReactNode }) {
  return <p className="empty">{children}</p>;
}
