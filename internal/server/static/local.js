// ブラウザで選んだフォルダ (<input webkitdirectory>) の File 一覧を、サーバ側の一覧と同じ形に整える純粋ロジック。
// DOM やネットワークには触れない (node でテストできる)。

// サーバ (internal/model) の読み込み上限と揃える。
export const MAX_FILE_BYTES = 512 * 1024 * 1024;
// これを超えるファイルは、プレビュー時に「時間がかかる」旨を表示する。
export const LARGE_FILE_BYTES = 64 * 1024 * 1024;
// 一覧に載せる最大件数 (これを超えたら打ち切って警告する)。
export const MAX_LIST_FILES = 5000;

export function kindOf(name) {
  const m = /\.([^./\\]+)$/.exec(name);
  const ext = m ? m[1].toLowerCase() : '';
  return ext === '3mf' || ext === 'stl' ? ext : '';
}

// entries: File 互換 ({name, size, lastModified, webkitRelativePath})。
// webkitRelativePath は "<選んだフォルダ名>/<サブ>/<ファイル>"。先頭のフォルダ名を除いたものを path (相対パス) にする。
// "." 始まりのファイル/ディレクトリ (隠し) はサーバ側 (scan) と同様に除外する。
export function buildLocalList(entries, { maxFiles = MAX_LIST_FILES } = {}) {
  let rootName = '';
  const files = [];
  let hiddenSkipped = 0;
  let truncated = false;
  for (const e of entries) {
    const full = (e.webkitRelativePath || e.name).split('/');
    if (!rootName && full.length > 1) rootName = full[0];
    const parts = full.length > 1 ? full.slice(1) : full;
    const name = parts[parts.length - 1];
    const kind = kindOf(name);
    if (!kind) continue;
    if (parts.some((p) => p.startsWith('.'))) {
      hiddenSkipped++;
      continue;
    }
    if (files.length >= maxFiles) {
      truncated = true;
      continue;
    }
    files.push({
      path: parts.join('/'),
      name,
      dir: parts.slice(0, -1).join('/'),
      size: e.size,
      modTime: e.lastModified,
      kind,
      file: e,
    });
  }
  files.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  return { rootName, files, hiddenSkipped, truncated };
}

// プレビュー前に表示する注意。問題なければ null。
export function sizeNotice(size) {
  if (size > MAX_FILE_BYTES) {
    return { block: true, text: `ファイルが大きすぎるため開けない (${fmt(size)} / 上限 ${fmt(MAX_FILE_BYTES)})` };
  }
  if (size > LARGE_FILE_BYTES) {
    return { block: false, text: `大きなファイル (${fmt(size)}) を解析中。時間がかかる場合がある…` };
  }
  return null;
}

function fmt(n) {
  return `${Math.round(n / 1024 / 1024)} MB`;
}
