import { useEffect, type ReactNode } from "react";
import { Layout } from "./components/Layout";
import { Notice } from "./components/Notice";
import { AuditPage } from "./pages/AuditPage";
import { DashboardPage } from "./pages/DashboardPage";
import { GroupConfirmPage } from "./pages/GroupConfirmPage";
import { GroupPage } from "./pages/GroupPage";
import { GroupsPage } from "./pages/GroupsPage";
import { LoginPage } from "./pages/LoginPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { SettingsPage } from "./pages/SettingsPage";
import { TagsPage } from "./pages/TagsPage";
import { TokensPage } from "./pages/TokensPage";
import { RouterProvider, matchPath, useRouter } from "./router";
import { SessionProvider, useSession } from "./session";

const LOGIN = "/admin/login";

/** The screens behind the sign-in, by path. The relay serves the app for the
 *  same paths and no others (server/internal/admin/register.go). */
const routes: { path: string; render: (params: Record<string, string>) => ReactNode }[] = [
  { path: "/admin/", render: () => <DashboardPage /> },
  { path: "/admin/groups", render: () => <GroupsPage /> },
  { path: "/admin/groups/:id", render: (p) => <GroupPage id={p.id} /> },
  { path: "/admin/groups/:id/purge", render: (p) => <GroupConfirmPage id={p.id} kind="purge" /> },
  { path: "/admin/groups/:id/delete", render: (p) => <GroupConfirmPage id={p.id} kind="delete" /> },
  { path: "/admin/tags", render: () => <TagsPage /> },
  { path: "/admin/tokens", render: () => <TokensPage /> },
  { path: "/admin/audit", render: () => <AuditPage /> },
  { path: "/admin/settings", render: () => <SettingsPage /> },
];

export function App() {
  return (
    <RouterProvider>
      <SessionProvider>
        <Screens />
      </SessionProvider>
    </RouterProvider>
  );
}

function Screens() {
  const { location, navigate } = useRouter();
  const session = useSession();
  const onLogin = location.pathname === LOGIN;

  // The session decides where the admin may be: signed out, on the sign-in
  // screen; signed in, anywhere but there.
  useEffect(() => {
    if (session.status === "signed-out" && !onLogin) navigate(LOGIN, { replace: true });
    if (session.status === "signed-in" && onLogin) navigate("/admin/", { replace: true });
  }, [session.status, onLogin, navigate]);

  if (session.status === "error") {
    return (
      <Layout authenticated={false} centered>
        <Notice kind="error">{session.error}</Notice>
      </Layout>
    );
  }
  if (onLogin) {
    return (
      <Layout authenticated={false} centered>
        {session.status === "signed-out" && <LoginPage />}
      </Layout>
    );
  }
  // The relay only serves these screens to a signed-in browser, so the shell
  // can show while the session is confirmed; the screen follows.
  if (session.status === "loading") return <Layout authenticated />;
  if (session.status !== "signed-in") return null;

  let page: ReactNode = <NotFoundPage />;
  for (const route of routes) {
    const params = matchPath(route.path, location.pathname);
    if (params) {
      page = route.render(params);
      break;
    }
  }
  return (
    <Layout authenticated>
      {/* Keyed by the navigation, so every visit loads afresh, as a page load
          used to. */}
      <PageFrame key={location.key}>{page}</PageFrame>
    </Layout>
  );
}

function PageFrame({ children }: { children: ReactNode }) {
  return <>{children}</>;
}
