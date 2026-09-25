"""Serve the real calibre-bridge request handler with no Calibre installed.

Used by TestPluginContract in the Go suite so the Go client is exercised
against the Python code that will actually answer it, rather than against a
hand written httptest fake that can drift from it.

The mechanism is the plugin's own conftest.py: stub `calibre` and `qt` in
sys.modules before the plugin package is imported, then register the plugin
checkout under the `calibre_plugins.bindery_bridge` name Calibre would give
it. Nothing in the plugin is modified or monkeypatched, so a protocol change
on either side shows up as a failing assertion rather than as drift.

Usage: bridge_harness.py <plugin-root> [--api-key K] [--max-body N]
                                       [--unavailable N]
Prints "PORT <n>" on stdout once it is listening, or "SKIP <reason>" and
exits when the checkout is too old to serve the contract.
"""

import argparse
import inspect
import re
import sys
import types


def stub_calibre_and_qt():
    def module(name):
        mod = types.ModuleType(name)
        sys.modules[name] = mod
        return mod

    module("calibre")
    customize = module("calibre.customize")
    customize.InterfaceActionBase = object

    module("calibre.utils")
    utils_config = module("calibre.utils.config")

    class JSONConfig(dict):
        def __init__(self, name):
            super().__init__()
            self.defaults = {}

        def get(self, key, default=None):
            return super().get(key, self.defaults.get(key, default))

    utils_config.JSONConfig = JSONConfig

    utils_date = module("calibre.utils.date")
    utils_date.parse_date = lambda value: value

    module("calibre.gui2")
    gui2_actions = module("calibre.gui2.actions")
    gui2_actions.InterfaceAction = object

    module("calibre.ebooks")
    module("calibre.ebooks.metadata")
    meta = module("calibre.ebooks.metadata.meta")

    class Metadata:
        def __init__(self):
            self.title = "Stub"
            self.authors = []
            self.identifiers = {}
            self.rating = None
            self.series = None
            self.series_index = None
            self.cover_data = None

        def set_identifiers(self, ids):
            self.identifiers = dict(ids)

    meta.get_metadata = lambda stream, fmt: Metadata()

    constants = module("calibre.constants")
    constants.numeric_version = (9, 8, 0)

    qt = module("qt")
    qt_core = module("qt.core")
    for name in ("QDialog", "QDialogButtonBox", "QVBoxLayout", "QFormLayout",
                 "QLineEdit", "QSpinBox", "QWidget", "QPushButton", "QTimer"):
        setattr(qt_core, name, object)
    qt.core = qt_core


def register_plugin_package(root):
    pkg = types.ModuleType("calibre_plugins")
    pkg.__path__ = []
    sys.modules["calibre_plugins"] = pkg
    bridge = types.ModuleType("calibre_plugins.bindery_bridge")
    bridge.__path__ = [root]
    sys.modules["calibre_plugins.bindery_bridge"] = bridge
    pkg.bindery_bridge = bridge


IDENTIFIER_QUERY = re.compile(r'^identifiers:"?=([^:"]+)"?:"?=(.+?)"?$')


class FakeNewAPI:
    """The slice of Calibre's db.new_api that adder.py touches."""

    def __init__(self):
        self._next_id = 1
        self._by_identifier = {}
        self._books = {}

    def add_books(self, entries, add_duplicates=False, run_hooks=True):
        ids = []
        for mi, _formats in entries:
            book_id = self._next_id
            self._next_id += 1
            for key, value in (getattr(mi, "identifiers", None) or {}).items():
                self._by_identifier[(key, value)] = book_id
            self._books[book_id] = mi
            ids.append(book_id)
        return ids, []

    def search(self, query):
        match = IDENTIFIER_QUERY.match(query)
        if not match:
            return set()
        found = self._by_identifier.get((match.group(1), match.group(2)))
        return {found} if found else set()

    def find_identical_books(self, mi):
        return set()

    def all_book_ids(self):
        return set(self._books)

    def get_metadata(self, book_id):
        return self._books[book_id]

    def set_metadata(self, book_id, mi):
        self._books[book_id] = mi


class FakeDB:
    def __init__(self, library_path):
        self.library_path = library_path
        self.new_api = FakeNewAPI()


def split_by_signature(factory, wanted):
    """Split wanted into what factory accepts and what it does not.

    The bridge's factory grew `ingest_root` and `max_body_bytes` in the commit
    before 0.5.0 shipped, so every released 0.5.0 and 0.6.0 takes them. An
    older checkout takes neither, and passing them unconditionally raised
    TypeError before the socket was ever bound, which surfaced as "harness
    exited without reporting a port" with the real cause buried in stderr.

    The signature is asked rather than the version string because those two
    disagree inside the plugin's own history: the development commits between
    the metadata work and the hardening work already called themselves 0.5.0
    while still taking three arguments.
    """
    accepted = set(inspect.signature(factory).parameters)
    supported = {name: value for name, value in wanted.items() if name in accepted}
    missing = sorted(name for name in wanted if name not in accepted)
    return supported, missing


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("root")
    parser.add_argument("--api-key", default="")
    parser.add_argument("--max-body", type=int, default=64 * 1024 * 1024)
    parser.add_argument("--unavailable", type=int, default=0)
    parser.add_argument("--library", default="/calibre-library")
    args = parser.parse_args()

    stub_calibre_and_qt()
    register_plugin_package(args.root)

    from calibre_plugins.bindery_bridge.plugin import handlers

    version = getattr(handlers, "PLUGIN_VERSION", "an unknown version")

    db = FakeDB(args.library)
    remaining = {"unavailable": args.unavailable}

    def get_db():
        if remaining["unavailable"] > 0:
            remaining["unavailable"] -= 1
            return None
        return db

    supported, missing = split_by_signature(
        handlers.make_handler,
        {
            "api_key": args.api_key,
            "get_db": get_db,
            "get_gui": None,
            "ingest_root": "",
            "max_body_bytes": args.max_body,
        },
    )
    if missing:
        # Say which arguments were missing and which version the checkout
        # claims, because that is the pair that disagreed: every released
        # 0.5.0 takes these, so a checkout that reports 0.5.0 and does not is
        # parked on a development commit from before they landed.
        sys.stdout.write(
            "SKIP the checkout reports calibre-bridge %s, but its make_handler "
            "does not accept %s, so it predates the released 0.5.0 this "
            "contract covers. Point %s at the 0.5.0 tag or newer.\n"
            % (version, ", ".join(missing), "BINDERY_PLUGIN_SRC")
        )
        sys.stdout.flush()
        return

    handler = handlers.make_handler(**supported)

    from http.server import ThreadingHTTPServer

    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    sys.stdout.write("PORT %d\n" % server.server_address[1])
    sys.stdout.flush()
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    # The plugin logs through the logging module, which writes to stderr, so
    # stdout carries only the port line the Go side parses.
    main()
