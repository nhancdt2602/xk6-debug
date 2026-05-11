'use strict';

const parser = require('@babel/parser');
const traverse = require('@babel/traverse').default;
const generate = require('@babel/generator').default;
const t = require('@babel/types');
const plugin = require('./plugin');

function transform(code, filename) {
  filename = filename || 'test.js';
  const ast = parser.parse(code, {
    sourceType: 'module',
    plugins: ['dynamicImport'],
    sourceFilename: filename,
  });

  const pluginInstance = plugin({ types: t });
  traverse(ast, pluginInstance.visitor, undefined, {
    filename: filename,
    file: { opts: { filename: filename } },
  });

  return generate(ast, { retainLines: false }, code).code;
}

function assert(condition, msg) {
  if (!condition) {
    throw new Error('Assertion failed: ' + msg);
  }
}

// Test 1: Import injection
(function testImportInjection() {
  const code = `import http from 'k6/http';`;
  const result = transform(code);
  assert(
    result.includes(`from "k6/x/debug"`),
    'should inject debug import'
  );
  assert(
    result.includes('line') && result.includes('capture') &&
    result.includes('breakpoint') && result.includes('enterScope'),
    'should import line, capture, breakpoint, enterScope'
  );
  assert(
    result.includes(`import http from 'k6/http';`),
    'should preserve original import'
  );
  console.log('PASS: import injection');
})();

// Test 2: No duplicate import
(function testNoDuplicateImport() {
  const code = `import { line, capture, breakpoint, enterScope } from 'k6/x/debug';
import http from 'k6/http';`;
  const result = transform(code);
  const matches = result.match(/from ["']k6\/x\/debug["']/g);
  assert(matches.length === 1, 'should not duplicate import');
  console.log('PASS: no duplicate import');
})();

// Test 3: Variable declaration capture
(function testVariableCapture() {
  const code = `let resp = http.get('https://test.k6.io');`;
  const result = transform(code);
  assert(
    result.includes('capture('),
    'should insert capture call'
  );
  assert(
    result.includes('name: "resp"'),
    'capture should include variable name'
  );
  console.log('PASS: variable declaration capture');
})();

// Test 4: Assignment expression capture
(function testAssignmentCapture() {
  const code = `let x;
x = 42;`;
  const result = transform(code);
  // Should have capture for the assignment
  assert(
    result.includes('capture(') && result.includes('name: "x"'),
    'should capture assignment'
  );
  console.log('PASS: assignment expression capture');
})();

// Test 5: Debugger statement replacement
(function testDebuggerReplacement() {
  const code = `debugger;`;
  const result = transform(code);
  assert(
    !result.includes('debugger;') || result.includes('breakpoint('),
    'should replace debugger with breakpoint'
  );
  assert(
    result.includes('breakpoint('),
    'should insert breakpoint call'
  );
  console.log('PASS: debugger statement replacement');
})();

// Test 6: Full script transformation
(function testFullTransform() {
  const code = `import http from 'k6/http';

export default function() {
  let resp = http.get('https://test.k6.io');
  debugger;
  let body = resp.json();
}`;
  const result = transform(code, 'script.js');

  assert(result.includes('from "k6/x/debug"'), 'should have debug import');
  assert(result.includes('enterScope('), 'should inject enterScope at function entry');
  assert(result.includes('line('), 'should inject line() markers');
  assert(result.includes('name: "resp"'), 'should capture resp');
  assert(result.includes('name: "body"'), 'should capture body');
  assert(result.includes('breakpoint('), 'should have breakpoint');
  assert(result.includes('file: "script.js"'), 'should include filename');
  console.log('PASS: full script transformation');
})();

// Test 7: Multiple declarations in one statement
(function testMultipleDeclarations() {
  const code = `let a = 1, b = 2;`;
  const result = transform(code);
  const captureCount = (result.match(/capture\(/g) || []).length;
  assert(captureCount >= 2, 'should capture both variables, got ' + captureCount);
  console.log('PASS: multiple declarations');
})();

console.log('\nAll tests passed!');
