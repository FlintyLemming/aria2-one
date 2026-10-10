const test = require('node:test');
const assert = require('node:assert');

const m = require('./mirror.js');

test('module exports helpers without touching browser globals', () => {
  assert.strictEqual(typeof m, 'object');
  assert.strictEqual(m.API_BASE, '/api');
  assert.ok(m.REPO_RE instanceof RegExp);
  assert.strictEqual(typeof globalThis.window, 'undefined');
});

test('toInt accepts integers and integer strings only', () => {
  assert.strictEqual(m.toInt(5, 0), 5);
  assert.strictEqual(m.toInt(-3, 0), -3);
  assert.strictEqual(m.toInt(' 42 ', 0), 42);
  assert.strictEqual(m.toInt('+7', 0), 7);
  assert.strictEqual(m.toInt('-1', 0), -1);
  assert.strictEqual(m.toInt(1.5, 9), 9);
  assert.strictEqual(m.toInt('1.5', 9), 9);
  assert.strictEqual(m.toInt('abc', 9), 9);
  assert.strictEqual(m.toInt('', 9), 9);
  assert.strictEqual(m.toInt(null, 9), 9);
  assert.strictEqual(m.toInt(undefined, 9), 9);
  assert.strictEqual(m.toInt(NaN, 9), 9);
  assert.strictEqual(m.toInt(Infinity, 9), 9);
  assert.ok(Number.isNaN(m.toInt('x', NaN)));
});

test('normalizeRepo handles owner/name and GitHub URLs', () => {
  const cases = {
    'microsoft/PowerToys': 'microsoft/PowerToys',
    '  microsoft/PowerToys  ': 'microsoft/PowerToys',
    'microsoft/PowerToys/': 'microsoft/PowerToys',
    'microsoft/PowerToys.git': 'microsoft/PowerToys',
    'https://github.com/microsoft/PowerToys': 'microsoft/PowerToys',
    'http://github.com/microsoft/PowerToys': 'microsoft/PowerToys',
    'https://www.github.com/microsoft/PowerToys': 'microsoft/PowerToys',
    'HTTPS://GitHub.com/microsoft/PowerToys': 'microsoft/PowerToys',
    'github.com/microsoft/PowerToys': 'microsoft/PowerToys',
    'https://github.com/microsoft/PowerToys.git': 'microsoft/PowerToys',
    'https://github.com/microsoft/PowerToys/releases': 'microsoft/PowerToys',
    'https://github.com/microsoft/PowerToys/releases/tag/v0.85.1': 'microsoft/PowerToys',
    'https://github.com/microsoft/PowerToys?tab=readme': 'microsoft/PowerToys',
    'https://github.com/microsoft/PowerToys#readme': 'microsoft/PowerToys',
    'git@github.com:microsoft/PowerToys.git': 'microsoft/PowerToys',
    'jeessy2/ddns-go': 'jeessy2/ddns-go',
    'a_b/c.d-e': 'a_b/c.d-e'
  };
  for (const [input, want] of Object.entries(cases)) {
    assert.strictEqual(m.normalizeRepo(input), want, input);
  }
});

test('normalizeRepo rejects invalid input', () => {
  const bad = [
    '', '   ', null, undefined, 42, {},
    'PowerToys',
    'microsoft/',
    '/PowerToys',
    'https://github.com/microsoft',
    'https://github.com/',
    'gitlab.com/owner/name',
    'https://gitlab.com/owner/name',
    'owner/name/extra',
    'own er/name',
    'owner/na me',
    'owner/name!',
    'owner/.git'
  ];
  for (const input of bad) {
    assert.strictEqual(m.normalizeRepo(input), '', String(input));
  }
});

test('repoName and repoUrl', () => {
  assert.strictEqual(m.repoName('microsoft/PowerToys'), 'PowerToys');
  assert.strictEqual(m.repoName('https://github.com/jeessy2/ddns-go/releases'), 'ddns-go');
  assert.strictEqual(m.repoName('invalid'), '');
  assert.strictEqual(m.repoName(undefined), '');
  assert.strictEqual(m.repoUrl('microsoft/PowerToys'), 'https://github.com/microsoft/PowerToys');
  assert.strictEqual(m.repoUrl('javascript:alert(1)'), '');
  assert.strictEqual(m.repoUrl('a/b/c'), '');
  assert.strictEqual(m.repoUrl(null), '');
});

test('parseFileTypes splits on commas and whitespace, strips dots, dedupes', () => {
  assert.deepStrictEqual(m.parseFileTypes('exe, msi'), ['exe', 'msi']);
  assert.deepStrictEqual(m.parseFileTypes('exe msi\tdeb\npkg'), ['exe', 'msi', 'deb', 'pkg']);
  assert.deepStrictEqual(m.parseFileTypes('.exe,,  .msi , '), ['exe', 'msi']);
  assert.deepStrictEqual(m.parseFileTypes('tar.gz, .tar.gz, ..zip'), ['tar.gz', 'zip']);
  assert.deepStrictEqual(m.parseFileTypes('EXE, exe, Exe'), ['EXE']);
  assert.deepStrictEqual(m.parseFileTypes('exe，msi、deb;pkg；dmg'), ['exe', 'msi', 'deb', 'pkg', 'dmg']);
  assert.deepStrictEqual(m.parseFileTypes(''), []);
  assert.deepStrictEqual(m.parseFileTypes('  , . ,'), []);
  assert.deepStrictEqual(m.parseFileTypes(null), []);
  assert.deepStrictEqual(m.parseFileTypes(undefined), []);
  assert.deepStrictEqual(m.parseFileTypes(12), []);
  assert.deepStrictEqual(m.parseFileTypes(['exe', ' .msi ', '', null, 'exe', 'a b']), ['exe', 'msi', 'a', 'b']);
});

test('formatFileTypes joins arrays and round-trips with parseFileTypes', () => {
  assert.strictEqual(m.formatFileTypes(['exe', 'msi']), 'exe, msi');
  assert.strictEqual(m.formatFileTypes([]), '');
  assert.strictEqual(m.formatFileTypes(null), '');
  assert.strictEqual(m.formatFileTypes(undefined), '');
  assert.strictEqual(m.formatFileTypes(['exe', '', null, 3]), 'exe');
  assert.strictEqual(m.formatFileTypes('exe msi'), 'exe msi');
  const types = ['exe', 'tar.gz', 'AppImage'];
  assert.deepStrictEqual(m.parseFileTypes(m.formatFileTypes(types)), types);
});

