// The pairing hand-off page.
//
// Its whole job is to take a code out of this URL's fragment and offer it to
// the app. The fragment is the point: a fragment is never sent to the server,
// so the relay serving this page has not seen the code and cannot log it. That
// is also why every line below is client-side and this page makes no request
// of its own.

(function () {
  "use strict";

  /** readCode returns the code in the fragment, or "" when there is none. */
  function readCode() {
    var hash = window.location.hash;
    if (hash.charAt(0) === "#") {
      hash = hash.slice(1);
    }
    try {
      hash = decodeURIComponent(hash);
    } catch (err) {
      // A fragment that is not valid percent-encoding is used as it stands;
      // the decoder on the other side is the one that judges it.
    }
    return hash.trim();
  }

  /** VALID bounds what is put into a link: unpadded base64url and nothing else. */
  var VALID = /^[A-Za-z0-9_-]+$/;

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
      ok ? resolve() : reject(new Error("copy is unavailable"));
    });
  }

  function ready(fn) {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", fn);
    } else {
      fn();
    }
  }

  ready(function () {
    var code = readCode();
    var have = document.getElementById("have-code");
    var none = document.getElementById("no-code");

    if (code === "" || !VALID.test(code)) {
      // Anything that is not a code is not put into an href: a link built out
      // of an arbitrary fragment is how a page like this becomes a redirector
      // for someone else's URL.
      none.hidden = false;
      return;
    }
    have.hidden = false;

    document.getElementById("code").textContent = code;
    document.getElementById("open").setAttribute("href", "tpp://pair#" + code);

    var status = document.getElementById("status");
    document.getElementById("copy").addEventListener("click", function () {
      copyText(code).then(
        function () {
          status.textContent = "Copied. Paste it into the app's pairing screen.";
        },
        function () {
          status.textContent = "Select the code above and copy it.";
        },
      );
    });
  });
})();
