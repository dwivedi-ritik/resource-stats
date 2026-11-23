/* stats-lite front end: plain JavaScript, no build step.
   Receives snapshots over Server-Sent Events and keeps two minutes of
   history in memory for the sparkline charts. */
(function () {
  "use strict";

  // WHY: 120 points at the default one-second interval is two minutes, enough
  // to see a spike settle without the chart becoming an unreadable smear.
  var HISTORY = 120;
  // WHY: after 5 seconds without a sample the dot turns amber; the browser's
  // EventSource reconnects on its own, this only tells the person about it.
  var STALE_AFTER_MS = 5000;

  var $ = function (id) { return document.getElementById(id); };
  var history = { cpu: [], mem: [], rx: [], tx: [], at: [] };
  var lastSampleAt = 0;
  var hostInfo = null;

  // ---------- formatting ----------
  var UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];
  function splitBytes(n) {
    n = Number(n) || 0;
    var i = 0;
    while (n >= 1024 && i < UNITS.length - 1) { n /= 1024; i++; }
    var digits = i === 0 ? 0 : (n < 10 ? 2 : (n < 100 ? 1 : 0));
    return { value: n.toFixed(digits), unit: UNITS[i] };
  }
  function fmtBytes(n) { var s = splitBytes(n); return s.value + " " + s.unit; }
  function fmtRate(n) { var s = splitBytes(n); return s.value + " " + s.unit + "/s"; }
  function fmtPct(n) { n = Number(n) || 0; return (n < 10 ? n.toFixed(1) : Math.round(n)) + "%"; }
  function fmtUptime(seconds) {
    var d = Math.floor(seconds / 86400), h = Math.floor(seconds % 86400 / 3600), m = Math.floor(seconds % 3600 / 60);
    if (d > 0) return d + "d " + h + "h";
    if (h > 0) return h + "h " + m + "m";
    return m + "m";
  }
  function pctClass(p) { return p >= 90 ? "bad" : (p >= 75 ? "warn" : ""); }
  function cssVar(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }

  // ---------- charts ----------
  function LineChart(canvas, options) {
    this.canvas = canvas;
    this.ctx = canvas.getContext("2d");
    this.tooltip = canvas.parentNode.querySelector(".tooltip");
    this.series = options.series;        // [{key, colorVar, label}]
    this.fixedMax = options.max || null; // null = auto scale
    this.format = options.format;
    this.hoverIndex = -1;
    this.data = null;
    var self = this;
    canvas.addEventListener("mousemove", function (e) { self.onHover(e.offsetX); });
    canvas.addEventListener("mouseleave", function () { self.hoverIndex = -1; self.tooltip.hidden = true; self.draw(self.data); });
    canvas.addEventListener("touchstart", function (e) { if (e.touches[0]) self.onHover(e.touches[0].clientX - canvas.getBoundingClientRect().left); }, { passive: true });
  }
  LineChart.prototype.onHover = function (x) {
    if (!this.data) return;
    var width = this.canvas.clientWidth;
    var n = HISTORY;
    this.hoverIndex = Math.max(0, Math.min(n - 1, Math.round(x / width * (n - 1))));
    this.draw(this.data);
  };
  LineChart.prototype.draw = function (data) {
    this.data = data;
    if (!data) return;
    var canvas = this.canvas, ctx = this.ctx;
    var dpr = window.devicePixelRatio || 1;
    var width = canvas.clientWidth, height = canvas.clientHeight;
    if (width === 0 || height === 0) return;
    if (canvas.width !== Math.round(width * dpr) || canvas.height !== Math.round(height * dpr)) {
      canvas.width = Math.round(width * dpr);
      canvas.height = Math.round(height * dpr);
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);

    var pad = 3;
    var plotH = height - pad * 2;
    var max = this.fixedMax;
    if (!max) {
      max = 0;
      for (var s = 0; s < this.series.length; s++) {
        var arr = data[this.series[s].key];
        for (var i = 0; i < arr.length; i++) if (arr[i] > max) max = arr[i];
      }
      // WHY: a floor of 10 KB/s keeps an idle network from rendering noise as
      // a dramatic mountain range; headroom of 15% keeps peaks off the edge.
      max = Math.max(max * 1.15, 10 * 1024);
    }
    var stepX = width / (HISTORY - 1);

    // recessive midline so 50% has a reference without a full grid
    ctx.strokeStyle = cssVar("--track");
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(0, Math.round(pad + plotH / 2) + 0.5);
    ctx.lineTo(width, Math.round(pad + plotH / 2) + 0.5);
    ctx.stroke();

    for (s = 0; s < this.series.length; s++) {
      var spec = this.series[s];
      var color = cssVar(spec.colorVar);
      arr = data[spec.key];
      if (!arr.length) continue;
      var offset = HISTORY - arr.length;
      var points = [];
      for (i = 0; i < arr.length; i++) {
        points.push([(offset + i) * stepX, pad + plotH - Math.min(1, arr[i] / max) * plotH]);
      }
      // line, 2px round joins
      ctx.beginPath();
      ctx.lineWidth = 2;
      ctx.lineJoin = "round";
      ctx.lineCap = "round";
      ctx.strokeStyle = color;
      tracePath(ctx, points);
      ctx.stroke();
      // area wash at ~10% opacity
      ctx.lineTo(points[points.length - 1][0], height);
      ctx.lineTo(points[0][0], height);
      ctx.closePath();
      ctx.globalAlpha = 0.1;
      ctx.fillStyle = color;
      ctx.fill();
      ctx.globalAlpha = 1;
    }

    if (this.hoverIndex >= 0) this.drawHover(data, max, pad, plotH, stepX, width, height);
  };
  LineChart.prototype.drawHover = function (data, max, pad, plotH, stepX, width, height) {
    var ctx = this.ctx, idx = this.hoverIndex;
    var x = idx * stepX;
    ctx.strokeStyle = cssVar("--text-3");
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(Math.round(x) + 0.5, 0);
    ctx.lineTo(Math.round(x) + 0.5, height);
    ctx.stroke();
    var lines = [];
    var any = false;
    for (var s = 0; s < this.series.length; s++) {
      var spec = this.series[s];
      var arr = data[spec.key];
      var ai = idx - (HISTORY - arr.length);
      if (ai < 0 || ai >= arr.length) continue;
      any = true;
      var y = pad + plotH - Math.min(1, arr[ai] / max) * plotH;
      ctx.beginPath();
      ctx.arc(x, y, 4, 0, Math.PI * 2);
      ctx.fillStyle = cssVar("--surface");
      ctx.fill();
      ctx.beginPath();
      ctx.arc(x, y, 3, 0, Math.PI * 2);
      ctx.fillStyle = cssVar(spec.colorVar);
      ctx.fill();
      lines.push((this.series.length > 1 ? spec.label + " " : "") + "<b>" + this.format(arr[ai]) + "</b>");
    }
    if (!any) { this.tooltip.hidden = true; return; }
    var agoIdx = HISTORY - 1 - idx;
    var at = data.at[data.at.length - 1 - agoIdx];
    var when = at ? new Date(at).toLocaleTimeString() : (agoIdx + "s ago");
    this.tooltip.innerHTML = lines.join("<br>") + "<br><span style='opacity:.7'>" + when + "</span>";
    this.tooltip.hidden = false;
    var tipW = this.tooltip.offsetWidth;
    var left = Math.max(tipW / 2, Math.min(width - tipW / 2, x));
    this.tooltip.style.left = left + "px";
  };
  function tracePath(ctx, points) {
    ctx.moveTo(points[0][0], points[0][1]);
    for (var i = 1; i < points.length; i++) {
      var p = points[i - 1], c = points[i];
      var mx = (p[0] + c[0]) / 2;
      // WHY: a midpoint bezier smooths second-to-second jitter without
      // overshooting past real values the way Catmull-Rom splines can.
      ctx.bezierCurveTo(mx, p[1], mx, c[1], c[0], c[1]);
    }
  }

  var cpuChart = new LineChart($("cpu-chart"), { series: [{ key: "cpu", colorVar: "--cpu", label: "CPU" }], max: 100, format: fmtPct });
  var memChart = new LineChart($("mem-chart"), { series: [{ key: "mem", colorVar: "--mem", label: "Memory" }], max: 100, format: fmtPct });
  var netChart = new LineChart($("net-chart"), { series: [{ key: "rx", colorVar: "--in", label: "In" }, { key: "tx", colorVar: "--out", label: "Out" }], format: fmtRate });

  function push(key, value) {
    var arr = history[key];
    arr.push(value);
    if (arr.length > HISTORY) arr.shift();
  }

  // ---------- rendering ----------
  var coreEls = [];
  function renderCores(perCore) {
    var box = $("cores");
    if (coreEls.length !== perCore.length) {
      box.innerHTML = "";
      coreEls = perCore.map(function (_, i) {
        var el = document.createElement("div");
        el.className = "core";
        el.title = "Core " + (i + 1);
        el.appendChild(document.createElement("i"));
        box.appendChild(el);
        return el;
      });
    }
    for (var i = 0; i < perCore.length; i++) {
      coreEls[i].firstChild.style.height = Math.max(2, Math.min(100, perCore[i])) + "%";
      coreEls[i].title = "Core " + (i + 1) + ": " + fmtPct(perCore[i]);
    }
  }

  function renderHost(h) {
    if (!hostInfo || hostInfo.hostname !== h.hostname) {
      document.title = (h.hostname || "stats-lite") + " · stats-lite";
    }
    hostInfo = h;
    $("hostname").textContent = h.hostname || "this machine";
    var os = [h.platform, h.platform_version].filter(Boolean).join(" ");
    if (!os) os = h.os;
    $("host-subtitle").textContent = [os, h.arch, "up " + fmtUptime(h.uptime_seconds)].filter(Boolean).join("  ·  ");
    var cores = h.logical_cores + (h.physical_cores && h.physical_cores !== h.logical_cores ? " threads · " + h.physical_cores + " cores" : " cores");
    $("cpu-meta").textContent = cores;
    $("cpu-meta").title = h.cpu_model || "";
    var foot = [];
    if (h.cpu_model) foot.push(h.cpu_model);
    if (h.load1 || h.load5 || h.load15) foot.push("Load " + h.load1.toFixed(2) + " · " + h.load5.toFixed(2) + " · " + h.load15.toFixed(2));
    if (h.processes) foot.push(h.processes + " processes");
    $("cpu-foot").textContent = foot.join("   ·   ");
  }

  function renderMemory(m) {
    $("mem-value").textContent = m.used_percent < 10 ? m.used_percent.toFixed(1) : Math.round(m.used_percent);
    $("mem-meta").textContent = fmtBytes(m.used) + " of " + fmtBytes(m.total);
    $("mem-used").textContent = fmtBytes(m.used);
    $("mem-available").textContent = fmtBytes(m.available);
    $("mem-total").textContent = fmtBytes(m.total);
    var swapRow = $("swap-row");
    if (m.swap_total > 0) {
      swapRow.hidden = false;
      $("swap-text").textContent = fmtBytes(m.swap_used) + " of " + fmtBytes(m.swap_total);
      $("swap-fill").style.width = Math.min(100, m.swap_percent) + "%";
      $("swap-fill").className = "bar-fill mem " + pctClass(m.swap_percent);
    } else {
      swapRow.hidden = true;
    }
  }

  function renderNetwork(n) {
    var rx = splitBytes(n.rx_bytes_per_sec), tx = splitBytes(n.tx_bytes_per_sec);
    $("net-rx").textContent = rx.value; $("net-rx-unit").textContent = rx.unit + "/s";
    $("net-tx").textContent = tx.value; $("net-tx-unit").textContent = tx.unit + "/s";
    var box = $("interfaces");
    // WHY: five rows cover a home server (LAN, Wi-Fi, a bridge or two);
    // more becomes a wall of idle virtual interfaces.
    var ifaces = n.interfaces.slice(0, 5);
    syncRows(box, ifaces, function (i) { return i.name; }, function (i) {
      return '<span class="name"></span><span class="val"></span><span class="sub"></span><span class="sub right"></span>';
    }, function (el, i) {
      el.children[0].textContent = i.name;
      el.children[1].textContent = "↓ " + fmtRate(i.rx_bytes_per_sec) + "   ↑ " + fmtRate(i.tx_bytes_per_sec);
      el.children[2].textContent = "total";
      el.children[3].textContent = "↓ " + fmtBytes(i.rx_total) + "   ↑ " + fmtBytes(i.tx_total);
    });
  }

  function renderDisks(disks, io) {
    var meta = $("disk-io");
    meta.textContent = io && io.available ? "read " + fmtRate(io.read_bytes_per_sec) + "  ·  write " + fmtRate(io.write_bytes_per_sec) : "";
    var box = $("disks");
    if (!disks.length) { box.innerHTML = '<div class="empty">No filesystems reported.</div>'; return; }
    syncRows(box, disks, function (d) { return d.mountpoint; }, function () {
      return '<span class="name"></span><span class="val"></span><div class="bar"><div class="bar-fill"></div></div>';
    }, function (el, d) {
      el.children[0].textContent = d.mountpoint;
      el.children[0].title = d.device + " (" + d.fstype + ")";
      el.children[1].textContent = fmtBytes(d.used) + " of " + fmtBytes(d.total) + "  ·  " + fmtPct(d.used_percent);
      var fill = el.children[2].firstChild;
      fill.style.width = Math.min(100, d.used_percent) + "%";
      fill.className = "bar-fill " + pctClass(d.used_percent);
    });
  }

  function batteryStateText(b) {
    switch (b.state) {
      case "charging": return "Charging";
      case "discharging": return "On battery";
      case "full": return "Plugged in, fully charged";
      case "idle": return "Plugged in, holding charge";
      case "empty": return "Empty";
      default: return b.plugged_in ? "Plugged in" : "Unknown state";
    }
  }
  function fmtDuration(seconds) {
    var h = Math.floor(seconds / 3600), m = Math.round(seconds % 3600 / 60);
    if (h === 0) return m + " min";
    return h + "h " + (m < 10 ? "0" + m : m) + "m";
  }
  function fmtWh(mwh) { return (mwh / 1000).toFixed(mwh < 10000 ? 1 : 0) + " Wh"; }

  function renderBattery(bs) {
    var card = $("battery-card");
    if (!bs || !bs.available || !bs.batteries.length) { card.hidden = true; return; }
    card.hidden = false;
    var b = bs.batteries[0];
    $("battery-value").textContent = Math.round(b.percent);
    var meta = [];
    if (b.cycle_count > 0) meta.push(b.cycle_count + " cycles");
    if (bs.batteries.length > 1) meta.push(bs.batteries.length + " batteries");
    $("battery-meta").textContent = meta.join(" · ");
    var fill = $("battery-fill");
    fill.style.width = Math.min(100, b.percent) + "%";
    // WHY: the colour follows the charge level only while unplugged; a low
    // battery that is charging is not an emergency.
    fill.className = "bar-fill battery" + (!b.plugged_in && b.percent <= 15 ? " low" : (!b.plugged_in && b.percent <= 30 ? " mid" : ""));
    $("battery-state").textContent = batteryStateText(b);
    var time = "";
    if (b.time_remaining_seconds > 0) {
      time = fmtDuration(b.time_remaining_seconds) + (b.state === "charging" ? " until full" : " left");
    }
    $("battery-time").textContent = time;
    $("battery-power").textContent = b.power_watts > 0 ? b.power_watts.toFixed(1) + " W" : "—";
    $("battery-health").textContent = b.health_percent > 0 ? Math.round(b.health_percent) + "%" : "—";
    $("battery-capacity").textContent = b.full_capacity_mwh > 0 ? fmtWh(b.current_capacity_mwh) + " of " + fmtWh(b.full_capacity_mwh) : "—";
    var extra = $("battery-extra");
    var rest = bs.batteries.slice(1);
    extra.hidden = rest.length === 0;
    syncRows(extra, rest, function (_, i) { return "battery-" + i; }, function () {
      return '<span class="name"></span><span class="val"></span><div class="bar"><div class="bar-fill battery"></div></div>';
    }, function (el, x, i) {
      el.children[0].textContent = "Battery " + (i + 2);
      el.children[1].textContent = Math.round(x.percent) + "% · " + batteryStateText(x);
      el.children[2].firstChild.style.width = Math.min(100, x.percent) + "%";
    });
  }

  function renderDocker(d) {
    var empty = $("docker-empty"), table = $("containers"), meta = $("docker-meta");
    if (!d.available) {
      table.hidden = true;
      empty.hidden = false;
      empty.innerHTML = "<div>Docker is not reachable.</div><div class='hint'></div>";
      empty.lastChild.textContent = d.error || "";
      meta.textContent = "";
      return;
    }
    var running = d.containers.filter(function (c) { return c.state === "running"; }).length;
    meta.textContent = running + " running · " + d.containers.length + " total";
    if (!d.containers.length) {
      table.hidden = true;
      empty.hidden = false;
      empty.innerHTML = "<div>No containers.</div>";
      return;
    }
    empty.hidden = true;
    table.hidden = false;
    var tbody = table.tBodies[0];
    syncRows(tbody, d.containers, function (c) { return c.id; }, function () {
      return '<td><div class="cname"></div><div class="cimage"></div></td>' +
        '<td><span class="pill"></span></td>' +
        '<td class="num"></td>' +
        '<td><div class="memcell"><span></span><div class="bar"><div class="bar-fill mem"></div></div></div></td>' +
        '<td class="num"></td><td class="num"></td><td class="num"></td><td class="num"></td>';
    }, function (tr, c) {
      var running = c.state === "running";
      tr.className = running ? "" : "stopped";
      tr.cells[0].children[0].textContent = c.name;
      tr.cells[0].children[0].title = c.name;
      tr.cells[0].children[1].textContent = c.image;
      tr.cells[0].children[1].title = c.image;
      var pill = tr.cells[1].firstChild;
      pill.className = "pill " + c.state;
      pill.textContent = c.status || c.state;
      setNum(tr.cells[2], running ? fmtPct(c.cpu_percent) : "—", running && c.cpu_percent >= 1);
      var memcell = tr.cells[3].firstChild;
      if (running && c.memory_limit) {
        memcell.children[0].textContent = fmtBytes(c.memory_usage) + " of " + fmtBytes(c.memory_limit);
        memcell.children[1].firstChild.style.width = Math.min(100, c.memory_percent) + "%";
        memcell.children[1].hidden = false;
      } else {
        memcell.children[0].textContent = running ? fmtBytes(c.memory_usage) : "—";
        memcell.children[1].hidden = true;
      }
      setNum(tr.cells[4], running ? fmtRate(c.rx_bytes_per_sec) : "—", running && c.rx_bytes_per_sec > 1024);
      setNum(tr.cells[5], running ? fmtRate(c.tx_bytes_per_sec) : "—", running && c.tx_bytes_per_sec > 1024);
      setNum(tr.cells[6], running ? fmtBytes(c.block_read) : "—", false);
      setNum(tr.cells[7], running ? fmtBytes(c.block_write) : "—", false);
    }, "tr");
  }
  function setNum(cell, text, active) {
    cell.textContent = text;
    cell.className = "num" + (active ? " active" : "");
  }

  // syncRows keeps one element per item keyed by id so updates do not rebuild
  // the DOM every second (which would break text selection and hover).
  function syncRows(container, items, keyOf, template, update, tag) {
    tag = tag || "div";
    var existing = {};
    Array.prototype.forEach.call(container.children, function (el) { existing[el.dataset.key] = el; });
    var seen = {};
    items.forEach(function (item, index) {
      var key = keyOf(item, index);
      seen[key] = true;
      var el = existing[key];
      if (!el) {
        el = document.createElement(tag);
        el.dataset.key = key;
        if (tag === "div") el.className = "row";
        el.innerHTML = template(item);
      }
      if (container.children[index] !== el) container.insertBefore(el, container.children[index] || null);
      update(el, item, index);
    });
    Object.keys(existing).forEach(function (key) { if (!seen[key]) container.removeChild(existing[key]); });
  }

  function render(snap) {
    lastSampleAt = Date.now();
    setStatus("live", "live");
    push("cpu", snap.cpu.total_percent);
    push("mem", snap.memory.used_percent);
    push("rx", snap.network.rx_bytes_per_sec);
    push("tx", snap.network.tx_bytes_per_sec);
    push("at", snap.timestamp);

    renderHost(snap.host);
    $("cpu-value").textContent = snap.cpu.total_percent < 10 ? snap.cpu.total_percent.toFixed(1) : Math.round(snap.cpu.total_percent);
    renderCores(snap.cpu.per_core || []);
    renderMemory(snap.memory);
    renderNetwork(snap.network);
    renderDisks(snap.disks || [], snap.disk_io);
    renderBattery(snap.battery);
    renderDocker(snap.docker || { available: false, error: "", containers: [] });
    cpuChart.draw(history);
    memChart.draw(history);
    netChart.draw(history);
    $("foot-text").textContent = "Updated " + new Date(snap.timestamp).toLocaleTimeString();
  }

  function setStatus(cls, text) {
    var el = $("status");
    el.className = "status " + cls;
    $("status-text").textContent = text;
  }

  // ---------- transport ----------
  function connect() {
    if (!window.EventSource) { pollLoop(); return; }
    var source = new EventSource("api/stream");
    source.onmessage = function (e) {
      try { render(JSON.parse(e.data)); } catch (err) { console.error("bad snapshot", err); }
    };
    source.onerror = function () { setStatus("down", "reconnecting"); };
  }
  function pollLoop() {
    fetch("api/snapshot").then(function (r) { return r.json(); }).then(render).catch(function () { setStatus("down", "offline"); });
    setTimeout(pollLoop, 1000);
  }
  setInterval(function () {
    if (lastSampleAt && Date.now() - lastSampleAt > STALE_AFTER_MS && $("status").classList.contains("live")) {
      setStatus("stale", "no data for " + Math.round((Date.now() - lastSampleAt) / 1000) + "s");
    }
  }, 1000);
  window.addEventListener("resize", function () { cpuChart.draw(history); memChart.draw(history); netChart.draw(history); });

  // ---------- theme ----------
  var THEMES = ["auto", "light", "dark"];
  function applyTheme(theme) {
    if (theme === "auto") document.documentElement.removeAttribute("data-theme");
    else document.documentElement.setAttribute("data-theme", theme);
    $("theme-toggle").title = "Theme: " + theme + " (click to change)";
    try { localStorage.setItem("stats-lite-theme", theme); } catch (e) { /* private mode */ }
    cpuChart.draw(history); memChart.draw(history); netChart.draw(history);
  }
  var saved = "auto";
  try { saved = localStorage.getItem("stats-lite-theme") || "auto"; } catch (e) { /* ignore */ }
  if (THEMES.indexOf(saved) === -1) saved = "auto";
  applyTheme(saved);
  $("theme-toggle").addEventListener("click", function () {
    saved = THEMES[(THEMES.indexOf(saved) + 1) % THEMES.length];
    applyTheme(saved);
  });

  connect();
})();
