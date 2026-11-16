#!/usr/bin/env python3
"""Count local document/image pages; never export or return uploaded content."""
import argparse
import contextlib
import http.server
import json
import os
from pathlib import Path
import resource
import signal
import socketserver
import subprocess
import sys
import tempfile
import threading
import time
import urllib.parse
import uuid
import warnings
import zipfile

MAX_BYTES = 100 << 20
MAX_IMAGE_BYTES = 64 << 20
MAX_PAGES = 1000
MAX_IMAGE_PIXELS = 40_000_000
MAX_TOTAL_IMAGE_PIXELS = 200_000_000
JOB_SECONDS = 60
FORMATS = {'doc', 'docx', 'ppt', 'pptx', 'image'}
SOCKET = '/run/document-pages/pages.sock'


def property_value(name, value):
    from com.sun.star.beans import PropertyValue
    result = PropertyValue()
    result.Name, result.Value = name, value
    return result


def start_office(directory):
    import uno
    pipe = 'newapi_' + uuid.uuid4().hex
    profile = (directory / 'profile').as_uri()
    office = subprocess.Popen(
        ['/usr/bin/soffice', '-env:UserInstallation=' + profile,
         '--headless', '--nologo', '--nodefault', '--nolockcheck',
         '--nofirststartwizard', '--accept=pipe,name=' + pipe + ';urp;StarOffice.ComponentContext'],
        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    local = uno.getComponentContext()
    resolver = local.ServiceManager.createInstanceWithContext('com.sun.star.bridge.UnoUrlResolver', local)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline and office.poll() is None:
        try:
            context = resolver.resolve('uno:pipe,name=' + pipe + ';urp;StarOffice.ComponentContext')
            desktop = context.ServiceManager.createInstanceWithContext('com.sun.star.frame.Desktop', context)
            return office, desktop
        except Exception:
            time.sleep(0.05)
    office.terminate()
    raise ValueError('Office startup failed')


def count_office(path, kind):
    import uno
    import unohelper
    from com.sun.star.task import XInteractionHandler

    class AbortInteraction(unohelper.Base, XInteractionHandler):
        def handle(self, request):
            for continuation in request.getContinuations():
                abort = continuation.queryInterface(uno.getTypeByName('com.sun.star.task.XInteractionAbort'))
                if abort is not None:
                    abort.select()
                    return

    # These limits apply to this expendable job and its LibreOffice descendants.
    resource.setrlimit(resource.RLIMIT_CPU, (45, 45))
    resource.setrlimit(resource.RLIMIT_AS, (1536 << 20, 1536 << 20))
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_FSIZE, (128 << 20, 128 << 20))
    office, desktop = start_office(path.parent)
    document = None
    try:
        options = tuple(property_value(name, value) for name, value in (
            ('Hidden', True), ('ReadOnly', True), ('Silent', True),
            ('MacroExecutionMode', 0), ('UpdateDocMode', 0),
            ('PickListEntry', False), ('RepairPackage', False),
            ('StartPresentation', False), ('InteractionHandler', AbortInteraction())))
        document = desktop.loadComponentFromURL(path.as_uri(), '_blank', 0, options)
        if document is None:
            raise ValueError('Document cannot be opened')
        if kind in ('doc', 'docx'):
            if not document.supportsService('com.sun.star.text.TextDocument'):
                raise ValueError('Document is not Word')
            # XRenderable computes the current complete document layout. This
            # does not read the stale docProps/app.xml Pages property or export.
            pages = document.getRendererCount(document, ())
        else:
            if not document.supportsService('com.sun.star.presentation.PresentationDocument'):
                raise ValueError('Document is not PowerPoint')
            # Local presentation pagination counts every slide, including hidden.
            pages = document.getDrawPages().getCount()
        if type(pages) is not int or not 1 <= pages <= MAX_PAGES:
            raise ValueError('Document page count is outside supported limits')
        return pages
    finally:
        if document is not None:
            with contextlib.suppress(Exception):
                document.close(True)
        with contextlib.suppress(Exception):
            desktop.terminate()
        with contextlib.suppress(Exception):
            office.wait(timeout=3)
        if office.poll() is None:
            office.kill()


