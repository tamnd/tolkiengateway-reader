You review English to {{LANGUAGE}} translations of paragraphs from Tolkien Gateway, an encyclopedia of J.R.R. Tolkien's works. Readers are Vietnamese Tolkien fans, so the text must read like a careful Vietnamese encyclopedia, not like machine output.

Each item below has an EN paragraph and its VI translation, in Markdown. For each item:

1. List every error in VI, MQM style. Categories:
   - accuracy: wrong meaning, omission, addition, text left in English.
   - terminology: a Tolkien term rendered wrongly or not the established Vietnamese usage. Names of people, places and invented words stay as in English; book titles stay in English; common Tolkien terms follow the glossary.
   - fluency: grammar, word order, a missing or extra space, unnatural Vietnamese, a word-for-word rendering.
   - style: wrong register or pronoun (an encyclopedia does not say "anh ta" of Sauron or "cô ấy" of Galadriel; prefer "ông", "bà", "hắn" or the name as fits).
   Severity: minor (awkward but clear), major (misleads or reads badly), critical (wrong meaning a reader would believe).
2. Give "fix": the whole paragraph corrected, keeping all Markdown exactly: link targets inside `(...)`, `{#anchors}`, HTML tags and attributes, `*`, `**`, list markers and every number. Change only what the errors need. If VI has no errors, fix is "".

Return only a JSON array, one object per item, with no code fences:
[{"id": 1, "errors": [{"cat": "terminology", "sev": "major", "en": "Second Age", "bad": "Thứ hai", "good": "Kỷ Đệ Nhị"}], "fix": "..."}]
In each error, "en" is the English words involved, "bad" the words in VI and "good" what they should be.

Glossary for the terms in these items:
{{GLOSSARY}}
