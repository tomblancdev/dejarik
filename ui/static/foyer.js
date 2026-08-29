/* Le Foyer — the page's own state, the server's data, one render.

   DATA is what /state answers every few seconds: who you are, the rooms,
   the house. UI is what only this page knows: which card the ring is on, a
   PIN being turned. A poll replaces data and renders; it never touches ui —
   which is why the digits you are turning survive it (0.4 re-rendered the
   whole page from the server and wiped them).

   A pad drives it: ◀ ▶ walk a shelf, ▲ ▼ change shelves, A presses the
   card's first action, X its second, Y its third, B backs out. In PIN mode
   ◀ ▶ move between the four wheels, ▲ ▼ turn one, A confirms, B cancels.
   Arrow keys, Enter, x, y, Escape, the digits, a mouse and a finger do the
   same. */
(function () {
  'use strict';
  var body = document.body;
  var P = body.dataset.project, SESSION = body.dataset.session, CAPS = body.dataset.caps || '';
  var Q = '?session=' + encodeURIComponent(SESSION) + '&caps=' + encodeURIComponent(CAPS);
  var data = null;
  // qr: the link card whose code is shown (Mon vestiaire) — the page's own,
  // like the ring and a PIN; cleared by hand (B) or once the link lands
  var ui = { focus: null, pin: null, msg: null, busy: false, qr: null };
  var SHELVES = ['rooms', 'house', 'mine'];

  // --- the server's data ---------------------------------------------------
  var VERSION = body.dataset.version || '';
  function poll() {
    fetch('/foyer/' + P + '/state' + Q, { headers: { Accept: 'application/json' } })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) {
        // a new panel landed under an open seat: this page is the old one —
        // reload it, unless a PIN is being turned (the next poll will)
        if (d && VERSION && d.version && d.version !== VERSION && !ui.pin) { window.location.reload(); return; }
        if (d) {
          data = d;
          // the code shown on a link card goes away by itself once the link
          // landed (the card no longer offers it)
          if (ui.qr) { var still = cards().some(function (c) { return c.key === ui.qr && c.acts.some(function (a) { return a.verb === 'qr'; }); }); if (!still) ui.qr = null; }
          render();
        }
      })
      .catch(function () {})
      .then(function () { window.setTimeout(poll, ((data && data.poll_seconds) || 3) * 1000); });
  }
  function act(verb, target, pin) {
    if (ui.busy) return;
    ui.busy = true;
    window.setTimeout(function () { ui.busy = false; }, 8000);
    var form = new URLSearchParams({ session: SESSION, caps: CAPS });
    form.set(verb === 'open' ? 'app' : 'room', target);
    if (pin) form.set('pin', pin);
    // a link's unlink names its sidecar in the path, not the body
    var url = verb === 'unlink' ? '/foyer/' + P + '/links/' + encodeURIComponent(target) + '/unlink' : '/foyer/' + P + '/' + verb;
    fetch(url, { method: 'POST', body: form,
      headers: { Accept: 'application/json', 'Content-Type': 'application/x-www-form-urlencoded' } })
      .then(function (r) { return r.json().then(function (j) { return { ok: r.ok, j: j }; }); })
      .then(function (x) {
        if (x.ok) { data = x.j; ui.msg = x.j.notice ? { kind: 'good', text: x.j.notice } : null; ui.pin = null; }
        else {
          if (x.j.state) data = x.j.state;
          ui.msg = { kind: 'bad', text: x.j.error || 'refused' };
          // a locked room, or one whose PIN the panel no longer remembers: the wheels
          if (/four digits/i.test(x.j.error || '')) ui.pin = { card: ui.focus, verb: verb, target: target, digits: [0, 0, 0, 0], idx: 0 };
          else ui.pin = null;
        }
      })
      .catch(function () { ui.msg = { kind: 'bad', text: 'the panel did not answer' }; })
      .then(function () { ui.busy = false; render(); });
  }

  // --- the cards, from data -----------------------------------------------
  function cards() {
    var out = [];
    if (!data) return out;
    var known = data.known, me = data.guest && data.guest.person;
    (data.rooms || []).forEach(function (r) {
      var acts = [];
      if (known) {
        acts.push({ glyph: 'A', label: r.mine ? 'join your room' : 'join', verb: 'join', target: r.id, pin: r.locked && !r.mine });
        if (r.mine) acts.push({ glyph: 'Y', label: 'stop', verb: 'stop', target: r.id });
      }
      out.push({ key: 'room:' + r.id, shelf: 'rooms', title: r.app, icon: r.app_id,
        sub: 'opened by <b>' + esc(r.label || r.person || '?') + '</b>' + (r.mine ? ' · yours' : '') + ' · since ' + hhmm(r.since),
        players: [r.in, Math.max(4, r.in)], locked: r.locked, acts: acts });
    });
    (data.house_shelf || []).forEach(function (h) {
      var acts = [], sub, busy = false;
      if (!known) { sub = 'a device nobody pointed yet — an admin points it at a drawer on the panel'; busy = true; }
      else if (h.room) { sub = 'your room is open on it'; acts.push({ glyph: 'A', label: 'join your room', verb: 'join', target: h.room }); }
      else if (h.busy) { sub = esc(h.busy); busy = true; }
      else {
        sub = (data.guest.shared ? "the house's home" : 'your home') + ' · your saves';
        acts.push({ glyph: 'A', label: 'open a room', verb: 'open', target: h.id });
        acts.push({ glyph: 'X', label: 'lock with a PIN', verb: 'open', target: h.id, pin: true });
      }
      out.push({ key: 'house:' + h.id, shelf: 'house', title: h.title, icon: h.icon ? h.id : '', sub: sub, acts: acts, busy: busy });
    });
    // Mon vestiaire: what is yours — an account tied to your drawer, riding
    // under every seat you open. The link itself happens on your PHONE (the
    // provider's page belongs there, not in a stream): the card shows a code
    // to scan, and flips by itself once the appliance has taken the link.
    if (known) (data.links || []).forEach(function (l) {
      var st = l.status || {}, acts = [], sub;
      if (st.linked) { sub = 'linked · under every seat you open' + (st.reported_at ? ' · the appliance said so at ' + hhmm(st.reported_at) : ''); acts.push({ glyph: 'Y', label: 'unlink', verb: 'unlink', target: l.sidecar }); }
      else if (st.pending) sub = 'linking… the appliance takes it within seconds';
      else if (st.unlinking) sub = 'unlinking…';
      else { sub = (st.reported ? 'not linked' : 'the appliance has not said yet') + ' · music under your game, from your phone'; acts.push({ glyph: 'A', label: 'link — show the code', verb: 'qr', target: l.sidecar }); }
      out.push({ key: 'link:' + l.sidecar, shelf: 'mine', title: l.label, icon: '', sub: sub, acts: acts, qr: l.qr });
    });
    return out;
  }
  function current() {
    var list = cards();
    for (var i = 0; i < list.length; i++) if (list[i].key === ui.focus) return list[i];
    return null;
  }
  function esc(s) { return String(s).replace(/[&<>"]/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]; }); }
  function hhmm(iso) { var d = new Date(iso); return isNaN(d) ? '' : (('0' + d.getHours()).slice(-2) + ':' + ('0' + d.getMinutes()).slice(-2)); }

  // --- render ------------------------------------------------------------------
  function render() {
    var list = cards();
    if (!list.some(function (c) { return c.key === ui.focus; })) ui.focus = list.length ? list[0].key : null;
    if (ui.pin && ui.pin.card !== ui.focus) ui.pin = null;
    var who = document.getElementById('who');
    if (data && who) {
      var g = data.guest;
      who.innerHTML = (data.known ? esc(g.label) + (g.shared ? " · the house's" : '') : "nobody's drawer") + ' <span class="dim">· ' + esc(g.device) + '</span>';
    }
    var msg = document.getElementById('msg');
    msg.innerHTML = ui.msg ? '<div class="' + ui.msg.kind + '">' + esc(ui.msg.text) + '</div>' : '';
    var n = document.getElementById('rooms-n');
    if (data) { var inn = (data.rooms || []).reduce(function (a, r) { return a + r.in; }, 0); n.textContent = data.rooms.length ? (data.rooms.length + ' room' + (data.rooms.length > 1 ? 's' : '') + ' · ' + inn + ' in') : ''; }
    SHELVES.forEach(function (shelf) {
      var el = document.getElementById(shelf), html = '';
      if (!el) return;
      var mine = list.filter(function (c) { return c.shelf === shelf; });
      if (!mine.length) html = '<div class="empty">' + (shelf === 'rooms' ? 'none — open one below, and the others will find it here' : shelf === 'mine' ? (data && data.known ? 'nothing to link here' : 'a device nobody pointed yet has no locker') : (data ? 'the house has no game on the shelf' : 'reading the house…')) + '</div>';
      mine.forEach(function (c) {
        var focus = c.key === ui.focus;
        var pin = focus && ui.pin;
        var qr = c.qr && ui.qr === c.key;
        html += '<div class="card' + (focus ? ' focus' : '') + (c.busy ? ' busy' : '') + (qr ? ' wide' : '') + '" data-key="' + esc(c.key) + '">';
        html += '<div class="top"><span class="art">' + (c.icon ? '<img src="/foyer/' + P + '/icon/' + encodeURIComponent(c.icon) + Q + '" alt="">' : esc(c.title.slice(0, 1))) + '</span>';
        html += '<span class="name">' + esc(c.title) + '</span>' + (c.locked ? '<span class="lock">PIN</span>' : '') + '</div>';
        html += '<div class="sub">' + (pin ? (ui.pin.verb === 'open' ? 'lock with a PIN — turn the wheels' : 'its four digits, please') : qr ? 'scan with your phone — it opens the panel, signed in as you, then ' + esc(c.title) + "'s own page; this card flips when the link has landed" : c.sub) + '</div>';
        if (qr) html += '<img class="qr" src="' + esc(c.qr) + Q + '" alt="the code to scan">';
        if (c.players && !pin) { html += '<div class="players">'; for (var i = 0; i < c.players[1]; i++) html += '<i' + (i < c.players[0] ? '' : ' class="off"') + '></i>'; html += ' ' + c.players[0] + ' in</div>'; }
        if (qr) {
          html += '<div class="acts"><span class="key dim" data-glyph="B"><b>B</b>hide the code</span></div>';
        } else if (pin) {
          html += '<div class="pinrow"><span class="wheels">';
          ui.pin.digits.forEach(function (d, i) { html += '<span class="wheel' + (i === ui.pin.idx ? ' on' : '') + '" data-wheel="' + i + '"><b>' + d + '</b></span>'; });
          html += '</span></div>';
          html += '<div class="acts"><span class="key" data-glyph="A"><b>A</b>' + (ui.pin.verb === 'open' ? 'open locked' : ui.pin.verb) + '</span><span class="key dim" data-glyph="B"><b>B</b>cancel</span></div>';
        } else {
          html += '<div class="acts">';
          c.acts.forEach(function (a) { html += '<span class="key" data-glyph="' + a.glyph + '"><b>' + a.glyph + '</b>' + esc(a.label) + '</span>'; });
          html += '</div>';
        }
        html += '</div>';
      });
      el.innerHTML = html;
    });
    var f = document.querySelector('.card.focus');
    if (f && f.scrollIntoView) f.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  }

  // --- presses --------------------------------------------------------------
  function press(glyph) {
    var c = current();
    if (ui.pin) {
      if (glyph === 'A') act(ui.pin.verb, ui.pin.target, ui.pin.digits.join(''));
      else if (glyph === 'B') { ui.pin = null; render(); }
      return;
    }
    if (glyph === 'B') { ui.msg = null; ui.qr = null; render(); return; }
    if (!c) return;
    if (ui.qr === c.key) return;   // the code is up: B hides it, nothing else presses
    var a = null;
    for (var i = 0; i < c.acts.length; i++) if (c.acts[i].glyph === glyph) a = c.acts[i];
    if (!a) return;
    if (a.pin) { ui.pin = { card: c.key, verb: a.verb, target: a.target, digits: [0, 0, 0, 0], idx: 0 }; ui.msg = null; render(); return; }
    if (a.verb === 'qr') { ui.qr = c.key; ui.msg = null; render(); return; }
    act(a.verb, a.target);
  }
  function move(dir) {
    if (ui.pin) {
      var p = ui.pin;
      if (dir === 'left') p.idx = (p.idx + 3) % 4;
      else if (dir === 'right') p.idx = (p.idx + 1) % 4;
      else if (dir === 'up') p.digits[p.idx] = (p.digits[p.idx] + 1) % 10;
      else if (dir === 'down') p.digits[p.idx] = (p.digits[p.idx] + 9) % 10;
      render(); return;
    }
    var list = cards();
    if (!list.length) return;
    // by KEY, never by identity: every cards() call builds new objects
    var c = null;
    for (var k = 0; k < list.length; k++) if (list[k].key === ui.focus) c = list[k];
    if (!c) { ui.focus = list[0].key; render(); return; }
    var same = list.filter(function (x) { return x.shelf === c.shelf; });
    var i = 0;
    for (var j = 0; j < same.length; j++) if (same[j].key === c.key) i = j;
    if (dir === 'left' && i > 0) ui.focus = same[i - 1].key;
    else if (dir === 'right' && i < same.length - 1) ui.focus = same[i + 1].key;
    else if (dir === 'up' || dir === 'down') {
      // the next shelf that has a card, in the page's order (three shelves
      // since Mon vestiaire; the old "the other shelf" assumed two)
      var at = SHELVES.indexOf(c.shelf), step = dir === 'down' ? 1 : -1;
      for (var s = at + step; s >= 0 && s < SHELVES.length; s += step) {
        var other = list.filter(function (x) { return x.shelf === SHELVES[s]; });
        if (other.length) { ui.focus = other[Math.min(i, other.length - 1)].key; break; }
      }
    }
    if (ui.qr && ui.qr !== ui.focus) ui.qr = null;
    render();
  }
  function digit(d) {
    if (!ui.pin) return;
    ui.pin.digits[ui.pin.idx] = d;
    ui.pin.idx = Math.min(3, ui.pin.idx + 1);
    render();
  }

  // mouse and touch: the card, its keys, the wheels
  document.addEventListener('click', function (e) {
    var card = e.target.closest('.card');
    if (!card) return;
    if (card.dataset.key !== ui.focus) { ui.focus = card.dataset.key; if (ui.pin) ui.pin = null; }
    var w = e.target.closest('[data-wheel]');
    if (w && ui.pin) {
      var r = w.getBoundingClientRect(); ui.pin.idx = parseInt(w.dataset.wheel, 10);
      ui.pin.digits[ui.pin.idx] = (ui.pin.digits[ui.pin.idx] + ((e.clientY - r.top) < r.height / 2 ? 1 : 9)) % 10;
      render(); return;
    }
    var k = e.target.closest('[data-glyph]');
    if (k) { render(); press(k.dataset.glyph); return; }
    render();
  });

  document.addEventListener('keydown', function (e) {
    switch (e.key) {
      case 'ArrowUp': move('up'); break;
      case 'ArrowDown': move('down'); break;
      case 'ArrowLeft': move('left'); break;
      case 'ArrowRight': move('right'); break;
      case 'Enter': case ' ': press('A'); break;
      case 'x': case 'X': press('X'); break;
      case 'y': case 'Y': press('Y'); break;
      case 'Escape': case 'Backspace': press('B'); break;
      default:
        if (/^[0-9]$/.test(e.key) && ui.pin) { digit(parseInt(e.key, 10)); break; }
        return;
    }
    e.preventDefault();
  });

  // --- the pad ----------------------------------------------------------------
  // Two layouts. STANDARD (the pad is one the browser knows): 0 A, 1 B,
  // 2 X, 3 Y, 12-15 the D-pad, axes 0/1 the left stick. RAW (the browser
  // found no remap — Wolf's virtual pad on Firefox/Linux, read on the TV
  // 2026-08-29: "Y is 2, X is 3"): the buttons come in evdev code order,
  // BTN_SOUTH A, BTN_EAST B, BTN_NORTH = Y, BTN_WEST = X, then the
  // shoulders, select/start/mode, the thumbs, and a D-pad declared as keys
  // after them (11-14); a D-pad declared as a hat is axes 6/7. Held
  // directions repeat after a pause.
  var held = {}, repeatAt = {};
  function padState(gp) {
    var b = gp.buttons, ax = gp.axes, s = {};
    var P = function (i) { return !!(b[i] && b[i].pressed); };
    var lx = ax[0] || 0, ly = ax[1] || 0;
    if (gp.mapping === 'standard') {
      // Firefox on Linux builds the standard layout from the evdev codes by
      // POSITION: index 2 = BTN_WEST, 3 = BTN_NORTH. The kernel's LETTER
      // codes are the other way round (BTN_X is BTN_NORTH, BTN_Y is
      // BTN_WEST), so a pad that emits Xbox letters — Wolf's virtual pad,
      // 045e:02ea — has X and Y crossed while still reporting "standard"
      // (read on the TV 2026-08-29: Y at 2, X at 3). Keyed on the pad's
      // identity; a pad Firefox has a specific remapper for is straight.
      var crossed = /wolf|x-box|045e/i.test(gp.id || '');
      s.A = P(0); s.B = P(1); s.X = P(crossed ? 3 : 2); s.Y = P(crossed ? 2 : 3);
      s.up = P(12) || ly < -0.5; s.down = P(13) || ly > 0.5; s.left = P(14) || lx < -0.5; s.right = P(15) || lx > 0.5;
    } else {
      s.A = P(0); s.B = P(1); s.Y = P(2); s.X = P(3);
      var hx = ax[6] || 0, hy = ax[7] || 0;
      s.up = P(11) || hy < -0.5 || ly < -0.5; s.down = P(12) || hy > 0.5 || ly > 0.5;
      s.left = P(13) || hx < -0.5 || lx < -0.5; s.right = P(14) || hx > 0.5 || lx > 0.5;
    }
    return s;
  }
  var padLine = '', lastBtn = -1;
  function readout(gp) {
    var line = gp ? ((gp.id || 'pad').replace(/\s*\(.*$/, '').slice(0, 40) + ' · ' + (gp.mapping || 'raw mapping') + (lastBtn >= 0 ? ' · button ' + lastBtn : '')) : '';
    if (line !== padLine) { padLine = line; var el = document.getElementById('pad'); if (el) el.textContent = line; }
  }
  function tick(t) {
    var pads = navigator.getGamepads ? navigator.getGamepads() : [];
    var seen = null;
    for (var i = 0; i < pads.length; i++) {
      var gp = pads[i]; if (!gp) continue;
      seen = gp;
      for (var b = 0; b < gp.buttons.length; b++) if (gp.buttons[b] && gp.buttons[b].pressed) lastBtn = b;
      var s = padState(gp);
      ['A', 'B', 'X', 'Y', 'up', 'down', 'left', 'right'].forEach(function (k) {
        var id = i + ':' + k, isDir = k.length > 1;
        if (s[k]) {
          if (!held[id]) { held[id] = true; repeatAt[id] = t + 320; isDir ? move(k) : press(k); }
          else if (isDir && t >= repeatAt[id]) { repeatAt[id] = t + 140; move(k); }
        } else held[id] = false;
      });
    }
    readout(seen);
    window.requestAnimationFrame(tick);
  }
  window.requestAnimationFrame(tick);

  render();
  poll();
})();
