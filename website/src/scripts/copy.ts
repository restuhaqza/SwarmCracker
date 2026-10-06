export function initCopy(): void {
  const buttons = document.querySelectorAll('[data-copy-target]');
  
  buttons.forEach((btn) => {
    btn.addEventListener('click', async () => {
      try {
        const targetSelector = btn.getAttribute('data-copy-target');
        if (!targetSelector) return;
        
        const el = document.querySelector(targetSelector);
        if (!el) return;
        
        const text = el.textContent ?? '';
        
        // Try clipboard API
        if (typeof navigator !== 'undefined' && navigator.clipboard && navigator.clipboard.writeText) {
          try {
            await navigator.clipboard.writeText(text);
            showStatus(btn, 'Copied', true);
            return;
          } catch {
            // Fall through to fallback
          }
        }
        
        // Fallback: select the text so the user can copy it manually.
        // The clipboard write did NOT happen, so announce that honestly
        // instead of claiming "Copied".
        selectText(el);
        showStatus(btn, 'Press Ctrl+C to copy', false);
      } catch {
        // Silently ignore any errors
      }
    });
  });
}

/**
 * Announce the outcome of a copy attempt.
 *
 * When `copied` is true the button gets the transient "copied" state;
 * otherwise (manual fallback) the button keeps its normal look and the
 * status message carries the instruction instead.
 *
 * The reset timer is registered unconditionally — not only when the status
 * element exists — so the button state is always cleared and can never
 * stay "copied" forever.
 */
function showStatus(btn: Element, message: string, copied: boolean): void {
  if (copied) {
    btn.setAttribute('data-copied', 'true');
  }
  
  // Update adjacent aria-live status element
  // Traverse up to the enclosing <figure> (btn.parentElement is .code-block,
  // but the [aria-live="polite"] span is a sibling of .code-block inside <figure>)
  const figure = btn.closest('figure');
  const status = figure ? figure.querySelector('[aria-live="polite"]') : null;
  if (status) {
    status.textContent = message;
  }
  
  // Reset after 2 seconds
  setTimeout(() => {
    btn.removeAttribute('data-copied');
    if (status) {
      status.textContent = '';
    }
  }, 2000);
}

function selectText(el: Element): void {
  try {
    const range = document.createRange();
    range.selectNodeContents(el);
    const sel = window.getSelection();
    if (sel) {
      sel.removeAllRanges();
      sel.addRange(range);
    }
  } catch {
    // Silently ignore
  }
}
