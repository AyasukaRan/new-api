#!/usr/bin/env python3
"""Bounded integration checks on a new isolated Docker network, never live traffic."""
import http.client
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import uuid

import edge_switch as edge

ROOT = Path(__file__).resolve().parent
LABEL = 'newapi.home.rollout.test'
PREFIX = 'newapi-rollout-test-' + uuid.uuid4().hex[:8]
NETWORK = PREFIX
containers = []
results = []


def cmd(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.PIPE).strip()


def backend(name, version, unhealthy=False, alias=None):
    args = ['docker', 'run', '-d', '--name', name, '--label', LABEL + '=' + PREFIX,
            '--network', NETWORK, '--cpus', '.5', '--memory', '128m', '--pids-limit', '64',
            '--log-opt', 'max-size=2m', '--log-opt', 'max-file=2',
            '-v', str(ROOT / 'fixture.mjs') + ':/fixture.mjs:ro', '-e', 'VERSION=' + version]
    if alias:
        args += ['--network-alias', alias]
    if unhealthy:
        args += ['-e', 'UNHEALTHY=1']
    cmd(*args, 'node:22-alpine', 'node', '/fixture.mjs')
    containers.append(name)
    return json.loads(cmd('docker', 'inspect', name))[0]['NetworkSettings']['Networks'][NETWORK]['IPAddress']


def request(host, port, path, headers=None, body=None):
    c = http.client.HTTPConnection(host, port, timeout=3)
    c.request('POST' if body else 'GET', path, body=body, headers=headers or {})
    r = c.getresponse()
    data = r.read()
    c.close()
    return r.status, data


def stream(host, port):
    c = http.client.HTTPConnection(host, port, timeout=5)
    c.request('GET', '/stream')
    r = c.getresponse()
    assert r.status == 200
    first = r.readline().decode().strip()
    return c, r, first


def run(action, receipt, *extra, expected=0, timeout=8):
    args = ['python3', str(ROOT / 'edge_switch.py'), action, '--config', str(config),
            '--edge', proxy, '--network', NETWORK, '--host', '127.0.0.1', '--port', str(port),
            '--receipt', str(receipt), '--timeout', str(timeout), *extra]
    result = subprocess.run(args, text=True, capture_output=True, timeout=45)
    assert result.returncode == expected, {'action': action, 'stdout': result.stdout, 'error_type': 'command_failed'}
    return json.loads(result.stdout)


