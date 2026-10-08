/* Saved-contact recipient selection. Text is inserted through DOM textContent. */
(function (root) {
  'use strict';

  function segment(value, caret) {
    let quoted = false, escaped = false, start = 0, end = value.length;
    for (let i = 0; i < value.length; i++) {
      const ch = value[i];
      if (escaped) { escaped = false; continue; }
      if (ch === '\\' && quoted) { escaped = true; continue; }
      if (ch === '"') { quoted = !quoted; continue; }
      if (ch === ',' && !quoted) {
        if (i < caret) start = i + 1;
        else { end = i; break; }
      }
    }
    const typed = value.slice(start, caret).trim();
    const query = typed.includes('<') ? typed.slice(typed.lastIndexOf('<') + 1) : typed.replace(/^"/, '').replace(/"$/, '');
    return { start, end, query };
  }

  function replaceRecipient(value, caret, recipient) {
    const part = segment(value, caret);
    const prefix = value.slice(0, part.start);
    const inserted = (prefix ? ' ' : '') + recipient;
    return { value: prefix + inserted + value.slice(part.end), caret: prefix.length + inserted.length };
  }

  // The pure address-list operations are also exercised by Node's test runner.
  if (typeof module !== 'undefined' && module.exports) module.exports = { segment, replaceRecipient };
  if (!root.document) return;
  const doc = root.document;
  let current = null, version = 0;

  function close() {
    version++;
    if (!current) return;
    clearTimeout(current.timer);
    if (current.controller) current.controller.abort();
    current.list.remove();
    current.input.setAttribute('aria-expanded', 'false');
    current.input.removeAttribute('aria-activedescendant');
    current = null;
  }

  function select(index) {
    const state = current;
    if (!state || !state.items[index]) return;
    const result = replaceRecipient(state.input.value, state.input.selectionStart, state.items[index].recipient);
    state.input.value = result.value;
    state.input.setSelectionRange(result.caret, result.caret);
    close();
    state.input.focus();
    state.input.dispatchEvent(new Event('input', { bubbles: true }));
    // The synthetic input must not reopen a suggestion for the selected value.
    close();
  }

  function highlight(index) {
    if (!current) return;
    current.index = index;
    Array.from(current.list.children).forEach((option, i) => option.setAttribute('aria-selected', String(i === index)));
    const option = current.list.children[index];
    if (option) {
      current.input.setAttribute('aria-activedescendant', option.id);
      option.scrollIntoView({ block: 'nearest' });
    }
  }

  function search(input) {
    close();
    if (!input.isConnected) return;
    const list = doc.createElement('div');
    list.id = input.getAttribute('aria-controls');
    list.className = 'contact-options';
    list.setAttribute('role', 'listbox');
    list.setAttribute('aria-label', 'Saved contacts');
    list.hidden = true;
    input.parentElement.append(list);
    const state = { input, list, items: [], index: -1, timer: null, controller: null };
    current = state;
    const requestVersion = version;
    const original = input.value, caret = input.selectionStart;
    const query = segment(original, caret).query;
    if (new TextEncoder().encode(query).length > 128) return;
    state.timer = setTimeout(async function () {
      state.controller = new AbortController();
      try {
        const response = await fetch('/ui/contacts/search?q=' + encodeURIComponent(query), {
          credentials: 'same-origin', cache: 'no-store', signal: state.controller.signal
        });
        if (!response.ok || response.redirected) return;
        const items = await response.json();
        if (current !== state || version !== requestVersion || !input.isConnected || input.value !== original || input.selectionStart !== caret) return;
        state.items = items.slice(0, 10);
        for (const [index, item] of state.items.entries()) {
          const option = doc.createElement('button');
          option.type = 'button';
          option.tabIndex = -1;
          option.id = list.id + '-' + index;
          option.setAttribute('role', 'option');
          option.setAttribute('aria-selected', 'false');
          option.textContent = item.name ? item.name + ' <' + item.email + '>' : item.email;
          // Keep focus during the press, then remove the option after its click.
          // Removing it on touch-down can send the later click to a control below.
          option.addEventListener('pointerdown', function (event) { event.preventDefault(); });
          option.addEventListener('click', function (event) { event.preventDefault(); event.stopPropagation(); select(index); });
          list.append(option);
        }
        list.hidden = !state.items.length;
        input.setAttribute('aria-expanded', String(!!state.items.length));
      } catch (_) { /* Offline or expired sessions leave the typed draft intact. */ }
    }, 150);
  }

  function isRecipient(input) { return input && input.matches && input.matches('[data-contact-input]'); }
  doc.addEventListener('focusin', event => { if (isRecipient(event.target)) search(event.target); });
  doc.addEventListener('input', event => { if (isRecipient(event.target)) search(event.target); });
  doc.addEventListener('click', event => { if (isRecipient(event.target)) search(event.target); });
  doc.addEventListener('focusout', event => { if (current && event.target === current.input) close(); });
  doc.addEventListener('htmx:beforeSwap', close);
  doc.addEventListener('keydown', function (event) {
    if (!isRecipient(event.target)) return;
    if (event.key === 'Escape') { event.preventDefault(); close(); return; }
    if (!current || current.input !== event.target || !current.items.length) return;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const size = current.items.length;
      const down = event.key === 'ArrowDown';
      highlight(current.index < 0 ? (down ? 0 : size - 1) : (current.index + (down ? 1 : -1) + size) % size);
    } else if (event.key === 'Enter' && current.index >= 0) {
      event.preventDefault(); select(current.index);
    }
  });
  doc.addEventListener('keyup', event => {
    if (isRecipient(event.target) && ['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) search(event.target);
  });
  doc.addEventListener('submit', close);
})(typeof window !== 'undefined' ? window : globalThis);
