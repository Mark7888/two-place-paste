import { useCallback, useEffect, useState } from "react";

/** BeforeInstallPromptEvent is Chromium's offer to install the app. It is
 *  not in the DOM typings because no other engine has it. */
interface BeforeInstallPromptEvent extends Event {
  prompt: () => Promise<void>;
  userChoice: Promise<{ outcome: "accepted" | "dismissed" }>;
}

/** useInstallPrompt keeps the browser's offer to install the admin as an app,
 *  so the menu can offer it where people look for things to do, rather than
 *  only in the address bar. install is null when there is no offer: the app
 *  is installed already, the browser has no prompt (Safari, Firefox), or the
 *  relay is not on HTTPS. */
export function useInstallPrompt(): { install: (() => void) | null } {
  const [offer, setOffer] = useState<BeforeInstallPromptEvent | null>(null);

  useEffect(() => {
    const onOffer = (event: Event) => {
      event.preventDefault();
      setOffer(event as BeforeInstallPromptEvent);
    };
    const onInstalled = () => setOffer(null);
    window.addEventListener("beforeinstallprompt", onOffer);
    window.addEventListener("appinstalled", onInstalled);
    return () => {
      window.removeEventListener("beforeinstallprompt", onOffer);
      window.removeEventListener("appinstalled", onInstalled);
    };
  }, []);

  const install = useCallback(() => {
    if (!offer) return;
    // An offer can be used once, whatever the answer.
    setOffer(null);
    void offer.prompt();
  }, [offer]);

  return { install: offer ? install : null };
}
