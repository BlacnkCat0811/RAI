#!/usr/bin/env python3
"""Loopback-only synthetic UI fault proxy; private config never enters evidence.

Run with the private acceptance directory containing config.json/config-app.json.
It forwards to the real isolated fnOS API and can lose one successful POST ACK.
No payloads, headers, credentials or health values are printed or saved.
"""
import http.server
import json
import pathlib
import sys
import threading
import urllib.error
import urllib.request

private = pathlib.Path(sys.argv[1])
config = json.loads((private / 'config.json').read_text())
upstream = config['baseURL']
token = config['token']
lock = threading.Lock()
drop_path = None


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        self.forward()

    def do_POST(self):
        self.forward()

    def do_PUT(self):
        self.forward()

    def forward(self):
        global drop_path
        authorization = self.headers.get('Authorization', '')
        if authorization != 'Bearer ' + token:
            self.send_error(401)
            return
        body = self.rfile.read(int(self.headers.get('Content-Length', '0')))
        if self.path == '/__maintenance/drop-next' and self.command == 'POST':
            path = json.loads(body)['path']
            assert path == '/v2/training/plans' or (path.startswith('/v2/training/plans/') and path.endswith('/overrides'))
            with lock:
                drop_path = path
            status, headers, data = 200, {}, b'{}'
        else:
            request_headers = {'Authorization': authorization, 'Content-Type': self.headers.get('Content-Type', 'application/json')}
            if self.headers.get('Content-Encoding'):
                request_headers['Content-Encoding'] = self.headers['Content-Encoding']
            request = urllib.request.Request(upstream + self.path, data=body if self.command in ('POST', 'PUT') else None, method=self.command,
                                             headers=request_headers)
            try:
                response = urllib.request.urlopen(request, timeout=30)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                status, headers, data = response.code, response.headers, response.read()
            with lock:
                if self.command == 'POST' and self.path == drop_path and status == 200:
                    drop_path = None
                    status, data = 503, b'{"code":"synthetic_response_lost"}'
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        if headers.get('Retry-After'):
            self.send_header('Retry-After', headers['Retry-After'])
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)


server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
config['baseURL'] = 'http://127.0.0.1:' + str(server.server_port)
for name, value in [('config.json', config), ('config-app.json', {'configuration': {'baseURL': config['baseURL'], 'collectorEpoch': config['collectorEpoch']}, 'token': token})]:
    path = private / name
    path.write_text(json.dumps(value))
    path.chmod(0o600)
print('MAINTENANCE_UI_FAULT_PROXY=READY', flush=True)
server.serve_forever()
