(function () {
  const svgAttrs = 'viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"';
  const icons = {
    'alert-triangle': '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z"/><path d="M12 9v4"/><path d="M12 17h.01"/>',
    'arrow-right': '<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>',
    'bell-ring': '<path d="M6 8a6 6 0 0 1 12 0c0 7 3 7 3 9H3c0-2 3-2 3-9"/><path d="M10.3 21a2 2 0 0 0 3.4 0"/><path d="M4 2 2 4"/><path d="m22 4-2-2"/>',
    box: '<path d="m21 8-9-5-9 5 9 5 9-5Z"/><path d="M3 8v8l9 5 9-5V8"/><path d="M12 13v8"/>',
    'brain-circuit': '<path d="M12 5a3 3 0 0 0-5.8-1 3 3 0 0 0-3.1 4.8A3 3 0 0 0 5 14v1a4 4 0 0 0 7 2.6"/><path d="M12 5a3 3 0 0 1 5.8-1 3 3 0 0 1 3.1 4.8A3 3 0 0 1 19 14v1a4 4 0 0 1-7 2.6"/><path d="M12 5v14"/><circle cx="17" cy="10" r="1"/><circle cx="7" cy="10" r="1"/>',
    check: '<path d="m20 6-11 11-5-5"/>',
    'check-circle': '<path d="M9 12l2 2 4-4"/><circle cx="12" cy="12" r="10"/>',
    'chevron-right': '<path d="m9 18 6-6-6-6"/>',
    copy: '<rect x="9" y="9" width="13" height="13" rx="2"/><rect x="2" y="2" width="13" height="13" rx="2"/>',
    cpu: '<rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 1v3M15 1v3M9 20v3M15 20v3M20 9h3M20 14h3M1 9h3M1 14h3"/>',
    'file-code-2': '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/><path d="m10 13-2 2 2 2"/><path d="m14 17 2-2-2-2"/>',
    'file-text': '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/><path d="M8 13h8M8 17h8M8 9h2"/>',
    'git-branch': '<line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/>',
    info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/>',
    'maximize-2': '<path d="M15 3h6v6"/><path d="m21 3-7 7"/><path d="M9 21H3v-6"/><path d="m3 21 7-7"/>',
    move: '<path d="M12 2v20M2 12h20"/><path d="m15 5-3-3-3 3M15 19l-3 3-3-3M5 9l-3 3 3 3M19 9l3 3-3 3"/>',
    network: '<rect x="16" y="16" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="9" y="2" width="6" height="6" rx="1"/><path d="M5 16v-3a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v3"/><path d="M12 8v8"/>',
    play: '<path d="m5 3 14 9-14 9V3Z"/>',
    radar: '<path d="M19.1 4.9A10 10 0 1 1 12 2"/><path d="M12 12 19 5"/><circle cx="12" cy="12" r="2"/><path d="M13.4 10.6 21 3"/>',
    'refresh-cw': '<path d="M21 12a9 9 0 0 0-9-9 9.8 9.8 0 0 0-6.7 2.7L3 8"/><path d="M3 3v5h5"/><path d="M3 12a9 9 0 0 0 9 9 9.8 9.8 0 0 0 6.7-2.7L21 16"/><path d="M16 16h5v5"/>',
    'shield-alert': '<path d="M20 13c0 5-3.5 7.5-8 9-4.5-1.5-8-4-8-9V5l8-3 8 3v8Z"/><path d="M12 8v4"/><path d="M12 16h.01"/>',
    terminal: '<path d="m4 17 6-6-6-6"/><path d="M12 19h8"/>',
    'terminal-square': '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="m7 15 3-3-3-3"/><path d="M13 17h4"/>',
    x: '<path d="M18 6 6 18"/><path d="m6 6 12 12"/>',
    'zoom-in': '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/><path d="M11 8v6M8 11h6"/>',
    'zoom-out': '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/><path d="M8 11h6"/>'
  };

  function createIcons() {
    document.querySelectorAll('[data-lucide]').forEach((node) => {
      if (node.tagName.toLowerCase() === 'svg') return;
      const name = node.getAttribute('data-lucide');
      const cls = node.getAttribute('class') || '';
      const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      svg.setAttribute('data-lucide', name);
      svg.setAttribute('class', cls);
      svg.setAttribute('aria-hidden', 'true');
      svg.setAttribute('viewBox', '0 0 24 24');
      svg.setAttribute('fill', 'none');
      svg.setAttribute('stroke', 'currentColor');
      svg.setAttribute('stroke-width', '2');
      svg.setAttribute('stroke-linecap', 'round');
      svg.setAttribute('stroke-linejoin', 'round');
      svg.innerHTML = icons[name] || '<circle cx="12" cy="12" r="9"/>';
      node.replaceWith(svg);
    });
  }

  window.lucide = { createIcons };
})();
