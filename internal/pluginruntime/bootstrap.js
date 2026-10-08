(function (callNative, cryptoNative, logNative, envJSON, runJSON) {
  'use strict';
  var freeze = Object.freeze;
  var defineProperty = Object.defineProperty;
  var stringify = JSON.stringify;
  var parse = JSON.parse;
  var envData = parse(envJSON);
  var runData = parse(runJSON);

  class OBoardError extends Error {
    constructor(code, message) {
      super(message || code);
      this.name = 'OBoardError';
      this.code = code;
    }
  }

  // SecretRef is an opaque, instance-bound reference. It never carries the
  // secret value; only the Controller gateway can resolve it.
  class SecretRef {
    constructor(ref, name, prefix, suffix) {
      defineProperty(this, '_ref', { value: ref });
      defineProperty(this, 'name', { value: name, enumerable: true });
      defineProperty(this, '_prefix', { value: prefix || '' });
      defineProperty(this, '_suffix', { value: suffix || '' });
      freeze(this);
    }
    withPrefix(prefix) { return new SecretRef(this._ref, this.name, String(prefix), this._suffix); }
    withSuffix(suffix) { return new SecretRef(this._ref, this.name, this._prefix, String(suffix)); }
    toJSON() {
      var out = { $secret: this._ref };
      if (this._prefix) out.prefix = this._prefix;
      if (this._suffix) out.suffix = this._suffix;
      return out;
    }
    toString() { return '[SecretRef ' + this.name + ']'; }
  }
  freeze(SecretRef.prototype);

  function call(method, args) {
    var response = parse(callNative(method, stringify(args === undefined ? {} : args)));
    if (!response.ok) throw new OBoardError(response.code || 'INTERNAL_ERROR', response.message || '');
    return response.result;
  }

  function cryptoCall(op, args) {
    var response = parse(cryptoNative(op, stringify(args)));
    if (!response.ok) throw new OBoardError(response.code || 'INVALID_ARGUMENT', response.message || '');
    return response.result;
  }

  function serverArgs(serverId) {
    if (serverId === undefined || serverId === null || serverId === '') throw new OBoardError('INVALID_ARGUMENT', 'server_id is required');
    return { server_id: String(serverId) };
  }

  function options(value) {
    if (value === undefined) return {};
    if (value === null || typeof value !== 'object' || Array.isArray(value)) throw new OBoardError('INVALID_ARGUMENT', 'options must be an object');
    var out = {};
    for (var key in value) {
      if (Object.prototype.hasOwnProperty.call(value, key) && value[key] !== undefined) out[key] = value[key];
    }
    if (out.server_id !== undefined) out.server_id = String(out.server_id);
    return out;
  }

  function isSecret(value) { return value instanceof SecretRef; }

  function hmac(algorithm) {
    return function (key, data, opts) {
      opts = opts || {};
      if (isSecret(key)) {
        return call('crypto.hmac', { algorithm: algorithm, key: key.toJSON(), data: String(data), data_encoding: opts.input || 'utf8', output: opts.output || 'hex' }).digest;
      }
      return cryptoCall('hmac', { algorithm: algorithm, key: String(key), key_encoding: opts.keyEncoding || 'utf8', data: String(data), data_encoding: opts.input || 'utf8', output: opts.output || 'hex' });
    };
  }

  function digest(algorithm) {
    return function (data, opts) {
      opts = opts || {};
      return cryptoCall('hash', { algorithm: algorithm, data: String(data), data_encoding: opts.input || 'utf8', output: opts.output || 'hex' });
    };
  }

  var crypto = freeze({
    sha256: digest('sha256'),
    sha1: digest('sha1'),
    hmacSha256: hmac('sha256'),
    hmacSha1: hmac('sha1'),
    base64Encode: function (data, opts) { opts = opts || {}; return cryptoCall('base64_encode', { data: String(data), data_encoding: opts.input || 'utf8', url: !!opts.url }); },
    base64Decode: function (text, opts) { opts = opts || {}; return cryptoCall('base64_decode', { data: String(text), output: opts.output || 'utf8', url: !!opts.url }); },
    hexEncode: function (data, opts) { opts = opts || {}; return cryptoCall('hex_encode', { data: String(data), data_encoding: opts.input || 'utf8' }); },
    hexDecode: function (text, opts) { opts = opts || {}; return cryptoCall('hex_decode', { data: String(text), output: opts.output || 'utf8' }); },
    randomBytes: function (length, encoding) { return cryptoCall('random', { length: length, output: encoding || 'hex' }); },
    uuid: function () { return cryptoCall('uuid', {}); }
  });

  function httpRequest(opts) {
    var request = options(opts);
    if (request.json !== undefined) {
      if (request.body !== undefined) throw new OBoardError('INVALID_ARGUMENT', 'use either body or json');
      request.body = stringify(request.json);
      var headers = {};
      var hasType = false;
      for (var name in (request.headers || {})) {
        headers[name] = request.headers[name];
        if (name.toLowerCase() === 'content-type') hasType = true;
      }
      if (!hasType) headers['Content-Type'] = 'application/json';
      request.headers = headers;
      delete request.json;
    }
    var response = call('http.request', request);
    response.ok = response.status >= 200 && response.status < 300;
    return freeze(response);
  }

  var oboard = freeze({
    servers: freeze({
      get: function (serverId) { return call('servers.get', serverArgs(serverId)); },
      list: function () { return call('servers.list', {}).servers; },
      health: function (serverId) { return call('servers.health', serverArgs(serverId)); },
      metrics: function (serverId) { return call('servers.metrics', serverArgs(serverId)); }
    }),
    network: freeze({
      ping: function (opts) { return call('network.ping', options(opts)); },
      trace: function (opts) { return call('network.trace', options(opts)); },
      tcpProbe: function (opts) { return call('network.tcpProbe', options(opts)); },
      dnsLookup: function (opts) { return call('network.dnsLookup', options(opts)); },
      httpProbe: function (opts) { return call('network.httpProbe', options(opts)); }
    }),
    http: freeze({ request: httpRequest }),
    state: freeze({
      get: function (key) { return call('state.get', { key: String(key) }); },
      list: function (prefix, opts) { opts = opts || {}; return call('state.list', { prefix: prefix === undefined ? '' : String(prefix), limit: opts.limit || 0 }).entries; },
      set: function (key, value) { return call('state.set', { key: String(key), value: value === undefined ? null : value }); },
      delete: function (key, opts) { var args = { key: String(key) }; if (opts && opts.expected_version !== undefined) args.expected_version = opts.expected_version; return call('state.delete', args); },
      compareAndSwap: function (key, expectedVersion, value) { return call('state.compareAndSwap', { key: String(key), expected_version: expectedVersion, value: value === undefined ? null : value }); }
    }),
    notifications: freeze({ send: function (opts) { return call('notifications.send', options(opts)); } }),
    users: freeze({
      get: function (userId) { return call('users.get', { user_id: String(userId) }); },
      list: function () { return call('users.list', {}).users; },
      notify: function (opts) {
        var request = options(opts);
        if (Array.isArray(request.user_ids)) request.user_ids = request.user_ids.map(function (id) { return String(id); });
        return call('users.notify', request);
      }
    }),
    plans: freeze({
      list: function () { return call('plans.list', {}).plans; },
      users: function (planId) { return call('plans.users', { plan_id: String(planId) }).users; },
      notify: function (opts) {
        var request = options(opts);
        if (request.plan_id !== undefined) request.plan_id = String(request.plan_id);
        return call('plans.notify', request);
      }
    }),
    ui: freeze({ publish: function (opts) { return call('ui.publish', options(opts)); } }),
    crypto: crypto
  });

  var env = {};
  var raw = {};
  var types = {};
  for (var name in envData) {
    var entry = envData[name];
    var value = entry.value;
    if (entry.type === 'secret') value = new SecretRef(value.$secret, name);
    else if (value !== null && typeof value === 'object') value = freeze(value);
    defineProperty(env, name, { value: value, enumerable: true });
    raw[name] = entry.raw;
    types[name] = entry.type;
  }
  defineProperty(env, 'get', { value: function (key, fallback) { return Object.prototype.hasOwnProperty.call(env, key) ? env[key] : fallback; } });
  defineProperty(env, 'raw', { value: function (key) { return Object.prototype.hasOwnProperty.call(raw, key) ? (types[key] === 'secret' ? '[SecretRef ' + key + ']' : raw[key]) : undefined; } });
  defineProperty(env, 'has', { value: function (key) { return Object.prototype.hasOwnProperty.call(raw, key); } });
  defineProperty(env, 'names', { value: function () { return Object.keys(raw); } });
  defineProperty(env, 'type', { value: function (key) { return types[key]; } });
  freeze(env);

  function format(args) {
    var parts = [];
    for (var i = 0; i < args.length; i++) {
      var item = args[i];
      if (typeof item === 'string') parts.push(item);
      else if (item instanceof Error) parts.push((item.code ? item.code + ': ' : '') + item.message);
      else {
        try { parts.push(stringify(item)); } catch (e) { parts.push(String(item)); }
      }
    }
    return parts.join(' ');
  }
  function logger(level) { return function () { logNative(level, format(arguments)); }; }
  var log = freeze({ debug: logger('debug'), info: logger('info'), warn: logger('warn'), error: logger('error') });

  defineProperty(globalThis, 'oboard', { value: oboard, enumerable: true });
  defineProperty(globalThis, 'env', { value: env, enumerable: true });
  defineProperty(globalThis, 'log', { value: log, enumerable: true });
  defineProperty(globalThis, 'console', { value: freeze({ log: log.info, info: log.info, debug: log.debug, warn: log.warn, error: log.error }), enumerable: true });
  defineProperty(globalThis, 'OBoardError', { value: OBoardError, enumerable: true });
  defineProperty(globalThis, 'SecretRef', { value: SecretRef, enumerable: true });
  return freeze(runData);
})
