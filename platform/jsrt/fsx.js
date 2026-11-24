// node:fs 的补齐层:补上 moejs 的 node 包没有实现、但在 Node 里常用的元数据类 API。
//
// 三种形态都在这里:
//   - xxxSync   同步;
//   - xxx(cb)   Node 的回调形态(错误优先);
//   - promises.xxx  Promise 形态(也被 node:fs/promises 的包装模块复用)。
//
// 为什么形态在 JS、操作在 Go:形态是纯编排;真正的文件操作必须由宿主用 os.Root 做,
// 路径才不会越出工作区(与 moejs 自带的 node:fs 同一条边界),也才能复用写权限开关。
// 这一层只负责把宿主的结果包成 Node 的形状与错误码。
const host = globalThis.__adaHost;

// unwrap 解宿主的结果信封:{ok,value} 或 {ok:false,code,message,path}。
function unwrap(raw, op) {
  const res = JSON.parse(raw);
  if (res.ok) return res.value;
  const err = new Error(res.message);
  err.code = res.code;
  err.syscall = "fs." + op;
  if (res.path !== undefined) err.path = res.path;
  throw err;
}

function sync(op, args) {
  return unwrap(host.fsx.call(op, JSON.stringify(args)), op);
}

function asyncOp(op, args) {
  return Promise.resolve(host.fsx.callAsync(op, JSON.stringify(args))).then(function (raw) {
    return unwrap(raw, op);
  });
}

function callback(op, args, cb) {
  if (typeof cb !== "function") throw new TypeError("callback 必须是函数");
  asyncOp(op, args).then(
    function (v) {
      cb(null, v);
    },
    function (e) {
      cb(e);
    },
  );
}

// pathOf 只挡最明显的坏参数:详细的路径约束在宿主侧(os.Root + 工作区检查)。
function pathOf(v, what) {
  if (typeof v !== "string" || v === "") throw new TypeError(what + " 必须是非空字符串");
  if (v.indexOf("\u0000") >= 0) throw new TypeError(what + " 不能包含 NUL");
  return v;
}

function modeOf(v) {
  const n = Number(v);
  if (!isFinite(n) || n < 0) throw new TypeError("mode 必须是数字");
  return Math.floor(n);
}

function intOf(v, what) {
  const n = Number(v);
  if (!isFinite(n)) throw new TypeError(what + " 必须是数字");
  return Math.floor(n);
}

// secondsOf 把 Node 接受的时间形态(秒数 / Date)统一成秒数。
function secondsOf(v, what) {
  if (v instanceof Date) return v.getTime() / 1000;
  const n = Number(v);
  if (!isFinite(n)) throw new TypeError(what + " 必须是数字或 Date");
  return n;
}

// —— mkdtemp ——

export function mkdtempSync(prefix) {
  return sync("mkdtemp", [pathOf(prefix, "prefix")]);
}

export function mkdtemp(prefix, options, cb) {
  if (typeof options === "function") {
    cb = options;
  }
  callback("mkdtemp", [pathOf(prefix, "prefix")], cb);
}

// —— symlink ——

export function symlinkSync(target, path, type) {
  return sync("symlink", [pathOf(target, "target"), pathOf(path, "path"), type === undefined ? "" : String(type)]);
}

export function symlink(target, path, type, cb) {
  if (typeof type === "function") {
    cb = type;
    type = undefined;
  }
  callback("symlink", [pathOf(target, "target"), pathOf(path, "path"), type === undefined ? "" : String(type)], cb);
}

// —— link ——

export function linkSync(existingPath, newPath) {
  return sync("link", [pathOf(existingPath, "existingPath"), pathOf(newPath, "newPath")]);
}

export function link(existingPath, newPath, cb) {
  callback("link", [pathOf(existingPath, "existingPath"), pathOf(newPath, "newPath")], cb);
}

// —— chmod ——

export function chmodSync(path, mode) {
  return sync("chmod", [pathOf(path, "path"), modeOf(mode)]);
}

export function chmod(path, mode, cb) {
  callback("chmod", [pathOf(path, "path"), modeOf(mode)], cb);
}

// —— chown ——

export function chownSync(path, uid, gid) {
  return sync("chown", [pathOf(path, "path"), intOf(uid, "uid"), intOf(gid, "gid")]);
}

export function chown(path, uid, gid, cb) {
  callback("chown", [pathOf(path, "path"), intOf(uid, "uid"), intOf(gid, "gid")], cb);
}

// —— truncate ——

export function truncateSync(path, len) {
  return sync("truncate", [pathOf(path, "path"), len === undefined ? 0 : intOf(len, "len")]);
}

export function truncate(path, len, cb) {
  if (typeof len === "function") {
    cb = len;
    len = 0;
  }
  callback("truncate", [pathOf(path, "path"), len === undefined ? 0 : intOf(len, "len")], cb);
}

// —— utimes ——

export function utimesSync(path, atime, mtime) {
  return sync("utimes", [pathOf(path, "path"), secondsOf(atime, "atime"), secondsOf(mtime, "mtime")]);
}

export function utimes(path, atime, mtime, cb) {
  callback("utimes", [pathOf(path, "path"), secondsOf(atime, "atime"), secondsOf(mtime, "mtime")], cb);
}

// promises 是给 node:fs/promises 与 fs.promises 用的 Promise 形态。
export const promises = {
  mkdtemp: function mkdtemp(prefix) {
    return asyncOp("mkdtemp", [pathOf(prefix, "prefix")]);
  },
  symlink: function symlink(target, path, type) {
    return asyncOp("symlink", [pathOf(target, "target"), pathOf(path, "path"), type === undefined ? "" : String(type)]);
  },
  link: function link(existingPath, newPath) {
    return asyncOp("link", [pathOf(existingPath, "existingPath"), pathOf(newPath, "newPath")]);
  },
  chmod: function chmod(path, mode) {
    return asyncOp("chmod", [pathOf(path, "path"), modeOf(mode)]);
  },
  chown: function chown(path, uid, gid) {
    return asyncOp("chown", [pathOf(path, "path"), intOf(uid, "uid"), intOf(gid, "gid")]);
  },
  truncate: function truncate(path, len) {
    return asyncOp("truncate", [pathOf(path, "path"), len === undefined ? 0 : intOf(len, "len")]);
  },
  utimes: function utimes(path, atime, mtime) {
    return asyncOp("utimes", [pathOf(path, "path"), secondsOf(atime, "atime"), secondsOf(mtime, "mtime")]);
  },
};
