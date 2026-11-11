#!/usr/bin/env python3
"""Warm-probe a named app, gracefully switch nginx, and retain rollback capacity.

Application creation, database migration and master-task ownership are separate
release gates. This tool never stops or removes either application container.
"""
import argparse
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import uuid

SAFE_LOG = '''# newapi-rollout-safe-log begin
 map $status $newapi_error { ~^[45] 1; default 0; }
 log_format newapi_error escape=json '{"time":"$time_iso8601","request_id":"$request_id","method":"$request_method","status":$status,"upstream_status":"$upstream_status","upstream":"$upstream_addr","seconds":$request_time,"upstream_seconds":"$upstream_response_time"}';
 access_log /dev/stdout newapi_error if=$newapi_error;
# newapi-rollout-safe-log end'''
NAME = re.compile(r'^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$')
UPSTREAM = re.compile(r'(upstream\s+gateway\s*\{[^{}]*?\bserver\s+)([a-zA-Z0-9_.-]+):3000(\s+resolve\s*;)')


def command(*args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=30)
    if result.returncode:
        raise RuntimeError('command_failed')
    return result.stdout.strip()


def inspect(name):
    if not NAME.fullmatch(name):
        raise ValueError('invalid_container_name')
    info = json.loads(command('docker', 'inspect', name))[0]
    if not info['State']['Running']:
        raise RuntimeError('container_not_running')
    return info


def status(host, port, expected=None):
    conn = http.client.HTTPConnection(host, port, timeout=2)
    try:
        conn.request('GET', '/api/status', headers={'Connection': 'close'})
        response = conn.getresponse()
        if response.status != 200:
            raise RuntimeError('unhealthy_status')
        body = json.loads(response.read(1048576))
        version = body.get('data', {}).get('version')
        if body.get('success') is not True or not isinstance(version, str):
            raise RuntimeError('invalid_status')
        if expected is not None and version != expected:
            raise RuntimeError('unexpected_version')
        return version
    finally:
        conn.close()


def wait_for(probe, timeout):
    deadline = time.monotonic() + timeout
    while True:
        try:
            result = probe()
            if result:
                return result
        except (OSError, ValueError, RuntimeError, http.client.HTTPException):
            pass
        if time.monotonic() >= deadline:
            raise TimeoutError('probe_timeout')
        time.sleep(.1)


def workers(edge):
    lines = command('docker', 'top', edge, '-eo', 'pid,args').splitlines()
    return {int(line.split()[0]) for line in lines if 'nginx: worker process' in line}


def digest(blob):
    return hashlib.sha256(blob).hexdigest()


def render(config, candidate):
    if not NAME.fullmatch(candidate):
        raise ValueError('invalid_candidate')
    text = config.decode()
    if len(UPSTREAM.findall(text)) != 1:
        raise RuntimeError('unexpected_upstream_configuration')
    if re.search(r'worker_shutdown_timeout\s+(?!0\s*;)', text):
        raise RuntimeError('finite_worker_shutdown_timeout')
    text = UPSTREAM.sub(lambda match: match[1] + candidate + ':3000' + match[3], text)
    text, count = re.subn(r'^\s*error_log\s+[^;]+;', 'error_log /dev/null crit;', text, flags=re.M)
    if count != 1:
        raise RuntimeError('unexpected_error_log_configuration')
    if '# newapi-rollout-safe-log begin' not in text:
        text, count = re.subn(r'^\s*access_log\s+off;', SAFE_LOG, text, flags=re.M)
        if count != 1:
            raise RuntimeError('unexpected_access_log_configuration')
    return text.encode()


def persist_json(path, data):
    temporary = path.with_suffix('.tmp')
    with temporary.open('w') as file:
        json.dump(data, file, indent=2)
        file.write('\n')
        file.flush()
        os.fsync(file.fileno())
    os.replace(temporary, path)


