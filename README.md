# xk6-debug

A k6 extension for debugging JavaScript test scripts. Inspect variables and pause at breakpoints while your k6 script runs — in the terminal or with full VS Code/Cursor IDE integration.

## Getting Started

This walkthrough takes you from zero to a working debug session. You will:

1. Build a custom k6 binary with the debug extension
2. Write a simple k6 script with a `debugger` statement
3. Preprocess the script (one command)
4. Run it and see variable values printed to your terminal

### Prerequisites

- Go 1.25+
- Node.js 18+
- A local HTTP server on port 8000 (any will do — `python3 -m http.server 8000` works)

### Step 1: Build the custom k6 binary

From the `xk6-debug/` directory:

```bash
go build -o k6-debug ./cmd/k6debug/
```

This produces a `./k6-debug` binary with the debug extension built in.

### Step 2: Write a test script

Create a file called `script.js`:

```js
import http from 'k6/http';

export const options = {
  vus: 1,
  iterations: 1,
};

export default function () {
  let resp = http.get('http://localhost:8000');
  debugger;
  let status = resp.status;
  let body = resp.body;
}
```

The `debugger` statement is the key part. It tells the debugger to pause execution at that line, just like it would in a browser.

### Step 3: Preprocess the script

The preprocessor rewrites your script to add debug hooks. Run it once before each debug session:

```bash
# Install dependencies (first time only)
cd preprocessor && npm install && cd ..

# Preprocess
./preprocessor/bin/k6-debug-preprocess script.js -o /tmp/debug_script.js
```

The preprocessor:
- Injects `import { capture, breakpoint, enterScope } from 'k6/x/debug'`
- Inserts `capture()` after each variable assignment to record values
- Replaces `debugger` statements with `breakpoint()` calls
- Injects `enterScope()` at the top of each function/block to track variable scoping
- Tags each instrumented call with its lexical scope ID so the debugger only shows in-scope variables

### Step 4: Start a local server

Open a separate terminal and start any HTTP server:

```bash
python3 -m http.server 8000
```

### Step 5: Run the script

```bash
K6_AUTO_EXTENSION_RESOLUTION=false ./k6-debug run /tmp/debug_script.js
```

### What you should see

After the HTTP request completes, two things happen:

**Variable captures appear on stderr.** Each captured variable prints as a JSON line:

```json
{"type":"capture","vu":1,"file":"script.js","line":9,"col":6,"name":"resp","value":{ ... }}
```

You will see captures for `resp`, `status`, and `body` — showing their values at the moment they were assigned.

**The script pauses at `debugger`.** The terminal prints:

```
[k6-debug] Breakpoint hit at script.js:10 (VU #1). Press Enter to continue...
```

The VU is frozen. No more code runs until you press Enter. This is how you know the debugger is working — the script stops exactly where you placed `debugger`, and you can see the variables that were captured before that point.

Press Enter to resume. The remaining variables (`status`, `body`) are captured and the script finishes normally.

---

## VS Code / Cursor IDE Integration (DAP mode)

For a full IDE debugging experience with breakpoints, stepping, and variable inspection. The extension handles instrumentation, launching, and attaching automatically.

### Step 1: Install the VS Code extension

```bash
cd vscode/k6-debug-extension
npm install && npm run package
```

Then install the `.vsix` file:
- **VS Code**: `code --install-extension k6-debug-0.1.0.vsix`
- **Cursor**: `cursor --install-extension k6-debug-0.1.0.vsix`

### Step 2: Configure launch.json

