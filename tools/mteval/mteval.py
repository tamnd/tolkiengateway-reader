# /// script
# requires-python = ">=3.11"
# dependencies = ["requests", "sacrebleu"]
# ///
"""Score machine translation engines on real Tolkien Gateway chunks.

    uv run mteval.py translate google      # fills out/google.jsonl
    uv run mteval.py report gpt google     # scores every engine named

sample.jsonl holds chunks the translator sent to ChatGPT, with ChatGPT's
answer as `gpt`. Each engine writes out/<engine>.jsonl with one `text` per
chunk id, so engines that run elsewhere (the NMT models on a server) only need
to produce that file.

There is no human reference, so no single number is the truth. The report
puts side by side what can be checked without one:

  markup   the page still works: headings, anchors, link targets and HTML
           tags are the source's, as tgw's own validator demands
  names    Tolkien names and titles the source links to are kept as written,
           which the style rules ask for
  titles   italic titles (The Lord of the Rings) are kept as written
  nums     every year and count in the source is still in the output
  english  share of output words that are common English words, i.e. text
           the engine left untranslated
  back     chrF of a back translation into English against the source, a
           fidelity proxy (it flatters Google, which does the back translation)
  vs-gpt   chrF against ChatGPT's translation, agreement with the current one
"""

from __future__ import annotations

import collections
import json
import os
import re
import sys
import time
import urllib.parse
from pathlib import Path

import requests
import sacrebleu

import markup

HERE = Path(__file__).parent
OUT = HERE / "out"


def load(path: Path) -> dict[str, dict]:
    if not path.exists():
        return {}
    return {r["id"]: r for r in map(json.loads, path.read_text().splitlines()) if r}


def sample() -> list[dict]:
    return list(load(HERE / "sample.jsonl").values())


def engine_texts(name: str) -> dict[str, str]:
    if name == "gpt":
        return {r["id"]: r["gpt"] for r in sample()}
    return {k: v["text"] for k, v in load(OUT / f"{name}.jsonl").items()}


# ---------------------------------------------------------------- engines

def google(text: str, sl: str = "en", tl: str = "vi") -> str:
    """The free endpoint the Translate web widgets use. Unofficial: fine for an
    evaluation, not something to put 25 MB through."""
    for attempt in range(5):
        r = requests.post(
            "https://translate.googleapis.com/translate_a/single",
            params={"client": "gtx", "sl": sl, "tl": tl, "dt": "t"},
            data={"q": text}, timeout=60)
        if r.status_code == 200:
            return "".join(seg[0] for seg in r.json()[0] if seg[0])
        time.sleep(2 ** attempt)
    r.raise_for_status()
    return ""


def translate_masked(fn, source: str) -> tuple[str, list[str]]:
    m = markup.mask(source)
    return markup.unmask(fn(m.text), m)


ENGINES = {"google": google}


def run_translate(name: str) -> None:
    fn = ENGINES[name]
    OUT.mkdir(exist_ok=True)
    path = OUT / f"{name}.jsonl"
    done = load(path)
    with path.open("a") as w:
        for r in sample():
            if r["id"] in done:
                continue
            start = time.monotonic()
            text, problems = translate_masked(fn, r["source"])
            w.write(json.dumps({"id": r["id"], "text": text, "tag_problems": problems,
                                "seconds": round(time.monotonic() - start, 2)},
                               ensure_ascii=False) + "\n")
            w.flush()
            time.sleep(0.3)


# ---------------------------------------------------------------- metrics

_HEADING = re.compile(r"^(#{1,6})\s.*?(\{#[^}]*\})?\s*$", re.M)
_TARGET = re.compile(r"\]\((<[^>]*>|[^)\s]*)")
_HTML = re.compile(r"<[A-Za-z/][^<>]*>")


_ESCAPED = re.compile(r"\\([!-/:-@\[-`{-~])")
_AUTOLINK = re.compile(r"\[(https?://[^\]\s]+)\]\(\1\)")


def tidy(text: str) -> str:
    """What tgw does to every answer before validating it: undo backslash
    escapes and the links chat models wrap around bare URLs."""
    text = re.sub(r"</?escape>", "", text)
    return _ESCAPED.sub(r"\1", _AUTOLINK.sub(r"\1", text))


def markup_ok(source: str, text: str) -> bool:
    """The same checks tgw's validateChunk makes before it accepts a chunk."""
    source, text = tidy(source), tidy(text)
    def shape(s: str):
        return ([(h, a) for h, a in _HEADING.findall(s)],
                collections.Counter(_TARGET.findall(s)),
                collections.Counter(_HTML.findall(s)))
    return shape(source) == shape(text)


_NAMED_LINK = re.compile(r"(\*?)\[([^\]]+)\]\((?:<)?([^)>\s]*)")
_DICT = Path("/usr/share/dict/words")
_WORDS = {w.lower() for w in _DICT.read_text().split()} if _DICT.exists() else set()


def english_word(w: str) -> bool:
    w = w.lower()
    return any(x in _WORDS for x in (w, w.removesuffix("s"), w.removesuffix("es"),
                                     w.removesuffix("'s")))


