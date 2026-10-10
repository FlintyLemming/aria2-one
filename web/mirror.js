(function () {
  'use strict';

  // ===== 纯函数（可在 Node 中单测） =====
  var REPO_RE = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;
  var API_BASE = '/api';

  var STATUS_LABELS = {
    idle: { cls: 'label-default', text: '未检查', active: false },
    checking: { cls: 'label-primary', text: '检查中', active: true },
    downloading: { cls: 'label-primary', text: '下载中', active: true },
    synced: { cls: 'label-success', text: '已同步', active: false },
    incomplete: { cls: 'label-warning', text: '不完整', active: false },
    error: { cls: 'label-danger', text: '错误', active: false }
  };

  function trimStr(v) {
    return typeof v === 'string' ? v.trim() : '';
  }

  function pad2(n) {
    return (n < 10 ? '0' : '') + n;
  }

  function toNumber(v) {
    if (typeof v === 'number') return isFinite(v) ? v : NaN;
    if (typeof v === 'string' && v.trim() !== '') return Number(v);
    return NaN;
  }

  // 整数解析：number 必须是整数，字符串必须是纯整数，否则返回 def
  function toInt(v, def) {
    if (typeof v === 'number') return isFinite(v) && Math.floor(v) === v ? v : def;
    if (typeof v === 'string' && /^\s*[-+]?\d+\s*$/.test(v)) return parseInt(v, 10);
    return def;
  }

  // 仓库地址规范化为 owner/name；无法识别时返回 ''
  function normalizeRepo(input) {
    var s = trimStr(input);
    if (!s) return '';
    var hadHost = false;
    var stripped = s
      .replace(/^git@github\.com:/i, '')
      .replace(/^(?:(?:https?|git|ssh):\/\/)?(?:[^@\/\s]+@)?(?:www\.)?github\.com\//i, '');
    if (stripped !== s) hadHost = true;
    stripped = stripped.replace(/[?#].*$/, '');
    var parts = stripped.split('/').filter(function (p) { return p !== ''; });
    // 纯 owner/name 形式不允许多余路径段，避免把 gitlab.com/a/b 误识别
    if (parts.length < 2 || (!hadHost && parts.length !== 2)) return '';
    var repo = parts[0] + '/' + parts[1].replace(/\.git$/i, '');
    return REPO_RE.test(repo) ? repo : '';
  }

  function repoName(input) {
    var repo = normalizeRepo(input);
    return repo ? repo.split('/')[1] : '';
  }

  function repoUrl(repo) {
    return typeof repo === 'string' && REPO_RE.test(repo) ? 'https://github.com/' + repo : '';
  }

  // 文件后缀：逗号 / 空白 / 中文逗号顿号分隔，去前导 "."，去空、去重（不区分大小写）
  function parseFileTypes(value) {
    var items;
    if (Array.isArray(value)) {
      items = [];
      value.forEach(function (v) {
        if (typeof v === 'string') items = items.concat(v.split(/[\s,，、;；]+/));
      });
    } else if (typeof value === 'string') {
      items = value.split(/[\s,，、;；]+/);
    } else {
      return [];
    }
    var seen = {};
    var out = [];
    items.forEach(function (raw) {
      var ext = raw.trim().replace(/^\.+/, '').trim();
      if (!ext) return;
      var key = ext.toLowerCase();
      if (seen[key]) return;
      seen[key] = true;
      out.push(ext);
    });
    return out;
  }

  function formatFileTypes(value) {
    if (Array.isArray(value)) {
      return value.filter(function (v) { return typeof v === 'string' && v !== ''; }).join(', ');
    }
    return typeof value === 'string' ? value : '';
  }

  function statusLabel(status) {
    var hit = Object.prototype.hasOwnProperty.call(STATUS_LABELS, status) ? STATUS_LABELS[status] : null;
    if (hit) return { cls: hit.cls, text: hit.text, active: hit.active };
    return { cls: 'label-default', text: status ? String(status) : '未知', active: false };
  }

  function formatBytes(value) {
    var n = toNumber(value);
    if (!isFinite(n) || n < 0) return '-';
    if (n < 1024) return Math.round(n) + ' B';
    var units = ['KB', 'MB', 'GB', 'TB', 'PB'];
    var v = n / 1024;
    var i = 0;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + ' ' + units[i];
  }

  // 时间解析：空串 / 缺省 / Go 零值（0001-01-01）/ 非法 → null
  function parseTime(value) {
    var t;
    if (value instanceof Date) t = value.getTime();
    else if (typeof value === 'number') t = value;
    else if (typeof value === 'string' && value.trim() !== '') t = Date.parse(value);
    else return null;
    if (!isFinite(t) || t <= 0) return null;
    return t;
  }

  function nowMs(now) {
    if (now instanceof Date) return now.getTime();
    if (typeof now === 'number' && isFinite(now)) return now;
    return Date.now();
  }

  function formatDate(value) {
    var t = parseTime(value);
    if (t === null) return '';
    var d = new Date(t);
    return d.getFullYear() + '-' + pad2(d.getMonth() + 1) + '-' + pad2(d.getDate());
  }

  function formatDateTime(value) {
    var t = parseTime(value);
    if (t === null) return '';
    var d = new Date(t);
    return formatDate(t) + ' ' + pad2(d.getHours()) + ':' + pad2(d.getMinutes()) + ':' + pad2(d.getSeconds());
  }

  // 相对时间：无时间返回 ''，30 天以上显示日期
  function formatRelativeTime(value, now) {
    var t = parseTime(value);
    if (t === null) return '';
    var diff = Math.round((nowMs(now) - t) / 1000);
    var abs = Math.abs(diff);
    var suffix = diff < 0 ? '后' : '前';
    if (abs < 60) return '刚刚';
    if (abs < 3600) return Math.floor(abs / 60) + ' 分钟' + suffix;
    if (abs < 86400) return Math.floor(abs / 3600) + ' 小时' + suffix;
    if (abs < 30 * 86400) return Math.floor(abs / 86400) + ' 天' + suffix;
    return formatDate(t);
  }

  // file.error 仅对未完成且不在下载中的文件有意义
  function fileErrorText(f) {
    if (!f || typeof f !== 'object' || f.status === 'done' || f.status === 'downloading') return '';
    return trimStr(f.error);
  }

  // 下载失败的文件列表：[{name, error}]
  function fileErrors(files) {
    var out = [];
    (Array.isArray(files) ? files : []).forEach(function (f) {
      var err = fileErrorText(f);
      if (err) out.push({ name: f.name == null ? '' : String(f.name), error: err });
    });
    return out;
  }

  // 状态标签下显示的失败原因：error / incomplete 时显示 lastError；incomplete 缺少 lastError 时由 file.error 汇总
  function statusError(state) {
    state = state && typeof state === 'object' ? state : {};
    if (state.status !== 'error' && state.status !== 'incomplete') return '';
    var msg = trimStr(state.lastError);
    if (msg || state.status !== 'incomplete') return msg;
    var errs = fileErrors(state.files);
    if (!errs.length) return '';
    return '部分文件下载失败：' + errs.map(function (e) { return e.name + '：' + e.error; }).join('；');
  }

  // 由 state.files 计算进度
  function progressSummary(files) {
    var list = Array.isArray(files) ? files : [];
    var r = { total: list.length, done: 0, downloading: 0, pending: 0, failed: 0, totalSize: 0, completedSize: 0, percent: 0, text: '-' };
    list.forEach(function (f) {
      f = f || {};
      var size = toNumber(f.size);
      size = isFinite(size) && size > 0 ? size : 0;
      var completed = toNumber(f.completedLength);
      completed = isFinite(completed) && completed > 0 ? completed : 0;
      if (size > 0 && completed > size) completed = size;
      if (f.status === 'done') {
        r.done++;
        completed = size;
      } else if (f.status === 'downloading') {
        r.downloading++;
      } else {
        r.pending++;
        if (fileErrorText(f)) r.failed++;
      }
      r.totalSize += size;
      r.completedSize += completed;
    });
    if (r.total === 0) return r;
    var pct = r.totalSize > 0 ? (r.completedSize * 100) / r.totalSize : (r.done * 100) / r.total;
    pct = Math.max(0, Math.min(100, Math.floor(pct)));
    if (pct === 100 && r.done < r.total) pct = 99;
    r.percent = pct;
    r.text = r.done + '/' + r.total;
    return r;
  }

  function describeInterval(minutes) {
    var n = toInt(minutes, NaN);
    if (!isFinite(n)) return '';
    if (n <= 0) return '仅手动检查';
    if (n % 1440 === 0) return '每 ' + n / 1440 + ' 天检查';
    if (n % 60 === 0) return '每 ' + n / 60 + ' 小时检查';
    return '每 ' + n + ' 分钟检查';
  }

  function describeMirrorRules(m) {
    m = m || {};
    var rules = [];
    var types = formatFileTypes(m.fileTypes);
    if (types) rules.push('后缀: ' + types);
    if (typeof m.regex === 'string' && m.regex) rules.push('正则: ' + m.regex);
    if (!types && !m.regex) rules.push('全部文件');
    if (m.includePrerelease) rules.push('含预发布');
    var keep = toInt(m.keepVersions, 0);
    if (keep > 0) rules.push('保留最新 ' + keep + ' 个版本');
    if (m.saveChangelog === false) rules.push('不保存 CHANGELOG');
    return rules;
  }

  function joinPath() {
    var segs = [];
    for (var i = 0; i < arguments.length; i++) {
      var s = arguments[i];
      if (typeof s !== 'string' || s === '') continue;
      if (segs.length === 0 && s.charAt(0) === '/') segs.push('');
      s.split('/').forEach(function (p) { if (p) segs.push(p); });
    }
    if (segs.length === 1 && segs[0] === '') return '/';
    return segs.join('/');
  }

  function safeHttpUrl(url) {
    return typeof url === 'string' && /^https?:\/\/[^\s]+$/i.test(url) ? url : '';
  }

  // 后端返回的直链只接受站内绝对路径（不允许 // 协议相对地址）或 http(s)
  function safeDownloadUrl(url) {
    if (typeof url !== 'string' || url === '') return '';
    if (/^\/(?![\/\\])/.test(url)) return url;
    return safeHttpUrl(url);
  }

  function mirrorPath(id, suffix) {
    var p = API_BASE + '/mirrors/' + encodeURIComponent(String(id == null ? '' : id));
    return suffix ? p + '/' + suffix : p;
  }

  function deletePath(id, deleteFiles) {
    return mirrorPath(id) + (deleteFiles ? '?deleteFiles=true' : '');
  }

  // AriaNg 的确认框会对文本做 $translate（含 $interpolate），中和花括号避免表达式被执行
  function dialogSafeText(s) {
    return String(s == null ? '' : s).replace(/\{/g, '｛').replace(/\}/g, '｝');
  }

  function extractError(resp) {
    if (!resp || typeof resp !== 'object') return '未知错误';
    var data = resp.data;
    if (data && typeof data === 'object' && typeof data.error === 'string' && data.error.trim() !== '') {
      return data.error.trim();
    }
    var status = resp.status;
    if (status === -1 || status === 0 || status === undefined) return '无法连接到服务器，请检查网络';
    if (status === 401) return '未授权（HTTP 401），请刷新页面重新登录';
    if (status === 404) return '接口不存在（HTTP 404），请确认后端版本已支持镜像功能';
    return '请求失败（HTTP ' + status + (resp.statusText ? ' ' + resp.statusText : '') + '）';
  }

  function newMirrorForm() {
    return {
      id: '',
      name: '',
      repo: '',
      folder: '',
      enabled: true,
      saveChangelog: true,
      fileTypesText: '',
      regex: '',
      includePrerelease: false,
      keepVersions: 0
    };
  }

  function formFromMirror(m) {
    m = m || {};
    return {
      id: typeof m.id === 'string' ? m.id : '',
      name: typeof m.name === 'string' ? m.name : '',
      repo: typeof m.repo === 'string' ? m.repo : '',
      folder: typeof m.folder === 'string' ? m.folder : '',
      enabled: typeof m.enabled === 'boolean' ? m.enabled : true,
      saveChangelog: typeof m.saveChangelog === 'boolean' ? m.saveChangelog : true,
      fileTypesText: formatFileTypes(m.fileTypes),
      regex: typeof m.regex === 'string' ? m.regex : '',
      includePrerelease: m.includePrerelease === true,
      keepVersions: toInt(m.keepVersions, 0)
    };
  }

  function validateMirrorForm(form) {
    form = form || {};
    if (!trimStr(form.repo)) return '请填写仓库（owner/name 或 GitHub 地址）';
    var keep = form.keepVersions;
    if (keep !== null && keep !== undefined && keep !== '') {
      var n = toInt(keep, NaN);
      if (isNaN(n) || n < 0) return '保留版本数必须是非负整数（0 表示保留全部）';
    }
    return '';
  }

  // 构造 MirrorInput（POST / PUT 全量对象）
  function buildMirrorInput(form) {
    form = form || {};
    var rawRepo = trimStr(form.repo);
    var types = form.fileTypesText !== undefined ? form.fileTypesText : form.fileTypes;
    return {
      name: trimStr(form.name),
      repo: normalizeRepo(rawRepo) || rawRepo,
      folder: trimStr(form.folder),
      enabled: typeof form.enabled === 'boolean' ? form.enabled : true,
      saveChangelog: typeof form.saveChangelog === 'boolean' ? form.saveChangelog : true,
      fileTypes: parseFileTypes(types),
      regex: trimStr(form.regex),
      includePrerelease: form.includePrerelease === true,
      keepVersions: toInt(form.keepVersions, 0)
    };
  }

  function mirrorToInput(m, overrides) {
    var input = buildMirrorInput(formFromMirror(m));
    if (overrides) {
      Object.keys(overrides).forEach(function (k) { input[k] = overrides[k]; });
    }
    return input;
  }

  function settingsToForm(view) {
    view = view || {};
    return {
      rootDir: typeof view.rootDir === 'string' ? view.rootDir : '',
      intervalMinutes: toInt(view.intervalMinutes, 0),
      proxy: typeof view.proxy === 'string' ? view.proxy : '',
      githubToken: '',
      clearToken: false,
      tokenSet: view.githubTokenSet === true
    };
  }

  function validateSettingsForm(form) {
    form = form || {};
    var n = toInt(form.intervalMinutes, NaN);
    if (isNaN(n)) return '检查间隔必须是整数（0 表示仅手动检查）';
    if (n !== 0 && (n < 10 || n > 43200)) return '检查间隔须为 0（仅手动）或 10 ~ 43200 分钟';
    return '';
  }

  // 构造 SettingsInput：githubToken 缺省表示保持不变，"" 表示清除
  function buildSettingsInput(form) {
    form = form || {};
    var out = {
      rootDir: trimStr(form.rootDir),
      intervalMinutes: toInt(form.intervalMinutes, 0),
      proxy: trimStr(form.proxy)
    };
    if (form.clearToken) {
      out.githubToken = '';
    } else if (trimStr(form.githubToken)) {
      out.githubToken = trimStr(form.githubToken);
    }
    return out;
  }

  function normalizeImportResult(data) {
    data = data && typeof data === 'object' ? data : {};
    var skipped = Array.isArray(data.skipped) ? data.skipped : [];
    return {
      imported: toInt(data.imported, 0),
      skipped: skipped.map(function (s) {
        s = s && typeof s === 'object' ? s : {};
        return { key: s.key == null ? '' : String(s.key), reason: s.reason == null ? '' : String(s.reason) };
      })
    };
  }

  // latest / partial 由后端判断（缺省视为非最新、已完成，兼容旧后端）；partial 文件不提供直链
  function normalizeVersions(data) {
    var list = data && Array.isArray(data.versions) ? data.versions : [];
    return list.map(function (v) {
      v = v || {};
      return {
        tag: v.tag == null ? '' : String(v.tag),
        modTime: typeof v.modTime === 'string' ? v.modTime : '',
        latest: v.latest === true,
        files: (Array.isArray(v.files) ? v.files : []).map(function (f) {
          f = f || {};
          var partial = f.partial === true;
          return {
            name: f.name == null ? '' : String(f.name),
            size: f.size,
            url: partial ? '' : safeDownloadUrl(f.url),
            partial: partial
          };
        })
      };
    });
  }

  // 规范化后端的 MirrorView，并附加 view（标签 / 进度 / 规则描述）供模板使用
  function normalizeMirror(raw) {
    raw = raw && typeof raw === 'object' ? raw : {};
    var m = {};
    Object.keys(raw).forEach(function (k) { m[k] = raw[k]; });
    m.id = m.id == null ? '' : String(m.id);
    m.name = typeof m.name === 'string' ? m.name : '';
    m.repo = typeof m.repo === 'string' ? m.repo : '';
    m.folder = typeof m.folder === 'string' ? m.folder : '';
    m.enabled = m.enabled !== false;
    m.saveChangelog = m.saveChangelog !== false;
    m.fileTypes = Array.isArray(m.fileTypes) ? m.fileTypes : [];
    m.regex = typeof m.regex === 'string' ? m.regex : '';
    m.includePrerelease = m.includePrerelease === true;
    m.keepVersions = toInt(m.keepVersions, 0);

    var st = raw.state && typeof raw.state === 'object' ? raw.state : {};
    var state = {};
    Object.keys(st).forEach(function (k) { state[k] = st[k]; });
    state.status = typeof state.status === 'string' && state.status ? state.status : 'idle';
    state.files = Array.isArray(state.files) ? state.files : [];
    ['latestTag', 'releaseName', 'releaseUrl', 'lastError'].forEach(function (k) {
      state[k] = typeof state[k] === 'string' ? state[k] : '';
    });
    ['publishedAt', 'lastCheck', 'lastSuccess'].forEach(function (k) {
      state[k] = parseTime(state[k]) === null ? '' : state[k];
    });
    m.state = state;
    var errs = fileErrors(state.files);
    var errMap = Object.create(null);
    errs.forEach(function (e) { errMap[e.name] = e.error; });
    m.view = {
      label: statusLabel(state.status),
      error: statusError(state),
      progress: progressSummary(state.files),
      fileErrors: errMap,
      failedText: errs.map(function (e) { return e.name + '：' + e.error; }).join('\n'),
      rules: describeMirrorRules(m).join(' · ')
    };
    return m;
  }

  // 行内启停成功后同步到正在编辑同一镜像的表单，其余未保存的修改保持不变
  function applyEnabledToForm(form, id, enabled) {
    if (!form || typeof form !== 'object' || !form.id || form.id !== id) return false;
    form.enabled = enabled === true;
    return true;
  }

  var helpers = {
    REPO_RE: REPO_RE,
    API_BASE: API_BASE,
    toInt: toInt,
    normalizeRepo: normalizeRepo,
    repoName: repoName,
    repoUrl: repoUrl,
    parseFileTypes: parseFileTypes,
    formatFileTypes: formatFileTypes,
    statusLabel: statusLabel,
    formatBytes: formatBytes,
    parseTime: parseTime,
    formatDate: formatDate,
    formatDateTime: formatDateTime,
    formatRelativeTime: formatRelativeTime,
    fileErrors: fileErrors,
    statusError: statusError,
    progressSummary: progressSummary,
    describeInterval: describeInterval,
    describeMirrorRules: describeMirrorRules,
    joinPath: joinPath,
    safeHttpUrl: safeHttpUrl,
    safeDownloadUrl: safeDownloadUrl,
    mirrorPath: mirrorPath,
    deletePath: deletePath,
    dialogSafeText: dialogSafeText,
    extractError: extractError,
    newMirrorForm: newMirrorForm,
    formFromMirror: formFromMirror,
    validateMirrorForm: validateMirrorForm,
    buildMirrorInput: buildMirrorInput,
    mirrorToInput: mirrorToInput,
    settingsToForm: settingsToForm,
    validateSettingsForm: validateSettingsForm,
    buildSettingsInput: buildSettingsInput,
    normalizeImportResult: normalizeImportResult,
    normalizeVersions: normalizeVersions,
    normalizeMirror: normalizeMirror,
    applyEnabledToForm: applyEnabledToForm
  };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = helpers;
  }
  if (typeof window === 'undefined') {
    return;
  }

  // ===== 浏览器部分 =====
  var ROUTE = '/mirrors';
  var POLL_INTERVAL = 5000;

  var IMPORT_PLACEHOLDER = [
    'track-list:',
    '  powertoys:',
    '    repo-info: "microsoft/PowerToys"',
    '    folder-name: "PowerToys"',
    '    get-changes-info: true',
    '    file-type:',
    '      - exe',
    '      - msi',
    '  opentrace:',
    '    repo-info: "Archeb/opentrace"',
    '    regex: "(osx|win)"'
  ].join('&#10;').replace(/"/g, '&quot;');

  var TEMPLATE = [
    '<section class="content aria-one-mirror">',

    // 工具条
    '<div class="mirror-toolbar">',
    '<div class="mirror-toolbar-buttons">',
    '<button type="button" class="btn btn-primary btn-sm" ng-click="openAdd()"><i class="fa fa-plus"></i> 添加镜像</button>',
    '<button type="button" class="btn btn-default btn-sm" ng-click="syncAll()" ng-disabled="busy.syncAll"><i class="fa fa-refresh" ng-class="{\'fa-spin\': busy.syncAll}"></i> 全部检查</button>',
    '<button type="button" class="btn btn-default btn-sm" ng-class="{active: panel === \'import\'}" ng-click="togglePanel(\'import\')"><i class="fa fa-upload"></i> 导入 YAML</button>',
    '<button type="button" class="btn btn-default btn-sm" ng-class="{active: panel === \'settings\'}" ng-click="togglePanel(\'settings\')"><i class="fa fa-cog"></i> 设置</button>',
    '</div>',
    '<div class="mirror-summary text-muted small" ng-if="settings">',
    '<span title="检查间隔"><i class="fa fa-clock-o"></i> <span ng-bind="h.describeInterval(settings.intervalMinutes)"></span></span>',
    '<span title="镜像根目录"><i class="fa fa-folder-o"></i> <span ng-bind="h.joinPath(downloadDir, settings.rootDir)"></span></span>',
    '<span title="GitHub Token"><i class="fa fa-key"></i> <span ng-bind="settings.githubTokenSet ? \'Token 已设置\' : \'Token 未设置\'"></span></span>',
    '</div>',
    '</div>',

    // 提示信息
    '<div class="alert mirror-alert" ng-if="notice" ng-class="\'alert-\' + notice.type">',
    '<button type="button" class="close" ng-click="clearNotice()" title="关闭">&times;</button>',
    '<span ng-bind="notice.text"></span>',
    '</div>',
    '<div class="alert alert-danger mirror-alert" ng-if="loadError">',
    '<i class="fa fa-exclamation-triangle"></i> 加载镜像列表失败：<span ng-bind="loadError"></span>',
    '</div>',

    // 设置面板
    '<div class="box box-default mirror-panel" ng-if="panel === \'settings\'">',
    '<div class="box-header with-border"><h3 class="box-title"><i class="fa fa-cog"></i> 镜像设置</h3>',
    '<div class="box-tools pull-right"><button type="button" class="btn btn-box-tool" ng-click="closePanel()" title="关闭"><i class="fa fa-times"></i></button></div></div>',
    '<form class="box-body" ng-submit="saveSettings()" novalidate autocomplete="off">',
    '<div ng-if="settingsLoading" class="text-muted"><i class="fa fa-spinner fa-spin"></i> 正在加载设置…</div>',
    '<div class="row" ng-if="settingsForm">',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-set-root">根目录</label>',
    '<div class="input-group"><span class="input-group-addon mirror-mono" ng-bind="(downloadDir || \'/downloads\') + \'/\'"></span>',
    '<input id="mirror-set-root" type="text" class="form-control" ng-model="settingsForm.rootDir" placeholder="github-mirror"></div>',
    '<p class="help-block">镜像文件保存在下载目录下的该子目录中，可多级（如 mirrors/github）。修改后不会移动已有文件。</p>',
    '</div>',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-set-interval">检查间隔（分钟）</label>',
    '<input id="mirror-set-interval" type="number" class="form-control" ng-model="settingsForm.intervalMinutes" placeholder="360">',
    '<p class="help-block">0 表示仅手动检查；否则取值 10 ~ 43200。</p>',
    '</div>',
    '</div>',
    '<div class="row" ng-if="settingsForm">',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-set-proxy">HTTP 代理</label>',
    '<input id="mirror-set-proxy" type="text" class="form-control" ng-model="settingsForm.proxy" placeholder="http://127.0.0.1:7890（留空直连）">',
    '<p class="help-block">同时用于 GitHub API 请求和 aria2 下载，仅支持 http:// 代理。</p>',
    '</div>',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-set-token">GitHub Token</label>',
    '<input id="mirror-set-token" type="password" class="form-control" autocomplete="new-password" ng-model="settingsForm.githubToken" ng-disabled="settingsForm.clearToken" placeholder="{{settingsForm.tokenSet ? \'已设置，留空保持不变\' : \'未设置\'}}">',
    '<div class="checkbox mirror-inline-check"><label><input type="checkbox" ng-model="settingsForm.clearToken" ng-disabled="!settingsForm.tokenSet"> 清除 Token</label></div>',
    '<p class="help-block">仅用于提高 GitHub API 限额（未认证时每小时 60 次），未填写时使用环境变量 GITHUB_TOKEN。</p>',
    '</div>',
    '</div>',
    '<div class="alert alert-danger mirror-alert" ng-if="settingsError"><i class="fa fa-exclamation-triangle"></i> <span ng-bind="settingsError"></span></div>',
    '<div class="mirror-form-actions" ng-if="settingsForm">',
    '<button type="submit" class="btn btn-primary btn-sm" ng-disabled="settingsSaving"><i class="fa" ng-class="settingsSaving ? \'fa-spinner fa-spin\' : \'fa-check\'"></i> 保存设置</button>',
    '<button type="button" class="btn btn-default btn-sm" ng-click="closePanel()">取消</button>',
    '</div>',
    '</form>',
    '</div>',

    // 添加 / 编辑面板
    '<div class="box box-primary mirror-panel" ng-if="panel === \'form\' && form">',
    '<div class="box-header with-border"><h3 class="box-title"><i class="fa" ng-class="form.id ? \'fa-pencil\' : \'fa-plus\'"></i> <span ng-bind="form.id ? \'编辑镜像\' : \'添加镜像\'"></span></h3>',
    '<div class="box-tools pull-right"><button type="button" class="btn btn-box-tool" ng-click="closePanel()" title="关闭"><i class="fa fa-times"></i></button></div></div>',
    '<form class="box-body" ng-submit="saveForm()" novalidate autocomplete="off">',
    '<div class="row">',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-f-repo">仓库 <span class="text-red">*</span></label>',
    '<input id="mirror-f-repo" type="text" class="form-control" ng-model="form.repo" ng-blur="normalizeFormRepo()" placeholder="owner/name 或 https://github.com/owner/name">',
    '<p class="help-block">支持直接粘贴 GitHub 仓库地址。私有仓库不受支持。</p>',
    '</div>',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-f-name">名称</label>',
    '<input id="mirror-f-name" type="text" class="form-control" ng-model="form.name" placeholder="{{h.repoName(form.repo) || \'默认使用仓库名\'}}">',
    '<p class="help-block">仅用于页面显示，留空时使用仓库名。</p>',
    '</div>',
    '</div>',
    '<div class="row">',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-f-folder">文件夹</label>',
    '<input id="mirror-f-folder" type="text" class="form-control" ng-model="form.folder" placeholder="{{h.repoName(form.repo) || \'默认使用仓库名\'}}">',
    '<p class="help-block mirror-break">保存到 <span class="mirror-mono" ng-bind="h.joinPath(downloadDir, settings.rootDir, form.folder || h.repoName(form.repo) || \'…\') + \'/<版本号>/\'"></span><span ng-if="form.id">；修改文件夹不会移动已下载的文件</span></p>',
    '</div>',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-f-keep">保留版本数</label>',
    '<input id="mirror-f-keep" type="number" class="form-control" ng-model="form.keepVersions" placeholder="0">',
    '<p class="help-block">0 表示保留全部版本；N 表示只保留最新的 N 个版本目录。</p>',
    '</div>',
    '</div>',
    '<div class="row">',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-f-types">文件后缀</label>',
    '<input id="mirror-f-types" type="text" class="form-control" ng-model="form.fileTypesText" placeholder="如 exe, msi, tar.gz">',
    '<p class="help-block">逗号或空格分隔，不区分大小写；留空表示不按后缀过滤。</p>',
    '</div>',
    '<div class="form-group col-sm-6">',
    '<label for="mirror-f-regex">正则</label>',
    '<input id="mirror-f-regex" type="text" class="form-control mirror-mono" ng-model="form.regex" placeholder="如 (windows|linux)-amd64">',
    '<p class="help-block">Go RE2 语法，对文件名做部分匹配（需从开头匹配请加 ^）；与后缀同时填写时须同时满足，两者都为空则下载全部文件。</p>',
    '</div>',
    '</div>',
    '<div class="mirror-checks">',
    '<label class="checkbox-inline"><input type="checkbox" ng-model="form.enabled"> 启用定时检查</label>',
    '<label class="checkbox-inline"><input type="checkbox" ng-model="form.saveChangelog"> 保存 CHANGELOG.md</label>',
    '<label class="checkbox-inline"><input type="checkbox" ng-model="form.includePrerelease"> 包含预发布版本</label>',
    '</div>',
    '<div class="alert alert-danger mirror-alert" ng-if="formError"><i class="fa fa-exclamation-triangle"></i> <span ng-bind="formError"></span></div>',
    '<div class="mirror-form-actions">',
    '<button type="submit" class="btn btn-primary btn-sm" ng-disabled="formSaving"><i class="fa" ng-class="formSaving ? \'fa-spinner fa-spin\' : \'fa-check\'"></i> <span ng-bind="form.id ? \'保存\' : \'添加\'"></span></button>',
    '<button type="button" class="btn btn-default btn-sm" ng-click="closePanel()">取消</button>',
    '</div>',
    '</form>',
    '</div>',

    // 导入面板
    '<div class="box box-default mirror-panel" ng-if="panel === \'import\'">',
    '<div class="box-header with-border"><h3 class="box-title"><i class="fa fa-upload"></i> 导入原脚本 config.yaml</h3>',
    '<div class="box-tools pull-right"><button type="button" class="btn btn-box-tool" ng-click="closePanel()" title="关闭"><i class="fa fa-times"></i></button></div></div>',
    '<form class="box-body" ng-submit="doImport()" novalidate>',
    '<p class="help-block mirror-import-help">粘贴 github-release-mirror 脚本的 config.yaml。只读取 <code>track-list</code>（repo-info、folder-name、get-changes-info、file-type、regex），其余配置忽略；',
    '原脚本的 regex 用 Python <code>re.match</code> 从开头匹配，导入时会自动转换为 <code>^(?:原正则)</code>。与已有镜像文件夹重复的条目会被跳过。</p>',
    '<p class="help-block mirror-import-help">导入本身不会触发检查；若检查间隔不为 0，定时任务会在约 30 秒内依次检查这些从未检查过的镜像（每个镜像一次 API 请求）。',
    '如需先调整再同步，请先在「设置」中把检查间隔设为 0，或导入后停用相应条目。<span ng-if="settings && settings.intervalMinutes === 0">当前为仅手动检查，导入后请点击「全部检查」。</span></p>',
    '<textarea class="form-control mirror-mono mirror-yaml" rows="10" spellcheck="false" ng-model="importForm.yaml" placeholder="' + IMPORT_PLACEHOLDER + '"></textarea>',
    '<div class="alert alert-danger mirror-alert" ng-if="importError"><i class="fa fa-exclamation-triangle"></i> <span ng-bind="importError"></span></div>',
    '<div class="alert mirror-alert mirror-import-result" ng-if="importResult" ng-class="importResult.imported > 0 ? \'alert-success\' : \'alert-warning\'">',
    '<div>成功导入 <strong ng-bind="importResult.imported"></strong> 个镜像<span ng-if="importResult.skipped.length">，跳过 <strong ng-bind="importResult.skipped.length"></strong> 项：</span></div>',
    '<ul ng-if="importResult.skipped.length"><li ng-repeat="s in importResult.skipped track by $index"><code ng-bind="s.key || \'(未命名)\'"></code> <span ng-bind="s.reason"></span></li></ul>',
    '</div>',
    '<div class="mirror-form-actions">',
    '<button type="submit" class="btn btn-primary btn-sm" ng-disabled="importing"><i class="fa" ng-class="importing ? \'fa-spinner fa-spin\' : \'fa-upload\'"></i> 导入</button>',
    '<button type="button" class="btn btn-default btn-sm" ng-click="closePanel()">关闭</button>',
    '</div>',
    '</form>',
    '</div>',

    // 列表
    '<div class="mirror-loading text-muted" ng-if="loading && !mirrors.length"><i class="fa fa-spinner fa-spin"></i> 正在加载…</div>',
    '<div class="mirror-empty text-muted" ng-if="!loading && !loadError && !mirrors.length">',
    '<i class="fa fa-github"></i>',
    '<p>还没有镜像。点击「添加镜像」，或通过「导入 YAML」导入原脚本的配置。</p>',
    '</div>',
    '<div class="box mirror-list-box" ng-if="mirrors.length">',
    '<div class="table-responsive">',
    '<table class="table mirror-table">',
    '<thead><tr><th>名称 / 仓库</th><th>最新版本</th><th>状态</th><th>文件</th><th>上次检查</th><th class="mirror-actions-head">操作</th></tr></thead>',
    '<tbody ng-repeat="m in mirrors track by m.id" ng-class="{\'mirror-disabled\': !m.enabled}">',
    '<tr class="mirror-row">',
    // 名称 + 仓库
    '<td class="mirror-name-cell">',
    '<div class="mirror-name"><strong ng-bind="m.name || m.repo"></strong> <span class="label label-default" ng-if="!m.enabled">已停用</span></div>',
    '<div class="small"><a ng-if="h.repoUrl(m.repo)" ng-href="{{h.repoUrl(m.repo)}}" target="_blank" rel="noopener noreferrer"><i class="fa fa-github"></i> <span ng-bind="m.repo"></span></a>',
    '<span ng-if="!h.repoUrl(m.repo)" ng-bind="m.repo"></span></div>',
    '<div class="small text-muted mirror-rules" ng-bind="m.view.rules"></div>',
    '</td>',
    // 最新版本
    '<td class="mirror-version-cell">',
    '<div class="mirror-version-tag" ng-if="m.state.latestTag">',
    '<a ng-if="h.safeHttpUrl(m.state.releaseUrl)" ng-href="{{h.safeHttpUrl(m.state.releaseUrl)}}" target="_blank" rel="noopener noreferrer" title="{{m.state.releaseName}}"><i class="fa fa-tag"></i> <span ng-bind="m.state.latestTag"></span></a>',
    '<span ng-if="!h.safeHttpUrl(m.state.releaseUrl)" title="{{m.state.releaseName}}"><i class="fa fa-tag"></i> <span ng-bind="m.state.latestTag"></span></span>',
    '<div class="small text-muted mirror-nowrap" ng-if="m.state.publishedAt" title="{{h.formatDateTime(m.state.publishedAt)}}">发布于 <span ng-bind="rel(m.state.publishedAt)"></span></div>',
    '</div>',
    '<span class="text-muted" ng-if="!m.state.latestTag">-</span>',
    '</td>',
    // 状态（error / incomplete 时显示失败原因）
    '<td class="mirror-status-cell">',
    '<span class="label" ng-class="m.view.label.cls"><i class="fa fa-refresh fa-spin" ng-if="m.view.label.active"></i> <span ng-bind="m.view.label.text"></span></span>',
    '<div class="small text-red mirror-error" ng-if="m.view.error" ng-bind="m.view.error"></div>',
    '</td>',
    // 文件进度
    '<td class="mirror-progress-cell">',
    '<span ng-bind="m.view.progress.text" title="已完成 / 文件总数"></span>',
    '<span class="small text-muted" ng-if="m.view.progress.totalSize > 0"> · <span ng-bind="h.formatBytes(m.view.progress.totalSize)"></span></span>',
    '<div class="small text-red" ng-if="m.view.progress.failed > 0" title="{{m.view.failedText}}"><i class="fa fa-exclamation-circle"></i> <span ng-bind="m.view.progress.failed + \' 个失败\'"></span></div>',
    '<div class="mirror-progress" ng-if="m.state.status === \'downloading\' && m.view.progress.total > 0" title="{{h.formatBytes(m.view.progress.completedSize) + \' / \' + h.formatBytes(m.view.progress.totalSize)}}">',
    '<div class="progress progress-xxs"><div class="progress-bar progress-bar-primary" ng-style="{width: m.view.progress.percent + \'%\'}"></div></div>',
    '<span class="small" ng-bind="m.view.progress.percent + \'%\'"></span>',
    '</div>',
    '</td>',
    // 上次检查
    '<td class="mirror-time-cell">',
    '<span title="{{h.formatDateTime(m.state.lastCheck)}}" ng-bind="rel(m.state.lastCheck) || \'从未\'"></span>',
    '<div class="small text-muted" ng-if="m.state.lastSuccess && m.state.lastSuccess !== m.state.lastCheck" title="{{h.formatDateTime(m.state.lastSuccess)}}">成功于 <span ng-bind="rel(m.state.lastSuccess)"></span></div>',
    '</td>',
    // 操作
    '<td class="mirror-actions">',
    '<div class="btn-group btn-group-xs">',
    // 窄屏（< 1400px）只显示图标，文字保留在 title / aria-label 与读屏文本中
    '<button type="button" class="btn btn-default" title="立即检查" aria-label="立即检查" ng-click="syncOne(m)" ng-disabled="rowBusy[m.id] || m.state.status === \'checking\'"><i class="fa fa-refresh" ng-class="{\'fa-spin\': m.state.status === \'checking\'}"></i><span class="mirror-btn-text">检查</span></button>',
    '<button type="button" class="btn btn-default" title="编辑" aria-label="编辑" ng-click="openEdit(m)"><i class="fa fa-pencil"></i><span class="mirror-btn-text">编辑</span></button>',
    '<button type="button" class="btn btn-default" ng-click="toggleEnabled(m)" ng-disabled="rowBusy[m.id]" title="{{m.enabled ? \'已启用，点击停用定时检查\' : \'已停用，点击启用定时检查\'}}" aria-label="{{m.enabled ? \'已启用，点击停用定时检查\' : \'已停用，点击启用定时检查\'}}"><i class="fa" ng-class="m.enabled ? \'fa-toggle-on text-green\' : \'fa-toggle-off\'"></i><span class="mirror-btn-text" ng-bind="m.enabled ? \'启用\' : \'停用\'"></span></button>',
    '<button type="button" class="btn btn-default" title="已镜像的版本与文件" aria-label="已镜像的版本与文件" ng-class="{active: expanded[m.id]}" ng-click="toggleVersions(m)"><i class="fa fa-folder-open-o"></i><span class="mirror-btn-text">版本</span></button>',
    '<button type="button" class="btn btn-default" title="删除镜像" aria-label="删除镜像" ng-class="{active: deleting[m.id]}" ng-click="toggleDelete(m)"><i class="fa fa-trash-o text-red"></i><span class="mirror-btn-text">删除</span></button>',
    '</div>',
    '</td>',
    '</tr>',
    // 删除确认行
    '<tr class="mirror-detail-row" ng-if="deleting[m.id]">',
    '<td colspan="6"><div class="mirror-detail mirror-delete">',
    '<div><i class="fa fa-exclamation-triangle text-yellow"></i> 删除镜像「<strong ng-bind="m.name || m.repo"></strong>」：只删除镜像配置，定时检查随之停止。</div>',
    '<div class="checkbox"><label><input type="checkbox" ng-model="deleting[m.id].deleteFiles"> 同时删除已下载的文件（<span class="mirror-mono" ng-bind="h.joinPath(downloadDir, settings.rootDir, m.folder)"></span>）</label></div>',
    '<div class="alert alert-danger mirror-alert" ng-if="deleting[m.id].error"><span ng-bind="deleting[m.id].error"></span></div>',
    '<button type="button" class="btn btn-danger btn-xs" ng-click="confirmDelete(m)" ng-disabled="deleting[m.id].busy"><i class="fa" ng-class="deleting[m.id].busy ? \'fa-spinner fa-spin\' : \'fa-trash-o\'"></i> 删除</button> ',
    '<button type="button" class="btn btn-default btn-xs" ng-click="toggleDelete(m)">取消</button>',
    '</div></td>',
    '</tr>',
    // 版本展开行
    '<tr class="mirror-detail-row" ng-if="expanded[m.id]">',
    '<td colspan="6"><div class="mirror-detail">',
    '<div class="mirror-detail-head">',
    '<button type="button" class="btn btn-default btn-xs pull-right" ng-click="loadVersions(m)" ng-disabled="versions[m.id].loading"><i class="fa fa-refresh" ng-class="{\'fa-spin\': versions[m.id].loading}"></i> 刷新</button>',
    '<strong>已镜像的版本</strong> <span class="small text-muted mirror-mono" ng-bind="h.joinPath(downloadDir, settings.rootDir, m.folder)"></span>',
    '</div>',
    '<div class="text-muted" ng-if="versions[m.id].loading && !versions[m.id].list"><i class="fa fa-spinner fa-spin"></i> 正在加载…</div>',
    '<div class="text-red" ng-if="versions[m.id].error"><i class="fa fa-exclamation-triangle"></i> 加载失败：<span ng-bind="versions[m.id].error"></span></div>',
    '<div class="text-muted" ng-if="versions[m.id].list && !versions[m.id].list.length">磁盘上还没有已镜像的版本。</div>',
    '<div class="mirror-version" ng-repeat="v in versions[m.id].list track by $index">',
    '<div class="mirror-version-title"><i class="fa fa-tag"></i> <strong ng-bind="v.tag"></strong> ',
    '<span class="label label-success" ng-if="v.latest">最新</span> ',
    '<span class="small text-muted" ng-bind="h.formatDateTime(v.modTime)"></span></div>',
    '<ul class="list-unstyled mirror-files">',
    '<li ng-repeat="f in v.files track by $index">',
    '<a ng-if="f.url" ng-href="{{f.url}}" target="_blank" rel="noopener"><i class="fa fa-file-o"></i> <span ng-bind="f.name"></span></a>',
    '<span ng-if="!f.url"><i class="fa fa-file-o"></i> <span ng-bind="f.name"></span></span>',
    ' <span class="label label-warning" ng-if="f.partial" title="文件尚未下载完成，暂不提供直链">未完成</span>',
    ' <span class="small text-muted" ng-bind="h.formatBytes(f.size)"></span>',
    '<div class="small text-red" ng-if="v.latest && m.view.fileErrors[f.name]"><i class="fa fa-exclamation-circle"></i> <span ng-bind="m.view.fileErrors[f.name]"></span></div>',
    '</li>',
    '<li class="text-muted" ng-if="!v.files.length">（空目录）</li>',
    '</ul>',
    '</div>',
    '</div></td>',
    '</tr>',
    '</tbody>',
    '</table>',
    '</div>',
    '</div>',
    '</section>'
  ].join('');

  // 侧边栏：在 "Settings" 分组标题前插入入口（幂等）
  function insertSidebarEntry() {
    var menu = document.getElementById('siderbar-menu') || document.querySelector('section.sidebar > ul.sidebar-menu');
    if (!menu) {
      console.warn('[mirror] sidebar menu not found, skip menu entry');
      return;
    }
    if (menu.querySelector('li[data-href-match="' + ROUTE + '"]')) return;

    var header = document.createElement('li');
    header.className = 'header';
    header.textContent = 'GitHub';

    var item = document.createElement('li');
    item.setAttribute('data-href-match', ROUTE);
    var link = document.createElement('a');
    link.setAttribute('href', '#!' + ROUTE);
    var icon = document.createElement('i');
    icon.className = 'fa fa-github';
    var text = document.createElement('span');
    text.textContent = 'Release 镜像';
    link.appendChild(icon);
    link.appendChild(document.createTextNode(' '));
    link.appendChild(text);
    item.appendChild(link);

    var anchor = null;
    var children = menu.children;
    for (var i = 0; i < children.length; i++) {
      var li = children[i];
      if (li.classList && li.classList.contains('header')) {
        var label = (li.textContent || '').trim();
        if (label === 'Settings' || label === '设置') {
          anchor = li;
          break;
        }
      }
    }
    if (anchor) {
      menu.insertBefore(header, anchor);
      menu.insertBefore(item, anchor);
    } else {
      menu.appendChild(header);
      menu.appendChild(item);
    }
  }

  function MirrorController($scope, $http, $timeout, $q, $window, $injector, $animate) {
    var h = helpers;
    // 模板此时已插入 ng-view：关闭本页的 ngAnimate，避免带 fa-spin 的元素在 ng-if 移除时滞留
    angular.forEach($window.document.querySelectorAll('.aria-one-mirror'), function (el) {
      $animate.enabled(angular.element(el), false);
    });
    var destroyed = false;
    var pollTimer = null;
    var noticeTimer = null;
    var inflight = null;
    var rerun = false;
    var canceler = null;
    var commonService = $injector.has('ariaNgCommonService') ? $injector.get('ariaNgCommonService') : null;

    $scope.h = h;
    $scope.loading = true;
    $scope.loadError = '';
    $scope.settings = null;
    $scope.downloadDir = '/downloads';
    $scope.mirrors = [];
    $scope.now = Date.now();
    $scope.panel = '';
    $scope.busy = { syncAll: false };
    $scope.rowBusy = {};
    $scope.expanded = {};
    $scope.versions = {};
    $scope.deleting = {};
    $scope.notice = null;

    $scope.form = null;
    $scope.formError = '';
    $scope.formSaving = false;
    $scope.settingsForm = null;
    $scope.settingsLoading = false;
    $scope.settingsError = '';
    $scope.settingsSaving = false;
    $scope.importForm = { yaml: '' };
    $scope.importError = '';
    $scope.importResult = null;
    $scope.importing = false;

    function postJSON(method, url, body) {
      return $http({
        method: method,
        url: url,
        data: body || {},
        headers: { 'Content-Type': 'application/json' }
      });
    }

    function showNotice(type, text) {
      if (destroyed) return;
      if (noticeTimer) $timeout.cancel(noticeTimer);
      noticeTimer = null;
      $scope.notice = { type: type, text: text };
      if (type === 'success' || type === 'info') {
        noticeTimer = $timeout(function () {
          noticeTimer = null;
          $scope.notice = null;
        }, 5000);
      }
    }

    function scrollTop() {
      var body = $window.document.getElementById('content-body');
      if (body) body.scrollTop = 0;
    }

    $scope.clearNotice = function () {
      if (noticeTimer) $timeout.cancel(noticeTimer);
      noticeTimer = null;
      $scope.notice = null;
    };

    $scope.rel = function (t) {
      return h.formatRelativeTime(t, $scope.now);
    };

    function versionSignature(m) {
      return [m.state.status, m.state.latestTag, m.view.progress.done, m.view.progress.total].join('|');
    }

    function applyList(data) {
      data = data && typeof data === 'object' ? data : {};
      $scope.settings = data.settings && typeof data.settings === 'object' ? data.settings : null;
      $scope.downloadDir = typeof data.downloadDir === 'string' && data.downloadDir ? data.downloadDir : '/downloads';
      var old = {};
      $scope.mirrors.forEach(function (m) { old[m.id] = m; });
      var ids = {};
      $scope.mirrors = (Array.isArray(data.mirrors) ? data.mirrors : []).map(function (raw) {
        var m = h.normalizeMirror(raw);
        ids[m.id] = true;
        // 状态变化时自动刷新已展开的版本列表
        if ($scope.expanded[m.id] && old[m.id] && versionSignature(old[m.id]) !== versionSignature(m)) {
          $scope.loadVersions(m);
        }
        return m;
      });
      [$scope.expanded, $scope.versions, $scope.deleting, $scope.rowBusy].forEach(function (map) {
        Object.keys(map).forEach(function (id) {
          if (!ids[id]) delete map[id];
        });
      });
    }

    function upsertMirror(raw) {
      var m = h.normalizeMirror(raw);
      if (!m.id) return;
      for (var i = 0; i < $scope.mirrors.length; i++) {
        if ($scope.mirrors[i].id === m.id) {
          $scope.mirrors[i] = m;
          return;
        }
      }
      $scope.mirrors.push(m);
    }

    // 拉取列表：同一时刻只有一个请求；请求中再次触发则在结束后补一次
    function refresh() {
      if (destroyed) return $q.resolve();
      if (inflight) {
        rerun = true;
        return inflight;
      }
      canceler = $q.defer();
      inflight = $http.get(API_BASE + '/mirrors', { timeout: canceler.promise })
        .then(function (resp) {
          if (destroyed) return;
          applyList(resp.data);
          $scope.loadError = '';
        }, function (resp) {
          if (destroyed) return;
          $scope.loadError = h.extractError(resp);
        })
        .finally(function () {
          inflight = null;
          canceler = null;
          $scope.loading = false;
          $scope.now = Date.now();
          if (rerun && !destroyed) {
            rerun = false;
            refresh();
          }
        });
      return inflight;
    }

    function schedule() {
      if (destroyed) return;
      if (pollTimer) $timeout.cancel(pollTimer);
      pollTimer = $timeout(function () {
        pollTimer = null;
        (inflight || refresh()).finally(schedule);
      }, POLL_INTERVAL);
    }

    $scope.refresh = refresh;

    // ---- 面板 ----
    $scope.closePanel = function () {
      $scope.panel = '';
      $scope.form = null;
      $scope.formError = '';
      $scope.settingsError = '';
      $scope.importError = '';
    };

    $scope.togglePanel = function (name) {
      if ($scope.panel === name) {
        $scope.closePanel();
        return;
      }
      $scope.closePanel();
      $scope.panel = name;
      scrollTop();
      if (name === 'settings') loadSettings();
      if (name === 'import') {
        $scope.importResult = null;
        $scope.importError = '';
      }
    };

    $scope.openAdd = function () {
      $scope.closePanel();
      $scope.form = h.newMirrorForm();
      $scope.panel = 'form';
      scrollTop();
    };

    $scope.openEdit = function (m) {
      $scope.closePanel();
      $scope.form = h.formFromMirror(m);
      $scope.panel = 'form';
      scrollTop();
    };

    $scope.normalizeFormRepo = function () {
      if (!$scope.form) return;
      var repo = h.normalizeRepo($scope.form.repo);
      if (repo) $scope.form.repo = repo;
    };

    $scope.saveForm = function () {
      var form = $scope.form;
      if (!form || $scope.formSaving) return;
      var err = h.validateMirrorForm(form);
      if (err) {
        $scope.formError = err;
        return;
      }
      var input = h.buildMirrorInput(form);
      var editing = !!form.id;
      $scope.formError = '';
      $scope.formSaving = true;
      var req = editing
        ? postJSON('PUT', h.mirrorPath(form.id), input)
        : postJSON('POST', API_BASE + '/mirrors', input);
      req.then(function (resp) {
        if (destroyed) return;
        var saved = h.normalizeMirror(resp.data);
        if (saved.id) upsertMirror(resp.data);
        $scope.closePanel();
        var name = saved.name || input.name || input.repo;
        if (editing) {
          showNotice('success', '已保存镜像「' + name + '」');
        } else {
          showNotice('success', '已添加镜像「' + name + '」' + (input.enabled ? '，正在进行首次检查' : ''));
        }
        refresh();
      }, function (resp) {
        $scope.formError = h.extractError(resp);
      }).finally(function () {
        $scope.formSaving = false;
      });
    };

    function loadSettings() {
      $scope.settingsForm = null;
      $scope.settingsError = '';
      $scope.settingsLoading = true;
      $http.get(API_BASE + '/mirror-settings').then(function (resp) {
        if ($scope.panel !== 'settings') return;
        $scope.settings = resp.data;
        $scope.settingsForm = h.settingsToForm(resp.data);
      }, function (resp) {
        $scope.settingsError = '加载设置失败：' + h.extractError(resp);
      }).finally(function () {
        $scope.settingsLoading = false;
      });
    }

    $scope.saveSettings = function () {
      var form = $scope.settingsForm;
      if (!form || $scope.settingsSaving) return;
      var err = h.validateSettingsForm(form);
      if (err) {
        $scope.settingsError = err;
        return;
      }
      $scope.settingsError = '';
      $scope.settingsSaving = true;
      postJSON('PUT', API_BASE + '/mirror-settings', h.buildSettingsInput(form)).then(function (resp) {
        if (destroyed) return;
        $scope.settings = resp.data;
        $scope.closePanel();
        showNotice('success', '镜像设置已保存');
        refresh();
      }, function (resp) {
        $scope.settingsError = h.extractError(resp);
      }).finally(function () {
        $scope.settingsSaving = false;
      });
    };

    $scope.doImport = function () {
      if ($scope.importing) return;
      var yaml = $scope.importForm.yaml || '';
      if (!yaml.trim()) {
        $scope.importError = '请先粘贴 config.yaml 内容';
        return;
      }
      $scope.importError = '';
      $scope.importResult = null;
      $scope.importing = true;
      postJSON('POST', API_BASE + '/mirrors/import', { yaml: yaml }).then(function (resp) {
        if (destroyed) return;
        $scope.importResult = h.normalizeImportResult(resp.data);
        refresh();
      }, function (resp) {
        $scope.importError = h.extractError(resp);
      }).finally(function () {
        $scope.importing = false;
      });
    };

    // ---- 行操作 ----
    $scope.syncAll = function () {
      if ($scope.busy.syncAll) return;
      $scope.busy.syncAll = true;
      postJSON('POST', API_BASE + '/mirrors/sync', {}).then(function () {
        showNotice('info', '已开始检查所有已启用的镜像');
        refresh();
      }, function (resp) {
        showNotice('danger', '全部检查失败：' + h.extractError(resp));
      }).finally(function () {
        $scope.busy.syncAll = false;
      });
    };

    $scope.syncOne = function (m) {
      if ($scope.rowBusy[m.id]) return;
      $scope.rowBusy[m.id] = true;
      postJSON('POST', h.mirrorPath(m.id, 'sync'), {}).then(function () {
        showNotice('info', '已开始检查「' + (m.name || m.repo) + '」');
        refresh();
      }, function (resp) {
        showNotice('danger', '检查「' + (m.name || m.repo) + '」失败：' + h.extractError(resp));
      }).finally(function () {
        delete $scope.rowBusy[m.id];
      });
    };

    $scope.toggleEnabled = function (m) {
      if ($scope.rowBusy[m.id]) return;
      var enable = !m.enabled;
      $scope.rowBusy[m.id] = true;
      postJSON('PUT', h.mirrorPath(m.id), h.mirrorToInput(m, { enabled: enable })).then(function (resp) {
        if (destroyed) return;
        upsertMirror(resp.data && resp.data.id ? resp.data : angular.extend({}, m, { enabled: enable }));
        // 正在编辑同一镜像时同步表单的 enabled，避免保存表单时把启停改回去
        h.applyEnabledToForm($scope.form, m.id, resp.data && typeof resp.data.enabled === 'boolean' ? resp.data.enabled : enable);
        showNotice('success', (enable ? '已启用「' : '已停用「') + (m.name || m.repo) + '」');
        refresh();
      }, function (resp) {
        showNotice('danger', (enable ? '启用' : '停用') + '「' + (m.name || m.repo) + '」失败：' + h.extractError(resp));
      }).finally(function () {
        delete $scope.rowBusy[m.id];
      });
    };

    $scope.loadVersions = function (m) {
      var id = m.id;
      var entry = $scope.versions[id] || ($scope.versions[id] = { loading: false, error: '', list: null });
      if (entry.loading) return;
      entry.loading = true;
      entry.error = '';
      $http.get(h.mirrorPath(id, 'versions')).then(function (resp) {
        entry.list = h.normalizeVersions(resp.data);
      }, function (resp) {
        entry.error = h.extractError(resp);
      }).finally(function () {
        entry.loading = false;
      });
    };

    $scope.toggleVersions = function (m) {
      if ($scope.expanded[m.id]) {
        delete $scope.expanded[m.id];
        return;
      }
      $scope.expanded[m.id] = true;
      $scope.loadVersions(m);
    };

    $scope.toggleDelete = function (m) {
      if ($scope.deleting[m.id]) {
        if (!$scope.deleting[m.id].busy) delete $scope.deleting[m.id];
        return;
      }
      $scope.deleting[m.id] = { deleteFiles: false, busy: false, error: '' };
    };

    function askConfirm(title, text, onOk) {
      if (commonService && typeof commonService.confirm === 'function') {
        commonService.confirm(title, text, 'warning', function () {
          $scope.$evalAsync(onOk);
        });
        return;
      }
      if ($window.confirm(title + '\n\n' + text)) onOk();
    }

    $scope.confirmDelete = function (m) {
      var d = $scope.deleting[m.id];
      if (!d || d.busy) return;
      var deleteFiles = !!d.deleteFiles;
      var name = h.dialogSafeText(m.name || m.repo);
      var text = '确定要删除镜像「' + name + '」吗？\n' + (deleteFiles
        ? '目录 ' + h.dialogSafeText(h.joinPath($scope.downloadDir, $scope.settings && $scope.settings.rootDir, m.folder)) + ' 中已下载的文件将被永久删除，无法恢复！'
        : '已下载的文件会保留在磁盘上。');
      askConfirm('删除镜像', text, function () {
        doDelete(m, deleteFiles);
      });
    };

    function doDelete(m, deleteFiles) {
      var d = $scope.deleting[m.id];
      if (!d || d.busy) return;
      d.busy = true;
      d.error = '';
      $http({ method: 'DELETE', url: h.deletePath(m.id, deleteFiles) }).then(function () {
        if (destroyed) return;
        $scope.mirrors = $scope.mirrors.filter(function (x) { return x.id !== m.id; });
        delete $scope.deleting[m.id];
        delete $scope.expanded[m.id];
        delete $scope.versions[m.id];
        if ($scope.form && $scope.form.id === m.id) $scope.closePanel();
        showNotice('success', '已删除镜像「' + (m.name || m.repo) + '」' + (deleteFiles ? '及其文件' : ''));
        refresh();
      }, function (resp) {
        d.busy = false;
        d.error = '删除失败：' + h.extractError(resp);
      });
    }

    $scope.$on('$destroy', function () {
      destroyed = true;
      if (pollTimer) $timeout.cancel(pollTimer);
      if (noticeTimer) $timeout.cancel(noticeTimer);
      if (canceler) canceler.resolve();
      pollTimer = null;
      noticeTimer = null;
    });

    refresh().finally(schedule);
  }

  function registerAngular(attempt) {
    attempt = attempt || 0;
    if (!window.angular) {
      if (attempt < 100) {
        setTimeout(function () { registerAngular(attempt + 1); }, 50);
      } else {
        console.warn('[mirror] angular not found, mirror page disabled');
      }
      return;
    }
    try {
      var app = angular.module('ariaNg');
      app.config(['$routeProvider', function ($routeProvider) {
        $routeProvider.when(ROUTE, {
          template: TEMPLATE,
          controller: 'AriaOneMirrorController'
        });
      }]);
      app.controller('AriaOneMirrorController', [
        '$scope', '$http', '$timeout', '$q', '$window', '$injector', '$animate',
        MirrorController
      ]);
    } catch (e) {
      console.warn('[mirror] failed to register mirror page:', e);
    }
  }

  function init() {
    try {
      insertSidebarEntry();
    } catch (e) {
      console.warn('[mirror] failed to insert sidebar entry:', e);
    }
    registerAngular(0);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
