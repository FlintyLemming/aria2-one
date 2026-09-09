(function () {
  'use strict';

  // 1. 同源自适应 RPC 自动连接
  function autoConfigureRPC() {
    const host = window.location.hostname;
    const port = window.location.port || (window.location.protocol === 'https:' ? '443' : '80');
    const protocol = window.location.protocol === 'https:' ? 'https' : 'http';

    const defaultOptions = {
      language: (navigator.language && navigator.language.startsWith('zh') ? 'zh_Hans' : 'en'),
      theme: 'light',
      title: '${downspeed}, ${upspeed} - ${title}',
      titleRefreshInterval: 5000,
      browserNotification: false,
      browserNotificationSound: true,
      browserNotificationFrequency: 'unlimited',
      rpcAlias: '',
      rpcHost: host,
      rpcPort: port,
      rpcInterface: 'jsonrpc',
      protocol: protocol,
      httpMethod: 'POST',
      rpcRequestHeaders: '',
      secret: '',
      extendRpcServers: [],
      webSocketReconnectInterval: 5000,
      globalStatRefreshInterval: 1000,
      downloadTaskRefreshInterval: 1000,
      keyboardShortcuts: true,
      swipeGesture: true,
      dragAndDropTasks: true,
      rpcListDisplayOrder: 'recentlyUsed',
      afterCreatingNewTask: 'task-list',
      removeOldTaskAfterRetrying: false,
      confirmTaskRemoval: true,
      includePrefixWhenCopyingFromTaskDetails: true,
      showPiecesInfoInTaskDetailPage: 'le10240',
      afterRetryingTask: 'task-list-downloading',
      taskListIndependentDisplayOrder: false,
      displayOrder: 'default:asc',
      waitingTaskListPageDisplayOrder: 'default:asc',
      stoppedTaskListPageDisplayOrder: 'default:asc',
      fileListDisplayOrder: 'default:asc',
      peerListDisplayOrder: 'default:asc'
    };

    let options = Object.assign({}, defaultOptions);
    try {
      const raw = localStorage.getItem('AriaNg.Options');
      if (raw) {
        options = Object.assign(options, JSON.parse(raw));
      }
    } catch (e) {}

    options.rpcHost = host;
    options.rpcPort = port;
    options.protocol = protocol;
    options.rpcInterface = 'jsonrpc';
    options.httpMethod = 'POST';
    options.secret = '';
    options.extendRpcServers = [];

    try {
      localStorage.setItem('AriaNg.Options', JSON.stringify(options));
    } catch (e) {}
  }

  autoConfigureRPC();

  // 2. 注入 Angular 增强模块
  function patchAngular() {
    if (!window.angular) {
      setTimeout(patchAngular, 50);
      return;
    }

    try {
      const app = angular.module('ariaNg');
      app.run([
        '$templateCache',
        '$rootScope',
        'ariaNgCommonService',
        function ($templateCache, $rootScope, ariaNgCommonService) {
          // 辅助方法：生成下载直链 URL
          $rootScope.getDirectDownloadUrl = function (filePath) {
            if (!filePath) return '';
            let rel = filePath;
            if (rel.startsWith('/downloads/')) {
              rel = rel.substring('/downloads/'.length);
            } else if (rel.startsWith('/downloads')) {
              rel = rel.substring('/downloads'.length);
            }
            return '/download/' + rel.split('/').map(encodeURIComponent).join('/');
          };

          // 辅助方法：判断是否为媒体预览文件
          $rootScope.isMediaFile = function (filePath) {
            if (!filePath) return false;
            const exts = ['.mp4', '.mkv', '.webm', '.mp3', '.ogg', '.wav', '.flac', '.jpg', '.jpeg', '.png', '.gif', '.webp', '.pdf', '.txt'];
            const lower = filePath.toLowerCase();
            return exts.some(function (ext) { return lower.endsWith(ext); });
          };

          // 拦截确认弹窗：文案强化为物理删除提示
          const originConfirm = ariaNgCommonService.confirm;
          ariaNgCommonService.confirm = function (title, text, type, callback, isAsync, options) {
            if (
              text === 'Are you sure you want to remove the selected task?' ||
              text === '您是否要删除选中的任务?' ||
              text === 'Are you sure you want to clear stopped tasks?' ||
              text === '您是否要清除已结束的任务?'
            ) {
              text = '此操作将从下载列表移除任务，并永久删除磁盘上的对应物理文件！';
            }
            return originConfirm.call(this, title, text, type, callback, isAsync, options);
          };

          // 修改 views/list.html 模板：已完成任务添加直链下载按钮
          const listHtml = $templateCache.get('views/list.html');
          if (listHtml) {
            const target = 'ng-if="isTaskRetryable(task)"';
            const downloadBtnHtml =
              '<a class="btn-direct-download" ng-if="task.status === \'complete\' && task.files && task.files.length === 1" ng-href="{{getDirectDownloadUrl(task.files[0].path)}}" download="{{task.taskName}}" ng-click="$event.stopPropagation()">⬇️ 下载到本地</a>' +
              '<a class="btn-direct-download" ng-if="task.status === \'complete\' && task.files && task.files.length > 1" ng-href="#!/task/detail/{{task.gid}}" ng-click="$event.stopPropagation()">📁 查看文件列表</a> ';
            $templateCache.put('views/list.html', listHtml.replace(target, downloadBtnHtml + target));
          }

          // 修改 views/task-detail.html 模板：文件列表添加逐个下载与在线预览
          const detailHtml = $templateCache.get('views/task-detail.html');
          if (detailHtml) {
            const fileTarget = '<span class="task-size" ng-bind="file.length | readableVolume"></span>';
            const fileActionHtml =
              fileTarget +
              '<span ng-if="task.status === \'complete\' || file.completePercent === 100">' +
              '<a class="btn-direct-download" ng-href="{{getDirectDownloadUrl(file.path)}}" download="{{file.fileName}}" target="_blank">下载</a>' +
              '<a class="btn-direct-preview" ng-if="isMediaFile(file.path)" ng-href="{{getDirectDownloadUrl(file.path)}}" target="_blank">预览</a>' +
              '</span>';
            $templateCache.put('views/task-detail.html', detailHtml.replace(fileTarget, fileActionHtml));
          }
        }
      ]);
    } catch (e) {
      console.warn('[AriaNg Enhanced] failed to patch templates:', e);
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', patchAngular);
  } else {
    patchAngular();
  }
})();