test('statusLabel maps every status to a label class and Chinese text', () => {
  assert.deepStrictEqual(m.statusLabel('idle'), { cls: 'label-default', text: '未检查', active: false });
  assert.deepStrictEqual(m.statusLabel('checking'), { cls: 'label-primary', text: '检查中', active: true });
  assert.deepStrictEqual(m.statusLabel('downloading'), { cls: 'label-primary', text: '下载中', active: true });
  assert.deepStrictEqual(m.statusLabel('synced'), { cls: 'label-success', text: '已同步', active: false });
  assert.deepStrictEqual(m.statusLabel('incomplete'), { cls: 'label-warning', text: '不完整', active: false });
  assert.deepStrictEqual(m.statusLabel('error'), { cls: 'label-danger', text: '错误', active: false });
  assert.deepStrictEqual(m.statusLabel('weird'), { cls: 'label-default', text: 'weird', active: false });
  assert.deepStrictEqual(m.statusLabel(''), { cls: 'label-default', text: '未知', active: false });
  assert.deepStrictEqual(m.statusLabel(undefined), { cls: 'label-default', text: '未知', active: false });
  // 不会命中原型链属性
  assert.deepStrictEqual(m.statusLabel('toString'), { cls: 'label-default', text: 'toString', active: false });
  // 返回副本，修改不影响后续调用
  const l = m.statusLabel('synced');
  l.text = 'x';
  assert.strictEqual(m.statusLabel('synced').text, '已同步');
});

test('formatBytes', () => {
  assert.strictEqual(m.formatBytes(0), '0 B');
  assert.strictEqual(m.formatBytes(1), '1 B');
  assert.strictEqual(m.formatBytes(1023), '1023 B');
  assert.strictEqual(m.formatBytes(1024), '1.0 KB');
  assert.strictEqual(m.formatBytes(1536), '1.5 KB');
  assert.strictEqual(m.formatBytes(123456), '121 KB');
  assert.strictEqual(m.formatBytes(10 * 1024 * 1024), '10.0 MB');
  assert.strictEqual(m.formatBytes(150 * 1024 * 1024), '150 MB');
  assert.strictEqual(m.formatBytes(1.5 * 1024 * 1024 * 1024), '1.5 GB');
  assert.strictEqual(m.formatBytes(2 * Math.pow(1024, 4)), '2.0 TB');
  assert.strictEqual(m.formatBytes(Math.pow(1024, 6)), '1024 PB');
  assert.strictEqual(m.formatBytes('2048'), '2.0 KB');
  assert.strictEqual(m.formatBytes(-1), '-');
  assert.strictEqual(m.formatBytes(NaN), '-');
  assert.strictEqual(m.formatBytes(null), '-');
  assert.strictEqual(m.formatBytes(undefined), '-');
  assert.strictEqual(m.formatBytes(''), '-');
  assert.strictEqual(m.formatBytes('abc'), '-');
});

test('parseTime treats empty, missing and Go zero time as null', () => {
  assert.strictEqual(m.parseTime(''), null);
  assert.strictEqual(m.parseTime('   '), null);
  assert.strictEqual(m.parseTime(null), null);
  assert.strictEqual(m.parseTime(undefined), null);
  assert.strictEqual(m.parseTime('0001-01-01T00:00:00Z'), null);
  assert.strictEqual(m.parseTime('not a date'), null);
  assert.strictEqual(m.parseTime(0), null);
  assert.strictEqual(m.parseTime({}), null);
  assert.strictEqual(m.parseTime('2026-10-08T10:00:00Z'), Date.UTC(2026, 9, 8, 10, 0, 0));
  assert.strictEqual(m.parseTime('2026-10-08T18:00:00+08:00'), Date.UTC(2026, 9, 8, 10, 0, 0));
  assert.strictEqual(m.parseTime(1700000000000), 1700000000000);
  assert.strictEqual(m.parseTime(new Date(1700000000000)), 1700000000000);
  assert.strictEqual(m.parseTime(new Date('invalid')), null);
});

test('formatDate / formatDateTime use local time', () => {
  const local = new Date(2026, 0, 2, 3, 4, 5).getTime();
  assert.strictEqual(m.formatDate(local), '2026-01-02');
  assert.strictEqual(m.formatDateTime(local), '2026-01-02 03:04:05');
  assert.strictEqual(m.formatDateTime(new Date(2026, 11, 31, 23, 59, 9)), '2026-12-31 23:59:09');
  assert.strictEqual(m.formatDate(''), '');
  assert.strictEqual(m.formatDateTime(''), '');
  assert.strictEqual(m.formatDateTime('0001-01-01T00:00:00Z'), '');
  assert.strictEqual(m.formatDateTime(undefined), '');
});

test('formatRelativeTime', () => {
  const now = Date.UTC(2026, 9, 8, 12, 0, 0);
  const ago = (s) => new Date(now - s * 1000).toISOString();
  assert.strictEqual(m.formatRelativeTime('', now), '');
  assert.strictEqual(m.formatRelativeTime(undefined, now), '');
  assert.strictEqual(m.formatRelativeTime('0001-01-01T00:00:00Z', now), '');
  assert.strictEqual(m.formatRelativeTime(ago(0), now), '刚刚');
  assert.strictEqual(m.formatRelativeTime(ago(59), now), '刚刚');
  assert.strictEqual(m.formatRelativeTime(ago(60), now), '1 分钟前');
  assert.strictEqual(m.formatRelativeTime(ago(59 * 60 + 59), now), '59 分钟前');
  assert.strictEqual(m.formatRelativeTime(ago(3600), now), '1 小时前');
  assert.strictEqual(m.formatRelativeTime(ago(23 * 3600 + 3599), now), '23 小时前');
  assert.strictEqual(m.formatRelativeTime(ago(86400), now), '1 天前');
  assert.strictEqual(m.formatRelativeTime(ago(29 * 86400), now), '29 天前');
  assert.strictEqual(m.formatRelativeTime(ago(-30), now), '刚刚');
  assert.strictEqual(m.formatRelativeTime(ago(-5 * 60), now), '5 分钟后');
  assert.strictEqual(m.formatRelativeTime(ago(-2 * 86400), now), '2 天后');
  const old = now - 40 * 86400 * 1000;
  assert.strictEqual(m.formatRelativeTime(new Date(old).toISOString(), now), m.formatDate(old));
  // now 可为 Date
  assert.strictEqual(m.formatRelativeTime(ago(120), new Date(now)), '2 分钟前');
  // 缺省 now 使用当前时间
  assert.strictEqual(m.formatRelativeTime(new Date().toISOString()), '刚刚');
});

