import type { ReactNode } from "react";
import { Link, useRouter } from "../router";
import { useSession } from "../session";
import { Notice } from "./Notice";

const screens = [
  { to: "/admin/", label: "Dashboard", current: (p: string) => p === "/admin/" },
  { to: "/admin/groups", label: "Groups", current: (p: string) => p === "/admin/groups" || p.startsWith("/admin/groups/") },
  { to: "/admin/tags", label: "Tags", current: (p: string) => p === "/admin/tags" },
  { to: "/admin/tokens", label: "Tokens", current: (p: string) => p === "/admin/tokens" },
  { to: "/admin/audit", label: "Audit log", current: (p: string) => p === "/admin/audit" },
  { to: "/admin/settings", label: "Settings", current: (p: string) => p === "/admin/settings" },
];

/** Layout is the app shell: the header bar with the screen links, one
 *  column of content, and the footer with the source link the AGPL asks for
 *  (LICENSING.md). The notice of the last change sits at the top of the
 *  column. */
export function Layout({
  authenticated,
  centered,
  children,
}: {
  authenticated: boolean;
  /** centered puts the page in the middle of the viewport: the sign-in
   *  screen, which is one short form and nothing else. */
  centered?: boolean;
  children?: ReactNode;
}) {
  const { notice } = useRouter();
  return (
    <>
      <header className="bar">
        <span className="brand">
          TwoPlacePaste <span className="muted">admin</span>
        </span>
        {authenticated && (
          <>
            <Nav />
            <LogOut />
          </>
        )}
      </header>
      <main className={centered ? "centered" : undefined}>
        {notice && <Notice kind="ok">{notice}</Notice>}
        {children}
      </main>
      <footer>
        {authenticated && (
          <p>
            The server relays ciphertext only. These screens show names, sizes and counts — there is no content
            access here by construction (SPEC §4.4).
          </p>
        )}
        <p>
          <a href="/source">Source code</a> — this relay is free software under the AGPL-3.0.
        </p>
      </footer>
    </>
  );
}

function Nav() {
  const { location } = useRouter();
  return (
    <nav className="nav" aria-label="Admin">
      {screens.map((s) => (
        <Link key={s.to} to={s.to} aria-current={s.current(location.pathname) ? "page" : undefined}>
          {s.label}
        </Link>
      ))}
    </nav>
  );
}

function LogOut() {
  const { signOut } = useSession();
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        // Signed out either way: a failed request still drops the session
        // here, and the relay's own copy expires on its own.
        signOut().catch(() => undefined);
      }}
    >
      <button type="submit" className="link">
        Log out
      </button>
    </form>
  );
}
