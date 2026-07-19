/* OutReachCRM business snapshot — D3 charts (geo / treemap / leads / word cloud) */
(function () {
  "use strict";

  var TEAL = "#0f8f7b";
  var TEAL_SOFT = "#7ec8bb";
  var INK = "#12241f";
  var MUTED = "#5c726b";
  var BORDER = "#e4ebe7";
  var AMBER = "#d97706";
  var ROSE = "#c23b45";
  var PALETTE = [
    "#0f8f7b", "#1a6b8a", "#d97706", "#b45309", "#0f8a5f",
    "#c23b45", "#5c726b", "#14302b", "#2a9d8f", "#e9c46a"
  ];

  var data = window.__DASH_SNAPSHOT__ || {};

  function el(id) {
    return document.getElementById(id);
  }

  function sizeOf(node) {
    var r = node.getBoundingClientRect();
    return { width: Math.max(280, r.width || 320), height: Math.max(200, r.height || 240) };
  }

  function empty(sel, msg) {
    d3.select(sel).selectAll("*").remove();
    d3.select(sel)
      .append("div")
      .attr("class", "dash-empty")
      .text(msg || "No data yet — import leads or run a campaign.");
  }

  function tip() {
    var t = d3.select("body").selectAll(".dash-tip").data([0]);
    t = t.enter().append("div").attr("class", "dash-tip").merge(t);
    return t;
  }

  function showTip(html, ev) {
    tip()
      .style("opacity", 1)
      .html(html)
      .style("left", (ev.clientX + 12) + "px")
      .style("top", (ev.clientY + 12) + "px");
  }

  function hideTip() {
    tip().style("opacity", 0);
  }

  /* —— World map bubbles (Walmart-growth style) —— */
  function drawGeo() {
    var host = el("chart-geo");
    if (!host) return;
    var locs = data.locations || [];
    if (!locs.length) {
      empty(host, "No location signals yet (website / email TLD).");
      return;
    }
    var dim = sizeOf(host);
    var width = dim.width;
    var height = Math.max(260, dim.height);

    d3.select(host).selectAll("*").remove();
    var svg = d3.select(host).append("svg").attr("viewBox", [0, 0, width, height]);

    var projection = d3.geoEqualEarth().fitExtent(
      [[12, 12], [width - 12, height - 12]],
      { type: "Sphere" }
    );
    var path = d3.geoPath(projection);

    d3.json("https://cdn.jsdelivr.net/npm/world-atlas@2/land-110m.json")
      .then(function (world) {
        var land = topojson.feature(world, world.objects.land);
        svg.append("path")
          .datum({ type: "Sphere" })
          .attr("fill", "#f3f7f6")
          .attr("stroke", BORDER)
          .attr("d", path);
        svg.append("path")
          .datum(land)
          .attr("fill", "#dce8e4")
          .attr("stroke", "#c5d4cf")
          .attr("stroke-width", 0.4)
          .attr("d", path);

        var max = d3.max(locs, function (d) { return d.count; }) || 1;
        var r = d3.scaleSqrt().domain([0, max]).range([3, 28]);

        svg.append("g")
          .selectAll("circle")
          .data(locs)
          .join("circle")
          .attr("cx", function (d) { return projection([d.lng, d.lat])[0]; })
          .attr("cy", function (d) { return projection([d.lng, d.lat])[1]; })
          .attr("r", 0)
          .attr("fill", TEAL)
          .attr("fill-opacity", 0.55)
          .attr("stroke", "#0a5f52")
          .attr("stroke-width", 0.8)
          .style("cursor", "default")
          .on("mousemove", function (ev, d) {
            showTip("<strong>" + d.country + "</strong><br>" + d.count + " leads · ." + d.code, ev);
          })
          .on("mouseleave", hideTip)
          .transition()
          .duration(700)
          .delay(function (_, i) { return i * 28; })
          .attr("r", function (d) { return r(d.count); });
      })
      .catch(function () {
        // Fallback: scatter without basemap
        var x = d3.scaleLinear().domain([-180, 180]).range([20, width - 20]);
        var y = d3.scaleLinear().domain([-60, 80]).range([height - 20, 20]);
        var max = d3.max(locs, function (d) { return d.count; }) || 1;
        var r = d3.scaleSqrt().domain([0, max]).range([4, 26]);
        svg.selectAll("circle")
          .data(locs)
          .join("circle")
          .attr("cx", function (d) { return x(d.lng); })
          .attr("cy", function (d) { return y(d.lat); })
          .attr("r", function (d) { return r(d.count); })
          .attr("fill", TEAL)
          .attr("fill-opacity", 0.6)
          .on("mousemove", function (ev, d) {
            showTip("<strong>" + d.country + "</strong><br>" + d.count + " leads", ev);
          })
          .on("mouseleave", hideTip);
      });
  }

  /* —— Prev vs new leads area chart —— */
  function drawLeads() {
    var host = el("chart-leads");
    if (!host) return;
    var days = data.leadDays || [];
    if (!days.length) {
      empty(host, "No lead activity in the last 90 days.");
      return;
    }
    var dim = sizeOf(host);
    var margin = { top: 16, right: 16, bottom: 28, left: 36 };
    var width = dim.width;
    var height = Math.max(240, dim.height);
    var iw = width - margin.left - margin.right;
    var ih = height - margin.top - margin.bottom;

    d3.select(host).selectAll("*").remove();
    var svg = d3.select(host).append("svg").attr("viewBox", [0, 0, width, height]);
    var g = svg.append("g").attr("transform", "translate(" + margin.left + "," + margin.top + ")");

    var parse = d3.timeParse("%Y-%m-%d");
    var series = days.map(function (d) {
      return { date: parse(d.day), count: d.count || 0, prev: d.prev || 0 };
    });

    var x = d3.scaleTime()
      .domain(d3.extent(series, function (d) { return d.date; }))
      .range([0, iw]);
    var ymax = d3.max(series, function (d) { return Math.max(d.count, d.prev); }) || 1;
    var y = d3.scaleLinear().domain([0, ymax * 1.1]).nice().range([ih, 0]);

    g.append("g")
      .attr("transform", "translate(0," + ih + ")")
      .call(d3.axisBottom(x).ticks(6).tickSizeOuter(0))
      .call(function (sel) {
        sel.selectAll("text").attr("fill", MUTED).attr("font-size", 10);
        sel.selectAll("line,path").attr("stroke", BORDER);
      });
    g.append("g")
      .call(d3.axisLeft(y).ticks(4).tickSizeOuter(0))
      .call(function (sel) {
        sel.selectAll("text").attr("fill", MUTED).attr("font-size", 10);
        sel.selectAll("line,path").attr("stroke", BORDER);
      });

    var area = function (key) {
      return d3.area()
        .x(function (d) { return x(d.date); })
        .y0(ih)
        .y1(function (d) { return y(d[key]); })
        .curve(d3.curveMonotoneX);
    };
    var line = function (key) {
      return d3.line()
        .x(function (d) { return x(d.date); })
        .y(function (d) { return y(d[key]); })
        .curve(d3.curveMonotoneX);
    };

    g.append("path").datum(series).attr("fill", "rgba(92,114,107,0.18)").attr("d", area("prev"));
    g.append("path").datum(series).attr("fill", "none").attr("stroke", MUTED)
      .attr("stroke-width", 1.5).attr("stroke-dasharray", "4 3").attr("d", line("prev"));

    g.append("path").datum(series).attr("fill", "rgba(15,143,123,0.28)").attr("d", area("count"));
    g.append("path").datum(series).attr("fill", "none").attr("stroke", TEAL)
      .attr("stroke-width", 2.2).attr("d", line("count"));

    var legend = g.append("g").attr("transform", "translate(" + (iw - 150) + ",0)");
    legend.append("line").attr("x1", 0).attr("x2", 18).attr("y1", 6).attr("y2", 6)
      .attr("stroke", TEAL).attr("stroke-width", 2);
    legend.append("text").attr("x", 22).attr("y", 9).attr("fill", INK).attr("font-size", 11).text("New leads");
    legend.append("line").attr("x1", 0).attr("x2", 18).attr("y1", 22).attr("y2", 22)
      .attr("stroke", MUTED).attr("stroke-width", 1.5).attr("stroke-dasharray", "4 3");
    legend.append("text").attr("x", 22).attr("y", 25).attr("fill", MUTED).attr("font-size", 11).text("Prior window");
  }

  /* —— Campaign treemap —— */
  function drawTreemap() {
    var host = el("chart-treemap");
    if (!host) return;
    var camps = (data.campaigns || []).filter(function (c) {
      return (c.enrolled || 0) + (c.sent || 0) > 0;
    });
    if (!camps.length) {
      empty(host, "No campaign enrollment yet.");
      return;
    }
    var dim = sizeOf(host);
    var width = dim.width;
    var height = Math.max(280, dim.height);

    d3.select(host).selectAll("*").remove();
    var root = d3.hierarchy({
      children: camps.map(function (c) {
        return {
          name: c.name,
          value: Math.max(1, c.enrolled || c.sent || 1),
          sent: c.sent || 0,
          queued: c.queued || 0,
          status: c.status || ""
        };
      })
    }).sum(function (d) { return d.value; }).sort(function (a, b) { return b.value - a.value; });

    d3.treemap().size([width, height]).paddingInner(3).paddingOuter(2).round(true)(root);

    var svg = d3.select(host).append("svg").attr("viewBox", [0, 0, width, height]);
    var color = d3.scaleOrdinal().range(PALETTE);

    var leaf = svg.selectAll("g")
      .data(root.leaves())
      .join("g")
      .attr("transform", function (d) { return "translate(" + d.x0 + "," + d.y0 + ")"; });

    leaf.append("rect")
      .attr("width", function (d) { return Math.max(0, d.x1 - d.x0); })
      .attr("height", function (d) { return Math.max(0, d.y1 - d.y0); })
      .attr("rx", 6)
      .attr("fill", function (d, i) { return color(i); })
      .attr("fill-opacity", 0.88)
      .on("mousemove", function (ev, d) {
        showTip(
          "<strong>" + d.data.name + "</strong><br>" +
          d.data.value + " enrolled · " + d.data.sent + " sent · " + d.data.queued + " queued",
          ev
        );
      })
      .on("mouseleave", hideTip);

    leaf.append("text")
      .attr("x", 8)
      .attr("y", 18)
      .attr("fill", "#fff")
      .attr("font-size", 11)
      .attr("font-weight", 650)
      .text(function (d) {
        var w = d.x1 - d.x0;
        if (w < 48 || d.y1 - d.y0 < 22) return "";
        var n = d.data.name || "";
        return n.length > Math.floor(w / 7) ? n.slice(0, Math.floor(w / 7) - 1) + "…" : n;
      });
  }

  /* —— Horizontal stacked bars: sent vs queued —— */
  function drawSchedule() {
    var host = el("chart-schedule");
    if (!host) return;
    var camps = (data.campaigns || []).slice(0, 10);
    if (!camps.length) {
      empty(host, "No campaigns to chart.");
      return;
    }
    var dim = sizeOf(host);
    var margin = { top: 8, right: 16, bottom: 20, left: 110 };
    var width = dim.width;
    var height = Math.max(280, dim.height);
    var iw = width - margin.left - margin.right;
    var ih = height - margin.top - margin.bottom;

    d3.select(host).selectAll("*").remove();
    var svg = d3.select(host).append("svg").attr("viewBox", [0, 0, width, height]);
    var g = svg.append("g").attr("transform", "translate(" + margin.left + "," + margin.top + ")");

    var y = d3.scaleBand()
      .domain(camps.map(function (d) { return d.name; }))
      .range([0, ih])
      .padding(0.28);
    var xmax = d3.max(camps, function (d) { return (d.sent || 0) + (d.queued || 0); }) || 1;
    var x = d3.scaleLinear().domain([0, xmax]).nice().range([0, iw]);

    g.append("g")
      .call(d3.axisLeft(y).tickSize(0))
      .call(function (sel) {
        sel.select(".domain").remove();
        sel.selectAll("text").attr("fill", MUTED).attr("font-size", 10)
          .text(function (t) { return t.length > 16 ? t.slice(0, 15) + "…" : t; });
      });
    g.append("g")
      .attr("transform", "translate(0," + ih + ")")
      .call(d3.axisBottom(x).ticks(4).tickSizeOuter(0))
      .call(function (sel) {
        sel.selectAll("text").attr("fill", MUTED).attr("font-size", 10);
        sel.selectAll("line,path").attr("stroke", BORDER);
      });

    g.selectAll(".sent")
      .data(camps)
      .join("rect")
      .attr("class", "sent")
      .attr("y", function (d) { return y(d.name); })
      .attr("height", y.bandwidth())
      .attr("x", 0)
      .attr("width", 0)
      .attr("rx", 3)
      .attr("fill", TEAL)
      .transition().duration(600)
      .attr("width", function (d) { return x(d.sent || 0); });

    g.selectAll(".queued")
      .data(camps)
      .join("rect")
      .attr("class", "queued")
      .attr("y", function (d) { return y(d.name); })
      .attr("height", y.bandwidth())
      .attr("x", function (d) { return x(d.sent || 0); })
      .attr("width", 0)
      .attr("rx", 3)
      .attr("fill", TEAL_SOFT)
      .transition().duration(600)
      .attr("width", function (d) { return x(d.queued || 0); });
  }

  /* —— Category donut —— */
  function drawCategories() {
    var host = el("chart-categories");
    if (!host) return;
    var cats = data.categories || [];
    if (!cats.length) {
      empty(host, "No categories tagged yet.");
      return;
    }
    var dim = sizeOf(host);
    var width = dim.width;
    var height = Math.max(260, dim.height);
    var r = Math.min(width, height) / 2 - 8;

    d3.select(host).selectAll("*").remove();
    var svg = d3.select(host).append("svg").attr("viewBox", [0, 0, width, height]);
    var g = svg.append("g").attr("transform", "translate(" + (width * 0.38) + "," + (height / 2) + ")");

    var pie = d3.pie().value(function (d) { return d.count; }).sort(null);
    var arc = d3.arc().innerRadius(r * 0.55).outerRadius(r);
    var color = d3.scaleOrdinal().range(PALETTE);

    g.selectAll("path")
      .data(pie(cats))
      .join("path")
      .attr("fill", function (d, i) { return color(i); })
      .attr("d", arc)
      .attr("stroke", "#fff")
      .attr("stroke-width", 1.5)
      .on("mousemove", function (ev, d) {
        showTip("<strong>" + d.data.name + "</strong><br>" + d.data.count + " leads", ev);
      })
      .on("mouseleave", hideTip);

    var total = d3.sum(cats, function (d) { return d.count; });
    g.append("text")
      .attr("text-anchor", "middle")
      .attr("dy", "-0.2em")
      .attr("fill", INK)
      .attr("font-size", 22)
      .attr("font-weight", 750)
      .text(total);
    g.append("text")
      .attr("text-anchor", "middle")
      .attr("dy", "1.2em")
      .attr("fill", MUTED)
      .attr("font-size", 11)
      .text("leads");

    var legend = svg.append("g").attr("transform", "translate(" + (width * 0.68) + ",24)");
    cats.slice(0, 8).forEach(function (c, i) {
      var row = legend.append("g").attr("transform", "translate(0," + (i * 18) + ")");
      row.append("rect").attr("width", 10).attr("height", 10).attr("rx", 2).attr("fill", color(i));
      row.append("text").attr("x", 16).attr("y", 9).attr("fill", MUTED).attr("font-size", 11)
        .text((c.name.length > 14 ? c.name.slice(0, 13) + "…" : c.name) + " " + c.count);
    });
  }

  /* —— Word cloud —— */
  function drawWords() {
    var host = el("chart-words");
    if (!host) return;
    var words = data.words || [];
    if (!words.length || typeof d3.layout === "undefined" || !d3.layout.cloud) {
      empty(host, words.length ? "Word cloud unavailable." : "Not enough text for a cloud yet.");
      return;
    }
    var dim = sizeOf(host);
    var width = dim.width;
    var height = Math.max(280, dim.height);
    var max = d3.max(words, function (d) { return d.value; }) || 1;
    var size = d3.scaleSqrt().domain([1, max]).range([12, 42]);

    d3.select(host).selectAll("*").remove();

    d3.layout.cloud()
      .size([width, height])
      .words(words.map(function (d) {
        return { text: d.text, value: d.value, size: size(d.value) };
      }))
      .padding(3)
      .rotate(function () { return 0; })
      .font("Figtree, sans-serif")
      .fontSize(function (d) { return d.size; })
      .on("end", function (layout) {
        var svg = d3.select(host).append("svg").attr("viewBox", [0, 0, width, height]);
        var color = d3.scaleOrdinal().range(PALETTE);
        svg.append("g")
          .attr("transform", "translate(" + (width / 2) + "," + (height / 2) + ")")
          .selectAll("text")
          .data(layout)
          .join("text")
          .style("font-size", function (d) { return d.size + "px"; })
          .style("font-family", "Figtree, sans-serif")
          .style("font-weight", 650)
          .attr("text-anchor", "middle")
          .attr("fill", function (d, i) { return color(i % PALETTE.length); })
          .attr("transform", function (d) {
            return "translate(" + [d.x, d.y] + ")rotate(" + d.rotate + ")";
          })
          .text(function (d) { return d.text; })
          .style("opacity", 0)
          .transition().duration(500).delay(function (_, i) { return i * 12; })
          .style("opacity", 1);
      })
      .start();
  }

  /* —— Reply intents bars + sources legend —— */
  function drawIntents() {
    var host = el("chart-intents");
    if (!host) return;
    var intents = data.intents || [];
    var sources = data.sources || [];

    if (!intents.length && !sources.length) {
      empty(host, "No replies or sources yet.");
      return;
    }

    d3.select(host).selectAll("*").remove();

    if (intents.length) {
      var dim = sizeOf(host);
      var margin = { top: 8, right: 12, bottom: 20, left: 80 };
      var width = dim.width;
      var height = Math.min(220, Math.max(160, intents.length * 28 + 40));
      var iw = width - margin.left - margin.right;
      var ih = height - margin.top - margin.bottom;

      var svg = d3.select(host).append("svg").attr("viewBox", [0, 0, width, height]);
      var g = svg.append("g").attr("transform", "translate(" + margin.left + "," + margin.top + ")");
      var y = d3.scaleBand().domain(intents.map(function (d) { return d.name; })).range([0, ih]).padding(0.25);
      var x = d3.scaleLinear().domain([0, d3.max(intents, function (d) { return d.count; }) || 1]).nice().range([0, iw]);
      var color = d3.scaleOrdinal()
        .domain(["positive", "neutral", "negative", "unsubscribe", "bounce", "other"])
        .range([TEAL, MUTED, ROSE, AMBER, "#8b5a2b", "#1a6b8a"]);

      g.append("g").call(d3.axisLeft(y).tickSize(0))
        .call(function (sel) {
          sel.select(".domain").remove();
          sel.selectAll("text").attr("fill", MUTED).attr("font-size", 11);
        });
      g.selectAll("rect")
        .data(intents)
        .join("rect")
        .attr("y", function (d) { return y(d.name); })
        .attr("height", y.bandwidth())
        .attr("x", 0)
        .attr("width", 0)
        .attr("rx", 4)
        .attr("fill", function (d) { return color(String(d.name).toLowerCase()) || TEAL; })
        .transition().duration(500)
        .attr("width", function (d) { return x(d.count); });
    }

    var legendHost = el("legend-sources");
    if (legendHost && sources.length) {
      legendHost.innerHTML = "<h3 class=\"dash-mini-title\">Lead sources</h3>" +
        sources.slice(0, 8).map(function (s) {
          return "<div class=\"dash-source-row\"><span>" + escapeHtml(s.name) +
            "</span><strong>" + s.count + "</strong></div>";
        }).join("");
    }
  }

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function renderAll() {
    drawGeo();
    drawLeads();
    drawTreemap();
    drawSchedule();
    drawCategories();
    drawWords();
    drawIntents();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", renderAll);
  } else {
    renderAll();
  }

  var resizeTimer;
  window.addEventListener("resize", function () {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(renderAll, 220);
  });
})();
