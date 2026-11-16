import contextlib
import hashlib
import http.client
import io
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import threading
import unittest
import xml.etree.ElementTree as ET
import zipfile
import zlib

import worker


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__('document-pages', timeout=75)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


class OfficePaginationTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        import uno
        from PIL import __version__ as pillow_version
        cls.directory = tempfile.TemporaryDirectory(prefix='synthetic-office-test-', dir='/work')
        cls.root = Path(cls.directory.name)
        profile = cls.root / 'generator'
        profile.mkdir()
        office, desktop = worker.start_office(profile)
        try:
            hidden = (worker.property_value('Hidden', True), worker.property_value('MacroExecutionMode', 0))
            document = desktop.loadComponentFromURL('private:factory/swriter', '_blank', 0, hidden)
            document.Text.String = 'Synthetic page one. 中文分页测试。 No user data.'
            cursor = document.Text.createTextCursor()
            cursor.gotoEnd(False)
            document.Text.insertControlCharacter(cursor, 0, False)
            cursor.BreakType = uno.Enum('com.sun.star.style.BreakType', 'PAGE_BEFORE')
            document.Text.insertString(cursor, 'Synthetic page two. No user data.', False)
            for extension, name in [('docx', 'Office Open XML Text'), ('doc', 'MS Word 97')]:
                document.storeToURL((cls.root / ('synthetic.' + extension)).as_uri(), (worker.property_value('FilterName', name),))
            document.close(True)
            document = desktop.loadComponentFromURL('private:factory/simpress', '_blank', 0, hidden)
            slides = document.getDrawPages()
            while slides.getCount() < 2:
                slides.insertNewByIndex(slides.getCount())
            for extension, name in [('pptx', 'Impress MS PowerPoint 2007 XML'), ('ppt', 'MS PowerPoint 97')]:
                document.storeToURL((cls.root / ('synthetic.' + extension)).as_uri(), (worker.property_value('FilterName', name),))
            document.close(True)
        finally:
            with contextlib.suppress(Exception):
                desktop.terminate()
            with contextlib.suppress(Exception):
                office.wait(timeout=5)
            if office.poll() is None:
                office.kill()
        # Tamper only the cached metadata, never the document's actual layout.
        docx = cls.root / 'synthetic.docx'
        output = io.BytesIO()
        changed = False
        with zipfile.ZipFile(docx) as original, zipfile.ZipFile(output, 'w', zipfile.ZIP_DEFLATED) as revised:
            for info in original.infolist():
                data = original.read(info.filename)
                if info.filename == 'docProps/app.xml':
                    properties = ET.fromstring(data)
                    found_pages = False
                    for value in properties.iter():
                        if value.tag.rsplit('}', 1)[-1] == 'Pages':
                            value.text = '999'
                            found_pages = True
                    if not found_pages:
                        ET.SubElement(properties, '{http://schemas.openxmlformats.org/officeDocument/2006/extended-properties}Pages').text = '999'
                    changed = True
                    data = ET.tostring(properties, encoding='utf-8', xml_declaration=True)
                revised.writestr(info, data)
        if not changed:
            raise AssertionError('Synthetic fixture lacks cached page metadata')
        docx.write_bytes(output.getvalue())
        cls.socket_path = cls.root / 'pages.sock'
        cls.server = worker.PageServer(str(cls.socket_path), worker.PageHandler)
        cls.server.engine = subprocess.check_output(['/usr/bin/soffice', '--version'], text=True).strip()
        cls.server.image_engine = 'Pillow ' + pillow_version
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join(timeout=3)
        cls.directory.cleanup()

    def request(self, method, path, body=None, headers=None):
        connection = UnixHTTPConnection(self.socket_path)
        try:
            connection.request(method, path, body=body, headers=headers or {})
            response = connection.getresponse()
            return response.status, response.read()
        finally:
            connection.close()

    def test_real_office_pages_and_original_bytes(self):
        import json
        for extension in ('docx', 'doc', 'pptx', 'ppt'):
            with self.subTest(format=extension):
                path = self.root / ('synthetic.' + extension)
                data = path.read_bytes()
                before = hashlib.sha256(data).hexdigest()
                status, body = self.request('POST', '/pages?format=' + extension, data)
                self.assertEqual(status, 200, body)
                result = json.loads(body)
                self.assertEqual(result['pages'], 2)
                self.assertEqual(result['basis'], 'local_pagination')
                self.assertTrue(result['engine'].startswith('LibreOffice '))
                self.assertEqual(set(result), {'pages', 'engine', 'basis'})
                self.assertEqual(hashlib.sha256(path.read_bytes()).hexdigest(), before)
        self.assertFalse(list(Path('/work').glob('document-pages-*')))

    def test_invalid_documents_do_not_return_a_page_estimate(self):
        for kind, data in [('docx', b'not a zip document'), ('doc', b'not an OLE document'), ('pptx', b'PK\x03\x04broken')]:
            with self.subTest(format=kind):
                status, body = self.request('POST', '/pages?format=' + kind, data)
                self.assertEqual(status, 422)
                self.assertEqual(body, b'')
        status, body = self.request('POST', '/pages?format=txt', b'text')
        self.assertEqual((status, body), (415, b''))

    def test_real_image_frames_and_original_bytes(self):
        import json
        from PIL import Image
        for kind, pages in [('PNG', 1), ('JPEG', 1), ('WEBP', 1), ('GIF', 1), ('BMP', 1), ('TIFF', 1),
                            ('TIFF', 3), ('GIF', 3), ('WEBP', 3), ('PNG', 3), ('TIFF', 1000)]:
            with self.subTest(format=kind, pages=pages):
                output = io.BytesIO()
                frame = Image.new('RGB', (8, 8), 'red')
                options = {}
                if pages > 1:
                    options = {'save_all': True, 'append_images': [Image.new('RGB', (8, 8),
                               'blue' if index % 2 == 0 else 'green') for index in range(pages - 1)]}
                frame.save(output, format=kind, **options)
                data = output.getvalue()
                before = hashlib.sha256(data).hexdigest()
                # No extension is sent to the image decoder; magic bytes decide.
                status, body = self.request('POST', '/pages?format=image', data)
                self.assertEqual(status, 200, body)
                result = json.loads(body)
                self.assertEqual(result['pages'], pages)
                self.assertEqual(result['basis'], 'local_image_pages')
                self.assertTrue(result['engine'].startswith('Pillow '))
                self.assertEqual(set(result), {'pages', 'basis', 'engine'})
                self.assertEqual(hashlib.sha256(data).hexdigest(), before)
        self.assertFalse(list(Path('/work').glob('document-pages-*')))

    def test_invalid_and_oversized_images_do_not_return_estimates(self):
        from PIL import Image
        valid = io.BytesIO()
        Image.new('RGB', (8, 8), 'red').save(valid, format='PNG')
        data = valid.getvalue()
        # Valid PNG header/checksum with an unsafe advertised pixel count.
        ihdr = struct.pack('>II', 100_000, 100_000) + data[24:29]
        bomb = data[:16] + ihdr + struct.pack('>I', zlib.crc32(b'IHDR' + ihdr)) + data[33:]
        oversized = io.BytesIO()
        Image.new('RGB', (1, 1), 'red').save(oversized, format='TIFF', save_all=True,
            append_images=[Image.new('RGB', (1, 1), 'blue') for _ in range(worker.MAX_PAGES)])
        unsupported = io.BytesIO()
        Image.new('RGB', (32, 32), 'red').save(unsupported, format='ICO')
        for name, payload in [('not image', b'not an image'), ('truncated PNG', data[:40]),
                              ('pixel bomb', bomb), ('too many frames', oversized.getvalue()),
                              ('unsupported format', unsupported.getvalue())]:
            with self.subTest(name=name):
                self.assertEqual(self.request('POST', '/pages?format=image', payload), (422, b''))
                self.assertEqual(self.request('GET', '/health'), (200, b''))
        self.assertEqual(self.request('POST', '/pages?format=image', b'',
                         {'Content-Length': str(worker.MAX_IMAGE_BYTES + 1)}), (413, b''))
        self.assertFalse(list(Path('/work').glob('document-pages-*')))

    def test_busy_rejects_immediately_but_health_stays_available(self):
        self.server.document_slot.acquire()
        try:
            status, body = self.request('POST', '/pages?format=docx', b'example')
            self.assertEqual((status, body), (503, b''))
            self.assertEqual(self.request('GET', '/health'), (200, b''))
        finally:
            self.server.document_slot.release()

    def test_timed_out_parser_is_removed_and_next_request_succeeds(self):
        data = (self.root / 'synthetic.docx').read_bytes()
        before = worker.JOB_SECONDS
        worker.JOB_SECONDS = 0
        try:
            self.assertEqual(self.request('POST', '/pages?format=docx', data), (504, b''))
        finally:
            worker.JOB_SECONDS = before
        self.assertEqual(self.request('GET', '/health'), (200, b''))
        self.assertEqual(self.request('POST', '/pages?format=docx', data)[0], 200)
        self.assertFalse(list(Path('/work').glob('document-pages-*')))

    def test_oversized_upload_is_rejected_before_reading(self):
        status, body = self.request('POST', '/pages?format=docx', b'',
                                    {'Content-Length': str(worker.MAX_BYTES + 1)})
        self.assertEqual((status, body), (413, b''))
        self.assertEqual(self.request('GET', '/health'), (200, b''))


if __name__ == '__main__':
    os.umask(0o077)
    unittest.main(verbosity=2)
