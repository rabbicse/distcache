// Console behaviour: history (Up/Down), Tab completion, Ctrl+L clear, and
// sample buttons (click fills, Shift+click runs). Loaded as an external file
// because the CSP forbids inline scripts.
(function () {
  "use strict";
  var form = document.getElementById("console-form");
  var input = document.getElementById("console-input");
  var output = document.getElementById("output");
  var hint = document.getElementById("console-hint");
  if (!form || !input || !output) return;

  var history = Array.prototype.map.call(
    document.querySelectorAll("#history li"),
    function (li) { return li.textContent; }
  );
  var pos = -1; // -1 means the line being typed
  var draft = "";

  function setInput(v) {
    input.value = v;
    input.setSelectionRange(v.length, v.length);
  }

  function accept(cmd) {
    setInput(cmd + " ");
    input.focus();
    if (hint) hint.textContent = "";
  }

  input.addEventListener("keydown", function (e) {
    if (e.ctrlKey && (e.key === "l" || e.key === "L")) {
      e.preventDefault();
      output.textContent = "";
    } else if (e.key === "ArrowUp") {
      if (pos + 1 >= history.length) return;
      e.preventDefault();
      if (pos === -1) draft = input.value;
      pos++;
      setInput(history[pos]);
    } else if (e.key === "ArrowDown") {
      if (pos === -1) return;
      e.preventDefault();
      pos--;
      setInput(pos === -1 ? draft : history[pos]);
    } else if (e.key === "Tab" && !e.shiftKey && hint) {
      var first = hint.querySelector("[data-cmd]");
      if (first && input.value.indexOf(" ") === -1) {
        e.preventDefault();
        accept(first.getAttribute("data-cmd"));
      }
    }
  });

  document.addEventListener("click", function (e) {
    var t = e.target instanceof Element ? e.target.closest("[data-cmd]") : null;
    if (!t) return;
    var cmd = t.getAttribute("data-cmd");
    if (t.classList.contains("sample")) {
      setInput(cmd);
      input.focus();
      if (e.shiftKey) form.requestSubmit();
    } else {
      accept(cmd);
    }
  });

  document.addEventListener("keydown", function (e) {
    if (e.ctrlKey && (e.key === "l" || e.key === "L") && e.target !== input) {
      e.preventDefault();
      output.textContent = "";
    }
  });

  // Record the command and reset the line once the server has answered.
  document.body.addEventListener("htmx:afterRequest", function (e) {
    if (e.detail.elt === form && e.detail.successful) {
      var v = input.value.trim();
      if (v) history.unshift(v);
      if (history.length > 100) history.length = 100;
      pos = -1;
      draft = "";
      input.value = "";
      if (hint) hint.textContent = "";
    }
  });

  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail.target === output) output.scrollTop = output.scrollHeight;
  });
})();
