"""Hide Markdown and HTML markup from a machine translator, and put it back.

Plain MT engines translate text, not Markdown. Handed a Tolkien Gateway chunk
as is, they translate link targets, drop anchors and respace emphasis. So the
chunk is rewritten as text with small numbered XML tags, which Google and most
NMT models carry through:

    [Anduin](../A/Anduin.md)        ->  <a3>Anduin</a3>
    **Ethir Anduin**                ->  <b4>Ethir Anduin</b4>
    <sup id="cite_ref-1">…</sup>    ->  <x5/>
    {#History}                      ->  <x6/>

Only the visible text inside a pair is translated. unmask() rebuilds the
Markdown from the table, and reports tags the engine lost or invented.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field

# Units that are never translated: citation superscripts, whole, then any other
# HTML tag on its own, heading anchors, images, bare URLs and code spans.
_SUP = re.compile(r"<sup\b[^>]*>.*?</sup>", re.S)
_OPAQUE = re.compile(
    r"<(?!/?[xabi]\d+/?>)[A-Za-z/!][^<>\n]*>"
    r"|\{#[^}\n]*\}"
    r"|!\[[^\]\n]*\]\([^)\n]*\)"
    r"|`[^`\n]+`"
    r"|https?://\S+",
    re.S,
)
# [text](target "title"), with angle-bracket targets allowed.
_LINK = re.compile(r"\[((?:[^\[\]\n]|\[[^\]\n]*\])+)\]\((<[^>\n]*>|[^()\s]*(?:\([^()\s]*\)[^()\s]*)*)(\s+\"[^\"\n]*\")?\)")
_STRONG = re.compile(r"\*\*(?=\S)(.+?)(?<=\S)\*\*")
_EM = re.compile(r"(?<![*\w])\*(?=\S)([^*\n]+?)(?<=\S)\*(?![*\w])")
# Line prefixes kept outside the text: headings, list markers, quotes.
_PREFIX = re.compile(r"^(\s*(?:#{1,6}\s+|[-*+]\s+|\d+\.\s+|>\s*)*)")


@dataclass
class Masked:
    text: str
    table: dict[str, tuple[str, str]] = field(default_factory=dict)
    # kind per id: "x" opaque, "a" link, "b" strong, "i" emphasis


def mask(md: str) -> Masked:
    table: dict[str, tuple[str, str]] = {}
    counter = [0]

    def new_id(kind: str, payload: str, extra: str = "") -> str:
        counter[0] += 1
        tid = f"{kind}{counter[0]}"
        table[tid] = (payload, extra)
        return tid

    def opaque(m: re.Match) -> str:
        return f"<{new_id('x', m.group(0))}/>"

    def link(m: re.Match) -> str:
        tid = new_id("a", m.group(2), m.group(3) or "")
        return f"<{tid}>{m.group(1)}</{tid}>"

    def pair(kind: str):
        def sub(m: re.Match) -> str:
            tid = new_id(kind, "")
            return f"<{tid}>{m.group(1)}</{tid}>"
        return sub

    lines = []
    for line in md.split("\n"):
        prefix = _PREFIX.match(line).group(1)
        body = line[len(prefix):]
        if prefix:
            prefix = f"<{new_id('x', prefix)}/>"
        body = _SUP.sub(opaque, body)
        body = _LINK.sub(link, body)
        body = _OPAQUE.sub(opaque, body)
        body = _STRONG.sub(pair("b"), body)
        body = _EM.sub(pair("i"), body)
        lines.append(prefix + body)
    return Masked("\n".join(lines), table)


_TAG = re.compile(r"<\s*(/?)\s*([xabi]\d+)\s*(/?)\s*>")


def unmask(text: str, masked: Masked) -> tuple[str, list[str]]:
    """The Markdown back, and the tag ids the engine lost, repeated or made up."""
    seen: set[str] = set()
    problems: list[str] = []

    def sub(m: re.Match) -> str:
        closing, tid, selfclose = m.group(1), m.group(2), m.group(3)
        if tid not in masked.table:
            problems.append(f"invented {tid}")
            return ""
        payload, extra = masked.table[tid]
        kind = tid[0]
        if kind == "x":
            if tid in seen:
                problems.append(f"repeated {tid}")
            seen.add(tid)
            return payload
        if not closing:
            if tid in seen:
                problems.append(f"repeated {tid}")
            seen.add(tid)
        if kind == "a":
            return f"]({payload}{extra})" if closing else "["
        if kind == "b":
            return "**"
        return "*"

    # Engines put spaces just inside a pair that the source did not have, and
    # in Markdown "** text **" is no longer emphasis.
    text = re.sub(r"(<[abi]\d+>)\s+", r"\1", text)
    text = re.sub(r"\s+(</[abi]\d+>)", r"\1", text)
    out = _TAG.sub(sub, text)
    for tid in masked.table:
        if tid not in seen:
            problems.append(f"lost {tid}")
    return out, problems
