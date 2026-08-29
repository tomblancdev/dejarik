// The library's push (dejarik 0.8): three small hands on the form.
//  1. a file picked asks the panel which shelf its name belongs on, before
//     anything is uploaded, and sets the selector (or says "choose")
//  2. the bar fills while the push is in flight (htmx's xhr progress)
//  3. once landed, the form is reset - hx-preserve keeps it across the
//     block's polls, so it would otherwise keep the file it just pushed
(function () {
  'use strict';
  function form(el) { return el && el.closest ? el.closest('form.push') : null; }

  document.addEventListener('change', function (ev) {
    var f = form(ev.target);
    if (!f || ev.target.type !== 'file') return;
    var words = f.querySelector('[data-words]');
    var sel = f.querySelector('select[name=system]');
    var files = ev.target.files;
    if (!files || !files.length) { if (words) words.textContent = ''; return; }
    var name = files[0].name;
    fetch(f.dataset.detect + '?file=' + encodeURIComponent(name), { credentials: 'same-origin' })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (d.system) {
          sel.value = d.system;
          words.textContent = (files.length > 1 ? files.length + ' files → ' : name + ' → ') + 'the ' + d.system + ' shelf' + (files.length > 1 ? ' (every file its own, unless you pick one)' : '');
        } else {
          sel.value = '';
          words.textContent = name + ': ' + (d.error || 'no shelf takes it') + (d.candidates ? ' — ' + d.candidates.join(', ') : '');
          sel.focus();
        }
      })
      .catch(function () { words.textContent = ''; });
  });

  document.addEventListener('htmx:xhr:progress', function (ev) {
    var f = form(ev.target);
    if (!f) return;
    var bar = f.querySelector('progress.push');
    if (!bar) return;
    bar.hidden = false;
    if (ev.detail.lengthComputable) bar.value = Math.round(100 * ev.detail.loaded / ev.detail.total);
    else bar.removeAttribute('value');
  });

  document.addEventListener('htmx:beforeRequest', function (ev) {
    var f = form(ev.target);
    if (!f) return;
    var b = f.querySelector('button[type=submit]');
    if (b) b.disabled = true;
    var bar = f.querySelector('progress.push');
    if (bar) { bar.hidden = false; bar.value = 0; }
  });

  document.addEventListener('htmx:afterRequest', function (ev) {
    var f = form(ev.target);
    if (!f) return;
    var b = f.querySelector('button[type=submit]');
    if (b) b.disabled = false;
    var bar = f.querySelector('progress.push');
    if (bar) { bar.hidden = true; bar.value = 0; }
    var words = f.querySelector('[data-words]');
    if (words) words.textContent = '';
    if (ev.detail.successful) f.reset();
  });
})();