test('progressSummary computes counts, sizes and percent from state.files', () => {
  const empty = m.progressSummary([]);
  assert.strictEqual(empty.total, 0);
  assert.strictEqual(empty.text, '-');
  assert.strictEqual(empty.percent, 0);
  assert.strictEqual(m.progressSummary(undefined).text, '-');
  assert.strictEqual(m.progressSummary(null).total, 0);

  const synced = m.progressSummary([
    { name: 'a', size: 100, status: 'done', completedLength: 100 },
    { name: 'b', size: 300, status: 'done' }
  ]);
  assert.deepStrictEqual(synced, {
    total: 2, done: 2, downloading: 0, pending: 0, failed: 0,
    totalSize: 400, completedSize: 400, percent: 100, text: '2/2'
  });

  const dl = m.progressSummary([
    { name: 'a', size: 100, status: 'done', completedLength: 100 },
    { name: 'b', size: 300, status: 'downloading', completedLength: 50 },
    { name: 'c', size: 600, status: 'pending', completedLength: 0 }
  ]);
  assert.strictEqual(dl.total, 3);
  assert.strictEqual(dl.done, 1);
  assert.strictEqual(dl.downloading, 1);
  assert.strictEqual(dl.pending, 1);
  assert.strictEqual(dl.totalSize, 1000);
  assert.strictEqual(dl.completedSize, 150);
  assert.strictEqual(dl.percent, 15);
  assert.strictEqual(dl.text, '1/3');

  // 字符串数值、超出 size 的 completedLength 被截断
  const str = m.progressSummary([{ size: '200', status: 'downloading', completedLength: '500' }]);
  assert.strictEqual(str.completedSize, 200);
  // 未全部完成时不显示 100%
  assert.strictEqual(str.percent, 99);

  const almost = m.progressSummary([{ size: 100000, status: 'downloading', completedLength: 99999 }]);
  assert.strictEqual(almost.percent, 99);

  // 没有 size 信息时按文件数计算
  const nosize = m.progressSummary([{ status: 'done' }, { status: 'pending' }, null]);
  assert.strictEqual(nosize.total, 3);
  assert.strictEqual(nosize.done, 1);
  assert.strictEqual(nosize.pending, 2);
  assert.strictEqual(nosize.totalSize, 0);
  assert.strictEqual(nosize.percent, 33);
  assert.strictEqual(nosize.text, '1/3');

  // 非法 size / completedLength 视为 0
  const bad = m.progressSummary([{ size: -5, status: 'downloading', completedLength: 'x' }]);
  assert.strictEqual(bad.totalSize, 0);
  assert.strictEqual(bad.completedSize, 0);
  assert.strictEqual(bad.percent, 0);

  // 只统计 pending 且带 error 的文件；done / downloading 上的 error 忽略
  const failed = m.progressSummary([
    { name: 'a', size: 10, status: 'pending', error: 'Resource not found' },
    { name: 'b', size: 10, status: 'pending', error: '  ' },
    { name: 'c', size: 10, status: 'pending' },
    { name: 'd', size: 10, status: 'done', error: 'stale' },
    { name: 'e', size: 10, status: 'downloading', error: 'stale' }
  ]);
  assert.strictEqual(failed.pending, 3);
  assert.strictEqual(failed.failed, 1);
  assert.strictEqual(dl.failed, 0);
});

test('fileErrors lists pending files with a non-empty error', () => {
  assert.deepStrictEqual(m.fileErrors([
    { name: 'a.exe', status: 'pending', error: ' Resource not found ' },
    { name: 'b.exe', status: 'pending', error: '' },
    { name: 'c.exe', status: 'pending' },
    { name: 'd.exe', status: 'done', error: 'x' },
    { name: 'e.exe', status: 'downloading', error: 'x' },
    { name: 'f.exe', error: 'errorCode=3' },
    { name: 'g.exe', status: 'pending', error: 12 },
    null
  ]), [
    { name: 'a.exe', error: 'Resource not found' },
    { name: 'f.exe', error: 'errorCode=3' }
  ]);
  assert.deepStrictEqual(m.fileErrors(undefined), []);
  assert.deepStrictEqual(m.fileErrors('x'), []);
});

test('statusError shows the reason for error and incomplete only', () => {
  assert.strictEqual(m.statusError({ status: 'error', lastError: ' GitHub API 限流 ' }), 'GitHub API 限流');
  assert.strictEqual(m.statusError({ status: 'error', lastError: '' }), '');
  assert.strictEqual(
    m.statusError({ status: 'incomplete', lastError: '部分文件下载失败：a.exe：Resource not found' }),
    '部分文件下载失败：a.exe：Resource not found'
  );
  // 旧后端：incomplete 没有 lastError 时由 file.error 汇总
  assert.strictEqual(m.statusError({
    status: 'incomplete', lastError: '',
    files: [
      { name: 'a.exe', status: 'pending', error: 'Resource not found' },
      { name: 'b.exe', status: 'done' },
      { name: 'c.exe', status: 'pending', error: 'errorCode=3' }
    ]
  }), '部分文件下载失败：a.exe：Resource not found；c.exe：errorCode=3');
  assert.strictEqual(m.statusError({ status: 'incomplete', files: [{ name: 'a', status: 'pending' }] }), '');
  // 其它状态不显示（即使残留 lastError）
  ['idle', 'checking', 'downloading', 'synced', 'weird', undefined].forEach((status) => {
    assert.strictEqual(m.statusError({ status, lastError: 'old error' }), '', String(status));
  });
  assert.strictEqual(m.statusError(null), '');
  assert.strictEqual(m.statusError({ status: 'error', lastError: 5 }), '');
});

test('describeInterval', () => {
  assert.strictEqual(m.describeInterval(0), '仅手动检查');
  assert.strictEqual(m.describeInterval(10), '每 10 分钟检查');
  assert.strictEqual(m.describeInterval(90), '每 90 分钟检查');
  assert.strictEqual(m.describeInterval(360), '每 6 小时检查');
  assert.strictEqual(m.describeInterval(1440), '每 1 天检查');
  assert.strictEqual(m.describeInterval(2880), '每 2 天检查');
  assert.strictEqual(m.describeInterval('60'), '每 1 小时检查');
  assert.strictEqual(m.describeInterval(undefined), '');
  assert.strictEqual(m.describeInterval('abc'), '');
});

