'use strict';

const vscode = require('vscode');
const net = require('net');
const { spawn } = require('child_process');
const path = require('path');
const os = require('os');
const fs = require('fs');
const crypto = require('crypto');

// port → child process for launched k6-debug instances
const runningProcesses = new Map();

// Shared output channel for all k6 runs in this session
let outputChannel;

function getOutputChannel() {
  if (!outputChannel) {
    outputChannel = vscode.window.createOutputChannel('k6 Debugger');
  }
  return outputChannel;
}

// Returns a promise that resolves with an available TCP port.
function findFreePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, '127.0.0.1', () => {
      const port = server.address().port;
      server.close(() => resolve(port));
    });
    server.on('error', reject);
  });
}

// Waits for k6-debug to log "DAP server listening on" on stderr.
// Watching the log line avoids making a TCP probe connection, which would
// consume the server's single Accept() slot before VS Code can attach.
function waitForDAPReady(proc, timeoutMs = 20000) {
  return new Promise((resolve, reject) => {
    let done = false;
    let buf = '';

    const finish = (err) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      proc.stderr.removeListener('data', onData);
      if (err) reject(err); else resolve();
    };

    const onData = (chunk) => {
      buf += chunk.toString();
      if (buf.includes('DAP server listening on')) {
        finish(null);
      }
    };

    const timer = setTimeout(
      () => finish(new Error('Timed out waiting for k6-debug DAP server to start')),
      timeoutMs
    );

    proc.stderr.on('data', onData);
    proc.on('exit', (code) => finish(new Error(`k6-debug exited with code ${code}`)));
  });
}

// Instruments a k6 script using the bundled Babel plugin.
// Returns the instrumented source as a string.
function instrumentScript(inputPath) {
  const babel = require('@babel/core');
  const k6Plugin = require('./preprocessor-plugin');

  const source = fs.readFileSync(inputPath, 'utf8');
  const result = babel.transformSync(source, {
    filename: inputPath,   // plugin uses this for the `file` field in instrumented calls
    plugins: [k6Plugin],
    configFile: false,
    babelrc: false,
  });
  return result.code;
}

// Writes the instrumented source to a temp file and returns its path.
function writeInstrumentedFile(inputPath, code) {
  const hash = crypto.createHash('md5').update(inputPath).digest('hex').slice(0, 8);
  const outPath = path.join(os.tmpdir(), `k6_debug_${hash}.js`);
  fs.writeFileSync(outPath, code, 'utf8');
  return outPath;
}

// Resolves the k6-debug binary path.
// Priority: user setting → bundled binary → workspace root → PATH.
function resolveBinaryPath(folder) {
  // 1. Explicit user setting
  const setting = vscode.workspace.getConfiguration('k6debugger').get('binaryPath');
  if (setting) return setting;

  // 2. Bundled binary — present when installed from the Marketplace or a platform vsix.
  //    CI copies the platform binary into bin/ before packaging each vsix.
  const binName = process.platform === 'win32' ? 'k6-debug.exe' : 'k6-debug';
  const bundled = path.join(__dirname, 'bin', binName);
  if (fs.existsSync(bundled)) return bundled;

  // 3. Workspace root (dev/self-built)
  if (folder) {
    const candidates = [
      path.join(folder.uri.fsPath, 'k6-debug'),
      path.join(folder.uri.fsPath, 'xk6-debug', 'k6-debug'),
    ];
    for (const p of candidates) {
      if (fs.existsSync(p)) return p;
    }
  }

  // 4. PATH
  return 'k6-debug';
}

// Spawns k6-debug and returns the child process.
// stderr/stdout piping is set up by the caller BEFORE this returns,
// so do not attach stderr listeners here — the ready-watcher needs to be first.
function spawnK6(binaryPath, instrumentedFile, port, cwd, extraArgs) {
  const ch = getOutputChannel();
  const env = {
    ...process.env,
    K6_DEBUG_DAP: `:${port}`,
    K6_AUTO_EXTENSION_RESOLUTION: 'false',
  };

  const args = ['run', instrumentedFile, ...extraArgs];
  ch.appendLine(`\n> ${binaryPath} ${args.join(' ')}  [DAP port ${port}]\n`);

  return spawn(binaryPath, args, { env, cwd });
}

// Attaches output-channel pipes and process-exit logging to a spawned k6 process.
function pipeK6Output(proc, port) {
  const ch = getOutputChannel();
  proc.stdout.on('data', (d) => ch.append(d.toString()));
  proc.stderr.on('data', (d) => ch.append(d.toString()));
  proc.on('error', (err) => {
    ch.appendLine(`[k6-debug spawn error] ${err.message}`);
    if (err.code === 'ENOENT') {
      vscode.window.showErrorMessage(
        `k6-debug: binary not found. Set k6debugger.binaryPath in settings.`
      );
    }
  });
  proc.on('exit', (code) => {
    ch.appendLine(`\n[k6 exited with code ${code}]`);
    runningProcesses.delete(port);
  });
}

