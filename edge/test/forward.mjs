// The S3 path of the edge zone (e2e/edge_harness_test.go). wrangler dev answers a request under
// /cdn-cgi/ only when its Host is localhost, 127.0.0.1 or [::1] (validateCdnCgiRequest of the
// Miniflare entry Worker; any other Host gets a bare 403), and SigV4 covers the Host. So the server
// signs for 127.0.0.1:8787, and this forwarder, in the network namespace of the server container,
// passes each TCP connection to the Worker container at TARGET (<alias>:8787) unchanged.
import net from "node:net";

const target = process.env.TARGET ?? "";
const colon = target.lastIndexOf(":");
const host = target.slice(0, colon);
const port = Number(target.slice(colon + 1));
if (colon <= 0 || !Number.isInteger(port)) {
  console.error(`set TARGET to <host>:<port>, not "${target}"`);
  process.exit(2);
}

net
  .createServer((client) => {
    const upstream = net.connect(port, host);
    client.pipe(upstream);
    upstream.pipe(client);
    client.on("error", () => upstream.destroy());
    upstream.on("error", () => client.destroy());
  })
  .listen(8787, "127.0.0.1", () => console.log(`forwarding 127.0.0.1:8787 to ${target}`));
