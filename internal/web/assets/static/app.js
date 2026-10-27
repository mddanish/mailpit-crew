// MailPit-Crew: small progressive enhancements. Every page works without it.
(function () {
  "use strict";

  function csrf(form) {
    var el = form.querySelector('input[name="csrf"]');
    return el ? el.value : "";
  }

  // confirm dialogs for destructive forms
  document.addEventListener("submit", function (e) {
    var msg = e.target.getAttribute("data-confirm");
    if (msg && !window.confirm(msg)) e.preventDefault();
  });
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("button[data-confirm-click]");
    if (btn && !window.confirm(btn.getAttribute("data-confirm-click"))) e.preventDefault();
  });

  // tag rule "test" buttons: count stored messages a match finds
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("button[data-test]");
    if (!btn) return;
    var scope = btn.closest("tr, form");
    var out = scope.querySelector(".test-result");
    var input = scope.querySelector("input[data-match]");
    var q = btn.getAttribute("data-q") || (input ? input.value.trim() : "");
    if (!q) { out.textContent = "enter a match first"; return; }
    out.textContent = "…";
    fetch(btn.getAttribute("data-test") + "?q=" + encodeURIComponent(q), { credentials: "same-origin" })
      .then(function (r) { return r.json(); })
      .then(function (d) { out.textContent = d.error ? d.error : d.count + " stored message(s) match"; })
      .catch(function () { out.textContent = "test failed"; });
  });

  // "add or change" forms: help text, example and the right input per key
  function setupKeyForm(form) {
    var select = form.querySelector("select[data-keyselect]");
    var box = form.querySelector("[data-valuebox]");
    var help = form.querySelector("[data-keyhelp]");
    function render() {
      var opt = select.options[select.selectedIndex];
      var meta = opt && opt.getAttribute("data-meta") ? JSON.parse(opt.getAttribute("data-meta")) : null;
      box.textContent = "";
      var input;
      if (meta && meta.kind === "bool") {
        input = document.createElement("select");
        ["true", "false"].forEach(function (v) {
          var o = document.createElement("option");
          o.value = v; o.textContent = v;
          if ((meta.current || "").toLowerCase() === v || (v === "false" && !meta.current)) o.selected = true;
          input.appendChild(o);
        });
      } else if (meta && meta.options && meta.options.length) {
        input = document.createElement("select");
        meta.options.forEach(function (v) {
          var o = document.createElement("option");
          o.value = v; o.textContent = v;
          if ((meta.current || "").toLowerCase() === v) o.selected = true;
          input.appendChild(o);
        });
      } else {
        input = document.createElement("input");
        input.type = meta && meta.sensitive ? "password" : "text";
        input.autocomplete = "off";
        input.placeholder = meta && meta.example ? meta.example : "value";
        input.value = meta && meta.current ? meta.current : "";
      }
      input.name = "value";
      input.className = "value-input";
      box.appendChild(input);
      if (!meta) {
        help.textContent = "Choose one to see what it does and an example.";
        return;
      }
      var text = meta.desc || "";
      if (meta.default) text += " Default: " + meta.default + ".";
      if (meta.example) text += " Example: " + meta.example;
      if (meta.kind === "bool") text += " (false removes the setting)";
      help.textContent = text;
    }
    select.addEventListener("change", render);
    render();
  }
  Array.prototype.forEach.call(document.querySelectorAll("form.keyform"), setupKeyForm);

  // full editor: CodeMirror with Verify, problem markers and examples
  function setupEditor(form) {
    var ta = form.querySelector("textarea[data-editor]");
    var result = form.querySelector(".verify-result");
    var list = form.querySelector(".issues");
    var examples = [];
    try { examples = JSON.parse(form.querySelector(".examples-data").textContent || "[]") || []; } catch (e) {}
    var cm = null;
    if (window.CodeMirror) {
      var dark = window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches;
      cm = window.CodeMirror.fromTextArea(ta, {
        mode: ta.getAttribute("data-editor"),
        lineNumbers: true,
        styleActiveLine: true,
        indentUnit: 2,
        tabSize: 2,
        indentWithTabs: false,
        lineWrapping: true,
        theme: dark ? "material-darker" : "default",
        extraKeys: { Tab: function (c) { c.replaceSelection("  "); } }
      });
    }
    var marked = [];
    function showIssues(issues) {
      if (cm) {
        marked.forEach(function (l) { cm.removeLineClass(l, "background", "cm-issue-error"); cm.removeLineClass(l, "background", "cm-issue-warn"); });
        marked = [];
      }
      list.textContent = "";
      (issues || []).forEach(function (i) {
        var li = document.createElement("li");
        li.className = i.warn ? "warn" : "error";
        li.textContent = (i.warn ? "warning: " : "") + (i.line ? "line " + i.line + ": " : "") + i.msg;
        if (cm && i.line) {
          var h = cm.addLineClass(i.line - 1, "background", i.warn ? "cm-issue-warn" : "cm-issue-error");
          marked.push(h);
          li.classList.add("jump");
          li.addEventListener("click", function () { cm.focus(); cm.setCursor({ line: i.line - 1, ch: 0 }); });
        }
        list.appendChild(li);
      });
    }
    try { showIssues(JSON.parse(ta.getAttribute("data-issues") || "[]")); } catch (e) {}

    form.querySelector("[data-verify-btn]").addEventListener("click", function () {
      if (cm) cm.save();
      result.textContent = "checking…";
      var body = new URLSearchParams();
      body.set("csrf", csrf(form));
      body.set("content", ta.value);
      fetch(form.getAttribute("data-verify"), { method: "POST", body: body, credentials: "same-origin" })
        .then(function (r) { return r.json(); })
        .then(function (d) {
          result.textContent = d.ok ? (d.issues.length ? "valid, with warnings" : "valid") : "problems found";
          result.className = "verify-result " + (d.ok ? "ok" : "error");
          showIssues(d.issues);
        })
        .catch(function () { result.textContent = "verify failed"; });
    });

    var exSelect = form.querySelector("select[data-example]");
    if (exSelect) {
      exSelect.addEventListener("change", function () {
        var ex = examples[parseInt(exSelect.value, 10)];
        exSelect.value = "";
        if (!ex) return;
        var current = cm ? cm.getValue() : ta.value;
        var replace = current.trim() === "" || window.confirm("Replace the editor content with the example \"" + ex.name + "\"?\nCancel adds it at the end instead.");
        var text = replace ? ex.content : current.replace(/\n*$/, "\n\n") + ex.content;
        if (cm) cm.setValue(text); else ta.value = text;
      });
    }
    form.addEventListener("submit", function () { if (cm) cm.save(); });
  }
  Array.prototype.forEach.call(document.querySelectorAll("form.editor-form"), setupEditor);
})();
