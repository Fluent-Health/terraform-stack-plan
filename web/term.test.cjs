// Minimal node test for the pure term.js parser (no deps; run: node web/term.test.cjs).
const assert = require('node:assert');
const term = require('../internal/server/assets/term.js');

// SGR → HTML, escaped.
assert.strictEqual(term.ansi('plain <x>'), 'plain &lt;x&gt;');
assert.ok(term.ansi('\x1b[32mgreen\x1b[0m').includes('class="a-green"'), 'green span');

// LineBuffer: \n flushes, partial line stays pending (no spurious newline).
let lb = new term.LineBuffer();
let r = lb.push('hello');
assert.deepStrictEqual(r.completed, []);
assert.strictEqual(r.pending, 'hello');
r = lb.push(' world\n');
assert.deepStrictEqual(r.completed, ['hello world']);
assert.strictEqual(r.pending, '');

// \r overwrites the pending line (spinner collapses to its last frame).
lb = new term.LineBuffer();
lb.push('Still creating... [10s]\r');
r = lb.push('Still creating... [20s]\r');
assert.strictEqual(r.pending, '');           // reset by trailing \r
r = lb.push('Creation complete\n');
assert.deepStrictEqual(r.completed, ['Creation complete']);

// renderStatic collapses \r per line.
assert.deepStrictEqual(term.renderStatic('a\rb\nc'), ['b', 'c']);

console.log('term.js: all assertions passed');
