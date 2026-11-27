#!/usr/bin/env python3
"""US2 PostgreSQL blue/green release. Read-only by default; --apply resumes the journal."""
import argparse
import copy
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import socket
import time
import urllib.parse

import edge_switch
from rolling_release import check, command, environment, inspect, json_bytes, private_write, read_json

PROJECT = Path('/data/new-api-us2')
CADDY = 'new-api-us2-caddy'
LEGACY = 'new-api-us2-app'
DOMAIN = 'us2.llmapi.ayanamiran.com'
ADMIN = 2019
PORTS = {'blue': 3000, 'green': 3001}
CONFIG = PROJECT / 'caddy/Caddyfile'
APP_COMPOSE = PROJECT / 'compose.app.yaml'
ROLLOUT = PROJECT / 'caddy/config/rollout'
ACTIVE = ROLLOUT / 'active-blue'
REGISTRY = PROJECT / 'rollout/active.json'
WRITE_FAILURES = ('pending quota updates did not finish', 'request trace writes did not finish',
                  'failed to persist performance metrics during shutdown',
                  'server forced to shutdown', '服务未能正常关闭，已强制退出')


def caddy_config():
    # One matcher per request: changing the marker cannot make both slots miss.
    return f'''{{
    admin 127.0.0.1:{ADMIN}
}}
{DOMAIN} {{
    @blue file {{
        root /config/rollout
        try_files active-blue
    }}
    handle @blue {{
        reverse_proxy 127.0.0.1:{PORTS['blue']} {{
            header_up X-Forwarded-For {{remote_host}}
            header_up X-Forwarded-Proto {{scheme}}
            header_up X-Real-IP {{remote_host}}
        }}
    }}
    handle {{
        reverse_proxy 127.0.0.1:{PORTS['green']} {{
            header_up X-Forwarded-For {{remote_host}}
            header_up X-Forwarded-Proto {{scheme}}
            header_up X-Real-IP {{remote_host}}
        }}
    }}
}}
'''.encode()


def caddy_api(path):
    connection = http.client.HTTPConnection('127.0.0.1', ADMIN, timeout=5)
    try:
        connection.request('GET', path)
        response = connection.getresponse()
        check(response.status == 200, 'caddy_api_unavailable')
        return json.loads(response.read())
    finally:
        connection.close()


def load_caddy(content):
    edge_switch.write_bound_file(CONFIG, content)
    command('docker', 'exec', CADDY, 'caddy', 'validate', '--config', '/etc/caddy/Caddyfile', '--adapter', 'caddyfile')
    command('docker', 'exec', CADDY, 'caddy', 'reload', '--config', '/etc/caddy/Caddyfile', '--adapter', 'caddyfile')


def maintenance_config():
    return f'{{\n admin 127.0.0.1:{ADMIN}\n}}\n{DOMAIN} {{\n respond "Starting updated gateway" 503\n}}\n'.encode()


def route(slot):
    check(slot in PORTS and ROLLOUT.is_dir() and not ROLLOUT.is_symlink(), 'invalid_route')
    if slot == 'blue':
        private_write(ACTIVE, b'blue\n')
    else:
        ACTIVE.unlink(missing_ok=True)
        descriptor = os.open(ROLLOUT, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)


def current_slot():
    check(not ACTIVE.is_symlink(), 'unsafe_route_marker')
    return 'blue' if ACTIVE.exists() else 'green'


def validate_runtime(item):
    values = environment(item['Config']['Env'])
    check(values.get('SATELLITE_ROLE') == 'satellite' and values.get('SATELLITE_NODE_ID') == 'ran-us2'
          and values.get('NODE_NAME') == 'ran-us2' and values.get('HOST') == '127.0.0.1'
          and values.get('BATCH_UPDATE_ENABLED') == 'false', 'unsupported_satellite_runtime')
    dsn = urllib.parse.urlsplit(values.get('SQL_DSN', ''))
    check(dsn.scheme in ('postgres', 'postgresql') and dsn.hostname in ('127.0.0.1', 'localhost')
          and bool(dsn.path.strip('/')), 'postgresql_local_database_required')
    check(item['HostConfig']['NetworkMode'] == 'host' and not item['HostConfig'].get('AutoRemove')
          and not item['HostConfig'].get('Privileged') and not item['Config'].get('Cmd'),
          'unsupported_container_configuration')
    check(any(m['Destination'] == '/data' and m['RW'] for m in item['Mounts']), 'shared_data_mount_required')
    check(values.get('PORT') in tuple(map(str, PORTS.values())), 'unsupported_application_port')
    return values


