const http = require('http');

const PORT = 8080;
const TARGET_PORT = 8081;

// Map backend session ID to gateway session ID
let currentGatewaySessionId = null;

const server = http.createServer((req, res) => {
  console.log(`[Proxy] Incoming: ${req.method} ${req.url}`);
  let body = [];
  
  req.on('data', chunk => {
    body.push(chunk);
  });
  
  req.on('end', () => {
    const bodyData = Buffer.concat(body);
    
    // Rewrite path to /mcp for Kuadrant broker
    let targetPath = req.url;
    if (req.method === 'GET' && req.url === '/sse') {
      targetPath = '/mcp';
    } else if (req.method === 'POST' && req.url.startsWith('/message')) {
      targetPath = '/mcp';
    }

    const options = {
      hostname: 'localhost',
      port: TARGET_PORT,
      path: targetPath,
      method: req.method,
      headers: { ...req.headers }
    };

    // Remove host header to avoid routing issues
    options.headers['host'] = `localhost:${TARGET_PORT}`;
    
    // Inject Kuadrant's mcp-session-id header for POST requests
    if (req.method === 'POST' && targetPath === '/mcp') {
      if (currentGatewaySessionId) {
        options.headers['mcp-session-id'] = currentGatewaySessionId;
        console.log(`[Proxy] Injected mcp-session-id header: ${currentGatewaySessionId}`);
      } else {
        console.warn(`[Proxy] Warning: No mcp-session-id stored yet!`);
      }
    }
    
    console.log(`[Proxy] Forwarding to: ${options.method} ${options.path}`);
    
    // Forward the request
    const proxyReq = http.request(options, (proxyRes) => {
      // If this is the SSE stream response, extract Kuadrant's session ID
      if (req.method === 'GET' && targetPath === '/mcp') {
        const gwSessionId = proxyRes.headers['mcp-session-id'];
        if (gwSessionId) {
          currentGatewaySessionId = gwSessionId;
          console.log(`[Proxy] Captured mcp-session-id from Kuadrant: ${gwSessionId}`);
        }
      }
      
      res.writeHead(proxyRes.statusCode, proxyRes.headers);
      proxyRes.pipe(res);
    });
    
    proxyReq.on('error', (err) => {
      console.error('[Proxy] Error:', err);
      res.writeHead(502);
      res.end('Bad Gateway');
    });
    
    proxyReq.write(bodyData);
    proxyReq.end();
  });
});

server.listen(PORT, () => {
  console.log(`[Proxy] Listening on ${PORT}, forwarding to ${TARGET_PORT}`);
});
