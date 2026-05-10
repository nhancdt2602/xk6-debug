'use strict';

const fs = require('fs');
const path = require('path');
const parser = require('@babel/parser');
const traverse = require('@babel/traverse').default;
const generate = require('@babel/generator').default;
const plugin = require('./plugin');

function usage() {
  console.error(
    'Usage: k6-debug-preprocess <input.js> [-o output.js] [--source-maps]'
  );
  console.error('  If -o is omitted, prints to stdout.');
  console.error('  --source-maps generates an inline source map.');
  process.exit(1);
}

function main() {
  const args = process.argv.slice(2);
  if (args.length === 0) {
    usage();
  }

  let inputFile = null;
  let outputFile = null;
  let sourceMaps = false;

  for (let i = 0; i < args.length; i++) {
    if (args[i] === '-o' && i + 1 < args.length) {
      outputFile = args[++i];
    } else if (args[i] === '--source-maps') {
      sourceMaps = true;
    } else if (args[i].startsWith('-')) {
      console.error(`Unknown option: ${args[i]}`);
      usage();
    } else {
      inputFile = args[i];
    }
  }

  if (!inputFile) {
    console.error('Error: input file is required');
    usage();
  }

  const source = fs.readFileSync(inputFile, 'utf-8');
  const filename = path.resolve(inputFile);

  // Parse
  const ast = parser.parse(source, {
    sourceType: 'module',
    plugins: ['dynamicImport'],
    sourceFilename: filename,
  });

  // Create plugin instance with state
  const pluginInstance = plugin({ types: require('@babel/types') });

  // Traverse with plugin visitors
  traverse(ast, pluginInstance.visitor, undefined, {
    filename: filename,
    file: { opts: { filename: filename } },
  });

  // Generate
  const generateOpts = {
    sourceMaps: sourceMaps ? 'inline' : false,
    sourceFileName: filename,
    retainLines: true,
  };
  const output = generate(ast, generateOpts, source);

  if (outputFile) {
    fs.writeFileSync(outputFile, output.code, 'utf-8');
    console.error(`[k6-debug-preprocess] Written to ${outputFile}`);
  } else {
    process.stdout.write(output.code);
  }
}

main();
