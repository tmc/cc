# xterm.js Research Summary for Session Replay Viewer

## Quick Answer

**Yes, xterm.js works for write-only display**, but **ansi_up is better for your use case**.

## Detailed Findings

### 1. Can xterm.js render without a real terminal backend?

**YES.** xterm.js works perfectly in write-only mode:

```javascript
const term = new Terminal({
    disableStdin: true,  // Disables keyboard input
    cursorBlink: false,
});
term.open(document.getElementById('terminal'));
term.write('Your content here\r\n');  // Note: use \r\n, not just \n
```

No WebSocket, PTY, or backend process needed. Just call `term.write()` to push text.

### 2. Converting Claude Code session data

Format your JSONL session entries as ANSI escape sequences:

```javascript
// User message
const userMsg = '\x1b[1;36m[10:30:00] User:\x1b[0m\r\n  Can you help?\r\n\r\n';
term.write(userMsg);

// Assistant message
const assistantMsg = '\x1b[1;32m[10:30:05] Assistant:\x1b[0m\r\n  Sure!\r\n\r\n';
term.write(assistantMsg);

// Tool use
const toolUse = '\x1b[1;33m[10:30:10] Tool Use: Read\x1b[0m\r\n\x1b[2m  {"file_path": "..."}\x1b[0m\r\n\r\n';
term.write(toolUse);

// Tool result
const toolResult = '\x1b[1;34m[10:30:15] Tool Result:\x1b[0m\r\n\x1b[2m  package main...\x1b[0m\r\n\r\n';
term.write(toolResult);
```

**ANSI Color Codes:**
- `\x1b[1;36m` - Bold cyan (user)
- `\x1b[1;32m` - Bold green (assistant)
- `\x1b[1;33m` - Bold yellow (tool use)
- `\x1b[1;34m` - Bold blue (tool result)
- `\x1b[1;31m` - Bold red (errors)
- `\x1b[2m` - Dim text
- `\x1b[0m` - Reset formatting

### 3. CDN Availability

All these work via esm.sh:

```javascript
// xterm.js core and addons
import { Terminal } from 'https://esm.sh/@xterm/xterm@6.0.0';
import { FitAddon } from 'https://esm.sh/@xterm/addon-fit';
import { SearchAddon } from 'https://esm.sh/@xterm/addon-search';
import { WebglAddon } from 'https://esm.sh/@xterm/addon-webgl';

// Alternative: jsDelivr (more reliable)
import { Terminal } from 'https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/+esm';
import { FitAddon } from 'https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/+esm';
```

### 4. Useful Addons

| Addon | Purpose | Essential? |
|-------|---------|-----------|
| **@xterm/addon-fit** | Auto-resize to container | YES - Required for responsive layout |
| **@xterm/addon-search** | Ctrl+F text search | Useful for long sessions |
| **@xterm/addon-webgl** | Hardware-accelerated rendering | Optional - Only for huge sessions |
| **@xterm/addon-serialize** | Save/restore terminal state | Nice-to-have for persistence |

### 5. Basic Initialization Example

```javascript
import { Terminal } from 'https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/+esm';
import { FitAddon } from 'https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/+esm';

// Create terminal with read-only config
const term = new Terminal({
    cursorBlink: false,
    disableStdin: true,  // Makes it read-only
    fontSize: 14,
    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
    theme: {
        background: '#1e1e1e',
        foreground: '#d4d4d4',
    },
    scrollback: 10000,  // Keep 10k lines in buffer
});

// Add fit addon for responsive sizing
const fitAddon = new FitAddon();
term.loadAddon(fitAddon);

// Attach to DOM
term.open(document.getElementById('terminal'));
fitAddon.fit();

// Handle window resize
window.addEventListener('resize', () => fitAddon.fit());

// Write data
term.write('Session data here\r\n');
```

## Comparison: xterm.js vs ansi_up

### xterm.js

