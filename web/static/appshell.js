(function () {
  var KEY_COLLAPSE = "orc_sidebar_collapsed";
  var KEY_INSPECTOR = "orc_inspector_open";

  var body = document.body;
  var sidebar = document.getElementById("sidebar");
  var backdrop = document.getElementById("sidebar-backdrop");
  var menuBtn = document.getElementById("menu-btn");
  var collapseBtns = [
    document.getElementById("collapse-btn"),
    document.getElementById("collapse-btn-top"),
  ].filter(Boolean);
  var inspector = document.getElementById("inspector");
  var inspectorBody = document.getElementById("inspector-body");
  var inspectorToggle = document.getElementById("inspector-toggle");
  var inspectorClose = document.getElementById("inspector-close");

  function isMobile() {
    return window.matchMedia("(max-width: 960px)").matches;
  }

  function setCollapsed(on) {
    body.classList.toggle("sidebar-collapsed", on);
    try {
      localStorage.setItem(KEY_COLLAPSE, on ? "1" : "0");
    } catch (e) {}
    collapseBtns.forEach(function (btn) {
      var icon = btn.querySelector("i");
      if (icon) {
        icon.className = on
          ? "fi fi-rr-angle-double-small-right"
          : "fi fi-rr-angle-double-small-left";
      }
      btn.title = on ? "Expand sidebar" : "Collapse sidebar";
    });
  }

  function setInspector(on) {
    body.classList.toggle("inspector-open", on);
    body.classList.toggle("inspector-closed", !on);
    try {
      localStorage.setItem(KEY_INSPECTOR, on ? "1" : "0");
    } catch (e) {}
  }

  // restore preferences
  try {
    if (localStorage.getItem(KEY_COLLAPSE) === "1") setCollapsed(true);
    var insp = localStorage.getItem(KEY_INSPECTOR);
    if (insp === "0") setInspector(false);
    else setInspector(true);
  } catch (e) {
    setInspector(true);
  }

  collapseBtns.forEach(function (btn) {
    btn.addEventListener("click", function () {
      setCollapsed(!body.classList.contains("sidebar-collapsed"));
    });
  });

  if (menuBtn && sidebar) {
    function openMobile() {
      sidebar.classList.add("open");
      if (backdrop) backdrop.hidden = false;
      body.classList.add("sidebar-open");
    }
    function closeMobile() {
      sidebar.classList.remove("open");
      if (backdrop) backdrop.hidden = true;
      body.classList.remove("sidebar-open");
    }
    menuBtn.addEventListener("click", function () {
      if (sidebar.classList.contains("open")) closeMobile();
      else openMobile();
    });
    if (backdrop) backdrop.addEventListener("click", closeMobile);
    sidebar.querySelectorAll("a.nav-item").forEach(function (a) {
      a.addEventListener("click", function () {
        if (isMobile()) closeMobile();
      });
    });
  }

  if (inspectorToggle) {
    inspectorToggle.addEventListener("click", function () {
      setInspector(!body.classList.contains("inspector-open"));
    });
  }
  if (inspectorClose) {
    inspectorClose.addEventListener("click", function () {
      setInspector(false);
    });
  }

  // Row selection → open inspector
  document.addEventListener("click", function (e) {
    var row = e.target.closest("[data-inspect]");
    if (!row) return;
    if (e.target.closest("button, a, form, input, select, textarea, label")) return;
    document.querySelectorAll(".is-selected").forEach(function (el) {
      el.classList.remove("is-selected");
    });
    row.classList.add("is-selected");
    setInspector(true);
  });

  // After HTMX loads into inspector, keep pane open
  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail && e.detail.target && e.detail.target.id === "inspector-body") {
      setInspector(true);
    }
  });

  window.OutReachShell = {
    openInspector: function () {
      setInspector(true);
    },
    closeInspector: function () {
      setInspector(false);
    },
    clearInspector: function () {
      if (inspectorBody) {
        inspectorBody.innerHTML =
          '<div class="inspector-empty"><div class="inspector-empty-ico"><i class="fi fi-rr-search-alt"></i></div><h3>Select a record</h3><p class="muted">Click a lead, campaign, or queue item to inspect details here.</p></div>';
      }
    },
  };
})();
