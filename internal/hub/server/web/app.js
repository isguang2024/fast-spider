(() => {
  const copyButton = document.querySelector("[data-copy-target]");
  if (copyButton) {
    copyButton.addEventListener("click", async () => {
      const target = document.getElementById(copyButton.dataset.copyTarget || "");
      if (!target) return;
      const value = target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement
        ? target.value
        : target.textContent || "";
      try {
        await navigator.clipboard.writeText(value);
        const original = copyButton.textContent;
        copyButton.textContent = "已复制";
        copyButton.disabled = true;
        window.setTimeout(() => {
          copyButton.textContent = original;
          copyButton.disabled = false;
        }, 1600);
      } catch {
        if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement) {
          target.focus();
          target.select();
        }
      }
    });
  }

  for (const form of document.querySelectorAll("form[data-confirm]")) {
    form.addEventListener("submit", (event) => {
      const message = form.getAttribute("data-confirm") || "确认继续？";
      if (!window.confirm(message)) event.preventDefault();
    });
  }
})();
