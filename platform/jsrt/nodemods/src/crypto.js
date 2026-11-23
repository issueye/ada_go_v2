// node:crypto —— ada_go_v2 宿主自建的 Node 20 子集(虚拟 ES 模块,moejs 装载)。
//
// 只补插件真正需要且宿主能安全提供的部分:sha256 单向哈希、随机字节与 UUID。
// 不做 KDF/签名/证书/TLS;不接收 Buffer/TypedArray,因为跨 JSON 边界只有字符串,
// 用近似实现冒充 Node 的字节语义会让调用方以为拿到的是密码学正确的结果。
//
// 宿主能力来自 __adaHost.sha256(hex) 与 __adaHost.randomHex(n);
// 返回的 Buffer 用全局 Buffer(moejs node 包安装),没有时退化成 Uint8Array。
const host = globalThis.__adaHost

function requireHost(name) {
  if (!host || typeof host[name] !== "function") {
    throw new Error("node:crypto 不可用:宿主没有安装 " + name)
  }
  return host[name]
}

// bufferFromHex 只在 hex 解码时使用:优先全局 Buffer,保持与 Node 一致的返回类型。
function bufferFromHex(hex) {
  const B = globalThis.Buffer
  if (B && typeof B.from === "function") return B.from(hex, "hex")
  const out = new Uint8Array(hex.length / 2)
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16)
  return out
}

function hexOf(bytes) {
  let out = ""
  for (let i = 0; i < bytes.length; i++) {
    const b = bytes[i] & 0xff
    out += b < 16 ? "0" + b.toString(16) : b.toString(16)
  }
  return out
}

// Hash 只允许链式 update 字符串,digest 之后对象作废——与 Node 的可观察行为一致。
class Hash {
  constructor(algorithm) {
    const alg = String(algorithm === undefined ? "" : algorithm).toLowerCase()
    if (alg !== "sha256") {
      throw new Error("node:crypto 只实现 sha256,得到 " + JSON.stringify(String(algorithm)))
    }
    this.algorithm = "sha256"
    this.parts = []
    this.finalized = false
  }

  update(data) {
    if (this.finalized) throw new Error("node:crypto.Hash 已经调用过 digest")
    if (typeof data !== "string") {
      throw new TypeError("node:crypto.Hash.update 只接受字符串:跨边界不传递 Buffer/TypedArray")
    }
    this.parts.push(data)
    return this
  }

  digest(encoding) {
    if (this.finalized) throw new Error("node:crypto.Hash 已经调用过 digest")
    this.finalized = true
    const hex = requireHost("sha256")(this.parts.join(""))
    this.parts = []
    return decodeHex(hex, encoding)
  }
}

function decodeHex(hex, encoding) {
  const enc = encoding === undefined || encoding === null ? "" : String(encoding).toLowerCase()
  if (enc === "") return bufferFromHex(hex)
  if (enc === "hex") return hex
  if (enc === "base64" || enc === "base64url") {
    const B = globalThis.Buffer
    if (!B || typeof B.from !== "function" || typeof B.from(hex, "hex").toString !== "function") {
      throw new Error("node:crypto.digest 的 " + enc + " 输出需要 Buffer 支持")
    }
    return B.from(hex, "hex").toString(enc)
  }
  throw new Error("node:crypto.digest 不支持编码 " + JSON.stringify(encoding))
}

export function createHash(algorithm) {
  return new Hash(algorithm)
}

// randomBytes 返回 Buffer(有 Buffer 时)或 Uint8Array;长度由宿主 crypto/rand 生成。
export function randomBytes(size) {
  const n = Number(size)
  if (!Number.isInteger(n) || n < 0) {
    throw new TypeError("node:crypto.randomBytes 的 size 必须是非负整数")
  }
  if (n > 65536) {
    throw new RangeError("node:crypto.randomBytes 单次上限 65536 字节")
  }
  return bufferFromHex(requireHost("randomHex")(n))
}

// randomUUID 生成 RFC 4122 v4 UUID;随机源仍是宿主 crypto/rand。
export function randomUUID() {
  const bytes = randomBytes(16)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = hexOf(bytes)
  return hex.slice(0, 8) + "-" + hex.slice(8, 12) + "-" + hex.slice(12, 16) + "-" +
    hex.slice(16, 20) + "-" + hex.slice(20)
}

export default { createHash, randomBytes, randomUUID, Hash }
