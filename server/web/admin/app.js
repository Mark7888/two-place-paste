// The admin screen's behaviour. One file, no build step, no framework — the
// same rule the stylesheet follows (ROADMAP P4).
//
// It does three things, and every one of them is something HTML alone cannot:
// reveal a password field, copy a string to the clipboard, and show a
// creation code in a modal instead of on the page. Everything it needs is
// already in the document — no request is made from here, and no token is put
// anywhere that outlives the tab.
//
// Each page works without it. The login form submits, and an unused token
// renders its QR and URL inside a <noscript> block, so a browser with
// scripting off still onboards a device.

(function () {
  "use strict";

  /** copyText puts a string on the clipboard, preferring the async API and
   *  falling back to a hidden selection where it is unavailable — the admin is
   *  frequently served over plain http on a LAN, where navigator.clipboard is
   *  not exposed. */
  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      var area = document.createElement("textarea");
      area.value = text;
      area.setAttribute("readonly", "");
      area.style.position = "fixed";
      area.style.top = "-1000px";
      document.body.appendChild(area);
      area.select();
      var ok = false;
      try {
        ok = document.execCommand("copy");
      } catch (err) {
        ok = false;
      }
      document.body.removeChild(area);
      ok ? resolve() : reject(new Error("copy is not available in this browser"));
    });
  }

  /** wireCopyButtons makes every [data-copy] button copy the value named by
   *  the element it points at, and say so for a moment. */
  function wireCopyButtons(root) {
    root.querySelectorAll("[data-copy]").forEach(function (button) {
      button.addEventListener("click", function () {
        var source = document.getElementById(button.getAttribute("data-copy"));
        if (!source) return;
        var label = button.querySelector("[data-copy-label]") || button;
        var statusId = button.getAttribute("data-copy-status");
        var status = statusId ? document.getElementById(statusId) : null;
        var original = label.textContent;

        var say = function (text, hold) {
          label.textContent = text;
          if (status) status.textContent = text;
          window.setTimeout(function () {
            label.textContent = original;
            if (status) status.textContent = "";
          }, hold);
        };

        copyText(source.textContent.trim()).then(
          function () {
            say("Copied", 1600);
          },
          function () {
            // A browser that refuses both paths still leaves the value
            // selectable: the field it came from is user-select: all.
            say("Select and copy", 2400);
          },
        );
      });
    });
  }

  /** wireReveal turns a password field's affix button into a show/hide toggle. */
  function wireReveal(root) {
    root.querySelectorAll("[data-reveal]").forEach(function (button) {
      var input = document.getElementById(button.getAttribute("data-reveal"));
      if (!input) return;
      button.hidden = false;
      button.addEventListener("click", function () {
        var hidden = input.type === "password";
        input.type = hidden ? "text" : "password";
        button.textContent = hidden ? "Hide" : "Show";
        button.setAttribute("aria-pressed", String(hidden));
        button.setAttribute("aria-label", hidden ? "Hide the password" : "Show the password");
        // Typing should carry on where it left off, not at the start.
        var end = input.value.length;
        input.focus();
        try {
          input.setSelectionRange(end, end);
        } catch (err) {
          /* a field that does not support selection is not worth failing over */
        }
      });
    });
  }

  /** wireTokenDialog wires the one modal that shows a creation code.
   *
   *  There is a single dialog for the whole page rather than one per row: the
   *  QR is a request for a live credential, so its src is set when the dialog
   *  opens and cleared when it closes. A closed dialog holds no token image,
   *  and a page listing twenty unused tokens fetches none of them. */
  function wireTokenDialog(root) {
    var dialog = root.querySelector("#token-dialog");
    if (!dialog || typeof dialog.showModal !== "function") return;

    var title = dialog.querySelector("#token-dialog-title");
    var qr = dialog.querySelector("#token-dialog-qr");
    var url = dialog.querySelector("#token-dialog-url");
    var copied = dialog.querySelector("#token-dialog-copied");

    function open(button) {
      title.textContent = button.getAttribute("data-name") || "Creation code";
      url.textContent = button.getAttribute("data-url") || "";
      copied.textContent = "";
      qr.src = button.getAttribute("data-qr") || "";
      dialog.showModal();
    }

    dialog.addEventListener("close", function () {
      // Drop the image as soon as the dialog is dismissed: the user closed the
      // code, so the code should stop being on screen behind anything.
      qr.removeAttribute("src");
      copied.textContent = "";
    });

    // Clicking the backdrop — that is, the dialog element itself rather than
    // the panel inside it — dismisses. Escape already does.
    dialog.addEventListener("click", function (event) {
      if (event.target === dialog) dialog.close();
    });

    var autoshow = null;
    root.querySelectorAll("[data-token-dialog]").forEach(function (button) {
      button.addEventListener("click", function () {
        open(button);
      });
      if (button.hasAttribute("data-autoshow")) autoshow = button;
    });

    // A token that was just generated opens its dialog straight away: the
    // point of minting one is to hand it over, and that is the moment.
    if (autoshow) open(autoshow);
  }

  function ready(fn) {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", fn);
    } else {
      fn();
    }
  }

  ready(function () {
    wireCopyButtons(document);
    wireReveal(document);
    wireTokenDialog(document);
    // With scripting on, the inline fallback is redundant and its QR requests
    // are not. <noscript> already hides it; this is belt and braces for a
    // browser that renders noscript content anyway.
    document.querySelectorAll("[data-js-hide]").forEach(function (el) {
      el.hidden = true;
    });
  });
})();
