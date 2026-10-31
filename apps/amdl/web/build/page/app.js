// Runs amdl's TUI (amdl.wasm, from apps/amdl/web) in xterm.js. The Go side
// defines amdlStart, amdlInput and amdlResize, and calls amdlOutput and
// amdlExit defined here.
(function () {
  'use strict';
  var el = document.getElementById('term');
  // The theme is ?theme=light|dark, else the system's. The TUI reads the
  // terminal's background when it starts and picks its colors from it.
  var asked = new URLSearchParams(location.search).get('theme');
  var light = asked ? asked === 'light' : matchMedia('(prefers-color-scheme: light)').matches;
  document.documentElement.dataset.theme = light ? 'light' : 'dark';
  var term = new Terminal({
    fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace',
    fontSize: 13,
    lineHeight: 1.15,
    cursorBlink: true,
    scrollback: 2000,
    // bubbletea writes a bare \n when its input is not a tty, expecting the
    // terminal's newline translation (ONLCR).
    convertEol: true,
    theme: light
      ? { background: '#faf9f5', foreground: '#141413', cursor: '#e11d48', selectionBackground: '#e8e6dc' }
      : { background: '#141413', foreground: '#e8e6dc', cursor: '#fb7185', selectionBackground: '#3a3936' },
  });
  term.open(el);
  term.write('\x1b[2mloading amdl…\x1b[0m');
  term.focus();
  // A click anywhere on the page, the margin round the terminal included,
  // keeps the keyboard in the terminal.
  document.addEventListener('mousedown', function (e) {
    if (term.element.contains(e.target)) return;
    e.preventDefault();
    term.focus();
  });

  // What is typed while amdl.wasm loads waits here, up to a few lines, and
  // goes to the TUI once it runs.
  var early = '';
  var typed = term.onData(function (d) { early = (early + d).slice(-512); });

  // size is how many cells fit the box, from the cell xterm.js measured.
  function size() {
    var cell = term._core && term._core._renderService && term._core._renderService.dimensions.css.cell;
    var w = cell && cell.width ? cell.width : 7.8;
    var h = cell && cell.height ? cell.height : 15;
    return { cols: Math.max(20, Math.floor(el.clientWidth / w)), rows: Math.max(8, Math.floor(el.clientHeight / h)) };
  }

  window.amdlOutput = function (bytes) { term.write(bytes); };
  window.amdlExit = function () {
    term.write('\r\n\x1b[2msession ended · press any key to start again\x1b[0m');
    var d = term.onData(function () { d.dispose(); location.reload(); });
  };

  var go = new Go();
  var url = el.dataset.wasm;
  var wasm = fetch(url);
  var load = WebAssembly.instantiateStreaming
    ? WebAssembly.instantiateStreaming(wasm, go.importObject).catch(function () {
        return fetch(url).then(function (r) { return r.arrayBuffer(); })
          .then(function (b) { return WebAssembly.instantiate(b, go.importObject); });
      })
    : wasm.then(function (r) { return r.arrayBuffer(); }).then(function (b) { return WebAssembly.instantiate(b, go.importObject); });

  load.then(function (res) {
    go.run(res.instance);
    var s = size();
    term.reset();
    term.resize(s.cols, s.rows);
    typed.dispose();
    term.onData(function (d) { window.amdlInput(d); });
    if (early) window.amdlInput(early);
    window.amdlStart(s.cols, s.rows);
    var t;
    new ResizeObserver(function () {
      clearTimeout(t);
      t = setTimeout(function () {
        var n = size();
        if (n.cols === term.cols && n.rows === term.rows) return;
        term.resize(n.cols, n.rows);
        window.amdlResize(n.cols, n.rows);
      }, 50);
    }).observe(el);
    term.focus();
  }, function (err) {
    term.write('\r\n\x1b[31mcould not start amdl: ' + err + '\x1b[0m');
  });
})();
