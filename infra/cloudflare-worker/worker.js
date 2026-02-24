/**
 * Roost Stream Relay — Cloudflare Worker
 *
 * Sits at stream.yourflock.org. Validates subscriber JWT tokens and proxies
 * HLS stream requests to the Roost backend.
 *
 * URL shape:  /stream/{token}/{...path}
 * Forwarded:  {ROOST_BACKEND_URL}/stream/{...path}
 *             Authorization: Bearer {token}
 *
 * Privacy rules (zero-logging policy):
 *   - Never log: token value, subscriber ID, IP address, User-Agent
 *   - Safe to log: path length, HTTP status, CF datacenter (colo)
 */

const CORS_HEADERS = {
  'Access-Control-Allow-Origin': '*',
  'Access-Control-Allow-Methods': 'GET, HEAD, OPTIONS',
  'Access-Control-Allow-Headers': 'Range, Accept, Accept-Encoding',
  'Access-Control-Expose-Headers': 'Content-Length, Content-Range',
  'Access-Control-Max-Age': '86400',
};

/**
 * Validate that a string is structurally a JWT.
 * Three dot-separated segments, each a non-empty base64url string.
 * Does NOT verify the signature — that is the backend's responsibility.
 *
 * @param {string} candidate
 * @returns {boolean}
 */
function isStructurallyValidJwt(candidate) {
  if (typeof candidate !== 'string') return false;

  const parts = candidate.split('.');
  if (parts.length !== 3) return false;

  // Each segment must be non-empty and contain only base64url characters.
  const base64urlPattern = /^[A-Za-z0-9_-]+$/;
  return parts.every((part) => part.length > 0 && base64urlPattern.test(part));
}

/**
 * Build a safe log object — no PII, no token, no IP.
 *
 * @param {Request} request
 * @param {number} status
 * @param {string} reason
 * @returns {object}
 */
function safeLog(request, status, reason) {
  return {
    status,
    reason,
    pathLen: new URL(request.url).pathname.length,
    method: request.method,
    colo: request.cf ? request.cf.colo : 'unknown',
  };
}

/**
 * Return a JSON error response.
 *
 * @param {number} status
 * @param {string} message
 * @returns {Response}
 */
function errorResponse(status, message) {
  return new Response(JSON.stringify({ error: message }), {
    status,
    headers: {
      'Content-Type': 'application/json',
      'Cache-Control': 'no-store',
      ...CORS_HEADERS,
    },
  });
}

export default {
  /**
   * @param {Request} request
   * @param {object} env
   * @param {ExecutionContext} ctx
   * @returns {Promise<Response>}
   */
  async fetch(request, env, ctx) {
    // --- CORS preflight ---
    if (request.method === 'OPTIONS') {
      return new Response(null, {
        status: 204,
        headers: CORS_HEADERS,
      });
    }

    // --- Method guard ---
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return errorResponse(405, 'Method not allowed');
    }

    const url = new URL(request.url);
    const pathname = url.pathname; // e.g. /stream/{token}/{...path}

    // --- Route guard ---
    // Must match /stream/{token}/... exactly.
    const streamPrefix = '/stream/';
    if (!pathname.startsWith(streamPrefix)) {
      return errorResponse(404, 'Not found');
    }

    // Strip the leading /stream/ and split into [token, ...rest]
    const afterPrefix = pathname.slice(streamPrefix.length); // "{token}/{...path}"
    const slashIdx = afterPrefix.indexOf('/');

    // Token is everything before the first slash (or the whole string if no slash).
    const token = slashIdx === -1 ? afterPrefix : afterPrefix.slice(0, slashIdx);

    // Remaining path is everything after the token segment.
    // If there is no slash, the stream path is empty (root of stream namespace).
    const streamPath = slashIdx === -1 ? '' : afterPrefix.slice(slashIdx); // starts with /

    // --- Token presence check ---
    if (!token) {
      console.log(JSON.stringify(safeLog(request, 401, 'missing_token')));
      return errorResponse(401, 'Missing token');
    }

    // --- Token format check (synchronous, no crypto) ---
    if (!isStructurallyValidJwt(token)) {
      console.log(JSON.stringify(safeLog(request, 401, 'malformed_token')));
      return errorResponse(401, 'Invalid token');
    }

    // --- Build backend URL (token stripped from URL) ---
    const backendBase = (env.ROOST_BACKEND_URL || 'https://roost.yourflock.org').replace(/\/$/, '');

    // Forward the original query string (e.g. ?_HLS_msn=, ?start=) unchanged.
    const backendUrl = `${backendBase}/stream${streamPath}${url.search}`;

    // --- Build forwarded request headers ---
    // Forward only safe, relevant headers. Drop identifying headers.
    const forwardedHeaders = new Headers();
    forwardedHeaders.set('Authorization', `Bearer ${token}`);

    // Range is critical for HLS byte-range requests.
    const range = request.headers.get('Range');
    if (range) {
      forwardedHeaders.set('Range', range);
    }

    // Accept and Accept-Encoding help the backend choose the right encoding.
    const accept = request.headers.get('Accept');
    if (accept) {
      forwardedHeaders.set('Accept', accept);
    }

    const acceptEncoding = request.headers.get('Accept-Encoding');
    if (acceptEncoding) {
      forwardedHeaders.set('Accept-Encoding', acceptEncoding);
    }

    // Mark the request as coming through the relay so the backend can trust
    // the Authorization header. Use a shared secret from the environment if set.
    if (env.RELAY_SECRET) {
      forwardedHeaders.set('X-Relay-Secret', env.RELAY_SECRET);
    }

    // --- Proxy to backend ---
    let backendResponse;
    try {
      backendResponse = await fetch(backendUrl, {
        method: request.method,
        headers: forwardedHeaders,
        // Do not follow redirects — pass them back to the client.
        redirect: 'manual',
      });
    } catch (err) {
      // Network error reaching the backend (DNS failure, connection refused, etc.)
      console.log(JSON.stringify({
        ...safeLog(request, 502, 'backend_unreachable'),
        errorType: err.name,
      }));
      return errorResponse(502, 'Stream backend unavailable');
    }

    // Log the outcome (safe fields only).
    console.log(JSON.stringify(safeLog(request, backendResponse.status, 'proxied')));

    // --- Build client response ---
    // Copy backend response headers, stripping any that would expose origin info.
    const responseHeaders = new Headers(backendResponse.headers);

    // Hard privacy requirement: never let the real origin URL leak to the client.
    responseHeaders.delete('Location'); // redirects would expose origin
    responseHeaders.delete('X-Powered-By');
    responseHeaders.delete('Server');
    responseHeaders.delete('Via');

    // HLS stream segments and playlists must never be cached by intermediaries.
    responseHeaders.set('Cache-Control', 'no-store');

    // Attach CORS headers to every response.
    for (const [key, value] of Object.entries(CORS_HEADERS)) {
      responseHeaders.set(key, value);
    }

    return new Response(backendResponse.body, {
      status: backendResponse.status,
      statusText: backendResponse.statusText,
      headers: responseHeaders,
    });
  },
};
