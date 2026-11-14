'use strict';

// Limits semantic-release's commit analysis to the module directory it runs
// from, and excludes Go modules nested inside it (drivers), so a commit that
// only touches <module>/drivers/<driver> releases that driver and never
// <module>.
//
// semantic-release reads commits through git-log-parser, which it has already
// loaded by the time this file runs; wrapping its parse() adds pathspecs to
// every `git log` call. Each .releaserc.json lists this package under both
// "extends" and "plugins" so it loads before commits are read.

const fs = require('node:fs');
const path = require('node:path');

const GIT_LOG_PARSER = '/node_modules/git-log-parser/src/index.js';
const SKIPPED_DIRECTORIES = new Set(['node_modules', 'testdata', 'vendor']);

function nestedModules(root) {
  const modules = [];
  const walk = (directory) => {
    for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
      if (!entry.isDirectory() || entry.name.startsWith('.') || SKIPPED_DIRECTORIES.has(entry.name)) {
        continue;
      }
      const child = path.join(directory, entry.name);
      if (fs.existsSync(path.join(child, 'go.mod'))) {
        modules.push(child);
      } else {
        walk(child);
      }
    }
  };
  walk(root);
  return modules;
}

function pathspecs(cwd) {
  return [cwd, ...nestedModules(cwd).map((directory) => `:(exclude)${directory}`)];
}

for (const id of Object.keys(require.cache)) {
  if (!id.split(path.sep).join('/').endsWith(GIT_LOG_PARSER)) {
    continue;
  }
  const parser = require.cache[id].exports;
  if (parser.bkitCommitFilter) {
    continue;
  }
  const parse = parser.parse;
  parser.parse = (config, options) => {
    const positional = config._ === undefined ? [] : [].concat(config._);
    return parse({ ...config, _: [...positional, ...pathspecs(options.cwd)] }, options);
  };
  parser.bkitCommitFilter = true;
}

module.exports = {};
