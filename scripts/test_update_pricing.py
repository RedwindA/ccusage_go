"""Check the pricing snapshot refresh offline against small upstream fixtures."""
import json
from pathlib import Path
import re
import tempfile
import unittest

import update_pricing

FLAKE_LOCK = {
    'nodes': {
        'litellm': {
            'locked': {'owner': 'BerriAI', 'repo': 'litellm', 'rev': 'd3b70c42ed1599727669ea7883c3971008a9113d', 'type': 'github'},
        },
    },
}


class CompactTest(unittest.TestCase):
    def test_keeps_only_priced_models_of_the_embedded_families(self):
        raw = {
            'claude-opus-5-5': {
                'input_cost_per_token': 4e-06,
                'output_cost_per_token': 2e-05,
                'cache_read_input_token_cost': None,
                'max_input_tokens': 1000000,
                'litellm_provider': 'anthropic',
                'provider_specific_entry': {'fast': 2, 'us': 1.1},
            },
            'o3': {'input_cost_per_token': 2e-06, 'output_cost_per_token': 8e-06},
            'o3-pro': {'input_cost_per_token': 2e-05, 'output_cost_per_token': 8e-05},
            'o4-mini': {'input_cost_per_token': 1.1e-06, 'output_cost_per_token': 4.4e-06},
            'o4-mini-deep-research': {'input_cost_per_token': 2e-06, 'output_cost_per_token': 8e-06},
            'o1': {'input_cost_per_token': 1.5e-05, 'output_cost_per_token': 6e-05},
            'o4': {'input_cost_per_token': 1e-06, 'output_cost_per_token': 1e-06},
            'o3x': {'input_cost_per_token': 1e-06, 'output_cost_per_token': 1e-06},
            'gemini-4-argon': {'input_cost_per_token': 2e-06, 'output_cost_per_token': 1e-05},
            'gpt-image-1': {'input_cost_per_token': 5e-06},
            'gpt-6-sol': {'input_cost_per_token': 2e-06, 'output_cost_per_token': None},
            'gpt-6-luna': 'not an object',
        }
        compact = update_pricing.compact_litellm(raw)
        self.assertEqual(json.loads(compact), {
            'claude-opus-5-5': {
                'input_cost_per_token': 4e-06,
                'max_input_tokens': 1000000,
                'output_cost_per_token': 2e-05,
                'provider_specific_entry': {'fast': 2, 'us': 1.1},
            },
            'o3': {'input_cost_per_token': 2e-06, 'output_cost_per_token': 8e-06},
            'o3-pro': {'input_cost_per_token': 2e-05, 'output_cost_per_token': 8e-05},
            'o4-mini': {'input_cost_per_token': 1.1e-06, 'output_cost_per_token': 4.4e-06},
            'o4-mini-deep-research': {'input_cost_per_token': 2e-06, 'output_cost_per_token': 8e-06},
        })
        self.assertTrue(compact.startswith('{\n  "claude-opus-5-5": {\n    "input_cost_per_token": 4e-06,'))
        self.assertTrue(compact.endswith('\n}\n'))

    def test_copies_every_embedded_snapshot_but_litellm(self):
        embedded = set()
        for source in update_pricing.PRICING_DIR.glob('*.go'):
            embedded.update(re.findall(r'^//go:embed (\S+\.json)$', source.read_text(encoding='utf-8'), re.M))
        self.assertEqual(sorted(embedded - {'litellm-pricing.json'}), sorted(update_pricing.COPIED.values()))

    def test_committed_snapshot_is_generator_output(self):
        committed = (update_pricing.PRICING_DIR / 'litellm-pricing.json').read_text(encoding='utf-8')
        self.assertEqual(update_pricing.compact_litellm(json.loads(committed)), committed)
        catalog = update_pricing.CATALOG_GO.read_text(encoding='utf-8')
        self.assertEqual(len(update_pricing.REV_LINE.findall(catalog)), 1)


class UpdateTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.upstream = self.root / 'ccusage'
        for source in update_pricing.COPIED:
            path = self.upstream / source
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(('{\n\t"source": "%s"\n}\n' % path.name).encode())
        (self.upstream / 'flake.lock').write_text(json.dumps(FLAKE_LOCK))
        self.out = self.root / 'pricing'
        self.out.mkdir()
        self.catalog = self.out / 'catalog.go'
        self.catalog.write_bytes(b'package pricing\n\n// Pinned by upstream flake.lock: BerriAI/litellm@0000\n//\n//go:embed litellm-pricing.json\n')
        self.litellm = self.root / 'model_prices_and_context_window.json'
        self.litellm.write_text(json.dumps({
            'gpt-6-sol': {'input_cost_per_token': 2e-06, 'output_cost_per_token': 1e-05, 'mode': 'chat'},
            'gemini-4-argon': {'input_cost_per_token': 2e-06, 'output_cost_per_token': 1e-05},
        }))

    def test_writes_snapshots_and_pins_the_flake_lock_revision(self):
        pin = update_pricing.update(self.upstream, self.litellm, self.out, self.catalog)

        self.assertEqual(pin, 'BerriAI/litellm@d3b70c42ed1599727669ea7883c3971008a9113d')
        self.assertEqual(
            (self.out / 'litellm-pricing.json').read_text(),
            '{\n  "gpt-6-sol": {\n    "input_cost_per_token": 2e-06,\n    "output_cost_per_token": 1e-05\n  }\n}\n',
        )
        for source, name in update_pricing.COPIED.items():
            self.assertEqual((self.out / name).read_bytes(), (self.upstream / source).read_bytes())
        self.assertEqual(
            self.catalog.read_text(),
            'package pricing\n\n// Pinned by upstream flake.lock: BerriAI/litellm@d3b70c42ed1599727669ea7883c3971008a9113d\n//\n//go:embed litellm-pricing.json\n',
        )

    def test_downloads_the_pinned_revision(self):
        self.assertEqual(
            update_pricing.litellm_source(json.dumps(FLAKE_LOCK))[1],
            'https://raw.githubusercontent.com/BerriAI/litellm/d3b70c42ed1599727669ea7883c3971008a9113d/model_prices_and_context_window.json',
        )

    def test_refuses_a_catalog_without_the_pin_line_before_writing(self):
        self.catalog.write_text('package pricing\n')
        with self.assertRaises(SystemExit):
            update_pricing.update(self.upstream, self.litellm, self.out, self.catalog)
        self.assertEqual(sorted(path.name for path in self.out.iterdir()), ['catalog.go'])


if __name__ == '__main__':
    unittest.main()
