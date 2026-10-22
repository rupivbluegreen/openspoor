// Open Spoor site: renders benchmark numbers from data/benchmarks.json.
// Never hardcode performance numbers here: missing values render as "—".
(function () {
  'use strict';

  var DASH = '—';
  var nf = new Intl.NumberFormat('en-US', { maximumFractionDigits: 2 });

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function isNum(v) { return typeof v === 'number' && isFinite(v); }

  function fmt(v) {
    if (isNum(v)) return nf.format(v);
    if (typeof v === 'string' && v.trim() !== '') return v;
    return null;
  }

  function fmtDate(v) {
    if (!v) return null;
    var d = new Date(v);
    if (isNaN(d.getTime())) return String(v);
    return d.toLocaleDateString('en-GB', { year: 'numeric', month: 'long', day: 'numeric' });
  }

  function fmtEnv(env) {
    if (!env) return null;
    if (typeof env === 'string') return env;
    if (typeof env === 'object') {
      return Object.keys(env).map(function (k) { return k + ': ' + env[k]; }).join(' · ');
    }
    return String(env);
  }

  function renderHeadline(list) {
    var ul = document.getElementById('stats');
    ul.textContent = '';
    (list || []).forEach(function (h) {
      var li = el('li', 'stat');
      var value = fmt(h.value);
      var v = el('span', 'stat-value');
      v.appendChild(el('span', 'stat-num', value == null ? DASH : value));
      if (value != null && h.unit) v.appendChild(el('span', 'stat-unit', h.unit));
      li.appendChild(v);
      li.appendChild(el('span', 'stat-label', h.label + (value == null && h.unit ? ' (' + h.unit + ')' : '')));
      if (value == null) li.appendChild(el('span', 'pending', 'Benchmark pending'));
      else if (h.detail) li.appendChild(el('span', 'stat-detail', h.detail));
      ul.appendChild(li);
    });
  }

  function renderMeta(data) {
    var env = fmtEnv(data.environment);
    var date = fmtDate(data.measuredAt);
    document.getElementById('bench-env').textContent = env || 'pending';
    document.getElementById('bench-date').textContent = date || 'pending';
    document.getElementById('bench-meta').hidden = false;
  }

  function renderRows(rows) {
    if (!Array.isArray(rows) || rows.length === 0) return;
    var wrap = document.getElementById('bench-rows');
    var tbody = wrap.querySelector('tbody');
    rows.forEach(function (r) {
      var tr = el('tr');
      tr.appendChild(el('td', null, r.name || ''));
      var val = fmt(r.value);
      tr.appendChild(el('td', 'num', val == null ? DASH : val + (r.unit ? ' ' + r.unit : '')));
      tr.appendChild(el('td', null, r.notes || ''));
      tbody.appendChild(tr);
    });
    wrap.hidden = false;
  }

  function renderRepro(text) {
    var code = document.querySelector('#bench-repro code');
    if (Array.isArray(text)) text = text.join('\n');
    code.textContent = text ? String(text) : 'Benchmark instructions pending.';
  }

  function loadBenchmarks() {
    fetch('data/benchmarks.json', { cache: 'no-cache' })
      .then(function (r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      })
      .then(function (data) {
        renderHeadline(data.headline);
        renderMeta(data);
        renderRows(data.rows);
        renderRepro(data.howToReproduce);
      })
      .catch(function () {
        var ul = document.getElementById('stats');
        ul.textContent = '';
        var li = el('li', 'stat');
        li.appendChild(el('span', 'stat-value', DASH));
        li.appendChild(el('span', 'stat-label', 'Benchmark data could not be loaded.'));
        ul.appendChild(li);
      });
  }

  function setupCopy() {
    document.querySelectorAll('button[data-copy]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var src = document.getElementById(btn.getAttribute('data-copy'));
        if (!src || !navigator.clipboard) return;
        navigator.clipboard.writeText(src.textContent).then(function () {
          btn.textContent = 'Copied';
          setTimeout(function () { btn.textContent = 'Copy'; }, 1600);
        }, function () {});
      });
    });
  }

  loadBenchmarks();
  setupCopy();
})();
