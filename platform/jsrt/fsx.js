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
import { Buffer } from "node:buffer";
import { Readable, Writable } from "node:stream";

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

// —— 文件描述符族 ——
//
// 句柄跨调用存活,由宿主(platform/jsrt/nodefsfd.go)的 fd 表管理;这里只做
// Node 的调用形状:fd 是数字,Buffer 在调用方原地填充/读取。

function fdOf(value) {
  const n = Number(value);
  if (!Number.isInteger(n) || n < 0) throw new TypeError("fd 必须是非负整数");
  return n;
}

function flagsOf(value) {
  return value === undefined || value === null ? "r" : String(value);
}

function posOf(value) {
  if (value === undefined || value === null) return -1;
  const n = Number(value);
  return Number.isInteger(n) ? n : -1;
}

function bufferOf(value, what) {
  if (value instanceof Uint8Array) return Buffer.from(value);
  throw new TypeError(what + " 必须是 Buffer / Uint8Array");
}

export function openSync(path, flags, mode) {
  return sync("open", [pathOf(path, "path"), flagsOf(flags), mode === undefined ? 438 : modeOf(mode)]);
}

export function open(path, flags, mode, cb) {
  if (typeof flags === "function") {
    cb = flags;
    flags = "r";
    mode = undefined;
  } else if (typeof mode === "function") {
    cb = mode;
    mode = undefined;
  }
  callback("open", [pathOf(path, "path"), flagsOf(flags), mode === undefined ? 438 : modeOf(mode)], cb);
}

export function closeSync(fd) {
  return sync("close", [fdOf(fd)]);
}

export function close(fd, cb) {
  callback("close", [fdOf(fd)], cb);
}

export function readSync(fd, buffer, offset, length, position) {
  const target = bufferOf(buffer, "buffer");
  const off = offset === undefined ? 0 : Number(offset);
  const len = length === undefined ? target.length - off : Number(length);
  const res = sync("read", [fdOf(fd), len, posOf(position)]);
  const bytes = Buffer.from(res.data, "base64");
  target.set(bytes, off);
  buffer.set(bytes, off);
  return res.bytesRead;
}

export function read(fd, buffer, offset, length, position, cb) {
  // Node 允许省略 offset/length/position,回调是最后一个参数。
  if (typeof offset === "function") {
    cb = offset;
    offset = 0;
    length = undefined;
    position = undefined;
  } else if (typeof length === "function") {
    cb = length;
    length = undefined;
    position = undefined;
  } else if (typeof position === "function") {
    cb = position;
    position = undefined;
  }
  if (typeof cb !== "function") throw new TypeError("read 需要一个回调");
  const target = bufferOf(buffer, "buffer");
  const off = offset === undefined ? 0 : Number(offset);
  const len = length === undefined ? target.length - off : Number(length);
  asyncOp("read", [fdOf(fd), len, posOf(position)]).then(
    function (res) {
      const bytes = Buffer.from(res.data, "base64");
      target.set(bytes, off);
      buffer.set(bytes, off);
      cb(null, res.bytesRead, buffer);
    },
    function (err) {
      cb(err, 0, buffer);
    },
  );
}

// writeSync 支持 Node 的两种重载:(fd, buffer, offset, length, position) 与
// (fd, string, position, encoding)。
export function writeSync(fd, data, offset, length, position) {
  const id = fdOf(fd);
  if (typeof data === "string") {
    const pos = offset === undefined || offset === null ? -1 : Number(offset);
    const enc = typeof length === "string" ? length : "utf8";
    return sync("write", [id, Buffer.from(data, enc).toString("base64"), Number.isInteger(pos) ? pos : -1]);
  }
  const source = bufferOf(data, "data");
  const off = offset === undefined ? 0 : Number(offset);
  const len = length === undefined ? source.length - off : Number(length);
  const slice = Buffer.from(source.subarray(off, off + len));
  return sync("write", [id, slice.toString("base64"), posOf(position)]);
}

