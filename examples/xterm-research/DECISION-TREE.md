# Decision Tree: Choosing a Session Replay Renderer

```
Do you need to display Claude Code session data?
│
├─ YES → What type of data are you rendering?
│        │
│        ├─ Structured JSONL events (user/assistant/tool messages)
│        │  │
│        │  └─ Use: ansi_up ✓
│        │     Why: Lightweight (15KB), simple, perfect for structured data
│        │     Bundle: import AnsiUp from 'https://esm.sh/ansi_up@6.0.2'
│        │     Code: const html = ansi_up.ansi_to_html(ansiText)
│        │
│        ├─ Raw TTY output with cursor positioning
│        │  │
│        │  └─ Use: xterm.js
│        │     Why: Full terminal emulation, handles escape sequences
│        │     Bundle: import { Terminal } from 'https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/+esm'
│        │     Code: term.write('data\r\n')
│        │
│        └─ Mixed content (some structured, some raw TTY)
│           │
│           └─ Use: Hybrid approach
│              Primary: ansi_up for messages
│              Embedded: xterm.js for tool results with TTY output
│
└─ What are your constraints?
   │
   ├─ Bundle size matters
   │  │
   │  ├─ Critical (< 20KB)
   │  │  └─ Use: fancy-ansi (5KB) or ansi_up (15KB)
   │  │
   │  └─ Can afford 300KB+
   │     └─ Use: xterm.js
   │
   ├─ Development time matters
   │  │
   │  ├─ Need it quick
   │  │  └─ Use: ansi_up (one function call)
   │  │
   │  └─ Can spend time on implementation
   │     └─ Use: xterm.js (more setup, configuration)
   │
   └─ Features matter
      │
      ├─ Need authentic terminal feel, cursor control, DEC modes
      │  └─ Use: xterm.js
      │
      └─ Need easy text selection, search, copy-paste
         └─ Use: ansi_up
```

## Decision Matrix

| Scenario | Recommended Solution | Why |
|----------|---------------------|-----|
| Claude Code session replay | **ansi_up** | Structured data, lightweight, simple |
| Terminal output playback | **xterm.js** | Needs cursor control, terminal features |
| CI/CD log viewer | **ansi_up** | Fast rendering, easy search |
| Remote terminal app | **xterm.js** | Interactive, real terminal emulation |
| Error log viewer | **ansi_up** | Simple, easy to copy errors |
| Shell history replay | **xterm.js** | Authentic terminal experience |
| API response viewer | **ansi_up** | JSON formatting, syntax highlighting |
| tmux session replay | **xterm.js** | Complex terminal states |

## Feature Requirements Checklist

Answer YES/NO to each question:

- [ ] Need cursor positioning and control?
- [ ] Need complex terminal escape sequences (DEC modes)?
- [ ] Need to handle raw PTY output?
- [ ] Need scrollback buffer management?
- [ ] Need authentic terminal appearance?

**If you answered YES to 2+ questions:** Use xterm.js
**If you answered NO to all questions:** Use ansi_up

## Performance Considerations

```
Session Size: Small (< 1000 lines)
├─ Either solution works fine
└─ Recommendation: ansi_up (simpler)

Session Size: Medium (1000-10000 lines)
├─ ansi_up: Fast DOM rendering
├─ xterm.js: Consider WebGL addon
└─ Recommendation: ansi_up (better text search)

Session Size: Large (10000+ lines)
├─ ansi_up: May slow down DOM
├─ xterm.js: Better with WebGL addon
└─ Recommendation: xterm.js with WebglAddon
   Alternative: Paginate/virtualize with ansi_up
```

## Implementation Complexity

