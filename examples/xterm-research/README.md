# xterm.js Research for Session Replay Viewer

This directory contains research and examples for implementing a web-based session replay viewer using xterm.js and alternatives.

## Files

- `xterm-example.html` - Full xterm.js implementation with read-only mode
- `ansi-up-example.html` - Lightweight ANSI-to-HTML converter approach
- `comparison.html` - Side-by-side comparison of both approaches
- `sse-integration-example.html` - Example showing SSE integration with Go backend

## Key Findings

### 1. xterm.js Write-Only Mode

**Yes, xterm.js can render without a real terminal backend.**

- Use `disableStdin: true` in Terminal options to make it read-only
- Simply call `term.write()` to push content programmatically
- No WebSocket or PTY backend required
- Works perfectly for display-only use cases

```javascript
const term = new Terminal({
    disableStdin: true,  // Makes it read-only
    cursorBlink: false,
});
term.open(document.getElementById('terminal'));
term.write('Hello from xterm.js\r\n');
```

### 2. Converting Claude Code Session Data

For Claude Code sessions, format messages with ANSI escape codes:

```javascript
// User message
term.write('\x1b[1;36m[timestamp] User:\x1b[0m\r\n  message content\r\n\r\n');

// Assistant message
term.write('\x1b[1;32m[timestamp] Assistant:\x1b[0m\r\n  response\r\n\r\n');

// Tool use
term.write('\x1b[1;33m[timestamp] Tool Use: Read\x1b[0m\r\n');
term.write('\x1b[2m  {json input}\x1b[0m\r\n\r\n');

// Tool result
term.write('\x1b[1;34m[timestamp] Tool Result:\x1b[0m\r\n');
term.write('\x1b[2m  result content\x1b[0m\r\n\r\n');
```

**Important:** Use `\r\n` for line breaks in xterm.js, not just `\n`.

### 3. CDN Availability

**esm.sh works perfectly:**

```javascript
import { Terminal } from 'https://esm.sh/@xterm/xterm@6.0.0';
import { FitAddon } from 'https://esm.sh/@xterm/addon-fit';
import { WebglAddon } from 'https://esm.sh/@xterm/addon-webgl';
import { SearchAddon } from 'https://esm.sh/@xterm/addon-search';
```

**Alternative CDNs:**
- jsDelivr: `https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/+esm`
- unpkg: `https://unpkg.com/@xterm/xterm/lib/xterm.js`

### 4. Useful Addons

- **@xterm/addon-fit** - Auto-resize terminal to fit container (essential)
- **@xterm/addon-search** - Add Ctrl+F search functionality
- **@xterm/addon-webgl** - Hardware-accelerated rendering (optional, for performance)
- **@xterm/addon-serialize** - Save/restore terminal state (experimental, useful for persistence)

### 5. Initialization Example

```javascript
import { Terminal } from 'https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/+esm';
import { FitAddon } from 'https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/+esm';

const term = new Terminal({
    cursorBlink: false,
    disableStdin: true,  // Read-only mode
    fontSize: 14,
    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
    theme: {
        background: '#1e1e1e',
        foreground: '#d4d4d4',
    }
});

const fitAddon = new FitAddon();
term.loadAddon(fitAddon);
term.open(document.getElementById('terminal'));
fitAddon.fit();

// Write data programmatically
term.write('Session data here\r\n');
```

## xterm.js vs Simpler Approaches

### xterm.js is the right choice if:
- You want authentic terminal look and feel
- Session data includes raw TTY output with cursor positioning
- You need scrollback buffer management
- Terminal-specific features matter (ANSI art, box drawing, etc.)

### Use ansi_up or similar if:
- Session data is more structured (JSON events, not raw TTY)
- You want lighter bundle size (15KB vs 300KB)
- You need easy text selection and copying
- You want to mix markdown/rich formatting with terminal output
- Standard DOM rendering is sufficient

## Recommendation for Claude Code Session Replay

**For your use case, consider ansi_up or a hybrid approach:**

1. **ansi_up** (recommended for most cases)
   - Lighter weight
   - Easier to implement search/filtering
   - Better for structured session data (JSONL events)
   - Simpler text selection
   - Can mix with markdown rendering

2. **xterm.js** (if you want terminal authenticity)
   - Use for raw TTY output from tools
   - Better if sessions include interactive shell output
   - More authentic terminal experience

3. **Hybrid** (best of both worlds)
   - Use ansi_up for message display
   - Embed xterm.js only for tool_result blocks with TTY output
   - Keep most of the interface lightweight

## Testing

Open the HTML files in a browser to see live examples. All files work standalone with no build step.

```bash
# Serve locally
python3 -m http.server 8000
# Then open http://localhost:8000/comparison.html
```