Add this to your `.vscode/launch.json`:

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Debug k6 Script",
      "type": "k6",
      "request": "launch",
      "program": "${file}"
    }
  ]
}
```

> **Tip**: Open any k6 script and press F5 — VS Code will offer to create this configuration automatically.

### Step 3: Press F5

Open your **original** k6 script (no preprocessing needed) and press F5. The extension will:

1. Instrument the script automatically
2. Find a free port
3. Start `k6-debug` with the DAP server
4. Attach the debugger once the server is ready

Set breakpoints by clicking in the gutter before or after pressing F5.

- When a breakpoint is hit, the editor highlights the paused line
- The Variables panel shows captured variables — only those in scope at the pause point
- Variables appear in source order and stale values from previous iterations are automatically cleared

### Configuration

| Setting | Description | Default |
|---|---|---|
| `k6debug.binaryPath` | Path to the `k6-debug` binary | Auto-detect (workspace root, then PATH) |

To set a custom binary path, add to your VS Code settings:

```json
{
  "k6debug.binaryPath": "/path/to/k6-debug"
}
```

To pass extra k6 flags (e.g. change VU count):

```json
{
  "name": "Debug k6 Script",
  "type": "k6",
  "request": "launch",
  "program": "${file}",
  "k6Args": ["--vus", "2", "--iterations", "5"]
}
```

### Attach mode (advanced)

If you prefer to start k6 manually and attach separately:

```bash
K6_DEBUG_DAP=:4711 K6_AUTO_EXTENSION_RESOLUTION=false ./k6-debug run /tmp/debug_script.js
```

```json
{
  "name": "Attach to k6 debugger",
  "type": "k6",
  "request": "attach",
  "port": 4711
}
```

### Debugger controls

| Button | Action |
|---|---|
| **Continue** (F5) | Resumes all VUs until the next breakpoint |
| **Step Over** (F10) | Steps to the next line in the current VU; other VUs stay frozen |
| **Disconnect** / **Stop** | Resumes all VUs, kills k6, and ends the debug session |

### Multi-VU debugging

When running with multiple VUs (`vus: 2` or more), the debugger uses all-stop mode:
- When one VU hits a breakpoint, all other VUs are frozen
- **Continue** resumes all VUs
- **Step Over** resumes only the active VU — others remain frozen until Continue
- Each VU appears as a separate thread in the Call Stack panel

### Troubleshooting

- **Nothing happens on F5**: Check the "k6 Debug" output channel in VS Code for error details.
- **Binary not found**: Set `k6debug.binaryPath` in VS Code settings to the full path of your `k6-debug` binary.
- **Port conflict**: The extension picks a random free port automatically — no manual port management needed in launch mode.
- **Breakpoints not hitting**: Ensure `launch.json` uses `"request": "launch"` (not `"attach"`) so the extension instruments the script. Raw uninstrumented scripts won't trigger the debugger.
- **HTTP requests failing**: Make sure any server your script needs is running before pressing F5.

---

## How it works

The preprocessor transforms your script before k6 runs it:

**Your code:**
```js
let resp = http.get('http://localhost:8000');
debugger;
let status = resp.status;
```

**After preprocessing:**
```js
import { capture, breakpoint, enterScope } from 'k6/x/debug';

export default function() {
  enterScope(1, 0);
  let resp = http.get('http://localhost:8000');
  capture({ line: 9, col: 6, name: "resp", file: "script.js", scope: 1 }, resp);
  breakpoint({ line: 10, col: 2, file: "script.js", scope: 1 });
  let status = resp.status;
  capture({ line: 11, col: 6, name: "status", file: "script.js", scope: 1 }, status);
}
```

- `enterScope(id, parentId)` registers a lexical scope and clears stale variables from previous entries
- `capture()` records a variable's value right after assignment, tagged with its scope
- `breakpoint()` replaces `debugger` and pauses execution

### Variable scoping

The debugger tracks lexical scopes from the AST:

- **Global scope (0)**: top-level variables like `options` — always visible, never cleared
- **Function scope**: variables like `resp`, `status` — visible inside the function, cleared on each new iteration
- **Block scope**: variables in `if`/`for`/`while` blocks — visible only when paused inside that block, cleared on re-entry

When paused, only variables whose scope is an ancestor of the current pause point are shown. This prevents confusion from stale variables that are no longer in scope.

---

## Reference

### Preprocessor CLI

```
k6-debug-preprocess <input.js> [-o output.js] [--source-maps]
```

| Flag | Description |
|---|---|
| `-o <file>` | Write output to a file instead of stdout |
| `--source-maps` | Embed an inline source map in the output |

### Environment variables

| Variable | Description |
|---|---|
| `K6_DEBUG_DAP` | Set to a `host:port` (e.g. `:4711`) to start the DAP server. Omit for CLI-only mode. |
| `K6_AUTO_EXTENSION_RESOLUTION` | Set to `false` to prevent k6 from trying to auto-resolve the debug extension. |

### DAP commands supported

| Command | What it does |
|---|---|
| initialize | Returns debugger capabilities |
| launch/attach | Acknowledges the debug session |
| setBreakpoints | Sets breakpoints by file and line |
| configurationDone | Signals the IDE is ready; unblocks VU execution |
| threads | Lists VUs as threads |
| stackTrace | Returns the current pause location with VU identity |
| scopes | Returns a "Local Variables" scope |
| variables | Returns in-scope captured variables in source order |
| continue | Resumes all VUs until next breakpoint |
| next | Steps one line in the active VU; others stay frozen |
| source | Returns file contents for the IDE |
| disconnect | Resumes all VUs and closes the debug session |

### Running tests

```bash
# Go unit tests
cd xk6-debug/
go test -race ./...

# Preprocessor tests
cd preprocessor/
node src/plugin.test.js
```
