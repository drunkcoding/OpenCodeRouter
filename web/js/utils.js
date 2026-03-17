export function initMarked() {
  if (typeof window.marked !== 'undefined') {
    const renderer = new window.marked.Renderer();
    const originalCode = renderer.code.bind(renderer);
    renderer.code = function(token) {
      const text = typeof token === 'string' ? token : token.text;
      const lang = typeof token === 'string' ? arguments[1] : token.lang;
      
      if (lang === 'diff' || (!lang && (text.match(/^-[^-]/m) || text.match(/^\+[^+]/m)))) {
        const lines = text.split('\n').map(line => {
          if (line.startsWith('+')) return '<span class="diff-add">' + line.replace(/</g, '&lt;').replace(/>/g, '&gt;') + '</span>';
          if (line.startsWith('-')) return '<span class="diff-rm">' + line.replace(/</g, '&lt;').replace(/>/g, '&gt;') + '</span>';
          return line.replace(/</g, '&lt;').replace(/>/g, '&gt;');
        });
        return '<pre class="diff-block" style="background:#1e1e1e;padding:10px;border-radius:4px;overflow-x:auto;"><code>' + lines.join('\n') + '</code></pre>';
      }
      return originalCode.apply(this, arguments);
    };
    window.marked.use({ renderer });
  }
}

export function processDiffs(text) {
  if (!text) return '';
  const lines = text.split('\n');
  let inDiff = false;
  let inCodeBlock = false;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].startsWith('```')) {
      inCodeBlock = !inCodeBlock;
      if (inDiff) {
        lines.splice(i, 0, '```');
        inDiff = false;
        i++;
      }
      continue;
    }
    if (!inCodeBlock) {
      const isDiffLine = lines[i].match(/^[+-] /) && lines[i].length > 2;
      if (isDiffLine && !inDiff) {
        lines.splice(i, 0, '```diff');
        inDiff = true;
        i++;
      } else if (!isDiffLine && inDiff && lines[i].trim() !== '') {
        lines.splice(i, 0, '```');
        inDiff = false;
        i++;
      }
    }
  }
  if (inDiff) lines.push('```');
  return lines.join('\n');
}