// Minimal QR code encoder: byte mode, error correction level M, versions 1 to 10, mask 0.
// It follows the structure of the QR specification (ISO/IEC 18004) and renders an SVG.

const ECC_PER_BLOCK = [10, 16, 26, 18, 24, 16, 18, 22, 22, 26]; // index: version - 1
const NUM_BLOCKS = [1, 1, 2, 2, 4, 4, 4, 5, 5, 5];

const rawModules = (ver: number) => {
  let n = (16 * ver + 128) * ver + 64;
  if (ver >= 2) {
    const a = Math.floor(ver / 7) + 2;
    n -= (25 * a - 10) * a - 55;
    if (ver >= 7) n -= 36;
  }
  return n;
};
const dataCodewords = (ver: number) =>
  Math.floor(rawModules(ver) / 8) - ECC_PER_BLOCK[ver - 1] * NUM_BLOCKS[ver - 1];

const gfMul = (x: number, y: number) => {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d);
    z ^= ((y >>> i) & 1) * x;
  }
  return z;
};

const rsDivisor = (degree: number) => {
  const r: number[] = new Array(degree).fill(0);
  r[degree - 1] = 1;
  let root = 1;
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < degree; j++) {
      r[j] = gfMul(r[j], root);
      if (j + 1 < degree) r[j] ^= r[j + 1];
    }
    root = gfMul(root, 2);
  }
  return r;
};

const rsRemainder = (data: number[], divisor: number[]) => {
  const r: number[] = new Array(divisor.length).fill(0);
  for (const b of data) {
    const factor = b ^ r.shift()!;
    r.push(0);
    divisor.forEach((c, i) => { r[i] ^= gfMul(c, factor); });
  }
  return r;
};