```
Simplest:
  ansi_up: 3 lines of code
  ├─ import AnsiUp from 'https://esm.sh/ansi_up@6.0.2'
  ├─ const ansi_up = new AnsiUp()
  └─ el.innerHTML = ansi_up.ansi_to_html(text)

Simple:
  xterm.js: ~15 lines of code
  ├─ import { Terminal } from 'cdn'
  ├─ import { FitAddon } from 'cdn'
  ├─ const term = new Terminal(config)
  ├─ const fit = new FitAddon()
  ├─ term.loadAddon(fit)
  ├─ term.open(el)
  ├─ fit.fit()
  └─ term.write(text)

Complex:
  Hybrid approach: ~50 lines of code
  ├─ Detect content type
  ├─ Route to appropriate renderer
  └─ Manage multiple renderers
```

## Bundle Size Impact

```
User's Network Speed:
│
├─ Fast (> 10 Mbps)
│  └─ xterm.js overhead: ~0.3s (negligible)
│
├─ Medium (1-10 Mbps)
│  └─ xterm.js overhead: ~1-3s (noticeable)
│
└─ Slow (< 1 Mbps, mobile)
   └─ xterm.js overhead: ~5-10s (significant)
       Recommendation: Use ansi_up
```

## Final Recommendation for Claude Code

```
For Claude Code Session Replay Viewer:

┌─────────────────────────────────────────┐
│                                         │
│  PRIMARY CHOICE: ansi_up                │
│                                         │
│  Reasons:                               │
│  ✓ Your data is structured (JSONL)     │
│  ✓ No need for cursor positioning      │
│  ✓ 20x smaller bundle (15KB vs 300KB)  │
│  ✓ Simpler to implement and maintain   │
│  ✓ Better text selection/copy          │
│  ✓ Native browser search (Ctrl+F)      │
│  ✓ Works great with SSE streaming      │
│                                         │
└─────────────────────────────────────────┘

Only use xterm.js if:
  - You add live terminal features later
  - Users request authentic terminal look
  - You need to display raw shell output
```

## Code Template Selection

### Template A: Pure ansi_up (Recommended)

```javascript
import AnsiUp from 'https://esm.sh/ansi_up@6.0.2';
const ansi_up = new AnsiUp();

// Format and display
function showEvent(event) {
    const ansi = formatEvent(event); // Your formatter
    output.innerHTML += ansi_up.ansi_to_html(ansi);
}
```

**Use when:** Standard Claude Code sessions (99% of cases)

### Template B: Pure xterm.js

```javascript
import { Terminal } from 'https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/+esm';
import { FitAddon } from 'https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/+esm';

const term = new Terminal({ disableStdin: true });
const fit = new FitAddon();
term.loadAddon(fit);
term.open(document.getElementById('terminal'));
fit.fit();

// Display
function showEvent(event) {
    term.write(formatEvent(event) + '\r\n');
}
```

**Use when:** Raw terminal output, need cursor control

### Template C: Hybrid

```javascript
import AnsiUp from 'https://esm.sh/ansi_up@6.0.2';
import { Terminal } from 'https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/+esm';

const ansi_up = new AnsiUp();
const term = new Terminal({ disableStdin: true });

function showEvent(event) {
    if (event.type === 'tool_result' && needsTerminal(event.content)) {
        // Use xterm.js for complex terminal output
        term.write(event.content + '\r\n');
    } else {
        // Use ansi_up for everything else
        output.innerHTML += ansi_up.ansi_to_html(formatEvent(event));
    }
}
```

**Use when:** Mixed content types

## Quick Start

1. **Copy** `ansi-up-example.html` for basic implementation
2. **Copy** `sse-integration-example.html` for streaming from Go backend
3. **Copy** `comparison.html` to see both approaches

## References

- [ansi_up GitHub](https://github.com/drudru/ansi_up)
- [xterm.js Docs](https://xtermjs.org/docs/)
- [ANSI Escape Codes](https://en.wikipedia.org/wiki/ANSI_escape_code)

## Still Unsure?

**Start with ansi_up.** You can always switch to xterm.js later if needed.
The ansi_up implementation is simpler and covers 95% of use cases.
