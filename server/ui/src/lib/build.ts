/** BUILD is the id of the build this bundle came from. The build fills it in
 *  (buildFiles in vite.config.ts); the dev server and the tests leave the
 *  placeholder, which isBuildId rejects, so they never offer an update. */
export const BUILD: string = "__TPP_BUILD__";

/** isBuildId reports whether s is a build id as the build writes it. */
export function isBuildId(s: unknown): s is string {
  return typeof s === "string" && /^[0-9a-f]{12}$/.test(s);
}

/** page.reload loads the page again. It is an object's method so that tests
 *  can replace it: jsdom cannot navigate. */
export const page = {
  reload(): void {
    window.location.reload();
  },
};

/** updateApp loads the build the relay serves now. It first asks the service
 *  worker to update, so that the new worker — and with it the new build's
 *  offline copy — is on its way; then it reloads, which fetches the new page
 *  and bundle from the relay either way (the worker is network first). */
export async function updateApp(): Promise<void> {
  try {
    const registration = await navigator.serviceWorker?.getRegistration("/admin/");
    await registration?.update();
  } catch {
    // No worker (plain http, or the browser has none) or no network for its
    // script: the reload still gets the new build when the relay is there.
  }
  page.reload();
}
