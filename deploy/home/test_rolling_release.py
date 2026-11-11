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