test('describeMirrorRules', () => {
  assert.deepStrictEqual(m.describeMirrorRules({ fileTypes: [], regex: '' }), ['全部文件']);
  assert.deepStrictEqual(m.describeMirrorRules(undefined), ['全部文件']);
  assert.deepStrictEqual(
    m.describeMirrorRules({ fileTypes: ['exe', 'msi'], regex: '^x64', includePrerelease: true, keepVersions: 3, saveChangelog: false }),
    ['后缀: exe, msi', '正则: ^x64', '含预发布', '保留最新 3 个版本', '不保存 CHANGELOG']
  );
  assert.deepStrictEqual(m.describeMirrorRules({ regex: '(osx|win)', saveChangelog: true, keepVersions: 0 }), ['正则: (osx|win)']);
});

test('joinPath', () => {
  assert.strictEqual(m.joinPath('/downloads', 'github-mirror', 'PowerToys'), '/downloads/github-mirror/PowerToys');
  assert.strictEqual(m.joinPath('/downloads/', '/mirrors/github/', 'X'), '/downloads/mirrors/github/X');
  assert.strictEqual(m.joinPath('/downloads', undefined, ''), '/downloads');
  assert.strictEqual(m.joinPath('a', 'b'), 'a/b');
  assert.strictEqual(m.joinPath('/'), '/');
  assert.strictEqual(m.joinPath(), '');
  assert.strictEqual(m.joinPath(null, 'x'), 'x');
});

test('safeHttpUrl and safeDownloadUrl', () => {
  assert.strictEqual(m.safeHttpUrl('https://github.com/a/b/releases/tag/v1'), 'https://github.com/a/b/releases/tag/v1');
  assert.strictEqual(m.safeHttpUrl('http://x'), 'http://x');
  assert.strictEqual(m.safeHttpUrl('javascript:alert(1)'), '');
  assert.strictEqual(m.safeHttpUrl('//evil.com'), '');
  assert.strictEqual(m.safeHttpUrl(''), '');
  assert.strictEqual(m.safeHttpUrl(undefined), '');
  assert.strictEqual(m.safeHttpUrl('https://a b'), '');

  assert.strictEqual(m.safeDownloadUrl('/download/github-mirror/A/v1/a.exe'), '/download/github-mirror/A/v1/a.exe');
  assert.strictEqual(m.safeDownloadUrl('https://example.com/a'), 'https://example.com/a');
  assert.strictEqual(m.safeDownloadUrl('//evil.com/a'), '');
  assert.strictEqual(m.safeDownloadUrl('/\\evil.com'), '');
  assert.strictEqual(m.safeDownloadUrl('javascript:alert(1)'), '');
  assert.strictEqual(m.safeDownloadUrl('download/a'), '');
  assert.strictEqual(m.safeDownloadUrl(''), '');
  assert.strictEqual(m.safeDownloadUrl(null), '');
});

test('mirrorPath and deletePath URL-encode ids', () => {
  assert.strictEqual(m.mirrorPath('9f2c1a7b3d4e5f60'), '/api/mirrors/9f2c1a7b3d4e5f60');
  assert.strictEqual(m.mirrorPath('9f2c', 'sync'), '/api/mirrors/9f2c/sync');
  assert.strictEqual(m.mirrorPath('9f2c', 'versions'), '/api/mirrors/9f2c/versions');
  assert.strictEqual(m.mirrorPath('a/b?c#d e'), '/api/mirrors/a%2Fb%3Fc%23d%20e');
  assert.strictEqual(m.mirrorPath('..'), '/api/mirrors/..');
  assert.strictEqual(m.mirrorPath(undefined), '/api/mirrors/');
  assert.strictEqual(m.deletePath('abc', true), '/api/mirrors/abc?deleteFiles=true');
  assert.strictEqual(m.deletePath('abc', false), '/api/mirrors/abc');
  assert.strictEqual(m.deletePath('a/b', true), '/api/mirrors/a%2Fb?deleteFiles=true');
});

test('dialogSafeText neutralizes interpolation braces', () => {
  assert.strictEqual(m.dialogSafeText('{{constructor.constructor("x")()}}'), '｛｛constructor.constructor("x")()｝｝');
  assert.strictEqual(m.dialogSafeText('PowerToys'), 'PowerToys');
  assert.strictEqual(m.dialogSafeText(null), '');
  assert.strictEqual(m.dialogSafeText(12), '12');
  assert.ok(!/[{}]/.test(m.dialogSafeText('{a}{{b}}')));
});

test('extractError prefers backend {error}', () => {
  assert.strictEqual(m.extractError({ status: 400, data: { error: '仓库格式无效' } }), '仓库格式无效');
  assert.strictEqual(m.extractError({ status: 409, data: { error: '  文件夹已被占用 ' } }), '文件夹已被占用');
  assert.strictEqual(m.extractError({ status: 415, data: { error: 'Content-Type 必须为 application/json' } }), 'Content-Type 必须为 application/json');
  assert.strictEqual(m.extractError({ status: -1, data: null }), '无法连接到服务器，请检查网络');
  assert.strictEqual(m.extractError({ status: 0 }), '无法连接到服务器，请检查网络');
  assert.match(m.extractError({ status: 401, data: '<html>' }), /401/);
  assert.match(m.extractError({ status: 404, data: '' }), /404/);
  assert.strictEqual(m.extractError({ status: 502, statusText: 'Bad Gateway', data: '<html>bad</html>' }), '请求失败（HTTP 502 Bad Gateway）');
  assert.strictEqual(m.extractError({ status: 500, data: { error: '' } }), '请求失败（HTTP 500）');
  assert.strictEqual(m.extractError({ status: 500, data: { error: 123 } }), '请求失败（HTTP 500）');
  assert.strictEqual(m.extractError(null), '未知错误');
  assert.strictEqual(m.extractError('x'), '未知错误');
});

test('newMirrorForm defaults', () => {
  assert.deepStrictEqual(m.newMirrorForm(), {
    id: '', name: '', repo: '', folder: '', enabled: true, saveChangelog: true,
    fileTypesText: '', regex: '', includePrerelease: false, keepVersions: 0
  });
  // 每次返回新对象
  assert.notStrictEqual(m.newMirrorForm(), m.newMirrorForm());
});

