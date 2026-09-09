import json, sys, http.server

rules = json.load(open(sys.argv[2]))
log = sys.argv[3]
requests = sys.argv[4]


class H(http.server.BaseHTTPRequestHandler):
    def _handle(self):
        n = int(self.headers.get('Content-Length') or 0)
        payload = self.rfile.read(n).decode('utf-8', 'replace') if n else ''
        with open(log, 'a') as f:
            f.write(f"{self.command} {self.path}\n{self.headers}\n{payload}\n")
        with open(requests, 'a') as f:
            f.write(json.dumps({'method': self.command, 'path': self.path, 'body': payload}) + "\n")
        hit = None
        for rule in rules:
            if rule.get('method', self.command) != self.command:
                continue
            if 'path' in rule and rule['path'] != self.path:
                continue
            if 'path_contains' in rule and rule['path_contains'] not in self.path:
                continue
            if 'times' in rule:
                if rule['times'] <= 0:
                    continue
                rule['times'] -= 1
            hit = rule
            break
        if hit is None:
            hit = {'status': 404,
                   'body': {'error': {'code': 'not_found',
                                      'message': 'no rule for ' + self.command + ' ' + self.path}}}
        body = hit.get('body', {})
        raw = (body if isinstance(body, str) else json.dumps(body)).encode()
        self.send_response(hit.get('status', 200))
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    do_GET = do_POST = do_PUT = _handle

    def log_message(self, *a):
        pass


http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
