// node:fs/promises 的包装模块:在 moejs 的 promises 模块之上补齐缺失的 Promise 形态。
//
// 只补 Promise 形态(Node 的 promises 模块没有 Sync/回调形态),名字与同步模块一致。
import * as base from "ada:nodefs/promises";
import * as ext from "ada:fsx";

export * from "ada:nodefs/promises";

// 逐个具名导出:不能 `export * from "ada:fsx"`(它带 promises 等其它名字,会与上游重名)。
export const chmod = ext.promises.chmod;
export const chown = ext.promises.chown;
export const close = ext.promises.close;
export const fstat = ext.promises.fstat;
export const fsync = ext.promises.fsync;
export const ftruncate = ext.promises.ftruncate;
export const link = ext.promises.link;
export const mkdtemp = ext.promises.mkdtemp;
export const open = ext.promises.open;
export const read = ext.promises.read;
export const symlink = ext.promises.symlink;
export const truncate = ext.promises.truncate;
export const utimes = ext.promises.utimes;
export const write = ext.promises.write;

export default base;
