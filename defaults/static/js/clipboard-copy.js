function copyTextFallback(text) {
  const node = document.createElement("textarea");
  node.value = text;
  node.setAttribute("readonly", "");
  node.style.position = "fixed";
  node.style.opacity = "0";
  document.body.appendChild(node);

  let copied;
  try {
    node.select();
    copied = document.execCommand("copy");
  } finally {
    node.remove();
  }
  if (!copied) {
    return Promise.reject(new Error("Clipboard copy failed"));
  }
  return Promise.resolve();
}

function copyText(text) {
  if (navigator.clipboard?.writeText) {
    return navigator.clipboard.writeText(text).catch(() => copyTextFallback(text));
  }
  return copyTextFallback(text);
}

function showFeedback(button, copied) {
  const originalLabel = button.getAttribute("aria-label") || "Copy";
  const feedback = copied
    ? button.dataset.copyFeedback || "Copied!"
    : "Copy failed";

  button.classList.toggle("ClipboardButton--success", copied);
  button.classList.add("tooltipped", "tooltipped-w");
  button.setAttribute("aria-label", feedback);

  window.setTimeout(() => {
    button.classList.remove(
      "ClipboardButton--success",
      "tooltipped",
      "tooltipped-w",
    );
    button.setAttribute("aria-label", originalLabel);
  }, 2000);
}

async function copyCode(button) {
  const code = button.closest(".highlight")?.querySelector("pre code");
  if (!code) {
    showFeedback(button, false);
    return;
  }

  try {
    await copyText(code.textContent || "");
    showFeedback(button, true);
    button.dispatchEvent(new CustomEvent("clipboard-copy", { bubbles: true }));
  } catch {
    showFeedback(button, false);
  }
}

document.addEventListener("click", (event) => {
  const target = event.target;
  if (!(target instanceof Element)) return;

  const button = target.closest("button[data-clipboard-copy]");
  if (button) {
    copyCode(button);
  }
});