with tempfile.TemporaryDirectory(prefix=PREFIX + '-') as temporary:
    temporary = Path(temporary)
    os.chmod(temporary, 0o755)
    config = temporary / 'nginx.conf'
    old, bridge, master, bad, proxy = [PREFIX + '-' + suffix for suffix in ['old', 'bridge', 'master', 'bad', 'edge']]
    try:
        cmd('docker', 'network', 'create', '--label', LABEL + '=' + PREFIX, NETWORK)
        old_ip = backend(old, 'old', alias='new-api')
        bridge_ip = backend(bridge, 'new')
        bad_ip = backend(bad, 'bad', unhealthy=True)
        # This candidate acquires the standard alias only after old is disconnected below.
        for ip, version in [(old_ip, 'old'), (bridge_ip, 'new')]:
            edge.wait_for(lambda ip=ip, version=version: edge.status(ip, 3000, version), 10)
        original = f'''worker_processes 2;
pid /tmp/nginx.pid;
error_log /dev/stderr warn;
events {{ worker_connections 256; }}
http {{
 access_log off;
 resolver 127.0.0.11 valid=1s ipv6=off;
 upstream gateway {{ zone gw 128k; server new-api:3000 resolve; keepalive 16; }}
 server {{ listen 8079; location / {{ proxy_pass http://gateway; proxy_http_version 1.1; proxy_buffering off; proxy_request_buffering off; proxy_set_header Connection ""; proxy_read_timeout 60s; }} }}
}}
'''.encode()
        config.write_bytes(original)
        cmd('docker', 'run', '-d', '--name', proxy, '--label', LABEL + '=' + PREFIX,
            '--network', NETWORK, '--cpus', '.5', '--memory', '64m', '--pids-limit', '64',
            '--log-opt', 'max-size=2m', '--log-opt', 'max-file=2',
            '-p', '127.0.0.1::8079', '-v', str(config) + ':/etc/nginx/nginx.conf:ro',
            'nginx:1.27-alpine', 'nginx', '-g', 'daemon off;')
        containers.append(proxy)
        port = int(cmd('docker', 'port', proxy, '8079/tcp').split(':')[-1])
        edge.wait_for(lambda: edge.status('127.0.0.1', port, 'old'), 10)
        before_workers = edge.workers(proxy)
        failure = run('switch', temporary / 'bad', '--current', old, '--candidate', bad, '--version', 'bad', expected=1, timeout=.4)
        assert config.read_bytes() == original and edge.workers(proxy) == before_workers
        assert edge.status('127.0.0.1', port, 'old') == 'old'
        results.append({'check':'unhealthy_candidate_keeps_original_route_and_workers','passed':True})
        first_c, first_r, first = stream('127.0.0.1', port)
        assert first == 'data: old:start'
        samples = []
        stop = threading.Event()
        def sample():
            while not stop.is_set():
                try:
                    samples.append(request('127.0.0.1', port, '/api/status')[0])
                except Exception:
                    samples.append(0)
                stop.wait(.02)
        watcher = threading.Thread(target=sample)
        watcher.start()
        receipt1 = temporary / 'switch-bridge'
        try:
            switched = run('switch', receipt1, '--current', old, '--candidate', bridge, '--version', 'new')
        finally:
            stop.set(); watcher.join()
        assert samples and set(samples) == {200}, samples
        pending = run('drain', receipt1, timeout=.3)
        assert pending['phase'] == 'draining' and pending['remaining_old_workers'] >= 1
        assert json.loads(cmd('docker','inspect',old))[0]['State']['Running']
        request(old_ip, 3000, '/release')
        assert 'data: old:end' in first_r.read().decode(); first_c.close()
        drained = run('drain', receipt1)
        assert drained['phase'] == 'drained' and drained['remaining_old_workers'] == 0
        results.append({'check':'first_switch_keeps_SSE_and_all_sampled_requests_200','passed':True,
                        'samples':len(samples),'old_workers':switched['old_worker_count'],
                        'new_workers':switched['new_worker_count'],'retained_during_stream':pending['remaining_old_workers']})
        # Inject only dummy secrets; assert none reach logs while HTTP failures remain observable.
        secret = 'DUMMY-DO-NOT-LOG-7391'
        assert request('127.0.0.1',port,'/fail?key='+secret, {'Authorization':'Bearer '+secret,'Cookie':'session='+secret}, secret)[0] == 503
        log = cmd('docker','logs',proxy)
        assert secret not in log
        events = [json.loads(line) for line in log.splitlines() if line.startswith('{')]
        assert any(event.get('status') == 503 for event in events)
        assert all(set(event) <= {'time','request_id','method','status','upstream_status','upstream','seconds','upstream_seconds'} for event in events)
        results.append({'check':'error_JSON_exists_without_query_header_cookie_or_body','passed':True})
        # Rollback while a candidate stream is active: retain candidate and finish that stream.
        second_c, second_r, second = stream('127.0.0.1',port)
        assert second == 'data: new:start'
        other_directory = temporary / 'wrong-config-directory'
        other_directory.mkdir()
        current_config, current_workers = config.read_bytes(), edge.workers(proxy)
        run('rollback',receipt1,'--config',str(other_directory / 'nginx.conf'),expected=1)
        assert config.read_bytes() == current_config and edge.workers(proxy) == current_workers
        assert run('rollback',receipt1)['phase'] == 'rolled_back'
        assert edge.workers(proxy) - current_workers
        assert run('rollback',receipt1)['phase'] == 'rolled_back'
        assert b'error_log /dev/null crit;' in config.read_bytes()
        assert edge.status('127.0.0.1',port,'old') == 'old'
        request(bridge_ip,3000,'/release')
        assert 'data: new:end' in second_r.read().decode(); second_c.close()
        results.append({'check':'rollback_restores_old_without_cutting_candidate_SSE','passed':True})
        # Reproduce crashes around writing or reloading rollback config. The disk
        # hash alone does not prove which upstream the active workers still use.
        for phase, snapshot in [('prepared', 'original'), ('switching', 'before'), ('switching', 'after')]:
            if snapshot == 'original':
                edge.write_bound_file(config, original)
            crash_receipt = temporary / ('crash-' + snapshot)
            run('switch',crash_receipt,'--current',old,'--candidate',bridge,'--version','new')
            crash_c, crash_r, crash_first = stream('127.0.0.1',port)
            assert crash_first == 'data: new:start'
            state_path = crash_receipt / 'state.json'
            interrupted = json.loads(state_path.read_text())
            interrupted['phase'] = phase
            edge.persist_json(state_path, interrupted)
            edge.write_bound_file(config, (crash_receipt / (snapshot + '.conf')).read_bytes())
            assert edge.status('127.0.0.1',port,'new') == 'new'
            interrupted_workers = edge.workers(proxy)
            assert run('rollback',crash_receipt)['phase'] == 'rolled_back'
            assert edge.workers(proxy) - interrupted_workers
            assert edge.status('127.0.0.1',port,'old') == 'old'
            assert edge.workers(proxy) & interrupted_workers
            request(bridge_ip,3000,'/release')
            assert 'data: new:end' in crash_r.read().decode(); crash_c.close()
            results.append({'check':'crash_rollback_reloads_' + phase + '_' + snapshot,
                            'passed':True,'candidate_stream_completed':True})
        # The actual bridge deployment performs two complete switches to the same app version.
        receipt2 = temporary / 'bridge-again'
        run('switch',receipt2,'--current',old,'--candidate',bridge,'--version','new')
        assert run('drain',receipt2)['phase'] == 'drained'
        cmd('docker','stop','-t','5',old)
        assert run('drain',receipt2)['phase'] == 'drained'
        cmd('docker','network','disconnect',NETWORK,old)
        master_ip = backend(master,'new',alias='new-api')
        edge.wait_for(lambda:edge.status(master_ip,3000,'new'),10)
        third_c, third_r, third = stream('127.0.0.1',port)
        assert third == 'data: new:start'
        receipt3 = temporary / 'new-master'
        switched2 = run('switch',receipt3,'--current',bridge,'--candidate',master,'--version','new')
        pending2 = run('drain',receipt3,timeout=.3)
        assert pending2['phase'] == 'draining'
        request(bridge_ip,3000,'/release')
        assert 'data: new:end' in third_r.read().decode(); third_c.close()
        assert run('drain',receipt3)['phase'] == 'drained'
        assert edge.status('127.0.0.1',port,'new') == 'new'
        current_workers = edge.workers(proxy)
        assert run('rollback',receipt3)['phase'] == 'rolled_back'
        assert edge.workers(proxy) - current_workers
        results.append({'check':'second_switch_and_same_version_rollback_confirm_fresh_workers','passed':True,
                        'old_workers':switched2['old_worker_count'],'new_workers':switched2['new_worker_count'],
                        'retained_during_stream':pending2['remaining_old_workers']})
    finally:
        for name in reversed(containers):
            # Name plus ownership label protects unrelated containers on this shared host.
            obj=json.loads(cmd('docker','inspect',name))[0]
            assert obj['Config']['Labels'].get(LABEL)==PREFIX
            cmd('docker','rm','-f',name)
        subprocess.run(['docker','network','rm',NETWORK],capture_output=True)
        leftovers=cmd('docker','ps','-aq','--filter','label='+LABEL+'='+PREFIX)
        assert not leftovers
print(json.dumps({'passed':True,'checks':results,'containers_removed':len(containers),'test_prefix':PREFIX},indent=2))
