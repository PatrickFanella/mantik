import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import urllib.error

from collect_closes import collect


class CollectTest(unittest.TestCase):
    def test_bounded_retry_and_retained_resume(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            credentials = root / "credentials.json"
            credentials.write_text(json.dumps(dict(key="fixture", secret="fixture")))
            os.chmod(credentials, 0o600)
            calendar = root / "calendar.json"
            calendar.write_text(json.dumps([dict(date="2020-03-12", close="16:00")]))
            args = SimpleNamespace(credentials=credentials, calendar=calendar,
                start="2020-03-12", end="2020-03-12", output=root / "pages")
            body = json.dumps(dict(symbol="SPY", trades=None, next_page_token=None)).encode()
            response = io.BytesIO(body)
            failure = urllib.error.HTTPError("https://fixture", 503, "fixture", {}, None)
            with patch("urllib.request.build_opener") as opener, patch("time.sleep"), patch("builtins.print"):
                opener.return_value.open.side_effect = [failure, response]
                collect(args)
                self.assertEqual(opener.return_value.open.call_count, 2)
                # The collector resumes even an empty original; normalization
                # separately refuses unsupported missing-auction evidence.
                collect(args)
                self.assertEqual(opener.return_value.open.call_count, 2)
                page = json.loads((args.output / "2020-03-12-0.json").read_text())
                self.assertEqual(page["original"], body.decode())
                page["sha256"] = "0"*64
                (args.output / "2020-03-12-0.json").write_text(json.dumps(page))
                with self.assertRaisesRegex(ValueError, "digest mismatch"):
                    collect(args)
