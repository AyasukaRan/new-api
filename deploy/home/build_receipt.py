#!/usr/bin/env python3
"""Prepare a home build from Git and record its artifacts for rolling_release.py."""
import argparse
from html.parser import HTMLParser
import re
from pathlib import Path
import shutil
import subprocess
import tarfile

from rolling_release import check, command, digest, inspect, json_bytes, private_write, read_json


class EntryAssets(HTMLParser):
    def __init__(self):
        super().__init__()
        self.paths = []

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == 'script' and attrs.get('src'):
            self.paths.append(attrs['src'])
        if tag == 'link' and 'stylesheet' in attrs.get('rel', '').split() and attrs.get('href'):
            self.paths.append(attrs['href'])


def prepare(root, commit):
    check(re.fullmatch('[a-f0-9]{40}', commit) and command('git', 'rev-parse', 'HEAD') == commit,
          'checkout_commit_mismatch')
    root.mkdir(parents=True, exist_ok=True)
    source = root / 'candidate'
    source.mkdir()
    archive = root / 'source.tar.gz'
    with archive.open('xb') as stream:
        subprocess.run(['git', 'archive', '--format=tar.gz', commit], stdout=stream, check=True)
    with tarfile.open(archive) as tar:
        check(all(entry.isdir() or entry.isfile() for entry in tar), 'unsupported_source_entry')
        tar.extractall(source, filter='data')
    files = {path.relative_to(source).as_posix(): digest(path)
             for path in sorted(source.rglob('*')) if path.is_file()}
    manifest = {
        'commit': commit, 'files': files, 'archive_sha256': digest(archive), 'immutable_git_archive': True,
        'web_tree': command('git', 'rev-parse', commit + ':web'),
        'module_trees': {mod: command('git', 'rev-parse', commit + ':' + mod) for mod in ('relaykit', 'tokenkit')},
    }
    path = root / 'source-manifest.json'
    private_write(path, json_bytes(manifest))
    print(digest(path))


def finish(root, dist, runtime_base, runtime_base_id, built_image):
    manifest_path = root / 'source-manifest.json'
    manifest = read_json(manifest_path)
    commit = manifest['commit']
    version = 'ran-' + commit[:9]
    image = 'new-api:' + version
    details = inspect(built_image or image, image=True)
    manifest_hash = digest(manifest_path)
    labels = {
        'org.opencontainers.image.revision': commit, 'org.opencontainers.image.version': version,
        'io.new-api.source-manifest-sha256': manifest_hash, 'io.new-api.frontend-base-path': '/',
    }
    check(all(details['Config']['Labels'].get(key) == value for key, value in labels.items()),
          'image_build_identity_mismatch')
    image_id = details['Id']
    check(command('docker', 'run', '--rm', '--network=none', image_id, '--version') == version,
          'binary_version_mismatch')
    if not runtime_base_id:
        runtime_base_id = inspect(runtime_base, image=True)['Id']
    check(re.fullmatch('new-api:ran-[0-9a-f]{9}', runtime_base) and
          re.fullmatch('sha256:[0-9a-f]{64}', runtime_base_id), 'invalid_home_runtime_base')
    source = root / 'candidate'
    check(all(digest(source / name) == expected for name, expected in manifest['files'].items()),
          'source_changed_during_build')
    target_dist = source / 'web/dist'
    if dist.resolve() != target_dist.resolve():
        shutil.copytree(dist, target_dist)
    assets = {'/' + path.relative_to(target_dist).as_posix(): digest(path)
              for path in sorted(target_dist.rglob('*')) if path.is_file()}
    entries = EntryAssets()
    entries.feed((target_dist / 'index.html').read_text())
    check(entries.paths and all(path in assets for path in entries.paths), 'invalid_frontend_entries')
    output = root / ('release-' + version)
    output.mkdir()
    binary = output / 'new-api'
    container = command('docker', 'create', '--network=none', image_id)
    try:
        subprocess.run(['docker', 'cp', container + ':/new-api', str(binary)], check=True)
    finally:
        command('docker', 'rm', '-v', container)
    receipt = {
        'result': 'passed', 'deployed': False, 'commit': commit, 'version': version, 'base_path': '/',
        'image': image, 'image_id': image_id, 'runtime_base': runtime_base, 'runtime_base_id': runtime_base_id,
        'manifest_sha256': manifest_hash, 'binary_sha256': digest(binary), 'assets': assets,
        'entry_assets': {path: assets[path] for path in entries.paths}, 'index_sha256': assets['/index.html'],
        'shared_sub2api_schema': True,
    }
    # Deployment validates its own gate evidence separately from this build receipt.
    path = output / 'release.json'
    private_write(path, json_bytes(receipt))
    print(path)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='action', required=True)
    start = commands.add_parser('prepare')
    start.add_argument('root', type=Path)
    start.add_argument('commit')
    end = commands.add_parser('finish')
    end.add_argument('root', type=Path)
    end.add_argument('--dist', required=True, type=Path)
    end.add_argument('--runtime-base', required=True)
    end.add_argument('--runtime-base-id')
    end.add_argument('--built-image')
    args = parser.parse_args()
    if args.action == 'prepare':
        prepare(args.root, args.commit)
    else:
        finish(args.root, args.dist, args.runtime_base, args.runtime_base_id, args.built_image)