export function write(fd, data, offset, length, position, cb) {
  if (typeof offset === "function") {
    cb = offset;
    offset = undefined;
    length = undefined;
    position = undefined;
  } else if (typeof length === "function") {
    cb = length;
    length = undefined;
    position = undefined;
  } else if (typeof position === "function") {
    cb = position;
    position = undefined;
  }
  if (typeof cb !== "function") throw new TypeError("write 需要一个回调");
  const id = fdOf(fd);
  const isString = typeof data === "string";
  let payload;
  let bytes;
  try {
    if (isString) {
      const pos = offset === undefined || offset === null ? -1 : Number(offset);
      const enc = typeof length === "string" ? length : "utf8";
      bytes = Buffer.from(data, enc);
      payload = [id, bytes.toString("base64"), Number.isInteger(pos) ? pos : -1];
    } else {
      const source = bufferOf(data, "data");
      const off = offset === undefined ? 0 : Number(offset);
      const len = length === undefined ? source.length - off : Number(length);
      bytes = Buffer.from(source.subarray(off, off + len));
      payload = [id, bytes.toString("base64"), posOf(position)];
    }
  } catch (err) {
    cb(err, 0, data);
    return;
  }
  asyncOp("write", payload).then(
    function (written) {
      cb(null, written, data);
    },
    function (err) {
      cb(err, 0, data);
    },
  );
}

export function fsyncSync(fd) {
  return sync("fsync", [fdOf(fd)]);
}

export function fsync(fd, cb) {
  callback("fsync", [fdOf(fd)], cb);
}

export function ftruncateSync(fd, len) {
  return sync("ftruncate", [fdOf(fd), len === undefined ? 0 : intOf(len, "len")]);
}

export function ftruncate(fd, len, cb) {
  if (typeof len === "function") {
    cb = len;
    len = 0;
  }
  callback("ftruncate", [fdOf(fd), len === undefined ? 0 : intOf(len, "len")], cb);
}

export function fstatSync(fd) {
  return sync("fstat", [fdOf(fd)]);
}

export function fstat(fd, cb) {
  callback("fstat", [fdOf(fd)], cb);
}

// —— 文件流 ——
//
// 两个流都建立在 fd 族之上:读按块 await,写按块 await,结束时关句柄。
// 注意:Readable.from 的回压只在缓冲层面(没有跨块暂停),大文件会被整段读进内存;
// 需要分块消费时用 for await 逐块处理,而不是一次 .on("data") 全收。

export function createReadStream(path, options) {
  const opts = options || {};
  const chunkSize = opts.highWaterMark === undefined ? 64 * 1024 : Number(opts.highWaterMark);
  const start = opts.start === undefined ? 0 : Number(opts.start);
  const end = opts.end === undefined ? -1 : Number(opts.end);
  let fd = -1;
  let pos = start;

  const closeQuiet = function () {
    if (fd >= 0) {
      try {
        closeSync(fd);
      } catch (_) {}
      fd = -1;
    }
  };
  const iterable = {
    next: function () {
      if (fd < 0) {
        try {
          fd = openSync(path, "r");
        } catch (err) {
          return Promise.reject(err);
        }
      }
      let want = chunkSize;
      if (end >= 0) {
        const left = end - pos + 1;
        if (left <= 0) {
          closeQuiet();
          return Promise.resolve({ value: undefined, done: true });
        }
        if (left < want) want = left;
      }
      if (want <= 0) {
        closeQuiet();
        return Promise.resolve({ value: undefined, done: true });
      }
      return asyncOp("read", [fd, want, pos]).then(
        function (res) {
          if (res.bytesRead <= 0) {
            closeQuiet();
            return { value: undefined, done: true };
          }
          pos += res.bytesRead;
          return { value: Buffer.from(res.data, "base64"), done: false };
        },
        function (err) {
          closeQuiet();
          throw err;
        },
      );
    },
    return: function () {
      closeQuiet();
      return Promise.resolve({ value: undefined, done: true });
    },
  };
  iterable[Symbol.asyncIterator] = function () {
    return iterable;
  };
  const stream = Readable.from(iterable, { highWaterMark: chunkSize });
  stream.on("close", closeQuiet);
  stream.on("error", closeQuiet);
  return stream;
}

