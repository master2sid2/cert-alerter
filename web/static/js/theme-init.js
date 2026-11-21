// Applies the saved (or system) colour theme before first paint.
(function () {
  var theme = 'light';
  try {
    theme = localStorage.getItem('ca.theme') ||
      (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
  } catch (e) {
    /* storage unavailable */
  }
  document.documentElement.dataset.theme = theme;
})();