def write_bound_file(path, blob):
    # Keep the inode because nginx sees a file bind mount, not its directory.
    with path.open('r+b') as file:
        file.seek(0)
        file.write(blob)
        file.truncate()
        file.flush()
        os.fsync(file.fileno())


def validate(edge, path):
    temporary = '/tmp/newapi-rollout-' + uuid.uuid4().hex + '.conf'
    command('docker', 'cp', str(path), edge + ':' + temporary)
    try:
        command('docker', 'exec', edge, 'nginx', '-t', '-c', temporary)
    finally:
        command('docker', 'exec', edge, 'rm', '-f', temporary)


def switch(args):
    if args.receipt.exists():
        raise RuntimeError('receipt_already_exists')
    edge, old, candidate = inspect(args.edge), inspect(args.current), inspect(args.candidate)
    if old['Id'] == candidate['Id']:
        raise RuntimeError('same_application_container')
    for info in (edge, old, candidate):
        if args.network not in info['NetworkSettings']['Networks']:
            raise RuntimeError('container_network_mismatch')
    network = candidate['NetworkSettings']['Networks'][args.network]
    original = args.config.read_bytes()
    match = UPSTREAM.search(original.decode())
    aliases = old['NetworkSettings']['Networks'][args.network].get('Aliases') or []
    if match and match[2] in (network.get('Aliases') or []):
        raise RuntimeError('candidate_owns_live_service_alias')
    if match is None or match[2] not in [args.current, *aliases]:
        raise RuntimeError('current_container_does_not_own_upstream')
    expected_mount = next((item for item in edge['Mounts'] if item['Destination'] == '/etc/nginx/nginx.conf'), None)
    if expected_mount is None or Path(expected_mount['Source']) != args.config.resolve():
        raise RuntimeError('nginx_config_mount_mismatch')
    old_ip = old['NetworkSettings']['Networks'][args.network]['IPAddress']
    current_version = status(old_ip, 3000)
    status(args.host, args.port, current_version)
    wait_for(lambda: status(network['IPAddress'], 3000, args.version), args.timeout)
    proposed = render(original, args.candidate)
    rollback_config = render(original, match[2])
    args.receipt.mkdir(mode=0o700, parents=True)
    before, after = args.receipt / 'before.conf', args.receipt / 'after.conf'
    (args.receipt / 'original.conf').write_bytes(original)
    before.write_bytes(rollback_config)
    after.write_bytes(proposed)
    state = {'edge': args.edge, 'config': str(args.config.resolve()), 'current': args.current,
             'candidate': args.candidate, 'current_id': old['Id'], 'candidate_id': candidate['Id'],
             'current_version': current_version, 'version': args.version, 'host': args.host, 'port': args.port,
             'network': args.network, 'original_sha256': digest(original),
             'before_sha256': digest(rollback_config), 'after_sha256': digest(proposed), 'phase': 'prepared'}
    persist_json(args.receipt / 'state.json', state)
    validate(args.edge, after)
    if args.config.read_bytes() != original:
        raise RuntimeError('configuration_changed_during_preflight')
    old_workers = workers(args.edge)
    if not old_workers:
        raise RuntimeError('no_nginx_workers')
    state['old_workers'] = sorted(old_workers)
    state['phase'] = 'switching'
    persist_json(args.receipt / 'state.json', state)
    try:
        write_bound_file(args.config, proposed)
        command('docker', 'kill', '--signal', 'HUP', args.edge)
        state['new_workers'] = sorted(wait_for(lambda: workers(args.edge) - old_workers, args.timeout))
        wait_for(lambda: status(args.host, args.port, args.version), args.timeout)
    except BaseException:
        rollback_workers = workers(args.edge)
        write_bound_file(args.config, rollback_config)
        command('docker', 'kill', '--signal', 'HUP', args.edge)
        wait_for(lambda: workers(args.edge) - rollback_workers, args.timeout)
        wait_for(lambda: status(args.host, args.port, current_version), args.timeout)
        state['phase'] = 'rolled_back'
        persist_json(args.receipt / 'state.json', state)
        raise
    state['phase'] = 'switched'
    persist_json(args.receipt / 'state.json', state)
    return {'phase': 'switched', 'old_application_retained': True,
            'old_worker_count': len(old_workers), 'new_worker_count': len(state['new_workers'])}