def count_image(path):
    # Decode only in an expendable, networkless job, never in the HTTP process.
    resource.setrlimit(resource.RLIMIT_CPU, (45, 45))
    resource.setrlimit(resource.RLIMIT_AS, (512 << 20, 512 << 20))
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_FSIZE, (0, 0))
    from PIL import Image, ImageFile
    Image.MAX_IMAGE_PIXELS = MAX_IMAGE_PIXELS
    ImageFile.LOAD_TRUNCATED_IMAGES = False
    warnings.simplefilter('error', Image.DecompressionBombWarning)
    formats = ['PNG', 'JPEG', 'WEBP', 'GIF', 'BMP', 'TIFF']
    with Image.open(path, formats=formats) as image:
        image.verify()
    pages, pixels = 0, 0
    with Image.open(path, formats=formats) as image:
        for index in range(MAX_PAGES + 1):
            try:
                image.seek(index)
            except EOFError:
                break
            if index == MAX_PAGES:
                raise ValueError('Image has too many pages')
            frame_pixels = image.width * image.height
            pixels += frame_pixels
            if not 0 < frame_pixels <= MAX_IMAGE_PIXELS or pixels > MAX_TOTAL_IMAGE_PIXELS:
                raise ValueError('Decoded image exceeds processing limits')
            image.load()
            pages += 1
    if pages == 0:
        raise ValueError('Image has no pages')
    return pages


def validate_format(path, kind):
    if kind == 'image':
        return  # Image recognition and validation stay inside the limited job.
    with path.open('rb') as source:
        header = source.read(8)
    if kind in ('doc', 'ppt'):
        if header != bytes.fromhex('d0cf11e0a1b11ae1'):
            raise ValueError('Expected an Office binary document')
        return
    if not header.startswith(b'PK\x03\x04'):
        raise ValueError('Expected an Office XML document')
    with zipfile.ZipFile(path) as archive:
        entries = archive.infolist()
        if len(entries) > 20000 or sum(item.file_size for item in entries) > 512 << 20:
            raise ValueError('Expanded document exceeds processing limits')
        names = {item.filename for item in entries}
        expected = 'word/document.xml' if kind == 'docx' else 'ppt/presentation.xml'
        if expected not in names or '[Content_Types].xml' not in names:
            raise ValueError('Office content does not match requested format')
        if any(item.flag_bits & 1 for item in entries):
            raise ValueError('Encrypted documents are unsupported')


