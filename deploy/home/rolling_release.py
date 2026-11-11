#!/usr/bin/env python3
"""Home app-only rolling release. Default: read-only preflight; --apply resumes a private journal."""
import argparse
import copy
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import urllib.request

PROJECT = Path('/data/new-api-ran')
COMPOSE, ENV = PROJECT / 'compose.yaml', PROJECT / '.env'
EDGE_CONFIG = Path('/data/new-api-ran-edge/target.current.conf')
EDGE_TEMPLATE = Path('/data/new-api-ran-edge/conf/target-active.conf')
APP, BRIDGE = 'new-api-ran-app', 'new-api-ran-bridge'
DB, CACHE, EDGE = 'new-api-ran-postgres', 'new-api-ran-redis', 'new-api-ran-edge110'
NETWORK = 'new-api-ran'
EDGE_TOOL = Path(__file__).with_name('edge_switch.py')
REQUIRED_CHECKS = {'backend', 'standalone', 'typecheck', 'lint', 'frontend-tests', 'frontend-build', 'binary', 'image'}


def check(condition, reason):
    if not condition:
        raise RuntimeError(reason)


def read_json(path):
    return json.loads(path.read_bytes())


def digest(path):
    result = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            result.update(block)
    return result.hexdigest()


def command(*args, input=None, stderr=False, timeout=180):
    result = subprocess.run(args, input=input, text=True, capture_output=True, timeout=timeout)
    check(result.returncode == 0, 'command_failed')
    return (result.stdout + (result.stderr if stderr else '')).strip()


def inspect(name, image=False, optional=False):
    args = ['docker', *(['image', 'inspect'] if image else ['inspect']), name]
    result = subprocess.run(args, capture_output=True, text=True, timeout=30)
    if optional and result.returncode and 'No such' in result.stderr:
        return None
    check(result.returncode == 0, 'inspect_failed')
    return json.loads(result.stdout)[0]


def environment(values):
    if isinstance(values, dict):
        check(all(isinstance(k, str) and isinstance(v, str) for k, v in values.items()), 'invalid_environment')
        return dict(values)
    result = {}
    for entry in values or []:
        key, separator, value = entry.partition('=')
        check(separator and key and key not in result, 'duplicate_or_invalid_environment')
        result[key] = value
    return result


def runtime_fingerprint(info):
    config_keys = ('Env', 'Entrypoint', 'Cmd', 'WorkingDir', 'User', 'ExposedPorts', 'Healthcheck', 'StopSignal')
    host_keys = ('Binds', 'PortBindings', 'RestartPolicy', 'NetworkMode', 'LogConfig', 'NanoCpus',
                 'Memory', 'MemorySwap', 'PidsLimit', 'Init', 'Privileged', 'ReadonlyRootfs',
                 'SecurityOpt', 'CapAdd', 'CapDrop', 'Devices', 'GroupAdd', 'Dns', 'ExtraHosts')
    config = {key: info['Config'].get(key) for key in config_keys}
    config['Env'] = environment(config['Env'])
    host = {key: info['HostConfig'].get(key) for key in host_keys}
    for key in ('Binds', 'SecurityOpt', 'CapAdd', 'CapDrop', 'Devices', 'GroupAdd', 'ExtraHosts'):
        if host[key] is not None:
            host[key] = sorted(host[key], key=lambda value: json.dumps(value, sort_keys=True))
    if host['PortBindings'] is not None:
        host['PortBindings'] = {key: sorted(value, key=lambda item: json.dumps(item, sort_keys=True))
                                if value is not None else None for key, value in host['PortBindings'].items()}
    # DNS priority, command arguments and healthcheck commands are ordered.
    values = {'config': config, 'host': host,
              'mounts': sorted(info['Mounts'], key=lambda value: json.dumps(value, sort_keys=True)),
              'networks': sorted(info['NetworkSettings']['Networks'])}
    return hashlib.sha256(json.dumps(values, sort_keys=True).encode()).hexdigest()


