const test = require('node:test');
const assert = require('node:assert');

test('autoConnectConfig updates localStorage correctly', () => {
  const fakeStorage = {};
  const mockLocalStorage = {
    getItem: (k) => fakeStorage[k] || null,
    setItem: (k, v) => { fakeStorage[k] = v; }
  };

  const mockLocation = {
    hostname: '192.168.1.100',
    port: '8080',
    protocol: 'http:'
  };

  function updateOptions(loc, storage) {
    const host = loc.hostname;
    const port = loc.port || (loc.protocol === 'https:' ? '443' : '80');
    const protocol = loc.protocol === 'https:' ? 'wss' : 'ws';

    let options = {};
    try {
      const raw = storage.getItem('AriaNg.Options');
      if (raw) options = JSON.parse(raw);
    } catch (e) {}

    options.rpcHost = host;
    options.rpcPort = port;
    options.protocol = protocol;
    options.rpcInterface = 'jsonrpc';
    options.httpMethod = 'POST';
    options.secret = '';
    options.extendRpcServers = [];

    storage.setItem('AriaNg.Options', JSON.stringify(options));
  }

  updateOptions(mockLocation, mockLocalStorage);
  const result = JSON.parse(fakeStorage['AriaNg.Options']);

  assert.strictEqual(result.rpcHost, '192.168.1.100');
  assert.strictEqual(result.rpcPort, '8080');
  assert.strictEqual(result.protocol, 'ws');
  assert.strictEqual(result.rpcInterface, 'jsonrpc');
  assert.strictEqual(result.secret, '');
});

test('media file detector recognizes extensions correctly', () => {
  const mediaExts = ['.mp4', '.mkv', '.webm', '.mp3', '.ogg', '.wav', '.flac', '.jpg', '.jpeg', '.png', '.gif', '.webp', '.pdf', '.txt'];
  function isMedia(path) {
    if (!path) return false;
    const lower = path.toLowerCase();
    return mediaExts.some(ext => lower.endsWith(ext));
  }

  assert.strictEqual(isMedia('movie.mkv'), true);
  assert.strictEqual(isMedia('song.MP3'), true);
  assert.strictEqual(isMedia('image.png'), true);
  assert.strictEqual(isMedia('archive.zip'), false);
  assert.strictEqual(isMedia('setup.exe'), false);
});
