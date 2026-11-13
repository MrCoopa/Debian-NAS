// Debian Storage Appliance - Modern Go + HTMX + Alpine.js Architecture
console.log("Debian Storage Appliance: Go + HTMX + Alpine.js initialized.");

(function () {
  var lastActionSwap = {};
  window.currentTab = window.currentTab || "dashboard";

  var TAB_CONTAINER_MAP = {
    dashboard: { id: "system-metrics-container", event: "nasRefresh" },
    disks: { id: "disks-container", event: "diskUpdate" },
    pools: { id: "pools-container", event: "poolUpdate" },
    datasets: { id: "datasets-container", event: "datasetUpdate" },
    shares: { id: "shares-container", event: "shareUpdate" },
    users: { id: "users-container", event: "userUpdate" },
    protection: { id: "snapshots-container", event: "snapshotUpdate" },
    services: { id: "services-container", event: "nasRefresh" },
    reporting: { id: "reporting-container", event: "nasRefresh" },
    system: { id: "network-container", event: "nasRefresh" }
  };

  window.nasEnsureSelectValue = function (selectEl, currentVal, onChange) {
    if (!selectEl || !selectEl.options) return;
    if (!currentVal) return;
    var hasCurrent = false;
    for (var i = 0; i < selectEl.options.length; i++) {
      var opt = selectEl.options[i];
      if (!opt.disabled && opt.value === currentVal) {
        hasCurrent = true;
        break;
      }
    }
    if (!hasCurrent && currentVal) {
      var newOpt = document.createElement("option");
      newOpt.value = currentVal;
      newOpt.text = currentVal;
      newOpt.selected = true;
      selectEl.appendChild(newOpt);
    }
    selectEl.value = currentVal;
    if (typeof onChange === "function") onChange(currentVal);
  };

  window.nasTriggerHasError = function (xhr) {
    if (!xhr || typeof xhr.getResponseHeader !== "function") {
      return { hasError: false, message: "" };
    }
    var raw = xhr.getResponseHeader("HX-Trigger");
    if (!raw) {
      return { hasError: false, message: "" };
    }
    try {
      var parsed = JSON.parse(raw);
      var toast = parsed.showToast || parsed["show-toast"];
      if (toast && toast.type === "error") {
        return { hasError: true, message: toast.message || "Operation failed" };
      }
    } catch (e) {}
    return { hasError: false, message: "" };
  };

  window.nasSyncSharePathMode = function (pathValue) {
    var path = (pathValue || "").trim();
    if (!path) {
      return false;
    }
    var select = document.getElementById("share-dataset-select");
    if (!select || !select.options) {
      return false;
    }
    for (var i = 0; i < select.options.length; i++) {
      if (select.options[i].value === path) {
        return false;
      }
    }
    return true;
  };

  window.nasCanBackgroundPoll = function (containerEl) {
    if (document.hidden || !containerEl) {
      return false;
    }
    var modals = document.querySelectorAll(".modal-backdrop");
    for (var i = 0; i < modals.length; i++) {
      if (window.getComputedStyle(modals[i]).display !== "none") {
        return false;
      }
    }
    var menus = containerEl.querySelectorAll("[data-nas-row-menu='true']");
    for (var j = 0; j < menus.length; j++) {
      if (window.getComputedStyle(menus[j]).display !== "none") {
        return false;
      }
    }
    var active = document.activeElement;
    if (active && containerEl.contains(active)) {
      var tag = (active.tagName || "").toUpperCase();
      if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") {
        return false;
      }
    }
    return true;
  };

  window.nasRefreshTab = function (tabName) {
    if (tabName) {
      window.currentTab = tabName;
    }
    var target = TAB_CONTAINER_MAP[tabName];
    if (target && window.htmx) {
      var el = document.getElementById(target.id);
      if (el) {
        delete lastActionSwap[target.id];
        el.dataset.nasManualRefresh = String(Date.now());
        window.htmx.trigger(el, target.event);
      }
    }
    if (tabName === "protection" && window.htmx) {
      var tasks = document.getElementById("snapshot-tasks-container");
      var cloud = document.getElementById("cloudsync-container");
      if (tasks) {
        delete lastActionSwap["snapshot-tasks-container"];
        tasks.dataset.nasManualRefresh = String(Date.now());
        window.htmx.trigger(tasks, "snapshotTaskUpdate");
      }
      if (cloud) {
        delete lastActionSwap["cloudsync-container"];
        cloud.dataset.nasManualRefresh = String(Date.now());
        window.htmx.trigger(cloud, "cloudSyncUpdate");
      }
    }
    if (tabName === "system" && window.htmx) {
      var arcStrip = document.getElementById("arc-telemetry-strip");
      if (arcStrip) {
        delete lastActionSwap["arc-telemetry-strip"];
        arcStrip.dataset.nasManualRefresh = String(Date.now());
        window.htmx.trigger(arcStrip, "arcUpdate");
      }
    }
  };

  // Deduplicate immediate self-triggered partial GETs right after an action response already updated the same container.
  document.addEventListener("htmx:beforeRequest", function (event) {
    var detail = event.detail;
    if (!detail || !detail.requestConfig || detail.requestConfig.verb !== "get") {
      return;
    }
    var elt = detail.elt;
    if (!elt || !elt.id) {
      return;
    }
    var reqPath = detail.requestConfig.path || "";
    if (reqPath.indexOf("/ui/partials/") !== 0) {
      return;
    }
    if (detail.xhr) {
      detail.xhr._nasReqStartedAt = Date.now();
    }
    var trig = detail.requestConfig.triggeringEvent;
    if (trig && trig.type && trig.type.indexOf("every") === 0) {
      if (!window.nasCanBackgroundPoll(elt)) {
        event.preventDefault();
      }
      return;
    }
    if (elt.dataset && elt.dataset.nasManualRefresh) {
      var manualAge = Date.now() - Number(elt.dataset.nasManualRefresh);
      delete elt.dataset.nasManualRefresh;
      if (manualAge < 500) {
        return;
      }
    }
    var swappedAt = lastActionSwap[elt.id];
    if (swappedAt && Date.now() - swappedAt < 450) {
      event.preventDefault();
    }
  });

  // Drop stale GET partial responses that were in flight before a newer mutation swapped the same container.
  document.addEventListener("htmx:beforeSwap", function (event) {
    var detail = event.detail;
    if (!detail || !detail.requestConfig || detail.requestConfig.verb !== "get") {
      return;
    }
    var target = detail.target;
    var reqStarted = detail.xhr && detail.xhr._nasReqStartedAt;
    if (target && target.id && reqStarted && lastActionSwap[target.id] && reqStarted < lastActionSwap[target.id]) {
      detail.shouldSwap = false;
    }
  });

  document.addEventListener("htmx:beforeOnLoad", function (event) {
    var detail = event.detail;
    if (!detail || !detail.requestConfig || detail.requestConfig.verb === "get") {
      return;
    }
    var target = detail.target;
    if (target && target.id && detail.xhr && detail.xhr.status >= 200 && detail.xhr.status < 300) {
      lastActionSwap[target.id] = Date.now();
    }
  });

  document.addEventListener("htmx:afterSwap", function (event) {
    var detail = event.detail;
    if (!detail) {
      return;
    }
    var target = detail.target;
    if (target && target.id && detail.requestConfig && detail.requestConfig.verb !== "get") {
      lastActionSwap[target.id] = Date.now();
    }
  });

  // Ensure both HTMX and Alpine.js initialize newly swapped/morphed live DOM elements
  document.addEventListener("htmx:afterSettle", function (event) {
    var target = (event.target && event.target !== document && event.target !== document.body && event.target.isConnected)
      ? event.target
      : (event.detail && event.detail.target);
    if (!target || !target.isConnected) {
      return;
    }
    if (window.htmx && typeof window.htmx.process === "function") {
      try {
        window.htmx.process(target);
      } catch (e) {}
    }
    if (window.Alpine && typeof window.Alpine.initTree === "function") {
      try {
        window.Alpine.initTree(target);
      } catch (e) {}
    }
  });

  document.addEventListener("visibilitychange", function () {
    if (document.hidden || !document.body) {
      return;
    }
    try {
      var stack = document.body._x_dataStack;
      var currentTab = stack && stack[0] ? stack[0].currentTab : "";
      if (currentTab && typeof window.nasRefreshTab === "function") {
        window.nasRefreshTab(currentTab);
      }
    } catch (e) {}
  });

  document.addEventListener("htmx:responseError", function () {
    window.dispatchEvent(new CustomEvent("show-toast", {
      detail: { message: "Request failed. Please check NAS connection.", type: "error" }
    }));
  });

  document.addEventListener("htmx:sendError", function () {
    window.dispatchEvent(new CustomEvent("show-toast", {
      detail: { message: "Network error reaching NAS backend.", type: "error" }
    }));
  });
})();