// ─── Debug configuration provider ────────────────────────────────────────────

class K6DebugConfigProvider {
  // Fill in defaults when F5 is pressed without an explicit launch.json entry.
  resolveDebugConfiguration(_folder, config) {
    if (!config.type && !config.request && !config.name) {
      const editor = vscode.window.activeTextEditor;
      if (editor && editor.document.languageId === 'javascript') {
        config.type = 'k6';
        config.request = 'launch';
        config.name = 'Debug k6 Script';
        config.program = editor.document.fileName;
      }
    }
    return config;
  }

  // Called after ${variable} substitution — this is where we do real work for launch.
  async resolveDebugConfigurationWithSubstitutedVariables(folder, config) {
    if (config.request !== 'launch') {
      return config; // attach mode: factory handles it directly
    }

    const program = config.program;
    if (!program) {
      vscode.window.showErrorMessage('k6 debug: "program" must be set in the launch configuration.');
      return undefined;
    }
    if (!fs.existsSync(program)) {
      vscode.window.showErrorMessage(`k6 debug: file not found: ${program}`);
      return undefined;
    }

    // 1. Instrument the script
    getOutputChannel().show(true);
    getOutputChannel().appendLine(`[k6 debug] Instrumenting ${program}...`);
    let instrumentedPath;
    try {
      const code = instrumentScript(program);
      instrumentedPath = writeInstrumentedFile(program, code);
      getOutputChannel().appendLine(`[k6 debug] Instrumented output: ${instrumentedPath}`);
    } catch (err) {
      vscode.window.showErrorMessage(`k6 debug: instrumentation failed — ${err.message}`);
      return undefined;
    }

    // 2. Find a free port
    let port;
    try {
      port = await findFreePort();
    } catch (err) {
      vscode.window.showErrorMessage(`k6 debug: could not find a free port — ${err.message}`);
      return undefined;
    }

    // 3. Spawn k6-debug
    const binaryPath = resolveBinaryPath(folder);
    const cwd = folder ? folder.uri.fsPath : path.dirname(program);
    const proc = spawnK6(binaryPath, instrumentedPath, port, cwd, config.k6Args || []);
    runningProcesses.set(port, proc);

    // 4. Wait for the DAP server to be ready by watching stderr for the listen message.
    //    IMPORTANT: attach the ready-watcher before the output-channel pipe so it
    //    receives data even if both listeners fire on the same chunk.
    getOutputChannel().appendLine(`[k6 debug] Waiting for DAP server on port ${port}...`);
    const readyPromise = waitForDAPReady(proc, 20000);
    pipeK6Output(proc, port); // attach output pipes after watcher is registered

    try {
      await readyPromise;
    } catch (err) {
      proc.kill();
      runningProcesses.delete(port);
      vscode.window.showErrorMessage(
        `k6 debug: ${err.message}. Check the "k6 Debugger" output channel for details.`
      );
      return undefined;
    }
    getOutputChannel().appendLine(`[k6 debug] DAP server ready — attaching debugger.`);

    // 5. Reuse attach mode — the factory connects to the known port
    config.request = 'attach';
    config.port = port;
    return config;
  }
}

// ─── Debug adapter descriptor factory ────────────────────────────────────────

class K6DebugAdapterFactory {
  createDebugAdapterDescriptor(session) {
    const port = session.configuration.port || 4711;
    return new vscode.DebugAdapterServer(port);
  }
}

// ─── Extension lifecycle ──────────────────────────────────────────────────────

function activate(context) {
  const factory = new K6DebugAdapterFactory();
  const provider = new K6DebugConfigProvider();

  context.subscriptions.push(
    vscode.debug.registerDebugAdapterDescriptorFactory('k6', factory),
    vscode.debug.registerDebugConfigurationProvider('k6', provider),

    // Kill k6-debug when the debug session ends (user clicks Stop or script finishes).
    vscode.debug.onDidTerminateDebugSession((session) => {
      const port = session.configuration.port;
      if (port && runningProcesses.has(port)) {
        const proc = runningProcesses.get(port);
        proc.kill();
        runningProcesses.delete(port);
      }
    }),
  );
}

function deactivate() {
  for (const proc of runningProcesses.values()) {
    proc.kill();
  }
  runningProcesses.clear();
}

module.exports = { activate, deactivate };
