import { connect } from "cloudflare:sockets";

const AUTH_SECRET = "d4TkphgXAsFk-Ij7APvyw3BPjOhQMDZcO9ngJ4o11wY";
const CHUNK_SIZE = 0x8000;

function encodeBase64(bytes) {
  let binary = "";

  for (let offset = 0; offset < bytes.length; offset += CHUNK_SIZE) {
    const chunk = bytes.subarray(offset, offset + CHUNK_SIZE);
    binary += String.fromCharCode(...chunk);
  }

  return btoa(binary);
}

function decodeBase64(value) {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);

  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index);
  }

  return bytes;
}

export default {
  async fetch(request, env, ctx) {
    const upgradeHeader = request.headers.get("Upgrade");
    if (!upgradeHeader || upgradeHeader.toLowerCase() !== "websocket") {
      return new Response("not a ws request", { status: 400 });
    }

    const authSecret = env.AUTH_SECRET || AUTH_SECRET;
    if (request.headers.get("Authorization") !== authSecret) {
      return new Response("unauthorized", { status: 403 });
    }

    const [client, server] = Object.values(new WebSocketPair());
    server.accept();

    const sockets = new Map();

    server.addEventListener("message", async (event) => {
      try {
        if (typeof event.data !== "string") {
          server.send(JSON.stringify({ type: "error", message: "invalid message type" }));
          return;
        }

        const msg = JSON.parse(event.data);
        const { type, id, data, target } = msg;

        if (type === "open") {
          if (typeof target !== "string" || !target) {
            server.send(JSON.stringify({ type: "close", id }));
            return;
          }

          try {
            const tcpSocket = connect(target);
            const writer = tcpSocket.writable.getWriter();
            const reader = tcpSocket.readable.getReader();
            
            sockets.set(id, { tcpSocket, writer, reader });

            ctx.waitUntil((async () => {
              try {
                while (true) {
                  const { value, done } = await reader.read();
                  if (done) break;
                  
                  server.send(JSON.stringify({
                    type: "data",
                    id,
                    data: encodeBase64(value)
                  }));
                }
              } catch (err) {
              } finally {
                server.send(JSON.stringify({ type: "close", id: id }));
                sockets.delete(id);
                try { reader.releaseLock(); } catch (e) {}
                try { writer.close(); } catch(e) {}
                try { tcpSocket.close(); } catch (e) {}
              }
            })());
          } catch (err) {
            server.send(JSON.stringify({ type: "close", id }));
          }
        } 

        else if (type === "data") {
          const socketEntry = sockets.get(id);
          if (socketEntry && typeof data === "string") {
            await socketEntry.writer.write(decodeBase64(data));
          }
        } 

        else if (type === "close") {
          const socketEntry = sockets.get(id);
          if (socketEntry) {
            sockets.delete(id);
            try { socketEntry.reader.cancel(); } catch (e) {}
            try { socketEntry.reader.releaseLock(); } catch (e) {}
            try { await socketEntry.writer.close(); } catch (e) {}
            try { socketEntry.tcpSocket.close(); } catch (e) {}
          }
        }
      } catch (err) {
      }
    });

    server.addEventListener("close", async () => {
      for (const [_, socketEntry] of sockets) {
        try { socketEntry.reader.cancel(); } catch (e) {}
        try { socketEntry.reader.releaseLock(); } catch (e) {}
        try { await socketEntry.writer.close(); } catch(e) {}
        try { socketEntry.tcpSocket.close(); } catch (e) {}
      }
      sockets.clear();
    });

    return new Response(null, { status: 101, webSocket: client });
  }
};