const SAMPLE = {
  id: '9f2c1a7b3d4e5f60',
  name: 'PowerToys',
  repo: 'microsoft/PowerToys',
  folder: 'PowerToys',
  enabled: true,
  saveChangelog: true,
  fileTypes: ['exe', 'msi'],
  regex: '',
  includePrerelease: false,
  keepVersions: 2,
  createdAt: '2026-10-08T10:00:00Z',
  state: { status: 'synced', files: [] }
};

test('formFromMirror converts a MirrorView into an editable form', () => {
  assert.deepStrictEqual(m.formFromMirror(SAMPLE), {
    id: '9f2c1a7b3d4e5f60', name: 'PowerToys', repo: 'microsoft/PowerToys', folder: 'PowerToys',
    enabled: true, saveChangelog: true, fileTypesText: 'exe, msi', regex: '',
    includePrerelease: false, keepVersions: 2
  });
  const f = m.formFromMirror({ id: 'x', enabled: false, saveChangelog: false, fileTypes: null });
  assert.strictEqual(f.enabled, false);
  assert.strictEqual(f.saveChangelog, false);
  assert.strictEqual(f.fileTypesText, '');
  assert.strictEqual(f.keepVersions, 0);
  assert.deepStrictEqual(m.formFromMirror(undefined), m.newMirrorForm());
});

test('validateMirrorForm', () => {
  assert.match(m.validateMirrorForm({ repo: '' }), /仓库/);
  assert.match(m.validateMirrorForm({ repo: '   ' }), /仓库/);
  assert.match(m.validateMirrorForm(undefined), /仓库/);
  assert.strictEqual(m.validateMirrorForm({ repo: 'a/b', keepVersions: 0 }), '');
  assert.strictEqual(m.validateMirrorForm({ repo: 'a/b', keepVersions: null }), '');
  assert.strictEqual(m.validateMirrorForm({ repo: 'a/b', keepVersions: undefined }), '');
  assert.strictEqual(m.validateMirrorForm({ repo: 'a/b', keepVersions: '3' }), '');
  assert.match(m.validateMirrorForm({ repo: 'a/b', keepVersions: -1 }), /保留版本数/);
  assert.match(m.validateMirrorForm({ repo: 'a/b', keepVersions: 1.5 }), /保留版本数/);
  assert.match(m.validateMirrorForm({ repo: 'a/b', keepVersions: 'x' }), /保留版本数/);
  // 仓库格式由后端校验并返回错误，前端不拦截
  assert.strictEqual(m.validateMirrorForm({ repo: 'not-a-repo' }), '');
});

test('buildMirrorInput produces a full MirrorInput', () => {
  const input = m.buildMirrorInput({
    id: 'ignored',
    name: '  PowerToys ',
    repo: ' https://github.com/microsoft/PowerToys/releases ',
    folder: ' PT ',
    enabled: false,
    saveChangelog: false,
    fileTypesText: '.exe, msi msi',
    regex: ' ^PowerToys ',
    includePrerelease: true,
    keepVersions: '3'
  });
  assert.deepStrictEqual(input, {
    name: 'PowerToys',
    repo: 'microsoft/PowerToys',
    folder: 'PT',
    enabled: false,
    saveChangelog: false,
    fileTypes: ['exe', 'msi'],
    regex: '^PowerToys',
    includePrerelease: true,
    keepVersions: 3
  });
  assert.deepStrictEqual(Object.keys(input).sort(), [
    'enabled', 'fileTypes', 'folder', 'includePrerelease', 'keepVersions', 'name', 'regex', 'repo', 'saveChangelog'
  ]);

  // 无法识别的仓库原样提交，交给后端报错
  assert.strictEqual(m.buildMirrorInput({ repo: ' not a repo ' }).repo, 'not a repo');
  // 默认值
  const d = m.buildMirrorInput({});
  assert.deepStrictEqual(d, {
    name: '', repo: '', folder: '', enabled: true, saveChangelog: true,
    fileTypes: [], regex: '', includePrerelease: false, keepVersions: 0
  });
  // 也接受 fileTypes 数组
  assert.deepStrictEqual(m.buildMirrorInput({ fileTypes: ['.zip', 'ZIP'] }).fileTypes, ['zip']);
  // 负数原样提交，交给后端校验
  assert.strictEqual(m.buildMirrorInput({ keepVersions: -2 }).keepVersions, -2);
  assert.strictEqual(m.buildMirrorInput({ keepVersions: null }).keepVersions, 0);
});

test('mirrorToInput builds a full PUT body with overrides', () => {
  const view = m.normalizeMirror(SAMPLE);
  const input = m.mirrorToInput(view, { enabled: false });
  assert.deepStrictEqual(input, {
    name: 'PowerToys', repo: 'microsoft/PowerToys', folder: 'PowerToys',
    enabled: false, saveChangelog: true, fileTypes: ['exe', 'msi'], regex: '',
    includePrerelease: false, keepVersions: 2
  });
  assert.strictEqual(input.state, undefined);
  assert.strictEqual(input.view, undefined);
  assert.strictEqual(input.id, undefined);
  assert.strictEqual(m.mirrorToInput(view).enabled, true);
});

