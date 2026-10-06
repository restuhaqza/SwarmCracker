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
            showCopied(btn);
            return;
          } catch {
            // Fall through to fallback
          }
        }
        
        // Fallback: select the text
        selectText(el);
        showCopied(btn);
      } catch {
        // Silently ignore any errors
      }
    });
  });
}

function showCopied(btn: Element): void {
  btn.setAttribute('data-copied', 'true');
  
  // Update adjacent aria-live status element
  const parent = btn.parentElement;
  if (parent) {
    const status = parent.querySelector('[aria-live="polite"]');
    if (status) {
      status.textContent = 'Copied';
      
      // Reset after 2 seconds
      setTimeout(() => {
        btn.removeAttribute('data-copied');
        status.textContent = '';
      }, 2000);
    }
  }
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
