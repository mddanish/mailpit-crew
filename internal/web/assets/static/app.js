// MailPit-Crew: small progressive enhancements. Every page works without it.
(function () {
  "use strict";

  // confirm dialogs for destructive forms
  document.addEventListener("submit", function (e) {
    var msg = e.target.getAttribute("data-confirm");
    if (msg && !window.confirm(msg)) {
      e.preventDefault();
    }
  });

  // confirm for single submit buttons inside a shared form
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("button[data-confirm-click]");
    if (btn && !window.confirm(btn.getAttribute("data-confirm-click"))) {
      e.preventDefault();
    }
  });

  // inbox: select all
  document.addEventListener("change", function (e) {
    if (!e.target.matches("input[data-select-all]")) return;
    var boxes = e.target.closest("form").querySelectorAll('input[name="id"]');
    for (var i = 0; i < boxes.length; i++) boxes[i].checked = e.target.checked;
  });

  // inbox: show a banner when new mail arrives
  var banner = document.getElementById("new-mail");
  if (banner) {
    var url = banner.getAttribute("data-state");
    var latest = banner.getAttribute("data-latest");
    setInterval(function () {
      if (document.hidden) return;
      fetch(url, { credentials: "same-origin" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (d) {
          if (d && d.latest && d.latest !== latest) banner.hidden = false;
        })
        .catch(function () {});
    }, 15000);
  }

  // tag rule "Test" buttons
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("button[data-test]");
    if (!btn) return;
    var row = btn.closest("tr");
    var input = row.querySelector("input[data-match]");
    var out = row.querySelector(".test-result");
    var q = input.value.trim();
    if (!q) {
      out.textContent = "enter a match first";
      return;
    }
    out.textContent = "…";
    fetch(btn.getAttribute("data-test") + "?q=" + encodeURIComponent(q), { credentials: "same-origin" })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        out.textContent = d.error ? d.error : d.count + " stored message(s) match";
      })
      .catch(function () { out.textContent = "test failed"; });
  });
})();
