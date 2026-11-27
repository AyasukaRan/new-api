"""Deterministic release control-flow checks; no Docker or production access."""
import argparse
import contextlib
import copy
import fcntl
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import rolling_release as release


def app(image, identity, running=True):
    return {'Id': identity, 'Image': image, 'Config': {'Labels': {}, 'Env': ['A=one', 'B=two=three'],
            'Cmd': ['run', '--serve'], 'Healthcheck': {'Test': ['CMD', 'probe']}},
            'HostConfig': {'Binds': ['b:/b', 'a:/a'], 'PortBindings': {'3000/tcp': [
                {'HostIp': '127.0.0.1', 'HostPort': '18081'}, {'HostIp': '::1', 'HostPort': '18081'}]}},
            'Mounts': [{'Source': 'b', 'Destination': '/b', 'RW': True}, {'Source': 'a', 'Destination': '/a', 'RW': True}],
            'NetworkSettings': {'Networks': {release.NETWORK: {}}},
            'State': {'Running': running, 'ExitCode': 0, 'OOMKilled': False}}


class FakeRollout(release.Rollout):
    def __init__(self):
        self.args = argparse.Namespace(state=Path('/private/release.json'))
        self.state = {'phase': 'prepared', 'old_id': 'old-container', 'old_version': 'old', 'edges': {}}
        self.release = {'image_id': 'new', 'runtime_base_id': 'old', 'version': 'new'}
        self.original, self.final, self.compose = b'old', b'new', b'old'
        self.containers = {release.APP: app('old', 'old-container')}
        self.events, self.pending = [], set()
        self.route = release.APP
        self.fail_bridge = self.fail_main = self.fail_restore = self.fail_stop = False

    def inspect(self, name, optional=False):
        item = self.containers.get(name)
        if not optional and item is None:
            raise RuntimeError('missing_container')
        return item

    def guard(self):
        self.events.append('guard')

    def save(self, phase=None, **values):
        if phase:
            self.state['phase'] = phase
        self.state.update(values)

    def ready(self, name, old=False):
        self.events.append('ready:' + name + (':old' if old else ''))
        if name == release.BRIDGE and self.fail_bridge:
            raise RuntimeError('bridge_failed')
        if name == release.APP and not old and self.fail_main:
            raise RuntimeError('master_failed')
        if old and self.fail_restore:
            raise RuntimeError('restore_failed')
        assert self.containers[name]['State']['Running']

    def compose_up(self, bridge=False):
        name = release.BRIDGE if bridge else release.APP
        image = 'new' if bridge or self.compose == self.final else 'old'
        self.containers[name] = app(image, image + '-container')
        self.events.append('start:' + name + ':' + image)
        self.assert_service()

    def assert_service(self):
        assert self.containers[self.route]['State']['Running'], 'stopped the serving application'
        masters = [name for name, item in self.containers.items() if item['State']['Running'] and name != release.BRIDGE]
        assert len(masters) <= 1, 'overlapping masters'

    def replace_compose(self, content):
        self.compose = content

    def switch(self, key, current, candidate, version):
        assert self.route == current
        assert self.containers[candidate]['State']['Running']
        self.events.append('switch:' + key)
        self.route = candidate
        self.assert_service()

    def drained(self, key):
        self.events.append('drain:' + key)
        return key not in self.pending

    def require_route(self, key, target, old=False, victim=None):
        release.check(self.route == target and key not in self.pending, 'serving_route_changed')
        self.ready(target, old=old)

    def stop(self, name, unserved=False):
        assert self.route != name, 'stopping ingress target'
        if name == release.APP and self.fail_stop:
            raise RuntimeError('unclean_shutdown')
        self.containers[name]['State']['Running'] = False
        self.events.append('stop:' + name)
        self.assert_service()
        return True

    def finish(self, phase):
        self.guard()
        self.save(phase)

    def cleanup_bridge(self):
        assert self.state['phase'] in ('complete', 'rolled_back')
        item = self.containers.get(release.BRIDGE)
        if item:
            assert not item['State']['Running']
            del self.containers[release.BRIDGE]
            self.events.append('remove:' + release.BRIDGE)