def kept_names(source: str) -> tuple[list[str], list[str]]:
    """What must survive translation as written: the words no English
    dictionary has in link texts that name their page (Angband in Siege of
    Angband, Dor-lómin, Tolkien), and the italic titles (The Lord of the
    Rings). The English around them, as in Misty Mountains, may be translated."""
    names, titles = [], []
    for star, text, target in _NAMED_LINK.findall(source):
        page = urllib.parse.unquote(urllib.parse.unquote(target.rsplit("/", 1)[-1]))
        page = page.removesuffix(".md").replace("_", " ")
        page = re.sub(r"\s*\(.*\)$", "", page)
        clean = re.sub(r"[*_]", "", text).strip()
        if not clean or not clean[0].isupper() or clean != page:
            continue
        if star:
            titles.append(clean)
        else:
            names += [w for w in re.findall(r"[^\W\d_][\w'-]*", clean) if not english_word(w)]
    return names, titles


_ENGLISH = set("""the of and to in is was were that with for as by on at from his
her their which who this be had has have not but or an are it its also into
they he she him them would could should after before during when where than
then there these those however because""".split())
_WORD = re.compile(r"[A-Za-zÀ-ỹ]+")


def plain(md: str) -> str:
    md = re.sub(r"<sup\b.*?</sup>", "", md, flags=re.S)
    md = re.sub(r"\]\([^)]*\)", "]", md)
    md = re.sub(r"<[^>]+>|\{#[^}]*\}|[*_#\[\]`|>]", "", md)
    return re.sub(r"\s+", " ", md).strip()


def english_share(text: str, names: list[str]) -> float:
    t = plain(text)
    for n in names:
        t = t.replace(n, " ")
    words = [w.lower() for w in _WORD.findall(t)]
    return sum(w in _ENGLISH for w in words) / max(1, len(words))


_NUMBER = re.compile(r"(?<![\w.:&])\d+(?:[,.]\d{3})*(?![\w:;])")


def numbers_kept(source: str, text: str) -> tuple[int, int]:
    """Years and counts in the source that the output still has; engines turn
    1630 into 16:30 or 1,000 into 1.000."""
    def numbers(t: str) -> collections.Counter:
        # Vietnamese groups thousands with dots, English with commas.
        return collections.Counter(re.sub(r"[,.]", "", n) for n in _NUMBER.findall(plain(t)))
    want, have = numbers(source), numbers(text)
    return sum((want & have).values()), sum(want.values())


def chrf(hyp: str, ref: str) -> float:
    return sacrebleu.sentence_chrf(hyp, [ref]).score


def back_translation(name: str, texts: dict[str, str]) -> dict[str, str]:
    """Cached; only fetched with MTEVAL_BACK=1, since Google rate limits fast."""
    path = OUT / f"{name}.back.jsonl"
    done = {k: v["text"] for k, v in load(path).items()}
    if not os.environ.get("MTEVAL_BACK"):
        return done
    with path.open("a") as w:
        for k, t in texts.items():
            if k in done:
                continue
            done[k] = google(plain(t), sl="vi", tl="en")
            w.write(json.dumps({"id": k, "text": done[k]}, ensure_ascii=False) + "\n")
            time.sleep(0.3)
    return done


def report(names: list[str]) -> None:
    src = {r["id"]: r for r in sample()}
    OUT.mkdir(exist_ok=True)
    rows = []
    for name in names:
        texts = engine_texts(name)
        ids = [k for k in src if k in texts]
        back = back_translation(name, {k: texts[k] for k in ids})
        m = n_names = kept = n_titles = kept_titles = nums = n_nums = 0
        eng = bk = vs = 0.0
        n_back = 0
        for k in ids:
            s, t = src[k]["source"], texts[k]
            m += markup_ok(s, t)
            ns, ts = kept_names(s)
            n_names += len(ns)
            kept += sum(x in t for x in ns)
            n_titles += len(ts)
            kept_titles += sum(x in t for x in ts)
            eng += english_share(t, ns + ts)
            a, b = numbers_kept(s, t)
            nums, n_nums = nums + a, n_nums + b
            if k in back:
                bk += chrf(back[k], plain(s))
                n_back += 1
            vs += chrf(plain(t), plain(src[k]["gpt"]))
        n = max(1, len(ids))
        rows.append((name, len(ids), 100 * m / n, 100 * kept / max(1, n_names),
                     100 * kept_titles / max(1, n_titles), 100 * nums / max(1, n_nums),
                     100 * eng / n,
                     bk / max(1, n_back), vs / n))
    print(f"{'engine':10} {'chunks':>6} {'markup%':>8} {'names%':>7} {'titles%':>8} {'nums%':>6} {'english%':>9} {'back':>6} {'vs-gpt':>7}")
    for r in rows:
        print(f"{r[0]:10} {r[1]:6d} {r[2]:8.1f} {r[3]:7.1f} {r[4]:8.1f} {r[5]:6.1f} {r[6]:9.1f} {r[7]:6.1f} {r[8]:7.1f}")


if __name__ == "__main__":
    cmd, *args = sys.argv[1:]
    if cmd == "translate":
        run_translate(args[0])
    elif cmd == "report":
        report(args)