// Returns the module matrix (true = dark), or null when the text does not fit version 10.
export function qrMatrix(text: string): boolean[][] | null {
  const bytes = Array.from(new TextEncoder().encode(text));
  let ver = 1;
  for (; ver <= 10; ver++) {
    const ccBits = ver < 10 ? 8 : 16;
    if (4 + ccBits + 8 * bytes.length <= dataCodewords(ver) * 8) break;
  }
  if (ver > 10) return null;

  // Data bits.
  const bits: number[] = [];
  const put = (v: number, n: number) => { for (let i = n - 1; i >= 0; i--) bits.push((v >>> i) & 1); };
  put(4, 4);
  put(bytes.length, ver < 10 ? 8 : 16);
  bytes.forEach((b) => put(b, 8));
  const cap = dataCodewords(ver) * 8;
  put(0, Math.min(4, cap - bits.length));
  while (bits.length % 8) bits.push(0);
  for (let pad = 0xec; bits.length < cap; pad ^= 0xec ^ 0x11) put(pad, 8);
  const data: number[] = [];
  for (let i = 0; i < bits.length; i += 8) data.push(parseInt(bits.slice(i, i + 8).join(""), 2));

  // Error correction and interleaving.
  const nb = NUM_BLOCKS[ver - 1];
  const eccLen = ECC_PER_BLOCK[ver - 1];
  const raw = Math.floor(rawModules(ver) / 8);
  const nShort = nb - (raw % nb);
  const shortLen = Math.floor(raw / nb);
  const div = rsDivisor(eccLen);
  const blocks: number[][] = [];
  for (let i = 0, k = 0; i < nb; i++) {
    const dat = data.slice(k, k + shortLen - eccLen + (i < nShort ? 0 : 1));
    k += dat.length;
    const ecc = rsRemainder(dat, div);
    if (i < nShort) dat.push(0);
    blocks.push(dat.concat(ecc));
  }
  const all: number[] = [];
  for (let i = 0; i < blocks[0].length; i++) {
    blocks.forEach((b, j) => { if (i !== shortLen - eccLen || j >= nShort) all.push(b[i]); });
  }

  // Function patterns.
  const size = ver * 4 + 17;
  const mod: boolean[][] = Array.from({ length: size }, () => new Array(size).fill(false));
  const fn: boolean[][] = Array.from({ length: size }, () => new Array(size).fill(false));
  const set = (x: number, y: number, dark: boolean) => {
    if (x < 0 || y < 0 || x >= size || y >= size) return;
    mod[y][x] = dark; fn[y][x] = true;
  };
  for (let i = 0; i < size; i++) { set(6, i, i % 2 === 0); set(i, 6, i % 2 === 0); }
  for (const [cx, cy] of [[3, 3], [size - 4, 3], [3, size - 4]]) {
    for (let dy = -4; dy <= 4; dy++) for (let dx = -4; dx <= 4; dx++) {
      const d = Math.max(Math.abs(dx), Math.abs(dy));
      set(cx + dx, cy + dy, d !== 2 && d !== 4);
    }
  }
  if (ver > 1) {
    const na = Math.floor(ver / 7) + 2;
    const step = Math.ceil((ver * 4 + 4) / (na * 2 - 2)) * 2;
    const pos = [6];
    for (let p = size - 7; pos.length < na; p -= step) pos.splice(1, 0, p);
    for (let i = 0; i < na; i++) for (let j = 0; j < na; j++) {
      if ((i === 0 && j === 0) || (i === 0 && j === na - 1) || (i === na - 1 && j === 0)) continue;
      for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) {
        set(pos[i] + dx, pos[j] + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
      }
    }
  }
  const formatBits = (mask: number) => {
    const d = (0 << 3) | mask; // level M has format bits 00
    let rem = d;
    for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
    return ((d << 10) | rem) ^ 0x5412;
  };
  const drawFormat = (mask: number) => {
    const b = formatBits(mask);
    const bit = (i: number) => ((b >>> i) & 1) !== 0;
    for (let i = 0; i <= 5; i++) set(8, i, bit(i));
    set(8, 7, bit(6)); set(8, 8, bit(7)); set(7, 8, bit(8));
    for (let i = 9; i < 15; i++) set(14 - i, 8, bit(i));
    for (let i = 0; i < 8; i++) set(size - 1 - i, 8, bit(i));
    for (let i = 8; i < 15; i++) set(8, size - 15 + i, bit(i));
    set(8, size - 8, true);
  };
  drawFormat(0);
  if (ver >= 7) {
    let rem = ver;
    for (let i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25);
    const b = (ver << 12) | rem;
    for (let i = 0; i < 18; i++) {
      const dark = ((b >>> i) & 1) !== 0;
      const a = size - 11 + (i % 3);
      const c = Math.floor(i / 3);
      set(a, c, dark); set(c, a, dark);
    }
  }

  // Data placement, then mask 0.
  let idx = 0;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let vert = 0; vert < size; vert++) {
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const up = ((right + 1) & 2) === 0;
        const y = up ? size - 1 - vert : vert;
        if (!fn[y][x] && idx < all.length * 8) {
          mod[y][x] = ((all[idx >>> 3] >>> (7 - (idx & 7))) & 1) !== 0;
          idx++;
        }
      }
    }
  }
  for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) {
    if (!fn[y][x] && (x + y) % 2 === 0) mod[y][x] = !mod[y][x];
  }
  drawFormat(0);
  return mod;
}

export function QR({ text, px = 220 }: { text: string; px?: number }) {
  const m = qrMatrix(text);
  if (!m) return null;
  const q = 4;
  const n = m.length + 2 * q;
  let d = "";
  m.forEach((row, y) => row.forEach((dark, x) => { if (dark) d += `M${x + q},${y + q}h1v1h-1z`; }));
  // A white background and black modules keep the code readable in dark mode.
  return (
    <svg width={px} height={px} viewBox={`0 0 ${n} ${n}`} shapeRendering="crispEdges" role="img" aria-label="QR code">
      <rect width={n} height={n} fill="#fff" />
      <path d={d} fill="#000" />
    </svg>
  );
}
