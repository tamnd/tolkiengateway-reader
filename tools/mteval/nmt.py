# /// script
# requires-python = ">=3.11,<3.13"
# dependencies = ["torch", "transformers<5", "sentencepiece", "protobuf"]
#
# [tool.uv.sources]
# torch = { index = "pytorch-cpu" }
#
# [[tool.uv.index]]
# name = "pytorch-cpu"
# url = "https://download.pytorch.org/whl/cpu"
# explicit = true
# ///
"""Translate sample.jsonl with a local NMT model, for mteval.py to score.

    uv run nmt.py vinai [limit]     # vinai/vinai-translate-en2vi-v2
    uv run nmt.py opus [limit]      # Helsinki-NLP/opus-mt-en-vi

Each line goes through the model masked, as for Google. These models were not
trained on markup and often drop the tags; then the text between the tags is
translated piece by piece instead, which keeps the page whole at some cost to
fluency. The output records how many lines needed that.
"""

from __future__ import annotations

import json
import re
import sys
import time
from pathlib import Path

import torch
from transformers import AutoModelForSeq2SeqLM, AutoTokenizer

import markup

HERE = Path(__file__).parent
MODELS = {
    "vinai": ("vinai/vinai-translate-en2vi-v2", dict(src_lang="en_XX"), "vi_VN"),
    "opus": ("Helsinki-NLP/opus-mt-en-vi", {}, None),
}
_TAG = re.compile(r"(<[xabi]\d+/?>|</[xabi]\d+>)")
_SENTENCE = re.compile(r"(?<=[.!?])\s+(?=[A-Z<])")


class Engine:
    def __init__(self, name: str):
        repo, tok_args, self.lang = MODELS[name]
        self.tok = AutoTokenizer.from_pretrained(repo, **tok_args)
        self.model = AutoModelForSeq2SeqLM.from_pretrained(repo).eval()
        torch.set_num_threads(4)

    def __call__(self, texts: list[str]) -> list[str]:
        out = []
        for i in range(0, len(texts), 8):
            batch = self.tok(texts[i:i + 8], return_tensors="pt", padding=True,
                             truncation=True, max_length=512)
            extra = {}
            if self.lang:
                extra["decoder_start_token_id"] = self.tok.lang_code_to_id[self.lang]
            with torch.no_grad():
                ids = self.model.generate(**batch, num_beams=4, max_length=512, **extra)
            out += self.tok.batch_decode(ids, skip_special_tokens=True)
        return out


def translate_line(engine: Engine, line: str) -> tuple[str, bool]:
    """A masked line in Vietnamese, and whether it had to be done in pieces."""
    if not re.search(r"[A-Za-z]", _TAG.sub("", line)):
        return line, False
    sentences = _SENTENCE.split(line)
    whole = " ".join(engine(sentences))
    if sorted(_TAG.findall(whole)) == sorted(_TAG.findall(line)):
        return whole, False
    parts = _TAG.split(line)
    todo = [i for i, p in enumerate(parts)
            if not _TAG.fullmatch(p) and re.search(r"[A-Za-z]", p)]
    for i, text in zip(todo, engine([parts[i].strip() for i in todo])):
        lead = " " if parts[i][:1].isspace() else ""
        trail = " " if parts[i][-1:].isspace() else ""
        parts[i] = lead + text + trail
    return "".join(parts), True


def main() -> None:
    name = sys.argv[1]
    limit = int(sys.argv[2]) if len(sys.argv) > 2 else None
    engine = Engine(name)
    out = HERE / "out" / f"{name}.jsonl"
    out.parent.mkdir(exist_ok=True)
    done = {json.loads(l)["id"] for l in out.read_text().splitlines()} if out.exists() else set()
    rows = [json.loads(l) for l in (HERE / "sample.jsonl").read_text().splitlines()][:limit]
    with out.open("a") as w:
        for r in rows:
            if r["id"] in done:
                continue
            start = time.monotonic()
            m = markup.mask(r["source"])
            lines = [translate_line(engine, l) for l in m.text.split("\n")]
            text, problems = markup.unmask("\n".join(l for l, _ in lines), m)
            w.write(json.dumps({"id": r["id"], "text": text, "tag_problems": problems,
                                "pieced": sum(p for _, p in lines),
                                "seconds": round(time.monotonic() - start, 2)},
                               ensure_ascii=False) + "\n")
            w.flush()
            print(r["id"][:8], round(time.monotonic() - start, 1), "s", flush=True)


if __name__ == "__main__":
    main()