def candidate_config(old, image, slot, journal):
    values = validate_runtime(old)
    values['PORT'] = str(PORTS[slot])
    values['SATELLITE_DIRECT_UPSTREAM'] = 'true'
    config = {key: copy.deepcopy(old['Config'][key]) for key in (
        'User', 'WorkingDir', 'Entrypoint', 'Cmd', 'Volumes', 'StopSignal', 'StopTimeout')
        if key in old['Config']}
    config.update(Image=image, Env=[key + '=' + value for key, value in sorted(values.items())],
                  Labels={'io.new-api.satellite-rollout': str(journal)},
                  Healthcheck={'Test': ['CMD', 'wget', '-q', '-T', '3', '-O', '/dev/null',
                                        f'http://127.0.0.1:{PORTS[slot]}/api/status'],
                               'Interval': 10000000000, 'Timeout': 4000000000,
                               'StartPeriod': 30000000000, 'Retries': 12},
                  HostConfig=copy.deepcopy(old['HostConfig']))
    return config


def application_connections(item, port):
    if not item['State']['Running']:
        return 0
    pid = item['State']['Pid']
    check(isinstance(pid, int) and pid > 0, 'application_process_unavailable')
    pending = 0
    for protocol in ('tcp', 'tcp6'):
        lines = Path(f'/proc/{pid}/net/{protocol}').read_text().splitlines()
        check(lines and 'local_address' in lines[0], 'application_connections_unreadable')
        for line in lines[1:]:
            fields = line.split()
            check(len(fields) >= 10 and re.fullmatch(r'[0-9A-Fa-f]+:[0-9A-Fa-f]{4}', fields[1]),
                  'application_connections_unreadable')
            if int(fields[1].rsplit(':', 1)[1], 16) == port and fields[3] in ('01', '08'):
                pending += 1
    after = inspect(item['Id'])
    check(after['State']['Running'] and after['State']['Pid'] == pid, 'application_process_changed')
    return pending