export function createWriteStream(path, options) {
  const opts = options || {};
  const flags = opts.flags === undefined ? "w" : String(opts.flags);
  let fd = -1;
  const closeQuiet = function () {
    if (fd >= 0) {
      try {
        closeSync(fd);
      } catch (_) {}
      fd = -1;
    }
  };
  const stream = new Writable({ highWaterMark: opts.highWaterMark });
  stream._write = function (chunk, enc, cb) {
    try {
      if (fd < 0) fd = openSync(path, flags);
    } catch (err) {
      cb(err);
      return;
    }
    let bytes;
    try {
      bytes = typeof chunk === "string" ? Buffer.from(chunk, enc || "utf8") : Buffer.from(chunk);
    } catch (err) {
      cb(err);
      return;
    }
    asyncOp("write", [fd, bytes.toString("base64"), -1]).then(
      function () {
        cb();
      },
      function (err) {
        cb(err);
      },
    );
  };
  stream.on("finish", closeQuiet);
  stream.on("error", closeQuiet);
  return stream;
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
  // 文件描述符族的 Promise 形态。read/write 返回 Node 的 {bytesRead|bytesWritten, buffer}
  // (fsp 的形状),不做 FileHandle 包装(Node 的 fsp.open 返回 FileHandle,那是另一套 API)。
  open: function open(path, flags, mode) {
    return asyncOp("open", [pathOf(path, "path"), flagsOf(flags), mode === undefined ? 438 : modeOf(mode)]);
  },
  close: function close(fd) {
    return asyncOp("close", [fdOf(fd)]).then(function () {
      return undefined;
    });
  },
  read: function read(fd, buffer, offset, length, position) {
    const target = bufferOf(buffer, "buffer");
    const off = offset === undefined ? 0 : Number(offset);
    const len = length === undefined ? target.length - off : Number(length);
    return asyncOp("read", [fdOf(fd), len, posOf(position)]).then(function (res) {
      const bytes = Buffer.from(res.data, "base64");
      target.set(bytes, off);
      buffer.set(bytes, off);
      return { bytesRead: res.bytesRead, buffer: buffer };
    });
  },
  write: function write(fd, data, offset, length, position) {
    const id = fdOf(fd);
    if (typeof data === "string") {
      const pos = offset === undefined || offset === null ? -1 : Number(offset);
      const enc = typeof length === "string" ? length : "utf8";
      return asyncOp("write", [id, Buffer.from(data, enc).toString("base64"), Number.isInteger(pos) ? pos : -1]).then(
        function (written) {
          return { bytesWritten: written, buffer: data };
        },
      );
    }
    const source = bufferOf(data, "data");
    const off = offset === undefined ? 0 : Number(offset);
    const len = length === undefined ? source.length - off : Number(length);
    const slice = Buffer.from(source.subarray(off, off + len));
    return asyncOp("write", [id, slice.toString("base64"), posOf(position)]).then(function (written) {
      return { bytesWritten: written, buffer: data };
    });
  },
  fsync: function fsync(fd) {
    return asyncOp("fsync", [fdOf(fd)]).then(function () {
      return undefined;
    });
  },
  ftruncate: function ftruncate(fd, len) {
    return asyncOp("ftruncate", [fdOf(fd), len === undefined ? 0 : intOf(len, "len")]).then(function () {
      return undefined;
    });
  },
  fstat: function fstat(fd) {
    return asyncOp("fstat", [fdOf(fd)]);
  },
};
