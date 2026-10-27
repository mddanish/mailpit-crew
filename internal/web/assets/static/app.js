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
