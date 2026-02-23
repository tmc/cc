# Quick Reference: Session Replay Viewer

## TL;DR

**Use ansi_up, not xterm.js** for Claude Code session replay.

## Minimal Working Example

```html
<!DOCTYPE html>
<html>
<head>
    <style>
        #output {
            background: #0e0e0e;
            color: #d4d4d4;
            font-family: monospace;
            padding: 20px;
            white-space: pre-wrap;
        }
    </style>
</head>
<body>
    <div id="output"></div>

    <script type="module">
        import AnsiUp from 'https://esm.sh/ansi_up@6.0.2';

        const ansi_up = new AnsiUp();
        const output = document.getElementById('output');

        // Format Claude Code message
        function addMessage(type, content) {
            const time = new Date().toLocaleTimeString();
            let ansiText = '';

            if (type === 'user') {
                ansiText = `\x1b[1;36m[${time}] User:\x1b[0m\n  ${content}\n\n`;
            } else if (type === 'assistant') {
                ansiText = `\x1b[1;32m[${time}] Assistant:\x1b[0m\n  ${content}\n\n`;
            }

            output.innerHTML += ansi_up.ansi_to_html(ansiText);
        }

        // Example usage
        addMessage('user', 'Help me debug this code');
        addMessage('assistant', 'Sure, let me read the file');
    </script>
</body>
</html>
```

## ANSI Color Codes Cheat Sheet

```javascript
const colors = {
    // Message types
    user:       '\x1b[1;36m',  // Bold cyan
    assistant:  '\x1b[1;32m',  // Bold green
    tool_use:   '\x1b[1;33m',  // Bold yellow
    tool_result:'\x1b[1;34m',  // Bold blue
    error:      '\x1b[1;31m',  // Bold red

    // Styles
    bold:       '\x1b[1m',
    dim:        '\x1b[2m',
    italic:     '\x1b[3m',
    underline:  '\x1b[4m',

    // Always reset after coloring
    reset:      '\x1b[0m',
};

// Usage
const msg = `${colors.user}User:${colors.reset} ${colors.dim}message here${colors.reset}`;
```

## SSE Integration Pattern

**Frontend:**
```javascript
const eventSource = new EventSource('/api/sessions/SESSION_ID/stream');

eventSource.onmessage = (e) => {
    const event = JSON.parse(e.data);
    const ansiText = formatEvent(event);
    output.innerHTML += ansi_up.ansi_to_html(ansiText);
};
```

**Go Backend:**
```go
func StreamHandler(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    flusher := w.(http.Flusher)

    // Stream JSONL events
    scanner := bufio.NewScanner(sessionFile)
    for scanner.Scan() {
        fmt.Fprintf(w, "data: %s\n\n", scanner.Text())
        flusher.Flush()
    }
}
```

## Event Format

```json
{
  "type": "user|assistant|tool_use|tool_result",
  "timestamp": "2026-02-14T10:30:00Z",
  "content": "message text",
  "metadata": {
    "name": "Read",
    "input": {...}
  }
}
```

## Complete Message Formatter

```javascript
function formatEvent(event) {
    const time = new Date(event.timestamp).toLocaleTimeString();

    switch(event.type) {
        case 'user':
            return `\x1b[1;36m[${time}] User:\x1b[0m\n  ${event.content}\n\n`;

        case 'assistant':
            return `\x1b[1;32m[${time}] Assistant:\x1b[0m\n  ${event.content}\n\n`;

        case 'tool_use':
            const toolName = event.metadata?.name || 'Unknown';
            let text = `\x1b[1;33m[${time}] Tool Use: ${toolName}\x1b[0m\n`;
            if (event.metadata?.input) {
                text += `\x1b[2m  ${JSON.stringify(event.metadata.input, null, 2)}\x1b[0m\n\n`;
            }
            return text;

        case 'tool_result':
            return `\x1b[1;34m[${time}] Tool Result:\x1b[0m\n\x1b[2m  ${event.content}\x1b[0m\n\n`;

        case 'error':
            return `\x1b[1;31m[${time}] Error:\x1b[0m\n  \x1b[31m${event.content}\x1b[0m\n\n`;

        default:
            return `\x1b[2m[${time}] ${event.type}: ${event.content}\x1b[0m\n\n`;
    }
}
```

## Common ANSI Sequences

| Code | Effect | Example |
|------|--------|---------|
| `\x1b[0m` | Reset all | `text\x1b[0m` |
| `\x1b[1m` | Bold | `\x1b[1mbold\x1b[0m` |
| `\x1b[2m` | Dim | `\x1b[2mdim\x1b[0m` |
| `\x1b[31m` | Red | `\x1b[31mred\x1b[0m` |
| `\x1b[32m` | Green | `\x1b[32mgreen\x1b[0m` |
| `\x1b[33m` | Yellow | `\x1b[33myellow\x1b[0m` |
| `\x1b[34m` | Blue | `\x1b[34mblue\x1b[0m` |
| `\x1b[35m` | Magenta | `\x1b[35mmagenta\x1b[0m` |
| `\x1b[36m` | Cyan | `\x1b[36mcyan\x1b[0m` |
| `\x1b[1;31m` | Bold Red | `\x1b[1;31mbold red\x1b[0m` |

## xterm.js (If You Really Need It)

```javascript
import { Terminal } from 'https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/+esm';
import { FitAddon } from 'https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/+esm';

const term = new Terminal({
    disableStdin: true,  // Read-only mode
    fontSize: 14,
    theme: { background: '#1e1e1e' }
});

const fitAddon = new FitAddon();
term.loadAddon(fitAddon);
term.open(document.getElementById('terminal'));
fitAddon.fit();

// Write data (use \r\n, not \n)
term.write('Hello\r\n');
```

## Decision Matrix

| Your Need | Use This |
|-----------|----------|
| Structured logs (JSONL) | **ansi_up** |
| Raw TTY output | xterm.js |
| Mixed content (markdown + code) | ansi_up |
| Smallest bundle | fancy-ansi (5KB) |
| Maximum features | xterm.js |
| Simple implementation | ansi_up |
| Need cursor positioning | xterm.js |

## Testing

```bash
# Serve examples locally
cd examples/xterm-research
python3 -m http.server 8000

# Open in browser
open http://localhost:8000/comparison.html
```

## Files in This Directory

- `xterm-example.html` - Full xterm.js demo
- `ansi-up-example.html` - ansi_up demo
- `comparison.html` - Side-by-side comparison
- `sse-integration-example.html` - SSE streaming demo
- `go-sse-backend.go` - Example Go server
- `SUMMARY.md` - Detailed analysis
- `alternative-ansi-converters.md` - Other libraries

## One-Liner Copy-Paste

```html
<script type="module">
import A from 'https://esm.sh/ansi_up@6.0.2';
const a=new A(),o=document.getElementById('out');
fetch('/api/sessions/'+SID+'/stream').then(r=>r.body.getReader()).then(r=>{
const d=new TextDecoder();
function read(){r.read().then(({done,value})=>{
if(done)return;o.innerHTML+=a.ansi_to_html(d.decode(value));read();
});}read();
});
</script>
```

(Minified for copy-paste - don't use in production!)
