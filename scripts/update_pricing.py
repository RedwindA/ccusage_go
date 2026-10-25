"""Refresh internal/pricing's embedded snapshots from an upstream ccusage checkout.

The models.dev pricing, its catalog rules and the Fast and Flex multiplier
overrides are copied verbatim. The LiteLLM table is downloaded at the revision the
checkout's flake.lock pins and reduced the way upstream's
rust/crates/ccusage-pricing-snapshots/build.rs reduces it, keeping LiteLLM's
own field names so internal/pricing reads the snapshot and a live download
the same way.

    python3 scripts/update_pricing.py ../ccusage
"""
import argparse
import json
from pathlib import Path
import re
import sys
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
PRICING_DIR = ROOT / 'internal' / 'pricing'
CATALOG_GO = PRICING_DIR / 'catalog.go'

# Upstream source path -> file name under internal/pricing.
COPIED = {
    'rust/crates/ccusage-pricing-snapshots/src/models-dev-pricing.json': 'models-dev-pricing.json',
    'rust/crates/ccusage-pricing-snapshots/src/models-dev-catalog-rules.json': 'models-dev-catalog-rules.json',
    'rust/crates/ccusage-core/src/fast-multiplier-overrides.json': 'fast-multiplier-overrides.json',
    'rust/crates/ccusage-core/src/flex-multiplier-overrides.json': 'flex-multiplier-overrides.json',
}

# The fields upstream's build.rs keeps, plus the provider entry it reads the
# service-tier multipliers from.
FIELDS = (
    'input_cost_per_token',
    'output_cost_per_token',
    'cache_creation_input_token_cost',
    'cache_read_input_token_cost',
    'input_cost_per_token_above_200k_tokens',
    'output_cost_per_token_above_200k_tokens',
    'cache_creation_input_token_cost_above_200k_tokens',
    'cache_read_input_token_cost_above_200k_tokens',
    'max_input_tokens',
    'provider_specific_entry',
)

PREFIXES = (
    'claude-', 'anthropic.', 'anthropic/', 'us.anthropic.', 'eu.anthropic.',
    'global.anthropic.', 'jp.anthropic.', 'au.anthropic.', 'gpt-', 'o3-',
    'o4-mini-', 'openai/', 'azure/', 'zai/', 'openrouter/openai/',
)

REV_LINE = re.compile(r'^(// Pinned by upstream flake\.lock: )\S+', re.M)


def is_embedded_model(model):
    return model.startswith(PREFIXES) or model in ('o3', 'o4-mini')


def compact_litellm(raw):
    """Keep the embedded model families, dropping null fields and unpriced models."""
    compact = {}
    for model, pricing in raw.items():
        if not is_embedded_model(model) or not isinstance(pricing, dict):
            continue
        fields = {f: pricing[f] for f in FIELDS if pricing.get(f) is not None}
        if 'input_cost_per_token' in fields and 'output_cost_per_token' in fields:
            compact[model] = fields
    return json.dumps(compact, indent=2, sort_keys=True) + '\n'


def litellm_source(flake_lock):
    """Return (owner/repo@rev, raw URL) for the LiteLLM input a flake.lock pins."""
    locked = json.loads(flake_lock)['nodes']['litellm']['locked']
    owner, repo, rev = locked['owner'], locked['repo'], locked['rev']
    url = f'https://raw.githubusercontent.com/{owner}/{repo}/{rev}/model_prices_and_context_window.json'
    return f'{owner}/{repo}@{rev}', url


def update(upstream, litellm_json=None, out=PRICING_DIR, catalog_go=CATALOG_GO):
    upstream = Path(upstream)
    pin, url = litellm_source((upstream / 'flake.lock').read_text(encoding='utf-8'))
    go, count = REV_LINE.subn(lambda m: m.group(1) + pin, Path(catalog_go).read_bytes().decode())
    if count != 1:
        raise SystemExit(f'{catalog_go}: expected one "// Pinned by upstream flake.lock:" line')
    if litellm_json is None:
        with urllib.request.urlopen(url, timeout=60) as response:
            raw = response.read()
    else:
        raw = Path(litellm_json).read_bytes()
    snapshot = compact_litellm(json.loads(raw)).encode()
    copies = {name: (upstream / source).read_bytes() for source, name in COPIED.items()}
    (Path(out) / 'litellm-pricing.json').write_bytes(snapshot)
    for name, data in copies.items():
        (Path(out) / name).write_bytes(data)
    Path(catalog_go).write_bytes(go.encode())
    return pin


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('upstream', help='upstream ccusage checkout to copy the snapshots from')
    parser.add_argument('--litellm-json', help='use this model_prices_and_context_window.json instead of downloading it')
    args = parser.parse_args(argv)
    pin = update(args.upstream, args.litellm_json)
    print(f'LiteLLM {pin}')


if __name__ == '__main__':
    sys.exit(main())