def follow_up(args):
    state = json.loads((args.receipt / 'state.json').read_text())
    config = Path(state['config'])
    if args.config.resolve() != config.resolve():
        raise RuntimeError('receipt_config_mismatch')
    config_hash = digest(config.read_bytes())
    if args.action == 'rollback' and state['phase'] == 'rolled_back':
        if config_hash != state['before_sha256']:
            raise RuntimeError('rollback_configuration_changed')
        if inspect(state['current'])['Id'] != state['current_id']:
            raise RuntimeError('rollback_container_changed')
        status(state['host'], state['port'], state['current_version'])
        return {'phase': 'rolled_back', 'both_applications_retained': True}
    if config_hash != state['after_sha256']:
        raise RuntimeError('active_configuration_changed')
    if args.action == 'drain':
        if state['phase'] == 'drained':
            return {'phase': 'drained', 'old_application_retained': True, 'remaining_old_workers': 0}
        try:
            wait_for(lambda: not (workers(state['edge']) & set(state['old_workers'])), args.timeout)
        except TimeoutError:
            return {'phase': 'draining', 'old_application_retained': True,
                    'remaining_old_workers': len(workers(state['edge']) & set(state['old_workers']))}
        state['phase'] = 'drained'
        persist_json(args.receipt / 'state.json', state)
        return {'phase': 'drained', 'old_application_retained': True, 'remaining_old_workers': 0}
    old = inspect(state['current'])
    if old['Id'] != state['current_id']:
        raise RuntimeError('rollback_container_changed')
    old_ip = old['NetworkSettings']['Networks'][state['network']]['IPAddress']
    status(old_ip, 3000, state['current_version'])
    original = (args.receipt / 'before.conf').read_bytes()
    if digest(original) != state['before_sha256']:
        raise RuntimeError('rollback_configuration_changed')
    validate(state['edge'], args.receipt / 'before.conf')
    rollback_workers = workers(state['edge'])
    write_bound_file(config, original)
    command('docker', 'kill', '--signal', 'HUP', state['edge'])
    wait_for(lambda: workers(state['edge']) - rollback_workers, args.timeout)
    wait_for(lambda: status(state['host'], state['port'], state['current_version']), args.timeout)
    state['phase'] = 'rolled_back'
    persist_json(args.receipt / 'state.json', state)
    return {'phase': 'rolled_back', 'both_applications_retained': True}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['switch', 'drain', 'rollback'])
    parser.add_argument('--receipt', type=Path, required=True)
    parser.add_argument('--edge', default='new-api-ran-edge110')
    parser.add_argument('--config', type=Path, default=Path('/data/new-api-ran-edge/target.current.conf'))
    parser.add_argument('--network', default='new-api-ran')
    parser.add_argument('--current', default='new-api-ran-app')
    parser.add_argument('--candidate')
    parser.add_argument('--version')
    parser.add_argument('--host', default='172.16.0.110')
    parser.add_argument('--port', type=int, default=8079)
    parser.add_argument('--timeout', type=float, default=20)
    args = parser.parse_args()
    if args.action == 'switch' and (not args.candidate or not args.version):
        parser.error('switch requires --candidate and --version')
    os.umask(0o077)
    with (args.config.parent / 'entry.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            result = switch(args) if args.action == 'switch' else follow_up(args)
        except BaseException as error:
            # Never emit command output, HTTP data, headers, query strings or secrets.
            print(json.dumps({'action': args.action, 'result': 'failed', 'error_type': type(error).__name__}), flush=True)
            return 1
    print(json.dumps(result), flush=True)
    return 0


if __name__ == '__main__':
    sys.exit(main())
