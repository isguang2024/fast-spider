(() => {
  const input = document.getElementById("bootstrap-token");
  const field = document.getElementById("setup-code-field");
  if (!input) return;

  const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ""));
  const token = fragment.get("code") || fragment.get("token") || window.location.hash.replace(/^#/, "");
  if (!token) return;

  input.value = token;
  if (field) field.classList.add("visually-hidden");
  history.replaceState(null, "", window.location.pathname + window.location.search);
})();