def dependencies():
    result = {}
    for name in (DB, CACHE, EDGE):
        item = inspect(name)
        check(item['State']['Running'], 'dependency_not_running')
        if name != EDGE:
            check(item['State'].get('Health', {}).get('Status') == 'healthy', 'dependency_unhealthy')
        result[name] = {'id': item['Id'], 'image': item['Image'], 'started_at': item['State']['StartedAt'],
                        'runtime': runtime_fingerprint(item)}
    return result


def log_bounds():
    sql = "SELECT json_build_object('min_id',min(id),'max_id',max(id)) FROM logs;"
    return json.loads(command('docker', 'exec', '-i', '-e',
                             'PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=15000',
                             DB, 'sh', '-ec', 'exec psql -XqAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"', input=sql))


def preserve_logs(before, after):
    if before['min_id'] is not None:
        check(after['min_id'] == before['min_id'] and after['max_id'] is not None
              and after['max_id'] >= before['max_id'], 'log_history_regressed')


def private_write(path, content, metadata=None):
    temporary = path.with_name(path.name + '.tmp')
    with temporary.open('wb') as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(content)
        stream.flush()
        os.fsync(stream.fileno())
    if metadata:
        os.chmod(temporary, metadata['mode'])
        os.chown(temporary, metadata['uid'], metadata['gid'])
    os.replace(temporary, path)
    descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def json_bytes(value):
    return (json.dumps(value, indent=2, ensure_ascii=False) + '\n').encode()


def fetch(info, path):
    check(path.startswith('/') and not path.startswith('//'), 'invalid_asset_path')
    address = info['NetworkSettings']['Networks'][NETWORK]['IPAddress']
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open('http://' + address + ':3000' + path, timeout=4) as response:
        return response.read()


def verify_release(path):
    receipt, root = read_json(path), path.parent.parent
    manifest_path = root / 'source-manifest.json'
    manifest = read_json(manifest_path)
    commit = receipt['commit']
    check(receipt['result'] == 'passed' and manifest['commit'] == commit and len(commit) == 40, 'invalid_release')
    check(receipt['version'] == 'ran-' + commit[:9] and receipt['image'] == 'new-api:' + receipt['version']
          and receipt['base_path'] == '/', 'release_identity_mismatch')
    check(digest(manifest_path) == receipt['manifest_sha256']
          and digest(path.parent / 'new-api') == receipt['binary_sha256']
          and digest(root / 'source.tar.gz') == manifest['archive_sha256'], 'artifact_hash_mismatch')
    for name, expected in manifest['files'].items():
        source = (root / 'candidate' / name).resolve()
        check(source.is_relative_to((root / 'candidate').resolve()) and digest(source) == expected, 'source_hash_mismatch')
    for name, expected in receipt['assets'].items():
        asset = (root / 'candidate/web/dist' / name.lstrip('/')).resolve()
        check(asset.is_relative_to((root / 'candidate/web/dist').resolve()) and digest(asset) == expected, 'asset_hash_mismatch')
    check(receipt['entry_assets'] and all(receipt['assets'].get(k) == v for k, v in receipt['entry_assets'].items()), 'entry_asset_mismatch')
    gates = read_json(path.parent / 'gates.json')
    check(gates.get('passed') and gates.get('commit') == commit
          and gates.get('manifest_sha256') == receipt['manifest_sha256'], 'validation_gate_identity_mismatch')
    check(REQUIRED_CHECKS | {'database-matrix', 'startup-matrix'} <= set(gates['checks']), 'missing_validation_gates')
    check(gates['evidence'], 'missing_gate_evidence')
    for evidence in gates['evidence']:
        check(Path(evidence['path']).is_absolute() and digest(Path(evidence['path'])) == evidence['sha256'], 'gate_evidence_changed')
    startup = [read_json(item) for item in root.glob('startup-*/result.json')]
    check(any(item.get('passed') and item.get('commit') == commit and item.get('new_image_id') == receipt['image_id']
              and item.get('old_image_id') == receipt['runtime_base_id'] and item.get('mixed_version_bridge', {}).get('passed')
              and not item.get('remaining_containers') for item in startup), 'missing_startup_matrix')
    target = inspect(receipt['image'], image=True)
    check(target['Id'] == receipt['image_id'] and inspect(receipt['runtime_base'], image=True)['Id'] == receipt['runtime_base_id'], 'image_tag_changed')
    labels = target['Config']['Labels']
    for key, value in {'org.opencontainers.image.revision': commit, 'org.opencontainers.image.version': receipt['version'],
                       'io.new-api.source-manifest-sha256': receipt['manifest_sha256'], 'io.new-api.frontend-base-path': '/'}.items():
        check(labels.get(key) == value, 'image_label_mismatch')
    return receipt


