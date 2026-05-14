#!/usr/bin/env python3
"""Local embedding helper for Best Buy compute clustering.

Input on stdin:
  {"texts": ["..."]}

Output on stdout:
  {"model": "BAAI/bge-small-en-v1.5", "vectors": [[...]]}
"""

import json
import os
import sys


def main() -> int:
    try:
        from fastembed import TextEmbedding
    except Exception as exc:  # pragma: no cover - runtime dependency guard
        print(f"fastembed import failed: {exc}", file=sys.stderr)
        return 2

    try:
        payload = json.load(sys.stdin)
    except json.JSONDecodeError as exc:
        print(f"invalid JSON input: {exc}", file=sys.stderr)
        return 2

    texts = payload.get("texts") or []
    if not isinstance(texts, list) or not all(isinstance(text, str) for text in texts):
        print("input must contain a string array field named texts", file=sys.stderr)
        return 2

    model_name = os.getenv("BESTBUY_COMPUTE_EMBED_MODEL", "BAAI/bge-small-en-v1.5")
    cache_dir = os.getenv("FASTEMBED_CACHE_PATH", "/data/embedding-cache")
    model = TextEmbedding(model_name=model_name, cache_dir=cache_dir)
    vectors = [vector.tolist() for vector in model.embed(texts)]
    json.dump({"model": model_name, "vectors": vectors}, sys.stdout)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
