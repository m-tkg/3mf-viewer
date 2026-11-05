import test from 'node:test';
import assert from 'node:assert/strict';
import { buildLocalList, kindOf, sizeNotice, MAX_FILE_BYTES, LARGE_FILE_BYTES } from '../static/local.js';

const f = (rel, size = 10, lastModified = 1) => ({ name: rel.split('/').pop(), webkitRelativePath: rel, size, lastModified });

test('kindOf は拡張子の大文字小文字を問わない', () => {
  assert.equal(kindOf('a.3MF'), '3mf');
  assert.equal(kindOf('a.Stl'), 'stl');
  assert.equal(kindOf('a.obj'), '');
  assert.equal(kindOf('stl'), '');
});

test('選んだフォルダ名を除いた相対パスで、サブフォルダ込みの一覧を作る', () => {
  const r = buildLocalList([
    f('models/b/z.stl', 5, 7),
    f('models/a.3mf', 20, 3),
    f('models/readme.txt'),
    f('models/b/c/deep.STL'),
  ]);
  assert.equal(r.rootName, 'models');
  assert.deepEqual(r.files.map((x) => x.path), ['a.3mf', 'b/c/deep.STL', 'b/z.stl']);
  const z = r.files.find((x) => x.name === 'z.stl');
  assert.deepEqual([z.dir, z.size, z.modTime, z.kind], ['b', 5, 7, 'stl']);
  assert.equal(r.files[0].dir, '');
  assert.ok(r.files.every((x) => !('absPath' in x)));
});

test('隠しファイル・隠しディレクトリは除外する', () => {
  const r = buildLocalList([f('m/.hidden/a.stl'), f('m/._x.stl'), f('m/ok.stl')]);
  assert.deepEqual(r.files.map((x) => x.path), ['ok.stl']);
  assert.equal(r.hiddenSkipped, 2);
});

test('件数上限で打ち切る', () => {
  const r = buildLocalList([f('m/a.stl'), f('m/b.stl'), f('m/c.stl')], { maxFiles: 2 });
  assert.equal(r.files.length, 2);
  assert.equal(r.truncated, true);
});

test('空のフォルダ', () => {
  const r = buildLocalList([]);
  assert.deepEqual(r.files, []);
  assert.equal(r.rootName, '');
});

test('sizeNotice: 上限超過はブロック、大きめは警告のみ', () => {
  assert.equal(sizeNotice(1024), null);
  assert.equal(sizeNotice(LARGE_FILE_BYTES + 1).block, false);
  assert.equal(sizeNotice(MAX_FILE_BYTES + 1).block, true);
});