def preflight(release_path, backup_path):
    release = verify_release(release_path)
    original = COMPOSE.read_bytes()
    compose, app, backup = json.loads(original), inspect(APP), read_json(backup_path)
    check(app['Image'] == release['runtime_base_id'] and app['Config']['Image'] == release['runtime_base'], 'base_image_mismatch')
    check(app['State'].get('Health', {}).get('Status') == 'healthy', 'base_unhealthy')
    check(inspect(BRIDGE, optional=True) is None, 'bridge_already_exists')
    service = compose['services']['new-api']
    check(service['image'] == release['runtime_base'] and service['container_name'] == APP, 'unexpected_compose')
    check(not service.get('env_file') and service['networks'] == ['private']
          and compose['networks']['private'] == {'name': NETWORK, 'external': True}, 'unexpected_compose_network_or_env')
    env = environment(app['Config']['Env'])
    check(env.get('NODE_TYPE', 'master') != 'slave' and env.get('NODE_NAME'), 'unexpected_master_role')
    check(not env.get('LOG_SQL_DSN') or env.get('LOG_SQL_DSN') == env.get('SQL_DSN'), 'separate_log_database_needs_guard')
    check(not env.get('UMAMI_WEBSITE_ID') and not env.get('GOOGLE_ANALYTICS_ID'), 'analytics_rendering_needs_review')
    check(backup.get('passed') and backup.get('toc_verified') and backup.get('payload_verified')
          and 0 <= time.time() - backup['finished_epoch'] <= 86400, 'backup_not_fresh_and_verified')
    dump = Path(backup['path'])
    check(dump.stat().st_size == backup['bytes'] and digest(dump) == backup['sha256'], 'backup_hash_mismatch')
    for name in (APP, DB, CACHE):
        actual, saved = inspect(name), backup['containers'][name]
        check(actual['Id'] == saved['id'] and actual['Image'] == saved['image']
              and actual['State']['StartedAt'] == saved['started_at'], 'backup_base_changed')
    # These receipts and snapshots never print credentials or request data.
    state = {'phase': 'prepared', 'release': str(release_path), 'release_sha256': digest(release_path),
             'backup': str(backup_path), 'backup_sha256': digest(backup_path), 'commit': release['commit'],
             'gates_sha256': digest(release_path.parent / 'gates.json'), 'edge_tool_sha256': digest(EDGE_TOOL),
             'old_id': app['Id'], 'old_version': release['runtime_base'].split(':', 1)[1],
             'old_runtime_sha256': runtime_fingerprint(app),
             'compose_sha256': digest(COMPOSE), 'env_sha256': digest(ENV), 'dependencies': dependencies(),
             'logs_before': log_bounds(), 'edges': {}, 'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}
    return release, original, app, state


class Rollout:
    def __init__(self, args, state):
        self.args, self.state = args, state
        self.private = args.state.with_suffix('.private')
        self.release = read_json(args.release)
        self.old = read_json(self.private / 'app.before.json')
        self.original = (self.private / 'compose.before').read_bytes()
        self.final = (self.private / 'compose.final').read_bytes()

    def save(self, phase=None, **values):
        if phase:
            self.state['phase'] = phase
        self.state.update(values)
        private_write(self.args.state, json_bytes(self.state))

    def guard(self):
        check(digest(self.args.release) == self.state['release_sha256']
              and digest(self.args.backup) == self.state['backup_sha256'], 'receipt_changed')
        check(digest(self.args.release.parent / 'gates.json') == self.state['gates_sha256']
              and digest(EDGE_TOOL) == self.state['edge_tool_sha256'], 'validation_or_edge_tool_changed')
        check(hashlib.sha256(self.original).hexdigest() == self.state['compose_sha256']
              and runtime_fingerprint(self.old) == self.state['old_runtime_sha256'], 'baseline_snapshot_changed')
        check(digest(self.private / 'compose.final') == self.state['final_sha256']
              and digest(self.private / 'compose.bridge') == self.state['bridge_sha256'], 'candidate_snapshot_changed')
        check(digest(ENV) == self.state['env_sha256'], 'environment_file_changed')
        check(COMPOSE.read_bytes() in (self.original, self.final), 'compose_changed_externally')
        check(dependencies() == self.state['dependencies'], 'dependency_changed')
        for tag, identity in ((self.release['image'], self.release['image_id']),
                              (self.release['runtime_base'], self.release['runtime_base_id'])):
            check(inspect(tag, image=True)['Id'] == identity, 'image_tag_changed')
        preserve_logs(self.state['logs_before'], log_bounds())

    def compose_up(self, bridge=False):
        path = self.private / 'compose.bridge' if bridge else COMPOSE
        service = 'new-api-bridge' if bridge else 'new-api'
        command('docker', 'compose', '--project-directory', str(PROJECT), '-p', json.loads(self.original)['name'],
                '-f', str(path), 'up', '-d', '--no-deps', '--no-build', '--pull', 'never', service)

    def replace_compose(self, content):
        check(COMPOSE.read_bytes() in (self.original, self.final), 'compose_changed_externally')
        private_write(COMPOSE, content, self.state['compose_metadata'])

    def ready(self, name, old=False):
        expected = copy.deepcopy(self.old)
        values = environment(expected['Config']['Env'])
        if not old:
            values['AUTH_CACHE_LEGACY_FENCE_KEYS'] = 'true'
        if name == BRIDGE:
            values.update(NODE_TYPE='slave', NODE_NAME=values['NODE_NAME'] + '-bridge')
            expected['HostConfig']['PortBindings'] = {}
        expected['Config']['Env'] = values
        identity = self.release['runtime_base_id'] if old else self.release['image_id']
        version = self.state['old_version'] if old else self.release['version']
        deadline = time.monotonic() + 90
        while True:
            item = inspect(name)
            check(item['Image'] == identity, 'application_image_mismatch')
            if name == BRIDGE:
                check('new-api' not in (item['NetworkSettings']['Networks'][NETWORK].get('Aliases') or []), 'bridge_owns_master_alias')
            check(runtime_fingerprint(item) == runtime_fingerprint(expected), 'application_configuration_changed')
            try:
                status = json.loads(fetch(item, '/api/status'))
                if item['State'].get('Health', {}).get('Status') == 'healthy' and status.get('success') and status['data']['version'] == version:
                    break
            except (OSError, ValueError, KeyError):
                pass
            check(time.monotonic() < deadline, 'application_readiness_timeout')
            time.sleep(.5)
        if not old:
            raw_index = (self.args.release.parent.parent / 'candidate/web/dist/index.html').read_bytes()
            check(hashlib.sha256(raw_index).hexdigest() == self.release['index_sha256'], 'index_artifact_changed')
            expected_index = raw_index.replace(b'<!--umami-->\n', b'<!--Umami QuantumNous-->\n').replace(
                b'<!--Google Analytics-->\n', b'<!--Google Analytics QuantumNous-->\n')
            check(fetch(item, '/usage-logs') == expected_index, 'served_index_mismatch')
            for path, expected_hash in self.release['entry_assets'].items():
                check(hashlib.sha256(fetch(item, path)).hexdigest() == expected_hash, 'served_asset_mismatch')

    def edge(self, action, receipt, **kwargs):
        args = [sys.executable, str(EDGE_TOOL), action, '--receipt', str(receipt), '--timeout', str(self.args.drain_timeout)]
        for key, value in kwargs.items():
            args.extend(['--' + key, value])
        # Switch can include readiness, reload, route verification and an automatic rollback.
        timeout = self.args.drain_timeout + 60 if action == 'drain' else 300 + 6 * self.args.drain_timeout
        return json.loads(command(*args, timeout=timeout))

    def switch(self, key, current, candidate, version):
        receipts = self.state['edges'].setdefault(key, [])
        if receipts:
            path = self.private / receipts[-1]
            edge_state = read_json(path / 'state.json') if (path / 'state.json').exists() else None
            if edge_state and edge_state['phase'] in ('switched', 'drained'):
                check(digest(EDGE_CONFIG) == edge_state['after_sha256'], 'edge_configuration_changed')
                return
            if edge_state and edge_state['phase'] in ('prepared', 'switching'):
                self.edge('rollback', path)
            elif edge_state:
                check(edge_state['phase'] == 'rolled_back', 'unknown_edge_phase')
            else:
                check(not path.exists(), 'incomplete_edge_receipt')
        path = self.private / (key + '-' + str(len(receipts) + 1))
        receipts.append(path.name)
        self.save()
        self.edge('switch', path, current=current, candidate=candidate, version=version)

    def drained(self, key):
        path = self.private / self.state['edges'][key][-1]
        return self.edge('drain', path)['phase'] == 'drained'

    def require_route(self, key, target, old=False, victim=None):
        receipt = read_json(self.private / self.state['edges'][key][-1] / 'state.json')
        check(receipt['phase'] == 'drained' and digest(EDGE_CONFIG) == receipt['after_sha256']
              and receipt['candidate'] == target and inspect(target)['Id'] == receipt['candidate_id'], 'serving_route_changed')
        if victim:
            check(inspect(victim)['Id'] == receipt['current_id'], 'drained_application_replaced')
        self.ready(target, old=old)

    def stop(self, name, unserved=False):
        item = inspect(name)
        running = item['State']['Running']
        if running:
            command('docker', 'stop', '-t', '150', name, timeout=165)
        item = inspect(name)
        # An unserved candidate may have exited during startup. Never accept a killed or OOM process.
        permitted_exit = item['State']['ExitCode'] == 0 or (unserved and not running and item['State']['ExitCode'] in (1, 2))
        check(not item['State']['Running'] and permitted_exit and not item['State']['OOMKilled'], 'application_not_cleanly_stopped')
        logs = command('docker', 'logs', '--since', self.state['started_at'], name, stderr=True).lower()
        check(not any(text in logs for text in ('pending quota updates did not finish', 'request trace writes did not finish',
                                                'failed to persist performance metrics during shutdown',
                                                'server forced to shutdown', '服务未能正常关闭，已强制退出')), 'application_writes_not_drained')
        after = log_bounds()
        preserve_logs(self.state.get('logs_drained', self.state['logs_before']), after)
        self.save(logs_drained=after)

    def finish(self, phase):
        self.guard()
        self.require_route('to-main' if phase == 'complete' else 'restore-route', APP, old=phase == 'rolled_back')
        check(COMPOSE.read_bytes() == (self.final if phase == 'complete' else self.original), 'final_compose_mismatch')
        current = EDGE_CONFIG.read_bytes()
        check((APP + ':3000').encode() in current, 'final_edge_target_mismatch')
        check(EDGE_TEMPLATE.read_bytes() in ((self.private / 'edge-template.before').read_bytes(), current), 'edge_template_changed')
        private_write(EDGE_TEMPLATE, current, self.state['template_metadata'])
        self.save(phase, finished_at=datetime.datetime.now(datetime.timezone.utc).isoformat())

    def cleanup_bridge(self):
        phase = self.state['phase']
        check(phase in ('complete', 'rolled_back'), 'release_not_complete')
        key = 'to-main' if phase == 'complete' else 'restore-route'
        self.require_route(key, APP, old=phase == 'rolled_back')
        item = inspect(BRIDGE, optional=True)
        if item is not None:
            receipt = read_json(self.private / self.state['edges'][key][-1] / 'state.json')
            check(item['Id'] == receipt['current_id'] and item['Image'] == self.release['image_id']
                  and item['Config'].get('Labels', {}).get('io.new-api.rollout') == str(self.args.state), 'unowned_bridge')
            check(not item['State']['Running'] and item['State']['ExitCode'] == 0
                  and not item['State']['OOMKilled'], 'bridge_not_cleanly_stopped')
            command('docker', 'rm', item['Id'])
            check(inspect(BRIDGE, optional=True) is None, 'bridge_cleanup_incomplete')
        self.save(bridge_removed=True)

    def run(self):
        self.guard()
        while True:
            phase = self.state['phase']
            if phase in ('complete', 'rolled_back'):
                # Completion is durable before removal; resume also finishes interrupted cleanup.
                self.cleanup_bridge()
                return phase
            if phase == 'prepared':
                item = inspect(BRIDGE, optional=True)
                if item:
                    check(item['Config'].get('Labels', {}).get('io.new-api.rollout') == str(self.args.state), 'unowned_bridge')
                self.compose_up(bridge=True)
                self.ready(BRIDGE)
                self.save('bridge_ready')
            elif phase == 'bridge_ready':
                self.switch('to-bridge', APP, BRIDGE, self.release['version'])
                self.save('drain_old')
            elif phase == 'drain_old':
                if not self.drained('to-bridge'):
                    return 'pending'
                self.save('stop_old')
            elif phase == 'stop_old':
                self.guard()
                self.require_route('to-bridge', BRIDGE)
                check(inspect(APP)['Id'] == self.state['old_id'], 'old_application_replaced')
                self.stop(APP)
                self.save('start_main')
            elif phase == 'start_main':
                self.guard()
                self.require_route('to-bridge', BRIDGE)
                try:
                    item = inspect(APP, optional=True)
                    check(not item or not item['State']['Running'] or item['Image'] == self.release['image_id'], 'old_master_still_running')
                    self.replace_compose(self.final)
                    self.compose_up()
                    self.ready(APP)
                    self.save('main_ready')
                except Exception as error:
                    # B remains the ingress target throughout restoration of A.
                    self.save('restore_old', failure_type=type(error).__name__,
                              failure_code=str(error) if type(error) is RuntimeError else None)
            elif phase == 'main_ready':
                self.guard()
                self.ready(APP)
                self.switch('to-main', BRIDGE, APP, self.release['version'])
                self.save('drain_bridge')
            elif phase == 'drain_bridge':
                if not self.drained('to-main'):
                    return 'pending'
                self.save('stop_bridge')
            elif phase == 'stop_bridge':
                self.require_route('to-main', APP, victim=BRIDGE)
                self.stop(BRIDGE)
                self.finish('complete')
            elif phase == 'restore_old':
                self.require_route('to-bridge', BRIDGE)
                item = inspect(APP, optional=True)
                if item and item['Image'] != self.release['runtime_base_id']:
                    check(item['Image'] == self.release['image_id'], 'unknown_master_image')
                    self.stop(APP, unserved=True)
                self.replace_compose(self.original)
                self.compose_up()
                self.ready(APP, old=True)
                self.save('old_restored')
            elif phase == 'old_restored':
                self.switch('restore-route', BRIDGE, APP, self.state['old_version'])
                self.save('drain_rollback_bridge')
            elif phase == 'drain_rollback_bridge':
                if not self.drained('restore-route'):
                    return 'pending'
                self.save('stop_rollback_bridge')
            elif phase == 'stop_rollback_bridge':
                self.require_route('restore-route', APP, old=True, victim=BRIDGE)
                self.stop(BRIDGE)
                self.finish('rolled_back')
            else:
                raise RuntimeError('unknown_journal_phase')


def initialize(args):
    release, original, app, state = preflight(args.release, args.backup)
    private = args.state.with_suffix('.private')
    private.mkdir(mode=0o700)
    for name, content in {'compose.before': original, 'env.before': ENV.read_bytes(), 'app.before.json': json_bytes(app),
                           'edge-template.before': EDGE_TEMPLATE.read_bytes()}.items():
        private_write(private / name, content)
    for name, path in (('compose_metadata', COMPOSE), ('template_metadata', EDGE_TEMPLATE)):
        metadata = path.stat()
        state[name] = {'mode': metadata.st_mode & 0o777, 'uid': metadata.st_uid, 'gid': metadata.st_gid}
    final = json.loads(original)
    service = final['services']['new-api']
    values = environment(service['environment'])
    values['AUTH_CACHE_LEGACY_FENCE_KEYS'] = 'true'
    service.update(image=release['image'], environment=values)
    bridge = copy.deepcopy(final)
    service = bridge['services']['new-api']
    service.update(container_name=BRIDGE, ports=[], depends_on={})
    service['environment'].update(NODE_TYPE='slave', NODE_NAME=environment(app['Config']['Env'])['NODE_NAME'] + '-bridge')
    labels = service.setdefault('labels', {})
    check(isinstance(labels, dict), 'unexpected_compose_labels')
    labels['io.new-api.rollout'] = str(args.state)
    bridge['services'] = {'new-api-bridge': service}
    private_write(private / 'compose.final', json_bytes(final))
    private_write(private / 'compose.bridge', json_bytes(bridge))
    state.update(final_sha256=digest(private / 'compose.final'), bridge_sha256=digest(private / 'compose.bridge'))
    for name in ('compose.final', 'compose.bridge'):
        command('docker', 'compose', '--project-directory', str(PROJECT), '-f', str(private / name), 'config', '-q')
    private_write(args.state, json_bytes(state))
    return state


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--release', type=Path, required=True)
    parser.add_argument('--backup', type=Path, required=True)
    parser.add_argument('--state', type=Path, required=True)
    parser.add_argument('--drain-timeout', type=float, default=20)
    args = parser.parse_args(argv)
    args.release, args.backup, args.state = args.release.resolve(), args.backup.resolve(), args.state.resolve()
    os.umask(0o077)
    try:
        check(0 < args.drain_timeout <= 30, 'invalid_drain_timeout')
        if not args.apply:
            if args.state.exists():
                state = read_json(args.state)
                Rollout(args, state).guard()
                phase = state['phase']
            else:
                _, _, _, state = preflight(args.release, args.backup)
                phase = 'preflight_passed'
            print(json.dumps({'phase': phase, 'deployment_performed': False}))
            return 0
        args.state.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        with (PROJECT / '.rolling-release.lock').open('a') as lock:
            os.fchmod(lock.fileno(), 0o600)
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            state = read_json(args.state) if args.state.exists() else initialize(args)
            result = Rollout(args, state).run()
        print(json.dumps({'phase': state['phase'], 'result': result, 'state': str(args.state)}))
        return 1 if result == 'rolled_back' else 0
    except Exception as error:
        # RuntimeError messages are fixed internal codes; other exception strings may contain private data.
        print(json.dumps({'result': 'failed', 'error_type': type(error).__name__,
                          'error_code': str(error) if type(error) is RuntimeError else None, 'state': str(args.state)}))
        return 1


if __name__ == '__main__':
    sys.exit(main())
