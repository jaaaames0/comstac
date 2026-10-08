const assert = require('node:assert/strict');
const test = require('node:test');
const {segment, replaceRecipient} = require('../static/contacts.js');

test('recipient segment honors commas and escaped quotes in names', () => {
  const value = '"Doe, Jane \\"JJ\\"" <jane@example.test>, jo';
  assert.equal(segment(value, value.length).query, 'jo');
  assert.equal(segment('"Doe, Jane" <jane@example.test>, jo', 8).start, 0);
  const email = '"Doe, Jane" <ja';
  assert.equal(segment(email, email.length).query, 'ja');
});

test('selection replaces only the recipient around the caret', () => {
  const value = 'first@example.test, jo, last@example.test';
  const caret = value.indexOf('jo') + 2;
  const recipient = '"Doe, José" <jose@example.test>';
  const result = replaceRecipient(value, caret, recipient);
  assert.equal(result.value, 'first@example.test, ' + recipient + ', last@example.test');
  assert.equal(result.value.slice(0, result.caret), 'first@example.test, ' + recipient);
  assert.equal(replaceRecipient('jo', 2, recipient).value, recipient);
});

test('selecting a named contact preserves other quoted recipients', () => {
  const value = '"Smith, Sam" <sam@example.test>, jane';
  const recipient = '"Doe, Jane" <jane@example.test>';
  assert.equal(replaceRecipient(value, value.length, recipient).value, '"Smith, Sam" <sam@example.test>, ' + recipient);
});
