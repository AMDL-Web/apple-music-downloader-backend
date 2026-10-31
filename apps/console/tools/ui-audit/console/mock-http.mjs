// The mock server's HTTP helpers: JSON answers, the error envelope, request
// bodies and Apple Music links.

export function json(res, status, body) {
  const raw = JSON.stringify(body);
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  res.end(raw);
}

export function apiError(res, status, code, message, details) {
  json(res, status, { error: { code, message, ...(details ? { details } : {}) } });
}

export function readBody(req) {
  return new Promise((resolve) => {
    let s = '';
    req.on('data', (c) => (s += c));
    req.on('end', () => {
      try {
        resolve(s ? JSON.parse(s) : null);
      } catch {
        resolve(null);
      }
    });
  });
}

export function parseLink(url) {
  const m = String(url || '').match(/music\.apple\.com\/([a-z]{2})\/(album|song|playlist|artist|station|music-video)\/(?:[^/?#]*\/)?([A-Za-z0-9.-]+)(?:\?i=(\d+))?/);
  if (!m) return null;
  return { storefront: m[1], type: m[2], id: m[3], track_id: m[4] };
}