class SatelliteRollout:
    def __init__(self, args):
        self.args = args
        self.state = read_json(args.state)
        self.private = args.state.with_suffix('.private')

    def save(self, phase=None, **values):
        if phase:
            self.state['phase'] = phase
        self.state.update(values)
        private_write(self.args.state, json_bytes(self.state))

    def item(self, target):
        item = inspect(target['name'])
        check(item['Id'] == target['id'] and item['Image'] == target['image_id'], 'application_identity_changed')
        return item

    def record_active(self, target):
        original = (self.private / 'compose.before.json').read_bytes()
        compose = json.loads(original)
        service = compose['services']['app']
        service.update(image=target['image_id'], container_name=target['name'])
        values = environment(service.get('environment', {}))
        values['PORT'] = str(PORTS[target['slot']])
        if target['id'] == self.state['new'].get('id'):
            values['SATELLITE_DIRECT_UPSTREAM'] = 'true'
        service['environment'] = values
        service['healthcheck']['test'] = ['CMD', 'wget', '-q', '-T', '3', '-O', '/dev/null',
                                        f'http://127.0.0.1:{PORTS[target["slot"]]}/api/status']
        private_write(APP_COMPOSE, json_bytes(compose))
        private_write(REGISTRY, json_bytes(dict(target, sync_lock=True)))

    def ready(self, target):
        item = self.item(target)
        check(item['State']['Running'], 'application_not_running')
        expected = read_json(self.private / 'app.before.json')
        values = validate_runtime(item)
        before = validate_runtime(expected)
        before['PORT'] = str(PORTS[target['slot']])
        if target['id'] == self.state['new'].get('id'):
            before['SATELLITE_DIRECT_UPSTREAM'] = 'true'
        check(values == before and item['HostConfig']['Binds'] == expected['HostConfig']['Binds'],
              'application_configuration_changed')
        edge_switch.status('127.0.0.1', PORTS[target['slot']], target['version'])
        return True

    def drained(self, target):
        item = self.item(target)
        port = PORTS[target['slot']]
        upstreams = caddy_api('/reverse_proxy/upstreams')
        check(isinstance(upstreams, list), 'invalid_upstream_metrics')
        pending = 0
        for upstream in upstreams:
            check(isinstance(upstream.get('num_requests'), int) and upstream['num_requests'] >= 0,
                  'invalid_upstream_metrics')
            if upstream.get('address') == f'127.0.0.1:{port}':
                pending += upstream['num_requests']
        connections = application_connections(item, port)
        now = time.time()
        quiet = self.state.get('quiet_since') if self.state.get('quiet_target') == target['id'] else None
        quiet = (quiet or now) if not pending and not connections else None
        self.save(pending_requests=pending, pending_connections=connections,
                  quiet_since=quiet, quiet_target=target['id'])
        return quiet is not None and now - quiet >= 2

    def terminate(self, target):
        item = self.item(target)
        if item['State']['Running']:
            # docker stop has an implicit SIGKILL deadline. Never use it here.
            signalled = self.state.get('term_target') == target['id']
            if not signalled:
                check(self.drained(target), 'application_not_drained')
                command('docker', 'kill', '--signal=TERM', target['name'])
                self.save(term_target=target['id'])
            return False
        check(not item['State']['OOMKilled'] and item['State']['ExitCode'] == 0, 'application_not_cleanly_stopped')
        logs = command('docker', 'logs', '--since', str(self.state['started_at']), target['name'], stderr=True).lower()
        check(not any(value in logs for value in WRITE_FAILURES), 'application_writes_not_drained')
        # An operator can start the retained container explicitly for rollback;
        # a host reboot must not resurrect the inactive sync worker.
        command('docker', 'update', '--restart=no', target['name'])
        return True

    def create(self):
        target = self.state['new']
        existing = inspect(target['name'], optional=True)
        if existing is None:
            with socket.socket() as probe:
                probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                probe.bind(('127.0.0.1', PORTS[target['slot']]))
            config = candidate_config(read_json(self.private / 'app.before.json'), target['image_id'],
                                      target['slot'], self.args.state)
            response = command('curl', '--fail', '--silent', '--show-error', '--unix-socket', '/var/run/docker.sock',
                               '-H', 'Content-Type: application/json', '--data-binary', '@-',
                               'http://localhost/containers/create?name=' + target['name'], input=json.dumps(config))
            target['id'] = json.loads(response)['Id']
            self.save()
        else:
            check(existing['Config'].get('Labels', {}).get('io.new-api.satellite-rollout') == str(self.args.state)
                  and existing['Image'] == target['image_id'], 'candidate_name_occupied')
            check(target.get('id') in (None, existing['Id']), 'candidate_identity_changed')
            target['id'] = existing['Id']
            self.save()
        if not self.item(target)['State']['Running']:
            command('docker', 'start', target['name'])
        deadline = time.monotonic() + self.args.ready_timeout
        while True:
            try:
                self.ready(target)
                return
            except (OSError, RuntimeError, ValueError, http.client.HTTPException):
                check(time.monotonic() < deadline, 'candidate_readiness_timeout')
                time.sleep(.5)

    def guard(self):
        check(self.state['commit'] == self.args.commit and self.state['image'] == self.args.image
              and self.state['new']['version'] == self.args.version, 'release_arguments_changed')
        check(inspect(CADDY)['Id'] == self.state['caddy_id'], 'caddy_container_changed')
        check(inspect(self.args.image, image=True)['Id'] == self.state['new']['image_id'], 'image_tag_changed')
        if self.state['phase'] not in ('prepared', 'bootstrap_maintenance', 'bootstrap_stop', 'bootstrap_start', 'bootstrap_config', 'bootstrap_rollback') and not (
                self.state['bootstrap'] and self.state['phase'] == 'rolled_back'):
            check(CONFIG.read_bytes() == caddy_config(), 'caddy_configuration_changed')

    def advance(self):
        self.guard()
        old, new = self.state['old'], self.state['new']
        phase = self.state['phase']
        if phase == 'prepared':
            if self.state['bootstrap']:
                check(CONFIG.read_bytes() == (self.private / 'Caddyfile.before').read_bytes(), 'caddy_configuration_changed')
                if not self.drained(old):
                    return False
                self.save('bootstrap_maintenance', bootstrap_gap_started=time.time())
            else:
                check(current_slot() == old['slot'], 'serving_route_changed')
                self.ready(old)
                self.save('start')
            phase = self.state['phase']
        if phase == 'bootstrap_maintenance':
            # The legacy Caddy configuration cannot switch without a reload.
            # Bootstrap is explicitly a maintenance window, never a hot rollout.
            if not self.drained(old):
                return False
            load_caddy(maintenance_config())
            self.save('bootstrap_stop', quiet_since=None)
            phase = 'bootstrap_stop'
        if phase == 'bootstrap_stop':
            if self.item(old)['State']['Running'] and self.state.get('term_target') != old['id']:
                if not self.drained(old):
                    return False
            if not self.terminate(old):
                return False
            self.save('bootstrap_start')
            phase = 'bootstrap_start'
        if phase in ('start', 'bootstrap_start'):
            self.create()
            self.save('bootstrap_config' if self.state['bootstrap'] else 'switch')
            phase = self.state['phase']
        if phase == 'bootstrap_config':
            self.ready(new)
            check(not self.item(old)['State']['Running'], 'legacy_sync_worker_still_running')
            ROLLOUT.mkdir(mode=0o700, parents=True, exist_ok=True)
            route('blue')
            load_caddy(caddy_config())
            self.record_active(new)
            self.save('complete', bootstrap_gap_seconds=time.time() - self.state['bootstrap_gap_started'])
            return True
        if phase == 'switch':
            self.ready(new)
            check(current_slot() in (old['slot'], new['slot']), 'serving_route_changed')
            route(new['slot'])
            self.record_active(new)
            self.save('drain')
            phase = 'drain'
        if phase == 'drain':
            check(current_slot() == new['slot'], 'serving_route_changed')
            self.ready(new)
            if self.item(old)['State']['Running'] and not self.state.get('term_target') == old['id']:
                if not self.drained(old):
                    return False
            if not self.terminate(old):
                return False
            self.save('complete')
        return self.state['phase'] == 'complete'

    def rollback(self):
        self.guard()
        old, new = self.state['old'], self.state['new']
        if self.state['phase'] == 'rolled_back':
            return True
        if self.state['bootstrap']:
            if new.get('id'):
                if self.item(new)['State']['Running']:
                    if not self.drained(new):
                        return False
                    if self.state['phase'] != 'bootstrap_rollback':
                        load_caddy(maintenance_config())
                        self.save('bootstrap_rollback', quiet_since=None, term_target=None)
                        return False
                    if not self.terminate(new):
                        return False
                stopped = self.item(new)
                check(not stopped['State']['Running'] and not stopped['State']['OOMKilled']
                      and stopped['State']['ExitCode'] in (0, 1, 2), 'candidate_exit_requires_review')
                command('docker', 'update', '--restart=no', new['name'])
            policy = read_json(self.private / 'app.before.json')['HostConfig']['RestartPolicy']
            command('docker', 'update', '--restart=' + policy['Name'], old['name'])
            if not self.item(old)['State']['Running']:
                command('docker', 'start', old['name'])
            edge_switch.wait_for(lambda: self.ready(old), self.args.ready_timeout)
            load_caddy((self.private / 'Caddyfile.before').read_bytes())
            private_write(APP_COMPOSE, (self.private / 'compose.before.json').read_bytes())
            REGISTRY.unlink(missing_ok=True)
            self.save('rolled_back')
            return True
        if self.state['phase'] != 'rollback_drain':
            # Preserve the original restart policy when putting the old image back.
            policy = read_json(self.private / 'app.before.json')['HostConfig']['RestartPolicy']
            command('docker', 'update', '--restart=' + policy['Name'], old['name'])
            if not self.item(old)['State']['Running']:
                command('docker', 'start', old['name'])
            edge_switch.wait_for(lambda: self.ready(old), self.args.ready_timeout)
            route(old['slot'])
            self.record_active(old)
            self.save('rollback_drain', term_target=None, quiet_since=None)
        self.ready(old)
        if not new.get('id'):
            self.save('rolled_back')
            return True
        if self.item(new)['State']['Running'] and not self.state.get('term_target') == new['id']:
            if not self.drained(new):
                return False
        if not self.terminate(new):
            return False
        self.save('rolled_back')
        return True


