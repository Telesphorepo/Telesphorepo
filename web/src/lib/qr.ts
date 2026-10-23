// QR codes for authenticator setup, from the qrcode package: error correction level M, a quiet
// zone of 4 modules, and every module a whole number of pixels so a phone camera reads each
// module the same size.
import QRCode from "qrcode";

const MODULE_PX = 5;

/** The QR code of text as an SVG string and its width in pixels. */
export async function qrSVG(text: string): Promise<{ svg: string; px: number }> {
  const modules = QRCode.create(text, { errorCorrectionLevel: "M" }).modules.size;
  const px = (modules + 8) * MODULE_PX;
  const svg = await QRCode.toString(text, { type: "svg", errorCorrectionLevel: "M", margin: 4, width: px, color: { dark: "#000000", light: "#ffffff" } });
  return { svg, px };
}