class RollingReleaseTests(unittest.TestCase):

    def test_additive_schema_failure_does_not_start_bridge(self):
        for flag, prepare in (('relay_file_schema', 'prepare_relay_file_schema'),
                              ('shared_sub2api_schema', 'prepare_sub2api_shared_schema')):
            with self.subTest(flag=flag):
                flow = FakeRollout()
                flow.release[flag] = True
                with patch.object(release, prepare, side_effect=RuntimeError('locked')):
                    with self.assertRaisesRegex(RuntimeError, 'locked'):
                        self.run_fake(flow)
                self.assertEqual(flow.state['phase'], 'prepared')
                self.assertNotIn('start:' + release.BRIDGE + ':new', flow.events)
                self.assertTrue(flow.containers[release.APP]['State']['Running'])
                self.assertFalse(flow.state.get(flag + '_prepared'))

    def test_shared_account_schema_precedes_bridge_and_resumes_once(self):
        flow = FakeRollout()
        flow.release['shared_sub2api_schema'] = True
        flow.pending.add('to-bridge')
        with patch.object(release, 'prepare_sub2api_shared_schema', side_effect=lambda: flow.events.append('prepare-schema')) as prepare:
            self.assertEqual(self.run_fake(flow), 'pending')
            self.assertTrue(flow.state['shared_sub2api_schema_prepared'])
            self.assertLess(flow.events.index('prepare-schema'), flow.events.index('start:' + release.BRIDGE + ':new'))
            flow.pending.clear()
            self.assertEqual(self.run_fake(flow), 'complete')
            prepare.assert_called_once_with()



    def test_document_pagination_mount_requires_isolated_verified_worker(self):
        receipt = {'document_pages': {'container_name': 'new-api-document-pages',
                   'image_id': 'sha256:' + '1' * 64, 'volume_name': 'new-api-document-pages-socket'}}
        worker = {'Image': receipt['document_pages']['image_id'], 'State': {'Health': {'Status': 'healthy'}},
                  'Config': {'User': '65532:65532'},
                  'HostConfig': {'NetworkMode': 'none', 'ReadonlyRootfs': True, 'Privileged': False,
                                 'CapDrop': ['ALL'], 'SecurityOpt': ['no-new-privileges:true'], 'Memory': 1024 ** 3},
                  'Mounts': [{'Type': 'volume', 'Name': 'new-api-document-pages-socket',
                              'Destination': '/run/document-pages', 'Source': '/volume/_data',
                              'Driver': 'local', 'Mode': 'rw', 'RW': True, 'Propagation': ''}]}
        with patch.object(release, 'inspect', return_value=worker):
            mount = release.document_pages_mount(receipt)
        self.assertFalse(mount['RW'])
        self.assertEqual(mount['Mode'], 'ro')
        self.assertTrue(worker['Mounts'][0]['RW'])
        for field, value in [('NetworkMode', 'bridge'), ('Privileged', True), ('ReadonlyRootfs', False),
                             ('Memory', 0), ('CapDrop', []), ('SecurityOpt', [])]:
            changed = copy.deepcopy(worker)
            changed['HostConfig'][field] = value
            with self.subTest(field=field), patch.object(release, 'inspect', return_value=changed):
                with self.assertRaisesRegex(RuntimeError, 'document_pages_isolation_changed'):
                    release.document_pages_mount(receipt)
        changed = copy.deepcopy(worker)
        changed['Mounts'].append({'Type': 'bind', 'Destination': '/data'})
        with patch.object(release, 'inspect', return_value=changed), self.assertRaisesRegex(RuntimeError, 'document_pages_mounts_changed'):
            release.document_pages_mount(receipt)
        self.assertIsNone(release.document_pages_mount({}))


    def test_optional_inspect_only_accepts_exact_named_missing_object(self):
        for message in ('Error: No such object: fixture-bridge', 'error: no such object: fixture-bridge',
                        'Error response from daemon: No such container: fixture-bridge',
                        'No such image: fixture-bridge', '  NO SUCH OBJECT: FIXTURE-BRIDGE\n'):
            result = release.subprocess.CompletedProcess([], 1, stdout='[]', stderr=message)
            with patch.object(release.subprocess, 'run', return_value=result):
                self.assertIsNone(release.inspect('fixture-bridge', optional=True))
                with self.assertRaisesRegex(RuntimeError, 'inspect_failed'):
                    release.inspect('fixture-bridge')
        for message in ('Cannot connect to the Docker daemon', 'permission denied',
                        'Cannot connect to the Docker daemon: dial unix /var/run/docker.sock: connect: no such file or directory',
                        'error: no such object: other-bridge',
                        'error: no such object: fixture-bridge\npermission denied',
                        'error:\nno such object: fixture-bridge'):
            result = release.subprocess.CompletedProcess([], 1, stdout='', stderr=message)
            with patch.object(release.subprocess, 'run', return_value=result):
                with self.assertRaisesRegex(RuntimeError, 'inspect_failed'):
                    release.inspect('fixture-bridge', optional=True)

    def run_fake(self, flow):
        with patch.object(release, 'inspect', side_effect=flow.inspect):
            return flow.run()

    def test_success_waits_for_each_drain_and_preserves_ingress(self):
        flow = FakeRollout()
        self.assertEqual(self.run_fake(flow), 'complete')
        events = [event for event in flow.events if event.startswith(('start:', 'switch:', 'stop:', 'drain:'))]
        self.assertEqual(events, ['start:' + release.BRIDGE + ':new', 'switch:to-bridge', 'drain:to-bridge',
                                 'stop:' + release.APP, 'start:' + release.APP + ':new', 'switch:to-main',
                                 'drain:to-main', 'stop:' + release.BRIDGE])
        self.assertEqual(flow.compose, flow.final)
        self.assertNotIn(release.BRIDGE, flow.containers)

    def test_first_long_drain_resumes_without_restarting_apps(self):
        flow = FakeRollout()
        flow.pending.add('to-bridge')
        self.assertEqual(self.run_fake(flow), 'pending')
        self.assertEqual(flow.state['phase'], 'drain_old')
        self.assertTrue(flow.containers[release.APP]['State']['Running'])
        self.assertEqual(flow.route, release.BRIDGE)
        flow.pending.clear()
        self.assertEqual(self.run_fake(flow), 'complete')
        self.assertEqual(flow.events.count('start:' + release.BRIDGE + ':new'), 1)

    def test_second_long_drain_keeps_bridge_until_resume(self):
        flow = FakeRollout()
        flow.pending.add('to-main')
        self.assertEqual(self.run_fake(flow), 'pending')
        self.assertEqual(flow.route, release.APP)
        self.assertTrue(flow.containers[release.BRIDGE]['State']['Running'])
        flow.pending.clear()
        self.assertEqual(self.run_fake(flow), 'complete')
        self.assertEqual(flow.events.count('start:' + release.APP + ':new'), 1)

    def test_bridge_failure_keeps_old_master_serving(self):
        flow = FakeRollout()
        flow.fail_bridge = True
        with self.assertRaisesRegex(RuntimeError, 'bridge_failed'):
            self.run_fake(flow)
        self.assertEqual(flow.route, release.APP)
        self.assertEqual(flow.containers[release.APP]['Image'], 'old')
        self.assertFalse(any(event.startswith('stop:') for event in flow.events))

    def test_failed_new_master_restores_old_while_bridge_serves(self):
        flow = FakeRollout()
        flow.fail_main = True
        self.assertEqual(self.run_fake(flow), 'rolled_back')
        self.assertEqual(flow.containers[release.APP]['Image'], 'old')
        self.assertEqual(flow.compose, flow.original)
        self.assertLess(flow.events.index('start:' + release.APP + ':old'), flow.events.index('switch:restore-route'))
        self.assertEqual(flow.events[-3:], ['stop:' + release.BRIDGE, 'guard', 'remove:' + release.BRIDGE])
        self.assertNotIn(release.BRIDGE, flow.containers)

    def test_failed_restore_keeps_bridge_and_can_resume(self):
        flow = FakeRollout()
        flow.fail_main = flow.fail_restore = True
        with self.assertRaisesRegex(RuntimeError, 'restore_failed'):
            self.run_fake(flow)
        self.assertEqual(flow.route, release.BRIDGE)
        self.assertTrue(flow.containers[release.BRIDGE]['State']['Running'])
        self.assertEqual(flow.state['phase'], 'restore_old')
        flow.fail_restore = False
        self.assertEqual(self.run_fake(flow), 'rolled_back')

    def test_unclean_old_shutdown_does_not_start_new_master(self):
        flow = FakeRollout()
        flow.fail_stop = True
        with self.assertRaisesRegex(RuntimeError, 'unclean_shutdown'):
            self.run_fake(flow)
        self.assertEqual(flow.route, release.BRIDGE)
        self.assertEqual(flow.state['phase'], 'stop_old')
        self.assertNotIn('start:' + release.APP + ':new', flow.events)

    def test_bridge_cleanup_is_owned_stopped_and_resumable_after_completion(self):
        for phase in ('complete', 'rolled_back'):
            with self.subTest(phase=phase), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                flow = object.__new__(release.Rollout)
                flow.args = argparse.Namespace(state=root / 'release.json')
                key = 'to-main' if phase == 'complete' else 'restore-route'
                flow.state = {'phase': phase, 'edges': {key: ['edge']}}
                flow.args.state.write_bytes(release.json_bytes(flow.state))
                flow.private = root
                (root / 'edge').mkdir()
                (root / 'edge/state.json').write_bytes(release.json_bytes({'current_id': 'bridge-id'}))
                flow.release = {'image_id': 'new'}
                bridge = app('new', 'bridge-id', running=False)
                bridge['Config']['Labels']['io.new-api.rollout'] = str(flow.args.state)
                containers = {release.BRIDGE: bridge}
                flow.require_route = lambda *_args, **_kwargs: None
                flow.guard = lambda: None
                flow.save = lambda **values: flow.state.update(values)

                def remove(*args):
                    self.assertEqual(args, ('docker', 'rm', 'bridge-id'), 'remove the verified ID without forcing or removing volumes')
                    self.assertEqual(release.read_json(flow.args.state)['phase'], phase)
                    del containers[release.BRIDGE]

                with patch.object(release, 'inspect', side_effect=lambda name, **_kwargs: containers.get(name)), \
                        patch.object(release, 'command', side_effect=remove) as command:
                    for field, value in (('Running', True), ('ExitCode', 137), ('OOMKilled', True)):
                        old_value = bridge['State'][field]
                        bridge['State'][field] = value
                        with self.assertRaisesRegex(RuntimeError, 'bridge_not_cleanly_stopped'):
                            flow.run()
                        bridge['State'][field] = old_value
                    bridge['Config']['Labels']['io.new-api.rollout'] = '/another-release.json'
                    with self.assertRaisesRegex(RuntimeError, 'unowned_bridge'):
                        flow.run()
                    bridge['Config']['Labels']['io.new-api.rollout'] = str(flow.args.state)
                    command.assert_not_called()
                    with patch.object(flow, 'save', side_effect=OSError('interrupted journal write')):
                        with self.assertRaises(OSError):
                            flow.run()
                    self.assertNotIn(release.BRIDGE, containers)
                    self.assertEqual(release.read_json(flow.args.state)['phase'], phase)
                    flow.save = lambda **values: flow.state.update(values)
                    self.assertEqual(flow.run(), phase)
                    self.assertTrue(flow.state['bridge_removed'])
                    command.assert_called_once_with('docker', 'rm', 'bridge-id')

    def test_bridge_cleanup_refuses_unfinished_or_undrained_release(self):
        flow = object.__new__(release.Rollout)
        flow.state = {'phase': 'stop_bridge'}
        with patch.object(release, 'command') as command:
            with self.assertRaisesRegex(RuntimeError, 'release_not_complete'):
                flow.cleanup_bridge()
            flow.state['phase'] = 'complete'
            flow.require_route = lambda *_args, **_kwargs: release.check(False, 'serving_route_changed')
            with self.assertRaisesRegex(RuntimeError, 'serving_route_changed'):
                flow.cleanup_bridge()
            command.assert_not_called()

    def test_resuming_after_external_edge_rollback_never_stops_serving_app(self):
        flow = FakeRollout()
        flow.pending.add('to-bridge')
        self.assertEqual(self.run_fake(flow), 'pending')
        flow.pending.clear()
        flow.state['phase'] = 'stop_old'
        flow.route = release.APP
        with self.assertRaisesRegex(RuntimeError, 'serving_route_changed'):
            self.run_fake(flow)
        self.assertTrue(flow.containers[release.APP]['State']['Running'])
        self.assertFalse(any(event.startswith('stop:') for event in flow.events))

    def test_runtime_normalizes_order_without_hiding_value_changes(self):
        original = app('old', 'original')
        reordered = copy.deepcopy(original)
        reordered['Config']['Env'].reverse()
        reordered['HostConfig']['Binds'].reverse()
        reordered['HostConfig']['PortBindings']['3000/tcp'].reverse()
        reordered['Mounts'].reverse()
        self.assertEqual(release.runtime_fingerprint(original), release.runtime_fingerprint(reordered))
        for mutate in (
                lambda item: item['Config']['Env'].append('C=added'),
                lambda item: item['Mounts'][0].update(RW=False),
                lambda item: item['HostConfig']['PortBindings']['3000/tcp'][0].update(HostPort='9999'),
                lambda item: item['Config']['Cmd'].reverse()):
            changed = copy.deepcopy(original)
            mutate(changed)
            self.assertNotEqual(release.runtime_fingerprint(original), release.runtime_fingerprint(changed))
        with self.assertRaisesRegex(RuntimeError, 'duplicate'):
            release.environment(['A=one', 'A=two'])

    def test_default_preflight_does_not_create_state_or_invoke_mutations(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch.object(release, 'preflight', return_value=(None, None, None, {})), \
                    patch.object(release, 'initialize', side_effect=AssertionError('mutation')), \
                    contextlib.redirect_stdout(io.StringIO()) as output:
                self.assertEqual(release.main(['--release', str(root / 'release.json'), '--backup', str(root / 'backup.json'),
                                               '--state', str(root / 'state.json')]), 0)
            self.assertEqual(list(root.iterdir()), [])
            self.assertFalse(json.loads(output.getvalue())['deployment_performed'])

    def test_http_sub2api_option_is_bound_to_resume_and_defaults_off(self):
        flow = object.__new__(release.Rollout)
        for saved, supplied in ((False, True), (True, False), (None, True)):
            with self.subTest(saved=saved, supplied=supplied):
                flow.state = {'trusted_https_proxy': None}
                if saved is not None:
                    flow.state['allow_http_sub2api_management'] = saved
                flow.args = argparse.Namespace(trusted_https_proxy=None, allow_http_sub2api_management=supplied)
                with self.assertRaisesRegex(RuntimeError, 'http_sub2api_management_changed'):
                    flow.guard()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for options in ([], ['--allow-http-sub2api-management']):
                with self.subTest(options=options), patch.object(release, 'preflight', return_value=(None, None, None, {})), \
                        patch.object(release, 'initialize', side_effect=AssertionError('mutation')), \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(release.main(['--release', str(root / 'release.json'), '--backup', str(root / 'backup.json'),
                                                   '--state', str(root / 'state.json'), *options]), 0)
            self.assertEqual(list(root.iterdir()), [])

    def test_candidate_environment_options_preserve_rollback(self):
        key = 'SUB2API_ALLOW_HTTP_MANAGEMENT'
        for enabled, existing, sync_mode in ((False, None, None), (True, None, None),
                                             (False, 'true', None), (True, 'false', None),
                                             (False, None, 'new'), (True, None, 'rotate')):
            with self.subTest(enabled=enabled, existing=existing, sync_mode=sync_mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                old = app('old', 'old-container')
                old['Config']['Env'].append('NODE_NAME=fixture')
                old['State']['Health'] = {'Status': 'healthy'}
                if existing is not None:
                    old['Config']['Env'].append(key + '=' + existing)
                if sync_mode == 'rotate':
                    old['Config']['Env'].append('SATELLITE_SYNC_NODES=' + json.dumps({'fixture': 'old-' * 12}))
                sync_path = None
                sync_value = json.dumps({'fixture': 'new-' * 12}, separators=(',', ':'))
                if sync_mode:
                    sync_path = root / 'satellite.env'
                    sync_path.write_text('SATELLITE_SYNC_NODES=' + sync_value + '\n')
                    sync_path.chmod(0o600)
                old_env = release.environment(old['Config']['Env'])
                original = {'name': 'fixture', 'services': {'new-api': {'environment': old_env, 'image': 'old'}}}
                compose, env, template = (root / name for name in ('compose', 'env', 'template'))
                compose.write_bytes(release.json_bytes(original))
                env.write_bytes(b'UNCHANGED=fixture\n')
                template.write_bytes(b'fixture')
                args = argparse.Namespace(state=root / 'state.json', release=root / 'release/release.json', backup=root / 'backup.json',
                                          trusted_https_proxy=None, allow_http_sub2api_management=enabled,
                                          satellite_sync_env=sync_path)
                raw_index = b'<html>fixture</html>'
                dist = root / 'candidate/web/dist'
                dist.mkdir(parents=True)
                (dist / 'index.html').write_bytes(raw_index)
                receipt = {'image': 'new', 'image_id': 'new', 'runtime_base_id': 'old', 'version': 'new',
                           'entry_assets': {}, 'index_sha256': hashlib.sha256(raw_index).hexdigest()}
                args.release.parent.mkdir()
                args.release.write_bytes(release.json_bytes(receipt))
                with patch.object(release, 'preflight', return_value=(receipt, compose.read_bytes(), old, {'old_version': 'old'})), \
                        patch.object(release, 'COMPOSE', compose), patch.object(release, 'ENV', env), \
                        patch.object(release, 'EDGE_TEMPLATE', template), patch.object(release, 'command', return_value=''):
                    state = release.initialize(args)
                self.assertIs(state['allow_http_sub2api_management'], enabled)
                self.assertEqual(state['satellite_sync_env'], {'path': str(sync_path), 'sha256': release.digest(sync_path)} if sync_path else None)
                self.assertNotIn('new-' * 12, json.dumps(state))
                self.assertEqual(release.read_json(compose), original)
                self.assertEqual(env.read_bytes(), b'UNCHANGED=fixture\n')
                flow = release.Rollout(args, state)
                flow.release = receipt
                for filename, service_name, container_name in (('compose.final', 'new-api', release.APP),
                                                                 ('compose.bridge', 'new-api-bridge', release.BRIDGE)):
                    saved = release.read_json(flow.private / filename)['services'][service_name]['environment']
                    self.assertEqual(saved.get(key), 'true' if enabled else existing)
                    self.assertEqual(saved.get('SATELLITE_SYNC_NODES'), sync_value if sync_mode else None)
                    self.assertEqual(saved.get('DASHBOARD_CONFIG_READ_ONLY'), 'true' if container_name == release.BRIDGE else None)
                    candidate = copy.deepcopy(old)
                    candidate['Image'] = 'new'
                    candidate['Config']['Env'] = saved
                    if container_name == release.BRIDGE:
                        candidate['HostConfig']['PortBindings'] = {}
                    with patch.object(release, 'inspect', return_value=candidate), \
                            patch.object(release, 'fetch', side_effect=lambda _item, path: b'{"success":true,"data":{"version":"new"}}' if path == '/api/status' else raw_index):
                        flow.ready(container_name)
                with patch.object(release, 'inspect', return_value=old), \
                        patch.object(release, 'fetch', return_value=b'{"success":true,"data":{"version":"old"}}'):
                    flow.ready(release.APP, old=True)
                self.assertEqual(release.read_json(flow.private / 'app.before.json'), old)
                self.assertEqual(release.read_json(flow.private / 'compose.before'), original)

    def test_satellite_environment_rejects_extra_keys_weak_secrets_and_unsafe_files(self):
        secret = 'test-secret-' * 4
        valid = 'SATELLITE_SYNC_NODES=' + json.dumps({'ran-us2': secret}) + '\n'
        invalid = [
            '', 'OTHER=' + json.dumps({'ran-us2': secret}), valid + 'SQL_DSN=forbidden\n',
            'SATELLITE_SYNC_NODES={}', 'SATELLITE_SYNC_NODES=[]',
            'SATELLITE_SYNC_NODES=' + json.dumps({'bad node': secret}),
            'SATELLITE_SYNC_NODES=' + json.dumps({'ran-us2': 'short'}),
            'SATELLITE_SYNC_NODES=' + json.dumps({'ran-us2': secret + '\n'}),
            'SATELLITE_SYNC_NODES=' + json.dumps({'ran-us2': ['not a secret']}),
            'SATELLITE_SYNC_NODES={"ran-us2":"' + secret + '","ran-us2":"' + secret + '"}',
        ]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'sync.env'
            for contents in invalid:
                with self.subTest(contents=contents):
                    path.write_text(contents)
                    path.chmod(0o600)
                    with self.assertRaises((RuntimeError, ValueError)):
                        release.satellite_sync_environment(path)
            path.write_text(valid)
            path.chmod(0o644)
            with self.assertRaisesRegex(RuntimeError, 'unsafe_satellite_sync_environment_file'):
                release.satellite_sync_environment(path)
            path.chmod(0o600)
            link = path.with_suffix('.link')
            link.symlink_to(path)
            with self.assertRaises(OSError):
                release.satellite_sync_environment(link)

    def test_satellite_environment_resume_binds_content_path_and_flag(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'sync.env'
            path.write_text('SATELLITE_SYNC_NODES=' + json.dumps({'ran-us2': 'a' * 32}) + '\n')
            path.chmod(0o600)
            _, binding = release.satellite_sync_environment(path)
            flow = object.__new__(release.Rollout)
            flow.state = {'trusted_https_proxy': None, 'satellite_sync_env': binding}
            flow.args = argparse.Namespace(trusted_https_proxy=None, allow_http_sub2api_management=False,
                                           satellite_sync_env=None)
            with self.assertRaisesRegex(RuntimeError, 'satellite_sync_environment_changed'):
                flow.guard()
            duplicate = path.with_name('same-content.env')
            duplicate.write_bytes(path.read_bytes())
            duplicate.chmod(0o600)
            flow.args.satellite_sync_env = duplicate
            with self.assertRaisesRegex(RuntimeError, 'satellite_sync_environment_changed'):
                flow.guard()
            path.write_text('SATELLITE_SYNC_NODES=' + json.dumps({'ran-us2': 'b' * 32}) + '\n')
            flow.args.satellite_sync_env = path
            with self.assertRaisesRegex(RuntimeError, 'satellite_sync_environment_changed'):
                flow.guard()

    def test_different_journals_share_one_exclusive_release_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with (root / '.rolling-release.lock').open('a') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                for name in ('one.json', 'two.json'):
                    with patch.object(release, 'PROJECT', root), \
                            patch.object(release, 'initialize', side_effect=AssertionError('mutation')), \
                            contextlib.redirect_stdout(io.StringIO()) as output:
                        self.assertEqual(release.main(['--apply', '--release', str(root / 'release.json'), '--backup', str(root / 'backup.json'),
                                                       '--state', str(root / name)]), 1)
                        self.assertEqual(json.loads(output.getvalue())['error_type'], 'BlockingIOError')
                    self.assertFalse((root / name).exists())

    def test_application_connections_block_stop_even_after_edge_drain(self):
        header = '  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode'
        for state in ('01', '08'):
            for protocol in ('tcp', 'tcp6'):
                with self.subTest(state=state, protocol=protocol):
                    flow = object.__new__(release.Rollout)
                    flow.state = {}
                    flow.save = lambda **values: flow.state.update(values)
                    live = app('old', 'old')
                    live['State']['Pid'] = 123
                    row = f'0: 00000000:0BB8 00000000:1234 {state} 0 0 0 0 0 1'
                    def contents(path):
                        return header + ('\n' + row if path.name == protocol else '')
                    with patch.object(release, 'inspect', return_value=live), \
                            patch.object(release.Path, 'read_text', contents), \
                            patch.object(release, 'command') as command:
                        self.assertFalse(flow.stop(release.APP))
                    command.assert_not_called()
                    self.assertEqual(flow.state['application_connections_pending'], 1)
        flow = object.__new__(release.Rollout)
        live['State']['Pid'] = 123
        for failure in (PermissionError(), 'malformed'):
            with patch.object(release, 'inspect', return_value=live), \
                    patch.object(release.Path, 'read_text', side_effect=failure if isinstance(failure, Exception) else None,
                                 return_value=failure if isinstance(failure, str) else ''), \
                    patch.object(release, 'command') as command:
                with self.assertRaises((PermissionError, RuntimeError)):
                    flow.stop(release.APP)
                command.assert_not_called()

    def test_application_stop_requires_stable_empty_application_sockets(self):
        flow = object.__new__(release.Rollout)
        flow.state = {'started_at': '2026-11-26T00:00:00Z', 'logs_before': {}}
        flow.save = lambda **values: flow.state.update(values)
        live = app('old', 'old')
        live['State']['Pid'] = 123
        stopped = app('old', 'old', running=False)
        sockets = ('sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n'
                   '0: 00000000:0BB8 00000000:0000 0A 0 0 0 0 0 1\n'
                   '1: 00000000:1234 00000000:0BB8 01 0 0 0 0 0 2')
        with patch.object(release.Path, 'read_text', return_value=sockets), \
                patch.object(release, 'inspect', side_effect=[live, live, stopped]), \
                patch.object(release, 'command', return_value='') as command, \
                patch.object(release, 'log_bounds', return_value={}), \
                patch.object(release, 'preserve_logs'):
            self.assertTrue(flow.stop(release.APP))
        self.assertEqual(command.call_args_list[0].args, ('docker', 'stop', '-t', '150', release.APP))
        replaced = copy.deepcopy(live)
        replaced['State']['Pid'] = 456
        with patch.object(release.Path, 'read_text', return_value=sockets), \
                patch.object(release, 'inspect', side_effect=[live, replaced]), \
                patch.object(release, 'command') as command:
            with self.assertRaisesRegex(RuntimeError, 'application_process_changed'):
                flow.stop(release.APP)
            command.assert_not_called()

    def test_pending_application_stop_keeps_release_journal_in_stop_phase(self):
        for phase, route in (('stop_old', release.BRIDGE), ('stop_bridge', release.APP),
                             ('stop_rollback_bridge', release.APP)):
            flow = FakeRollout()
            flow.state['phase'], flow.route = phase, route
            flow.containers[release.BRIDGE] = app('new', 'bridge')
            with patch.object(flow, 'stop', return_value=False):
                self.assertEqual(self.run_fake(flow), 'pending')
            self.assertEqual(flow.state['phase'], phase)
            self.assertTrue(all(v['State']['Running'] for v in flow.containers.values()))

    def test_bridge_compose_project_isolated_from_archived_same_service(self):
        flow = object.__new__(release.Rollout)
        flow.private = Path('/private/rollout')
        flow.original = b'{"name":"home"}'
        flow.state = {'commit': 'a' * 40}
        with patch.object(release, 'command') as command:
            flow.compose_up(bridge=True)
            bridge_args = command.call_args.args
            flow.compose_up()
            main_args = command.call_args.args
        self.assertEqual(bridge_args[bridge_args.index('-p') + 1], 'home-bridge-aaaaaaaaa')
        self.assertEqual(main_args[main_args.index('-p') + 1], 'home')
        self.assertEqual(bridge_args[-1], 'new-api-bridge')
        self.assertIn('--no-deps', bridge_args)
        self.assertEqual(bridge_args[bridge_args.index('-f') + 1], '/private/rollout/compose.bridge')

    def test_shutdown_detects_stderr_flush_failure_and_killed_process(self):
        flow = object.__new__(release.Rollout)
        flow.state = {'started_at': '2026-11-11T00:00:00Z'}
        stopped = app('old', 'old', running=False)
        for message in ('request trace writes did not finish', '服务未能正常关闭，已强制退出',
                        'failed to persist performance metrics during shutdown'):
            with patch.object(release, 'inspect', return_value=stopped), \
                    patch.object(release, 'command', return_value=message) as command:
                with self.assertRaisesRegex(RuntimeError, 'writes_not_drained'):
                    flow.stop(release.APP)
                self.assertTrue(command.call_args.kwargs['stderr'])
        stopped['State']['ExitCode'] = 137
        with patch.object(release, 'inspect', return_value=stopped):
            with self.assertRaisesRegex(RuntimeError, 'not_cleanly_stopped'):
                flow.stop(release.APP, unserved=True)

    def test_ready_checks_rendered_homepage_and_assets_against_release(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dist = root / 'candidate/web/dist'
            dist.mkdir(parents=True)
            raw = b'<html><!--umami-->\n<!--Google Analytics-->\n</html>'
            (dist / 'index.html').write_bytes(raw)
            rendered = b'<html><!--Umami QuantumNous-->\n<!--Google Analytics QuantumNous-->\n</html>'
            flow = object.__new__(release.Rollout)
            flow.args = argparse.Namespace(release=root / 'release/release.json')
            flow.old = app('old', 'old')
            flow.state = {'old_version': 'old'}
            flow.release = {'runtime_base_id': 'old', 'image_id': 'new', 'version': 'new',
                            'index_sha256': hashlib.sha256(raw).hexdigest(),
                            'entry_assets': {'/entry.js': hashlib.sha256(b'entry').hexdigest()}}
            candidate = copy.deepcopy(flow.old)
            candidate['Image'] = 'new'
            candidate['Config']['Env'].append('AUTH_CACHE_LEGACY_FENCE_KEYS=true')
            candidate['State']['Health'] = {'Status': 'healthy'}
            bodies = {'/api/status': b'{"success":true,"data":{"version":"new"}}', '/usage-logs': rendered, '/entry.js': b'entry'}
            with patch.object(release, 'inspect', return_value=candidate), \
                    patch.object(release, 'fetch', side_effect=lambda _item, path: bodies[path]):
                flow.ready(release.APP)
                bodies['/usage-logs'] = raw
                with self.assertRaisesRegex(RuntimeError, 'served_index_mismatch'):
                    flow.ready(release.APP)
                bodies['/usage-logs'] = rendered
                bodies['/entry.js'] = b'wrong bundle'
                with self.assertRaisesRegex(RuntimeError, 'served_asset_mismatch'):
                    flow.ready(release.APP)

    def test_release_accepts_bound_reused_gates_but_rejects_evidence_or_identity_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            commit = 'a' * 40
            work = root / 'release-ran-aaaaaaaaa'
            work.mkdir()
            dist = root / 'candidate/web/dist'
            dist.mkdir(parents=True)
            for path, content in ((root / 'candidate/main.go', b'code'), (root / 'source.tar.gz', b'archive'),
                                  (work / 'new-api', b'binary'), (dist / 'entry.js', b'entry'), (dist / 'index.html', b'index')):
                path.write_bytes(content)
            manifest = {'commit': commit, 'archive_sha256': release.digest(root / 'source.tar.gz'),
                        'files': {'main.go': release.digest(root / 'candidate/main.go')}}
            (root / 'source-manifest.json').write_bytes(release.json_bytes(manifest))
            receipt = {'result': 'passed', 'commit': commit, 'version': 'ran-aaaaaaaaa', 'image': 'new-api:ran-aaaaaaaaa',
                       'base_path': '/', 'runtime_base': 'new-api:old', 'runtime_base_id': 'old-id', 'image_id': 'new-id',
                       'manifest_sha256': release.digest(root / 'source-manifest.json'), 'binary_sha256': release.digest(work / 'new-api'),
                       'assets': {'/entry.js': release.digest(dist / 'entry.js')}, 'entry_assets': {'/entry.js': release.digest(dist / 'entry.js')}}
            (work / 'release.json').write_bytes(release.json_bytes(receipt))
            startup = root / 'startup-final/result.json'
            startup.parent.mkdir()
            startup.write_bytes(release.json_bytes({'passed': True, 'commit': commit, 'new_image_id': 'new-id',
                                                    'old_image_id': 'old-id', 'mixed_version_bridge': {'passed': True}, 'remaining_containers': []}))
            reused = root / 'prior-checks.json'
            reused.write_bytes(release.json_bytes({'commit': 'b' * 40, 'passed': True}))
            gates = {'passed': True, 'commit': commit, 'manifest_sha256': receipt['manifest_sha256'],
                     'checks': sorted(release.REQUIRED_CHECKS | {'database-matrix', 'startup-matrix'}),
                     'evidence': [{'path': str(path), 'sha256': release.digest(path)} for path in (startup, reused)]}
            (work / 'gates.json').write_bytes(release.json_bytes(gates))
            labels = {'org.opencontainers.image.revision': commit, 'org.opencontainers.image.version': receipt['version'],
                      'io.new-api.source-manifest-sha256': receipt['manifest_sha256'], 'io.new-api.frontend-base-path': '/'}
            with patch.object(release, 'inspect', side_effect=lambda name, **_kwargs: {'Id': 'old-id' if name == 'new-api:old' else 'new-id', 'Config': {'Labels': labels}}):
                self.assertEqual(release.verify_release(work / 'release.json'), receipt)
                reused.write_bytes(b'changed')
                with self.assertRaisesRegex(RuntimeError, 'gate_evidence_changed'):
                    release.verify_release(work / 'release.json')
                gates['commit'] = 'c' * 40
                (work / 'gates.json').write_bytes(release.json_bytes(gates))
                with self.assertRaisesRegex(RuntimeError, 'validation_gate_identity_mismatch'):
                    release.verify_release(work / 'release.json')

    def test_edge_crash_resume_distinguishes_unmodified_and_switched_config(self):
        for content, expected in ((b'original', ['rollback', 'switch']), (b'after', ['rollback', 'switch'])):
            with self.subTest(content=content), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                edge_config = root / 'nginx.conf'
                edge_config.write_bytes(content)
                receipt = root / 'to-bridge-1'
                receipt.mkdir()
                hashes = {key + '_sha256': hashlib.sha256(value).hexdigest()
                          for key, value in [('original', b'original'), ('before', b'before'), ('after', b'after')]}
                (receipt / 'state.json').write_text(json.dumps(dict(hashes, phase='switching')))
                flow = object.__new__(release.Rollout)
                flow.private = root
                flow.state = {'edges': {'to-bridge': [receipt.name]}}
                actions = []
                flow.save = lambda: None
                flow.edge = lambda action, *_args, **_kwargs: actions.append(action)
                with patch.object(release, 'EDGE_CONFIG', edge_config):
                    flow.switch('to-bridge', release.APP, release.BRIDGE, 'new')
                self.assertEqual(actions, expected)


if __name__ == '__main__':
    unittest.main()
