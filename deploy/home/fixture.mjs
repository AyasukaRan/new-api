import http from 'node:http'
const streams = new Set()
const version = process.env.VERSION
http.createServer((req, res) => {
  const path = req.url.split('?')[0]
  if (path === '/stream') {
    res.writeHead(200, {'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache'})
    res.write(`data: ${version}:start\n\n`)
    streams.add(res)
    res.on('close', () => streams.delete(res))
    return
  }
  if (path === '/release') {
    for (const stream of streams) stream.end(`data: ${version}:end\n\n`)
    res.end('released')
    return
  }
  if (path === '/fail' || process.env.UNHEALTHY === '1') {
    res.writeHead(503, {'Content-Type':'application/json'})
    res.end('{"success":false}')
    return
  }
  res.writeHead(200, {'Content-Type':'application/json'})
  res.end(JSON.stringify({success:true, data:{version}}))
}).listen(3000, '0.0.0.0')
