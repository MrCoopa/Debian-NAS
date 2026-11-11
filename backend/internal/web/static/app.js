// Debian Storage Appliance - Modern Go + HTMX + Alpine.js Architecture
console.log("Debian Storage Appliance: Go + HTMX + Alpine.js initialized.");

// Ensure Alpine.js processes newly swapped/morphed DOM elements
document.addEventListener("htmx:afterSettle", function(event) {
  if (window.Alpine && event.detail && event.detail.target) {
    try {
      window.Alpine.initTree(event.detail.target);
    } catch(e) {}
  }
});
