/** copyText puts a string on the clipboard, preferring the async API and
 *  falling back to a hidden selection where it is unavailable — the admin is
 *  frequently served over plain http on a LAN, where navigator.clipboard is
 *  not exposed. */
export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const area = document.createElement("textarea");
  area.value = text;
  area.setAttribute("readonly", "");
  area.style.position = "fixed";
  area.style.top = "-1000px";
  document.body.appendChild(area);
  area.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  } finally {
    document.body.removeChild(area);
  }
  if (!ok) throw new Error("copy is not available in this browser");
}