def prepare(args):
    check(re.fullmatch(r'[0-9a-f]{40}', args.commit) and re.fullmatch(r'ran-[0-9a-f]{9}', args.version)
          and args.version == 'ran-' + args.commit[:9], 'invalid_release_identity')
    image = inspect(args.image, image=True)
    labels = image['Config'].get('Labels', {})
    check(labels.get('org.opencontainers.image.revision') == args.commit
          and labels.get('org.opencontainers.image.version') == args.version, 'image_identity_mismatch')
    check(labels.get('io.new-api.satellite-sync-lock') == 'postgres-v1', 'image_missing_satellite_sync_lock')
    check(inspect(CADDY)['State']['Running'], 'caddy_not_running')
    if args.bootstrap:
        check(not REGISTRY.exists(), 'bootstrap_already_completed')
        item = inspect(LEGACY)
        slot = 'blue'
    else:
        old = read_json(REGISTRY)
        check(old.get('sync_lock') is True and current_slot() == old['slot'], 'managed_sync_lock_release_required')
        item = inspect(old['name'])
        check(item['Id'] == old['id'] and item['Image'] == old['image_id'], 'active_application_changed')
        check(inspect(item['Image'], image=True)['Config'].get('Labels', {}).get('io.new-api.satellite-sync-lock')
              == 'postgres-v1', 'active_image_missing_satellite_sync_lock')
        check(CONFIG.read_bytes() == caddy_config(), 'caddy_configuration_changed')
        slot = 'green' if old['slot'] == 'blue' else 'blue'
    values = validate_runtime(item)
    old_slot = next(key for key, port in PORTS.items() if str(port) == values['PORT'])
    connection = http.client.HTTPConnection('127.0.0.1', PORTS[old_slot], timeout=5)
    try:
        connection.request('GET', '/api/status', headers={'Connection': 'close'})
        response = connection.getresponse()
        payload = json.loads(response.read())
        check(response.status == 200 and payload.get('success'), 'active_application_unhealthy')
    finally:
        connection.close()
    old = {'name': item['Name'].lstrip('/'), 'id': item['Id'], 'image_id': item['Image'],
           'slot': old_slot, 'version': payload['data']['version']}
    state = {'phase': 'prepared', 'started_at': int(time.time()), 'bootstrap': args.bootstrap,
             'image': args.image, 'commit': args.commit, 'caddy_id': inspect(CADDY)['Id'], 'old': old,
             'new': {'name': f'new-api-us2-{slot}-{args.commit[:9]}-' + hashlib.sha256(str(args.state).encode()).hexdigest()[:8], 'slot': slot,
                     'image_id': image['Id'], 'version': args.version}}
    if args.apply:
        private = args.state.with_suffix('.private')
        private.mkdir(mode=0o700)
        private_write(private / 'app.before.json', json_bytes(item))
        private_write(private / 'Caddyfile.before', CONFIG.read_bytes())
        private_write(private / 'compose.before.json', APP_COMPOSE.read_bytes())
        private_write(args.state, json_bytes(state))
    return state


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--commit', required=True)
    parser.add_argument('--version', required=True)
    parser.add_argument('--state', type=Path, required=True)
    parser.add_argument('--bootstrap', action='store_true', help='One-time legacy upgrade with a startup gap; never overlaps old sync workers')
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--rollback', action='store_true')
    parser.add_argument('--timeout', type=int, default=30)
    parser.add_argument('--ready-timeout', type=int, default=120)
    args = parser.parse_args()
    check(os.geteuid() == 0 and args.state.is_absolute(), 'root_and_absolute_journal_required')
    check(not args.rollback or args.apply, 'rollback_requires_apply')
    if not args.apply:
        state = prepare(args)
        print(json.dumps({'result': 'preflight_passed', 'bootstrap': state['bootstrap'],
                          'old': state['old']['version'], 'new': state['new']['version']}))
        return
    REGISTRY.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    args.state.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with (REGISTRY.parent / 'release.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if not args.state.exists():
            check(not args.rollback, 'rollback_journal_missing')
            prepare(args)
        flow = SatelliteRollout(args)
        deadline = time.monotonic() + args.timeout
        while True:
            complete = flow.rollback() if args.rollback else flow.advance()
            if complete or time.monotonic() >= deadline:
                print(json.dumps({'result': 'complete' if complete else 'pending', 'phase': flow.state['phase'],
                                  'pending_requests': flow.state.get('pending_requests', 0),
                                  'pending_connections': flow.state.get('pending_connections', 0),
                                  'bootstrap_gap_seconds': flow.state.get('bootstrap_gap_seconds')}))
                return
            time.sleep(1)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, RuntimeError, http.client.HTTPException) as error:
        # Configuration, request bodies and tokens never enter release output.
        print(json.dumps({'result': 'failed', 'error': str(error) if isinstance(error, RuntimeError)
                          else type(error).__name__}))
        raise SystemExit(1)