test('settingsToForm / validateSettingsForm / buildSettingsInput', () => {
  const form = m.settingsToForm({ rootDir: 'github-mirror', intervalMinutes: 360, proxy: '', githubTokenSet: true });
  assert.deepStrictEqual(form, {
    rootDir: 'github-mirror', intervalMinutes: 360, proxy: '', githubToken: '', clearToken: false, tokenSet: true
  });
  assert.strictEqual(m.settingsToForm(undefined).tokenSet, false);
  assert.strictEqual(m.settingsToForm({ githubTokenSet: 'yes' }).tokenSet, false);

  assert.strictEqual(m.validateSettingsForm({ intervalMinutes: 0 }), '');
  assert.strictEqual(m.validateSettingsForm({ intervalMinutes: 10 }), '');
  assert.strictEqual(m.validateSettingsForm({ intervalMinutes: 43200 }), '');
  assert.strictEqual(m.validateSettingsForm({ intervalMinutes: '60' }), '');
  assert.match(m.validateSettingsForm({ intervalMinutes: 5 }), /10 ~ 43200/);
  assert.match(m.validateSettingsForm({ intervalMinutes: 43201 }), /10 ~ 43200/);
  assert.match(m.validateSettingsForm({ intervalMinutes: -1 }), /10 ~ 43200/);
  assert.match(m.validateSettingsForm({ intervalMinutes: null }), /整数/);
  assert.match(m.validateSettingsForm({ intervalMinutes: undefined }), /整数/);
  assert.match(m.validateSettingsForm({ intervalMinutes: 12.5 }), /整数/);
  assert.match(m.validateSettingsForm(undefined), /整数/);

  // Token 留空 → 不发送（保持不变）
  assert.deepStrictEqual(m.buildSettingsInput({ rootDir: ' /mirrors/ ', intervalMinutes: 60, proxy: ' http://p:1 ', githubToken: '  ' }), {
    rootDir: '/mirrors/', intervalMinutes: 60, proxy: 'http://p:1'
  });
  assert.ok(!('githubToken' in m.buildSettingsInput({ intervalMinutes: 60 })));
  // 填写 Token → 设置
  assert.strictEqual(m.buildSettingsInput({ intervalMinutes: 60, githubToken: ' ghp_x ' }).githubToken, 'ghp_x');
  // 勾选清除 → ""，优先于输入框
  assert.strictEqual(m.buildSettingsInput({ intervalMinutes: 60, githubToken: 'ghp_x', clearToken: true }).githubToken, '');
  assert.strictEqual(m.buildSettingsInput({ intervalMinutes: '30' }).intervalMinutes, 30);
});

test('normalizeImportResult', () => {
  assert.deepStrictEqual(m.normalizeImportResult({ imported: 2, skipped: [{ key: 'a', reason: '重复' }], mirrors: [] }), {
    imported: 2, skipped: [{ key: 'a', reason: '重复' }]
  });
  assert.deepStrictEqual(m.normalizeImportResult(null), { imported: 0, skipped: [] });
  assert.deepStrictEqual(m.normalizeImportResult({ skipped: [null, { key: 3 }] }), {
    imported: 0, skipped: [{ key: '', reason: '' }, { key: '3', reason: '' }]
  });
});

test('normalizeVersions sanitizes urls and fills defaults', () => {
  const out = m.normalizeVersions({
    versions: [
      { tag: 'v2', modTime: '2026-10-08T10:00:00Z', files: [
        { name: 'a.exe', size: 10, url: '/download/github-mirror/A/v2/a.exe' },
        { name: 'evil', size: 1, url: 'javascript:alert(1)' }
      ] },
      { tag: 'v1' },
      null
    ]
  });
  assert.strictEqual(out.length, 3);
  assert.deepStrictEqual(out[0].files, [
    { name: 'a.exe', size: 10, url: '/download/github-mirror/A/v2/a.exe', partial: false },
    { name: 'evil', size: 1, url: '', partial: false }
  ]);
  // 旧后端没有 latest / partial：不标最新、按已完成处理
  assert.strictEqual(out[0].latest, false);
  assert.deepStrictEqual(out[1], { tag: 'v1', modTime: '', latest: false, files: [] });
  assert.deepStrictEqual(out[2], { tag: '', modTime: '', latest: false, files: [] });
  assert.deepStrictEqual(m.normalizeVersions({}), []);
  assert.deepStrictEqual(m.normalizeVersions(null), []);
});

test('normalizeVersions uses backend latest / partial flags', () => {
  const out = m.normalizeVersions({
    versions: [
      // tag 含 / 时目录名已被安全化，前端不自行比较 tag
      { tag: 'cli_v1.4.0', latest: true, files: [
        { name: 'done.zip', size: 5, url: '/download/m/cli_v1.4.0/done.zip' },
        { name: 'part.zip', size: 9, url: '/download/m/cli_v1.4.0/part.zip', partial: true }
      ] },
      { tag: 'cli_v1.3.0', latest: 'true', files: [
        { name: 'old.zip', size: 3, url: '/download/m/cli_v1.3.0/old.zip', partial: 'yes' }
      ] }
    ]
  });
  assert.strictEqual(out[0].latest, true);
  assert.deepStrictEqual(out[0].files, [
    { name: 'done.zip', size: 5, url: '/download/m/cli_v1.4.0/done.zip', partial: false },
    // partial 文件不提供直链
    { name: 'part.zip', size: 9, url: '', partial: true }
  ]);
  // 只认布尔 true
  assert.strictEqual(out[1].latest, false);
  assert.deepStrictEqual(out[1].files[0], { name: 'old.zip', size: 3, url: '/download/m/cli_v1.3.0/old.zip', partial: false });
});

test('normalizeMirror fills state defaults and computes view', () => {
  const n = m.normalizeMirror({ id: 'x1', name: 'n', repo: 'a/b', folder: 'b', enabled: true, fileTypes: null });
  assert.strictEqual(n.state.status, 'idle');
  assert.deepStrictEqual(n.state.files, []);
  assert.strictEqual(n.state.lastCheck, '');
  assert.strictEqual(n.state.latestTag, '');
  assert.deepStrictEqual(n.fileTypes, []);
  assert.strictEqual(n.view.label.text, '未检查');
  assert.strictEqual(n.view.progress.text, '-');
  assert.strictEqual(n.view.rules, '全部文件');

  const full = m.normalizeMirror(Object.assign({}, SAMPLE, {
    state: {
      status: 'downloading',
      latestTag: 'v1',
      lastCheck: '2026-10-08T10:00:05Z',
      lastSuccess: '0001-01-01T00:00:00Z',
      files: [{ name: 'a', size: 100, status: 'downloading', completedLength: 25 }]
    }
  }));
  assert.strictEqual(full.state.lastCheck, '2026-10-08T10:00:05Z');
  assert.strictEqual(full.state.lastSuccess, '');
  assert.strictEqual(full.state.publishedAt, '');
  assert.strictEqual(full.view.label.cls, 'label-primary');
  assert.strictEqual(full.view.progress.percent, 25);
  assert.strictEqual(full.view.rules, '后缀: exe, msi · 保留最新 2 个版本');
  assert.strictEqual(full.createdAt, SAMPLE.createdAt);
  // 不修改原对象
  assert.strictEqual(SAMPLE.state.status, 'synced');
  assert.strictEqual(SAMPLE.view, undefined);

  const weird = m.normalizeMirror(null);
  assert.strictEqual(weird.id, '');
  assert.strictEqual(weird.enabled, true);
  assert.strictEqual(m.normalizeMirror({ enabled: false }).enabled, false);
  assert.strictEqual(m.normalizeMirror({ id: 12 }).id, '12');
  assert.strictEqual(n.view.error, '');
  assert.strictEqual(n.view.failedText, '');
  assert.deepStrictEqual(Object.keys(n.view.fileErrors), []);
});

