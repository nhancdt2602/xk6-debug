'use strict';

/**
 * Babel plugin that instruments k6 scripts for debugging.
 *
 * Transformations:
 * 1. Injects `import { line, capture, breakpoint, enterScope } from 'k6/x/debug';`
 * 2. Assigns a unique scope ID to each function body and block statement
 * 3. Injects `enterScope(scopeId, parentScopeId)` at the top of each scope
 * 4. Before each instrumented statement: `line({line, col, file, scope})`
 *    — this is where the IDE pauses for breakpoints and step-over
 * 5. After each variable assignment: `capture({line, col, name, file, scope}, value)`
 *    — purely stores the value for inspection; no pause logic
 * 6. Replaces `debugger` statements with `breakpoint({line, col, file, scope})`
 *
 * Scope tracking:
 * - Scope 0 is the program/global scope
 * - Each function body and block statement gets a unique scope ID
 * - enterScope() clears stale variables from a previous entry of the same scope
 * - Variables are tagged with their scope so the debugger only shows in-scope ones
 */
module.exports = function ({ types: t }) {
  let nextScopeId = 1; // 0 is reserved for global/program scope
  const scopeStack = [0]; // start with global scope

  function currentScope() {
    return scopeStack[scopeStack.length - 1];
  }

  function buildLine(loc, filename) {
    return t.expressionStatement(
      t.callExpression(t.identifier('line'), [
        t.valueToNode({
          line: loc.start.line,
          col: loc.start.column,
          file: filename,
          scope: currentScope(),
        }),
      ])
    );
  }

  function buildCapture(name, loc, filename) {
    return t.expressionStatement(
      t.callExpression(t.identifier('capture'), [
        t.valueToNode({
          line: loc.start.line,
          col: loc.start.column,
          name: name,
          file: filename,
          scope: currentScope(),
        }),
        t.identifier(name),
      ])
    );
  }

  function buildBreakpoint(loc, filename) {
    return t.expressionStatement(
      t.callExpression(t.identifier('breakpoint'), [
        t.valueToNode({
          line: loc.start.line,
          col: loc.start.column,
          file: filename,
          scope: currentScope(),
        }),
      ])
    );
  }

  function buildEnterScope(scopeId, parentId) {
    return t.expressionStatement(
      t.callExpression(t.identifier('enterScope'), [
        t.numericLiteral(scopeId),
        t.numericLiteral(parentId),
      ])
    );
  }

  // Injected call names — skip these during the second pass
  const injectedCalls = new Set(['line', 'capture', 'breakpoint', 'enterScope']);

  // Track which source lines already have a line() call to avoid duplicates
  const lineInstrumented = new Set();

  function markLine(loc) {
    if (loc) lineInstrumented.add(loc.start.line);
  }

  return {
    visitor: {
      Program: {
        enter(path, state) {
          // Reset per-file mutable state. Babel caches plugin instances across
          // transformSync calls (same function ref → same closure), so without
          // this reset the second launch skips all line() insertion because
          // lineInstrumented already contains every source line from run #1.
          nextScopeId = 1;
          scopeStack.length = 0;
          scopeStack.push(0);
          lineInstrumented.clear();

          const filename =
            state.filename || state.file.opts.filename || 'unknown';

          // Check if import already exists
          const hasImport = path.node.body.some(
            (node) =>
              t.isImportDeclaration(node) &&
              node.source.value === 'k6/x/debug'
          );
          if (hasImport) return;

          // Inject: import { line, capture, breakpoint, enterScope } from 'k6/x/debug';
          const importDecl = t.importDeclaration(
            [
              t.importSpecifier(t.identifier('line'), t.identifier('line')),
              t.importSpecifier(t.identifier('capture'), t.identifier('capture')),
              t.importSpecifier(t.identifier('breakpoint'), t.identifier('breakpoint')),
              t.importSpecifier(t.identifier('enterScope'), t.identifier('enterScope')),
            ],
            t.stringLiteral('k6/x/debug')
          );
          path.unshiftContainer('body', importDecl);
        },

        // Second pass: add line() before any statement not already instrumented.
        // Uses scope stored on ancestor nodes since the stack has been unwound.
        exit(path, state) {
          const filename =
            state.filename || state.file.opts.filename || 'unknown';

          function resolveScopeForPath(stmtPath) {
            let p = stmtPath;
            while (p) {
              if (p.isFunction()) {
                const id = p.getData('_debugScopeId');
                if (id !== undefined) return id;
              }
              if (p.isBlockStatement()) {
                if (p.parentPath && p.parentPath.isFunction()) {
                  const id = p.parentPath.getData('_debugScopeId');
                  if (id !== undefined) return id;
                }
                const id = p.getData('_debugScopeId');
                if (id !== undefined) return id;
              }
              p = p.parentPath;
            }
            return 0;
          }

          function buildLineWithScope(loc, filename, scopeId) {
            return t.expressionStatement(
              t.callExpression(t.identifier('line'), [
                t.valueToNode({
                  line: loc.start.line,
                  col: loc.start.column,
                  file: filename,
                  scope: scopeId,
                }),
              ])
            );
          }

          path.traverse({
            ExpressionStatement(stmtPath) {
              const node = stmtPath.node;
              if (!node.loc) return;
              const srcLine = node.loc.start.line;
              if (lineInstrumented.has(srcLine)) return;

              // Skip our own injected calls
              if (t.isCallExpression(node.expression)) {
                const callee = node.expression.callee;
                if (t.isIdentifier(callee) && injectedCalls.has(callee.name)) return;
              }

              lineInstrumented.add(srcLine);
              const scope = resolveScopeForPath(stmtPath);
              stmtPath.insertBefore(buildLineWithScope(node.loc, filename, scope));
            },

            ReturnStatement(stmtPath) {
              const node = stmtPath.node;
              if (!node.loc) return;
              const srcLine = node.loc.start.line;
              if (lineInstrumented.has(srcLine)) return;
              lineInstrumented.add(srcLine);
              const scope = resolveScopeForPath(stmtPath);
              stmtPath.insertBefore(buildLineWithScope(node.loc, filename, scope));
            },
          });
        },
      },

      // Track function scopes
      'FunctionDeclaration|FunctionExpression|ArrowFunctionExpression': {
        enter(path) {
          const body = path.node.body;
          if (!t.isBlockStatement(body)) return;
          const id = nextScopeId++;
          const parentId = currentScope();
          scopeStack.push(id);
          path.setData('_debugScopeId', id);
          path.get('body').unshiftContainer('body', buildEnterScope(id, parentId));
        },
        exit(path) {
          if (!t.isBlockStatement(path.node.body)) return;
          scopeStack.pop();
        },
      },

      // Track block scopes (if, for, while, try, etc.) — skip function bodies
      BlockStatement: {
        enter(path) {
          if (path.parentPath.isFunction()) return;
          const id = nextScopeId++;
          const parentId = currentScope();
          scopeStack.push(id);
          path.setData('_debugScopeId', id);
          path.unshiftContainer('body', buildEnterScope(id, parentId));
        },
        exit(path) {
          if (path.parentPath.isFunction()) return;
          scopeStack.pop();
        },
      },

      VariableDeclaration(path, state) {
        // Skip variable declarations in for-loop init (can't insertBefore/After there)
        if (
          path.parentPath.isForStatement({ init: path.node }) ||
          path.parentPath.isForInStatement({ left: path.node }) ||
          path.parentPath.isForOfStatement({ left: path.node })
        ) {
          return;
        }

        const filename =
          state.filename || state.file.opts.filename || 'unknown';

        const captures = [];
        for (const decl of path.node.declarations) {
          if (decl.init && t.isIdentifier(decl.id) && decl.loc) {
            captures.push(buildCapture(decl.id.name, decl.loc, filename));
          }
        }

        if (path.node.loc && !lineInstrumented.has(path.node.loc.start.line)) {
          markLine(path.node.loc);
          path.insertBefore(buildLine(path.node.loc, filename));
        }

        // Insert captures after in reverse order to maintain source ordering
        for (let i = captures.length - 1; i >= 0; i--) {
          path.insertAfter(captures[i]);
        }
      },

      AssignmentExpression(path, state) {
        const filename =
          state.filename || state.file.opts.filename || 'unknown';

        if (
          t.isIdentifier(path.node.left) &&
          path.parentPath.isExpressionStatement() &&
          path.node.loc
        ) {
          const stmtPath = path.parentPath;
          if (!lineInstrumented.has(path.node.loc.start.line)) {
            markLine(path.node.loc);
            stmtPath.insertBefore(buildLine(path.node.loc, filename));
          }
          stmtPath.insertAfter(buildCapture(path.node.left.name, path.node.loc, filename));
        }
      },

      DebuggerStatement(path, state) {
        const filename =
          state.filename || state.file.opts.filename || 'unknown';

        if (path.node.loc) {
          markLine(path.node.loc);
          path.replaceWith(buildBreakpoint(path.node.loc, filename));
        }
      },
    },
  };
};