class PageHandler(http.server.BaseHTTPRequestHandler):
    timeout = 15
    protocol_version = 'HTTP/1.1'
    server_version = 'new-api-document-pages'
    sys_version = ''

    def log_message(self, *args):
        # Never retain document names, content, or parser diagnostics.
        pass

    def answer(self, status, payload=None):
        body = b'' if payload is None else json.dumps(payload, separators=(',', ':')).encode()
        self.send_response(status)
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Connection', 'close')
        if body:
            self.send_header('Content-Type', 'application/json')
        self.end_headers()
        if body:
            self.wfile.write(body)
        self.close_connection = True

    def do_GET(self):
        self.answer(200 if self.path == '/health' else 404)

    def do_POST(self):
        request = urllib.parse.urlsplit(self.path)
        params = urllib.parse.parse_qs(request.query, keep_blank_values=True)
        if request.path != '/pages':
            self.answer(404)
            return
        if set(params) != {'format'} or len(params['format']) != 1 or params['format'][0] not in FORMATS:
            self.answer(415)
            return
        lengths = self.headers.get_all('Content-Length') or []
        if len(lengths) != 1 or not lengths[0].isascii() or not lengths[0].isdecimal() or self.headers.get('Transfer-Encoding'):
            self.answer(411)
            return
        size = int(lengths[0])
        kind = params['format'][0]
        if size < 1 or size > (MAX_IMAGE_BYTES if kind == 'image' else MAX_BYTES):
            self.answer(413)
            return
        if not self.server.document_slot.acquire(blocking=False):
            self.answer(503)
            return
        try:
            status, payload = self.count_uploaded_document(size, kind)
        except (TimeoutError, ConnectionError):
            status, payload = 408, None
        except (ValueError, OSError, zipfile.BadZipFile):
            status, payload = 422, None
        finally:
            self.server.document_slot.release()
        # A completed response also guarantees cleanup and releases the slot;
        # consecutive uploads must not race against the previous job's cleanup.
        self.answer(status, payload)

    def count_uploaded_document(self, size, kind):
        self.connection.settimeout(15)
        with tempfile.TemporaryDirectory(prefix='document-pages-', dir='/work') as name:
            directory = Path(name)
            path = directory / ('input.' + kind)
            with path.open('xb') as target:
                os.chmod(path, 0o600)
                remaining = size
                upload_deadline = time.monotonic() + 15
                while remaining:
                    budget = upload_deadline - time.monotonic()
                    if budget <= 0:
                        raise TimeoutError()
                    self.connection.settimeout(budget)
                    chunk = self.rfile.read(min(1 << 16, remaining))
                    if not chunk:
                        return 400, None
                    target.write(chunk)
                    remaining -= len(chunk)
            validate_format(path, kind)
            environment = {'PATH': '/usr/bin:/bin', 'HOME': name, 'TMPDIR': name,
                           'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8', 'SAL_USE_VCLPLUGIN': 'svp'}
            # The output pipe carries only a bounded integer. LibreOffice's
            # own output is discarded, never mixed with this response.
            with subprocess.Popen([sys.executable, __file__, '--job', str(path), '--format', kind],
                                  stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                  stderr=subprocess.DEVNULL, env=environment,
                                  start_new_session=True) as job:
                try:
                    result, _ = job.communicate(timeout=JOB_SECONDS)
                except subprocess.TimeoutExpired:
                    os.killpg(job.pid, signal.SIGKILL)
                    job.communicate()
                    return 504, None
                finally:
                    # Also remove descendants after a crashed Python/UNO job.
                    with contextlib.suppress(ProcessLookupError):
                        os.killpg(job.pid, signal.SIGKILL)
                if job.returncode != 0 or len(result) > 16:
                    return 422, None
            pages = int(result)
            if not 1 <= pages <= MAX_PAGES:
                return 422, None
            if kind == 'image':
                return 200, {'pages': pages, 'engine': self.server.image_engine, 'basis': 'local_image_pages'}
            return 200, {'pages': pages, 'engine': self.server.engine, 'basis': 'local_pagination'}


class PageServer(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    request_queue_size = 8
    allow_reuse_address = False
    daemon_threads = True

    def __init__(self, *args, **kwargs):
        self.document_slot = threading.BoundedSemaphore(1)
        self.http_slots = threading.BoundedSemaphore(8)
        super().__init__(*args, **kwargs)

    def process_request(self, request, client_address):
        if not self.http_slots.acquire(blocking=False):
            with contextlib.suppress(OSError):
                request.sendall(b'HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n')
            self.shutdown_request(request)
            return
        try:
            super().process_request(request, client_address)
        except Exception:
            self.http_slots.release()
            raise

    def process_request_thread(self, request, client_address):
        try:
            super().process_request_thread(request, client_address)
        finally:
            self.http_slots.release()

    def handle_error(self, request, client_address):
        pass


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--job', type=Path)
    parser.add_argument('--format', choices=sorted(FORMATS))
    parser.add_argument('--socket', default=SOCKET)
    args = parser.parse_args()
    if args.job:
        try:
            pages = count_image(args.job) if args.format == 'image' else count_office(args.job, args.format)
            print(pages, flush=True)
        except Exception:
            sys.exit(1)
        return
    version = subprocess.run(['/usr/bin/soffice', '--version'], capture_output=True, text=True,
                             timeout=10, check=True).stdout.strip()
    if not version.startswith('LibreOffice ') or len(version) > 128:
        raise RuntimeError('Unsupported Office runtime')
    from PIL import __version__ as pillow_version
    socket_path = Path(args.socket)
    with contextlib.suppress(FileNotFoundError):
        socket_path.unlink()
    with PageServer(str(socket_path), PageHandler) as server:
        os.chmod(socket_path, 0o660)
        server.engine = version
        server.image_engine = 'Pillow ' + pillow_version
        server.serve_forever()


if __name__ == '__main__':
    main()