test('normalizeMirror exposes failure reasons for incomplete mirrors', () => {
  const inc = m.normalizeMirror(Object.assign({}, SAMPLE, {
    state: {
      status: 'incomplete',
      lastError: '部分文件下载失败：b.msi：Resource not found',
      files: [
        { name: 'a.exe', size: 10, status: 'done' },
        { name: 'b.msi', size: 10, status: 'pending', error: 'Resource not found' },
        { name: 'constructor', size: 10, status: 'pending', error: 'errorCode=3' }
      ]
    }
  }));
  assert.strictEqual(inc.view.label.cls, 'label-warning');
  assert.strictEqual(inc.view.error, '部分文件下载失败：b.msi：Resource not found');
  assert.strictEqual(inc.view.progress.failed, 2);
  assert.strictEqual(inc.view.fileErrors['b.msi'], 'Resource not found');
  assert.strictEqual(inc.view.fileErrors['constructor'], 'errorCode=3');
  // 没有原型链，文件名不会命中 Object 的属性
  assert.strictEqual(inc.view.fileErrors['a.exe'], undefined);
  assert.strictEqual(inc.view.fileErrors['toString'], undefined);
  assert.strictEqual(inc.view.failedText, 'b.msi：Resource not found\nconstructor：errorCode=3');

  const err = m.normalizeMirror(Object.assign({}, SAMPLE, { state: { status: 'error', lastError: '仓库不存在或没有正式 Release' } }));
  assert.strictEqual(err.view.error, '仓库不存在或没有正式 Release');
  // 检查中不显示上一次残留的错误
  const checking = m.normalizeMirror(Object.assign({}, SAMPLE, { state: { status: 'checking', lastError: 'old' } }));
  assert.strictEqual(checking.view.error, '');
  assert.strictEqual(checking.state.lastError, 'old');
});

test('applyEnabledToForm only updates the form editing the same mirror', () => {
  const form = m.formFromMirror(SAMPLE);
  form.regex = 'unsaved';
  assert.strictEqual(m.applyEnabledToForm(form, SAMPLE.id, false), true);
  assert.strictEqual(form.enabled, false);
  assert.strictEqual(form.regex, 'unsaved');
  assert.strictEqual(m.applyEnabledToForm(form, SAMPLE.id, true), true);
  assert.strictEqual(form.enabled, true);

  assert.strictEqual(m.applyEnabledToForm(form, 'other', false), false);
  assert.strictEqual(form.enabled, true);
  // 添加表单（id 为空）不受影响
  const add = m.newMirrorForm();
  assert.strictEqual(m.applyEnabledToForm(add, '', false), false);
  assert.strictEqual(add.enabled, true);
  assert.strictEqual(m.applyEnabledToForm(null, SAMPLE.id, false), false);
  assert.strictEqual(m.applyEnabledToForm(undefined, SAMPLE.id, false), false);
});

// ===== 浏览器部分：在 vm 中用最小的 angular / window 替身加载 mirror.js =====
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');

function loadBrowserPart() {
  const registered = {};
  const app = {
    config(arr) { registered.config = arr; return app; },
    controller(name, arr) { registered.controller = arr; return app; }
  };
  const angular = {
    module: () => app,
    forEach: (list, fn) => Array.prototype.forEach.call(list || [], fn),
    element: (x) => x,
    extend: Object.assign
  };
  const document = {
    readyState: 'complete',
    getElementById: () => null,
    querySelector: () => null,
    querySelectorAll: () => [],
    addEventListener() {}
  };
  const ctx = { window: { angular, document }, angular, document, console: { warn() {}, log() {} }, setTimeout, clearTimeout };
  vm.createContext(ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname, 'mirror.js'), 'utf8'), ctx, { filename: 'mirror.js' });
  let route = null;
  registered.config[1]({ when: (p, r) => { route = Object.assign({ path: p }, r); } });
  const deps = registered.controller;
  return { route, Controller: deps[deps.length - 1], document };
}

const tick = () => new Promise((r) => setImmediate(r));
async function settle() {
  for (let i = 0; i < 10; i++) await tick();
}

function startController(server) {
  const { Controller, document } = loadBrowserPart();
  const timers = [];
  const $timeout = (fn, ms) => { const t = { fn, ms, cancelled: false }; timers.push(t); return t; };
  $timeout.cancel = (t) => { if (t) t.cancelled = true; };
  const $q = {
    resolve: (v) => Promise.resolve(v),
    defer: () => { let resolve; const promise = new Promise((r) => { resolve = r; }); return { promise, resolve }; }
  };
  const requests = [];
  const clone = (x) => JSON.parse(JSON.stringify(x));
  const $http = (cfg) => {
    requests.push(cfg);
    const hit = server.mirrors.find((x) => cfg.url === '/api/mirrors/' + x.id);
    if (cfg.method === 'GET' && cfg.url === '/api/mirrors') {
      return Promise.resolve({ status: 200, data: { settings: server.settings, downloadDir: '/downloads', mirrors: clone(server.mirrors) } });
    }
    if (cfg.method === 'PUT' && hit) {
      if (server.failPut) return Promise.reject({ status: 500, data: { error: '保存失败' } });
      Object.assign(hit, clone(cfg.data));
      return Promise.resolve({ status: 200, data: clone(hit) });
    }
    return Promise.reject({ status: 404, data: { error: '接口不存在' } });
  };
  $http.get = (url, cfg) => $http(Object.assign({ method: 'GET', url }, cfg));
  const listeners = {};
  const $scope = { $on: (name, fn) => { listeners[name] = fn; }, $evalAsync: (fn) => fn() };
  const $window = { document, confirm: () => true };
  Controller($scope, $http, $timeout, $q, $window, { has: () => false }, { enabled() {} });
  return {
    $scope,
    requests,
    async poll() {
      const t = timers.filter((x) => !x.cancelled && x.ms === 5000).pop();
      assert.ok(t, 'poll timer scheduled');
      t.cancelled = true;
      t.fn();
      await settle();
    },
    destroy() { if (listeners.$destroy) listeners.$destroy(); }
  };
}