**Pros:**
- Authentic terminal look and feel
- Perfect for raw TTY output with cursor control
- Built-in scrollback buffer management
- Supports complex terminal features (DEC modes, box drawing, etc.)
- Optional WebGL rendering for performance

**Cons:**
- Large bundle size (~300KB minified)
- More complex setup
- Quirky text selection
- Higher memory usage
- Overkill for structured logs

**Bundle size:** ~300KB (core) + ~50KB per addon

### ansi_up

**Pros:**
- Tiny bundle size (~15KB)
- Dead simple API (one function call)
- Easy text selection and copying
- Standard DOM rendering
- Works great with search/filtering
- Better for mixed content (markdown + code)

**Cons:**
- No terminal-specific features
- Manual scrolling/layout
- Less authentic terminal feel
- Need to convert `\n` to `<br>` yourself

**Bundle size:** ~15KB

### Example Code Comparison

**xterm.js:**
```javascript
const term = new Terminal({ disableStdin: true });
term.open(document.getElementById('terminal'));
term.write('Hello\r\n');  // Must use \r\n
```

**ansi_up:**
```javascript
const ansi_up = new AnsiUp();
const html = ansi_up.ansi_to_html('Hello\n');  // Can use \n
document.getElementById('output').innerHTML += html;
```

## Recommendation for Claude Code Session Replay

**Use ansi_up** for the following reasons:

1. **Your data is structured** - JSONL events, not raw TTY output
2. **Lighter weight** - 15KB vs 300KB makes a huge difference
3. **Easier text operations** - Search, copy, filter work naturally with DOM
4. **Better for mixed content** - You likely want to render markdown in assistant messages
5. **Simpler implementation** - Less setup, less to maintain
6. **Standard DOM** - Can apply CSS, custom styling, syntax highlighting easily

### Hybrid Approach (Best of Both Worlds)

If you need terminal features for specific content:

1. **Primary viewer:** ansi_up for message display
2. **Embedded terminals:** xterm.js only for tool results with TTY output
3. **Conditional rendering:** Detect if tool result contains cursor codes, use xterm.js; otherwise use ansi_up

```javascript
function renderToolResult(result) {
    if (result.includes('\x1b[')) {
        // Has ANSI codes - use ansi_up
        return ansi_up.ansi_to_html(result);
    } else if (hasCursorCodes(result)) {
        // Has complex terminal codes - embed xterm.js
        return embedXterm(result);
    } else {
        // Plain text - just escape HTML
        return escapeHtml(result);
    }
}
```

## SSE Integration Pattern

For streaming from Go backend:

```javascript
const eventSource = new EventSource('/api/sessions/SESSION_ID/stream');

eventSource.onmessage = (e) => {
    const event = JSON.parse(e.data);
    const html = formatMessage(event);  // Format with ansi_up
    document.getElementById('output').innerHTML += html;
};

function formatMessage(event) {
    const ansiText = `\x1b[1;36m${event.type}:\x1b[0m ${event.content}\n`;
    return ansi_up.ansi_to_html(ansiText);
}
```

## Performance Considerations

| Aspect | xterm.js | ansi_up |
|--------|----------|---------|
| Initial load | 300KB+ | 15KB |
| Memory usage | High (buffer overhead) | Low (standard DOM) |
| Render speed | Fast (canvas/WebGL) | Fast (DOM) |
| Text search | Via addon | Native browser |
| Copy/paste | Works but quirky | Standard browser behavior |
| Scrolling | Custom implementation | Native browser |

## Final Verdict

**For Claude Code session replay viewer: Use ansi_up**

It's simpler, lighter, and better suited for structured session data. Reserve xterm.js only if you need to display raw terminal output with cursor positioning.

## Try It Yourself

All example files are standalone HTML - no build step required:

```bash
cd docs/xterm-research
python3 -m http.server 8000
# Open http://localhost:8000/comparison.html
```

See the examples to test both approaches side-by-side with real Claude Code message formatting.
