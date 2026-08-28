/* Le Foyer — a pad drives the page.
   Everything actionable is a [data-nav] element with a data-key; the focus
   ring is the cursor. The D-pad / left stick (or the arrow keys) move it to
   the nearest element in that direction, A (or Enter) presses, B (or
   Escape) backs out of a PIN. A PIN is four wheels: up/down turn a digit,
   digits typed set one. After every htmx swap the cursor goes back to the
   element with the same key, and a PIN block that was open is re-opened.
   Mouse and touch keep working on their own. */
(function () {
  'use strict';
  var openLock = null;          // data-key of the OPEN form whose PIN block is open
  var lastKey = null;           // data-key of the focused element

  function navs() {
    return Array.prototype.filter.call(document.querySelectorAll('[data-nav]'), function (el) {
      return el.offsetParent !== null && !el.disabled;
    });
  }
  function focused() {
    var el = document.activeElement;
    return el && el.hasAttribute && el.hasAttribute('data-nav') ? el : null;
  }
  function focus(el) {
    if (!el) return;
    el.focus({ preventScroll: false });
    lastKey = el.getAttribute('data-key');
  }
  function center(r) { return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; }

  // the nearest element in a direction: on-axis distance, with a penalty
  // for being off-axis, so a pad walks the page the way an eye does
  function move(dir) {
    var cur = focused();
    var list = navs();
    if (!cur) { focus(list[0]); return; }
    var a = center(cur.getBoundingClientRect());
    var best = null, bestScore = Infinity;
    list.forEach(function (el) {
      if (el === cur) return;
      var b = center(el.getBoundingClientRect());
      var dx = b.x - a.x, dy = b.y - a.y, on, off;
      if (dir === 'left') { on = -dx; off = Math.abs(dy); }
      else if (dir === 'right') { on = dx; off = Math.abs(dy); }
      else if (dir === 'up') { on = -dy; off = Math.abs(dx); }
      else { on = dy; off = Math.abs(dx); }
      if (on <= 4) return;
      var score = on + off * (dir === 'up' || dir === 'down' ? 0.6 : 2.5);
      if (score < bestScore) { bestScore = score; best = el; }
    });
    if (best) focus(best);
  }

  // --- the wheels ---------------------------------------------------------
  function turn(wheel, delta) {
    var b = wheel.querySelector('b');
    var v = (parseInt(b.textContent, 10) + delta + 10) % 10;
    b.textContent = String(v);
    writePin(wheel.closest('form'));
  }
  function setDigit(wheel, d) {
    wheel.querySelector('b').textContent = String(d);
    writePin(wheel.closest('form'));
    var next = wheel.nextElementSibling;
    if (next && next.hasAttribute('data-wheel')) focus(next);
  }
  function writePin(form) {
    if (!form) return;
    var pin = form.querySelector('input[name=pin]');
    var wheels = form.querySelector('[data-wheels]');
    if (!pin || !wheels || wheels.offsetParent === null) { if (pin) pin.value = ''; return; }
    pin.value = Array.prototype.map.call(wheels.querySelectorAll('[data-wheel] b'), function (b) { return b.textContent; }).join('');
  }
  function showLock(form, on) {
    var block = form.querySelector('[data-locked]');
    if (!block) return;
    block.classList.toggle('hidden', !on);
    var plain = form.querySelector('[data-key^="open:"]');
    var lock = form.querySelector('[data-lock]');
    if (plain) plain.classList.toggle('hidden', on);
    if (lock) lock.classList.toggle('hidden', on);
    openLock = on ? form.querySelector('[data-lock]').getAttribute('data-key') : null;
    writePin(form);
    if (on) focus(block.querySelector('[data-wheel]'));
    else focus(plain);
  }
  function back() {
    var cur = focused();
    var form = cur && cur.closest('form[data-lockable]');
    if (form && !form.querySelector('[data-locked]').classList.contains('hidden')) { showLock(form, false); return true; }
    return false;
  }

  // --- presses --------------------------------------------------------------
  function press() {
    var cur = focused();
    if (!cur) { focus(navs()[0]); return; }
    if (cur.hasAttribute('data-wheel')) { move('right'); return; }
    cur.click();
  }
  document.addEventListener('click', function (e) {
    var lock = e.target.closest('[data-lock]');
    if (lock) { e.preventDefault(); showLock(lock.closest('form'), true); return; }
    var w = e.target.closest('[data-wheel]');
    if (w) {
      var r = w.getBoundingClientRect();
      turn(w, (e.clientY - r.top) < r.height / 2 ? 1 : -1);
      focus(w);
    }
  });
  document.addEventListener('submit', function (e) { writePin(e.target); }, true);
  document.body.addEventListener('htmx:configRequest', function (e) {
    var form = e.detail.elt;
    if (form && form.tagName === 'FORM') { writePin(form); var pin = form.querySelector('input[name=pin]'); if (pin) e.detail.parameters.pin = pin.value; }
  });

  document.addEventListener('keydown', function (e) {
    var cur = focused();
    var onWheel = cur && cur.hasAttribute('data-wheel');
    switch (e.key) {
      case 'ArrowUp': if (onWheel) turn(cur, 1); else move('up'); break;
      case 'ArrowDown': if (onWheel) turn(cur, -1); else move('down'); break;
      case 'ArrowLeft': move('left'); break;
      case 'ArrowRight': move('right'); break;
      case 'Enter': case ' ': if (cur && cur.hasAttribute('data-wheel')) { move('right'); } else return; break;
      case 'Escape': case 'Backspace': if (!back()) return; break;
      default:
        if (onWheel && /^[0-9]$/.test(e.key)) { setDigit(cur, parseInt(e.key, 10)); break; }
        return;
    }
    e.preventDefault();
  });

  // --- the pad ----------------------------------------------------------------
  // Standard mapping: 0 A, 1 B, 12 up, 13 down, 14 left, 15 right; axes 0/1
  // the left stick. Held directions repeat after a pause.
  var held = {}, repeatAt = {};
  function padState(gp) {
    var s = {};
    var b = gp.buttons, ax = gp.axes;
    s.a = b[0] && b[0].pressed; s.b = b[1] && b[1].pressed;
    s.up = (b[12] && b[12].pressed) || ax[1] < -0.5;
    s.down = (b[13] && b[13].pressed) || ax[1] > 0.5;
    s.left = (b[14] && b[14].pressed) || ax[0] < -0.5;
    s.right = (b[15] && b[15].pressed) || ax[0] > 0.5;
    return s;
  }
  function act(name) {
    var cur = focused();
    var onWheel = cur && cur.hasAttribute('data-wheel');
    if (name === 'a') press();
    else if (name === 'b') back();
    else if (name === 'up' && onWheel) turn(cur, 1);
    else if (name === 'down' && onWheel) turn(cur, -1);
    else move(name);
  }
  function poll(t) {
    var pads = navigator.getGamepads ? navigator.getGamepads() : [];
    for (var i = 0; i < pads.length; i++) {
      var gp = pads[i];
      if (!gp) continue;
      var s = padState(gp);
      ['a', 'b', 'up', 'down', 'left', 'right'].forEach(function (k) {
        var id = i + ':' + k;
        if (s[k]) {
          if (!held[id]) { held[id] = true; repeatAt[id] = t + 320; act(k); }
          else if ((k !== 'a' && k !== 'b') && t >= repeatAt[id]) { repeatAt[id] = t + 130; act(k); }
        } else { held[id] = false; }
      });
    }
    window.requestAnimationFrame(poll);
  }
  window.addEventListener('gamepadconnected', function () { document.body.classList.add('pad'); });
  window.requestAnimationFrame(poll);

  // --- after a swap: the cursor goes back where it was --------------------
  function restore() {
    if (openLock) {
      var lock = document.querySelector('[data-key="' + openLock + '"]');
      if (lock) { var f = lock.closest('form'); showLock(f, true); }
      else openLock = null;
    }
    var el = lastKey && document.querySelector('[data-key="' + lastKey + '"]');
    if (el && el.offsetParent !== null) focus(el); else if (!focused()) focus(navs()[0]);
  }
  document.body.addEventListener('htmx:afterSwap', function () { window.setTimeout(restore, 0); });
  document.body.addEventListener('htmx:afterSettle', function () { if (!focused()) restore(); });
  document.addEventListener('focusin', function (e) { var el = e.target.closest && e.target.closest('[data-nav]'); if (el) lastKey = el.getAttribute('data-key'); });

  // a page with an open PIN block must not be swapped from under the wheels:
  // hold the poll while one is open
  document.body.addEventListener('htmx:beforeRequest', function (e) {
    if (openLock && e.detail.elt && e.detail.elt.id === 'foyer') e.preventDefault();
  });

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', function () { focus(navs()[0]); });
  else focus(navs()[0]);
})();