function mirrorFixture(id, name) {
  return {
    id, name, repo: 'owner/' + name, folder: name, enabled: true, saveChangelog: true,
    fileTypes: ['exe'], regex: '', includePrerelease: false, keepVersions: 0,
    state: { status: 'synced', files: [] }
  };
}

test('row toggle updates an open edit form of the same mirror and keeps other unsaved edits', async () => {
  const server = {
    settings: { rootDir: 'github-mirror', intervalMinutes: 360, proxy: '', githubTokenSet: false },
    mirrors: [mirrorFixture('aaaa', 'Alpha'), mirrorFixture('bbbb', 'Beta')]
  };
  const c = startController(server);
  await settle();
  const $scope = c.$scope;
  assert.strictEqual($scope.mirrors.length, 2);

  $scope.openEdit($scope.mirrors[0]);
  $scope.form.regex = 'unsaved-regex';
  $scope.form.name = 'Unsaved name';
  assert.strictEqual($scope.form.enabled, true);

  // 5s 轮询：列表更新，但不覆盖打开的表单
  server.mirrors[0].name = 'Renamed on server';
  server.mirrors[0].state = { status: 'checking', files: [] };
  await c.poll();
  assert.strictEqual($scope.mirrors[0].name, 'Renamed on server');
  assert.strictEqual($scope.panel, 'form');
  assert.strictEqual($scope.form.id, 'aaaa');
  assert.strictEqual($scope.form.name, 'Unsaved name');
  assert.strictEqual($scope.form.regex, 'unsaved-regex');
  assert.strictEqual($scope.form.enabled, true);

  // 行内停用 Alpha → 表单的 enabled 同步为 false，其余修改保留
  $scope.toggleEnabled($scope.mirrors[0]);
  await settle();
  const put = c.requests.filter((r) => r.method === 'PUT').pop();
  assert.strictEqual(put.url, '/api/mirrors/aaaa');
  assert.strictEqual(put.data.enabled, false);
  assert.strictEqual($scope.mirrors[0].enabled, false);
  assert.strictEqual($scope.form.enabled, false);
  assert.strictEqual($scope.form.name, 'Unsaved name');
  assert.strictEqual($scope.form.regex, 'unsaved-regex');

  // 启停其它镜像不影响表单
  $scope.toggleEnabled($scope.mirrors[1]);
  await settle();
  assert.strictEqual(server.mirrors[1].enabled, false);
  assert.strictEqual($scope.form.enabled, false);

  // 启停失败时表单不变
  server.failPut = true;
  $scope.toggleEnabled($scope.mirrors[0]);
  await settle();
  assert.strictEqual($scope.form.enabled, false);
  server.failPut = false;

  // 之后的轮询仍不覆盖表单
  await c.poll();
  assert.strictEqual($scope.form.enabled, false);
  assert.strictEqual($scope.form.regex, 'unsaved-regex');

  // 保存表单时不会把启停改回去
  $scope.saveForm();
  await settle();
  const save = c.requests.filter((r) => r.method === 'PUT').pop();
  assert.strictEqual(save.url, '/api/mirrors/aaaa');
  assert.strictEqual(save.data.enabled, false);
  assert.strictEqual(save.data.regex, 'unsaved-regex');
  assert.strictEqual(save.data.name, 'Unsaved name');
  assert.strictEqual($scope.panel, '');
  c.destroy();
});

test('page template follows the versions / status / import contract', () => {
  const { route } = loadBrowserPart();
  assert.strictEqual(route.path, '/mirrors');
  assert.strictEqual(route.controller, 'AriaOneMirrorController');
  const tpl = route.template;
  // 最新标记来自后端 v.latest，不在前端比较 tag
  assert.ok(tpl.includes('ng-if="v.latest"'));
  assert.ok(!tpl.includes('v.tag === m.state.latestTag'));
  // partial 文件显示「未完成」（label-warning），直链只来自 normalizeVersions 处理后的 f.url
  assert.ok(/<span class="label label-warning" ng-if="f\.partial"[^>]*>未完成<\/span>/.test(tpl));
  assert.ok(tpl.includes('m.view.fileErrors[f.name]'));
  // 状态标签下显示 error / incomplete 的原因
  assert.ok(tpl.includes('ng-if="m.view.error" ng-bind="m.view.error"'));
  assert.ok(tpl.includes('m.view.progress.failed'));
  // 行操作按钮：文字放在可隐藏的 span 中，并保留 title / aria-label
  const start = tpl.indexOf('<td class="mirror-actions">');
  const actions = tpl.slice(start, tpl.indexOf('</td>', start));
  const buttons = actions.match(/<button[^>]*>/g);
  assert.strictEqual(buttons.length, 5);
  buttons.forEach((b) => {
    assert.ok(/ title="[^"]+"/.test(b), b);
    assert.ok(/ aria-label="[^"]+"/.test(b), b);
  });
  assert.strictEqual((actions.match(/class="mirror-btn-text"/g) || []).length, 5);
  // 导入说明与 spec 3.7 一致：导入不触发同步，定时任务约 30 秒内检查，不宣称避免 API 突发
  assert.ok(tpl.includes('导入本身不会触发检查'));
  assert.ok(tpl.includes('约 30 秒内'));
  assert.ok(!tpl.includes('避免'));
  assert.ok(!tpl.includes('导入后不会立即检查'));
});

test('inject.css keeps the mirror table within its container', () => {
  const css = fs.readFileSync(path.join(__dirname, 'inject.css'), 'utf8');
  const block = (sel) => {
    const i = css.indexOf(sel + ' {');
    assert.ok(i >= 0, sel);
    return css.slice(i, css.indexOf('}', i));
  };
  // 错误信息不再有大的 min-width，名称列 min-width 收窄
  assert.ok(!/min-width/.test(block('.aria-one-mirror .mirror-error')));
  assert.ok(/min-width:\s*110px/.test(block('.aria-one-mirror .mirror-name-cell')));
  // < 1400px 时操作按钮只显示图标
  const mq = css.indexOf('@media (max-width: 1399px)');
  assert.ok(mq >= 0);
  assert.ok(/\.mirror-actions \.mirror-btn-text \{[^}]*position:\s*absolute[^}]*clip:/.test(css.slice(mq)));
  // 名称 / 版本 / 状态列可换行（覆盖 Bootstrap 移动端 nowrap）
  assert.ok(/td\.mirror-status-cell \{[^}]*white-space:\s*normal[^}]*overflow-wrap:\s*anywhere/.test(css));
});
