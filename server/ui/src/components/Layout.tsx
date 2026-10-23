import { useEffect, useState, type ReactNode } from "react";
import { Link, useRouter } from "../router";
import { useInstallPrompt } from "../hooks/useInstallPrompt";
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
  const { notice, location } = useRouter();
  const [menuOpen, setMenuOpen] = useState(false);

  // The menu closes when it has done its job — a screen was chosen — and on
  // Escape, like any other popup.
  useEffect(() => setMenuOpen(false), [location.key]);
  useEffect(() => {
    if (!menuOpen) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setMenuOpen(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [menuOpen]);

  return (
    <>
      <header className={menuOpen ? "bar open" : "bar"}>
        <span className="brand">
          TwoPlacePaste <span className="muted">admin</span>
        </span>
        {authenticated && (
          <>
            {/* Shown on phones only (styles.css): there the screens fold
                behind it, and the bar stays one line. */}
            <button
              type="button"
              className="menu-button"
              aria-expanded={menuOpen}
              aria-controls="admin-menu"
              onClick={() => setMenuOpen((o) => !o)}
            >
              <span className="menu-icon" aria-hidden="true" />
              Menu
            </button>
            <div className="menu" id="admin-menu">
              <Nav />
              <InstallApp />
              <LogOut />
            </div>
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

/** InstallApp offers to install the admin as an app, when the browser has
 *  made that offer (useInstallPrompt). */
function InstallApp() {
  const { install } = useInstallPrompt();
  if (!install) return null;
  return (
    <button type="button" className="link install" onClick={install}>
      Install app
    </button>
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
