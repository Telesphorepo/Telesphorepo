// QR codes for authenticator setup, from qrcode-generator: byte mode with UTF-8, error
// correction level M, the smallest version that fits.
import qrcode from "qrcode-generator";

qrcode.stringToBytes = qrcode.stringToBytesFuncs["UTF-8"];

// Returns the module matrix (true = dark), or null when the text does not fit a QR code.
export function qrMatrix(text: string): boolean[][] | null {
  const qr = qrcode(0, "M");
  try {
    qr.addData(text, "Byte");
    qr.make();
  } catch {
    return null;
  }
  const n = qr.getModuleCount();
  return Array.from({ length: n }, (_, y) => Array.from({ length: n }, (_, x) => qr.isDark(y, x)));
}
