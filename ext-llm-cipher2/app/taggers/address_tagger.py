"""
AddressTagger — regex-based US-style street + ZIP detection.

Matches:
  * Street addresses with a recognised suffix
        "123 Main St", "456 Oak Avenue", "789 Park Blvd. Apt 5B",
        "10 Downing Street", "1600 Pennsylvania Avenue NW".
  * ZIP codes that immediately follow a US two-letter state abbreviation
        "NY 10001", "CA 94103-1107".
    Bare 5-digit numbers are *not* tagged because they are
    indistinguishable from order numbers / IDs without surrounding context.

Each match is canonicalised (lower-cased, whitespace-collapsed) and stored
in `EntityDataModel.address.seen` so a repeat reference within the same
chat reuses the same token. The original spelling is recorded in
`chat_decipher` so /decipher restores the user's casing on the way back.

NER-based detection (spaCy GPE/LOC) is intentionally out of scope here:
the existing `PersonTagger` already loads spaCy, and adding GPE/LOC
detection there is the cheaper extension path. This tagger stays
regex-only so it can run without any models.
"""
from __future__ import annotations

import base64
import hashlib
import re
from typing import Tuple

from app.data_model import EntityDataModel
from app.utils.logger import logger


# --- Patterns ---------------------------------------------------------------

_STREET_SUFFIX = (
    r"(?:Street|St|Avenue|Ave|Boulevard|Blvd|Road|Rd|Lane|Ln|Drive|Dr|"
    r"Court|Ct|Way|Place|Pl|Square|Sq|Highway|Hwy|Parkway|Pkwy|Trail|Trl|"
    r"Circle|Cir|Terrace|Ter|Plaza|Plz)"
)

# Optional unit/apt suffix: "Apt 5B", "Suite 200", "#12".
_UNIT = (
    r"(?:\s+(?:Apt|Apartment|Suite|Ste|Unit|#)\.?\s*[\w-]+)?"
)

# Optional cardinal direction prefix or postfix on the street body
# ("NW", "South", etc.).
_DIRECTION = r"(?:N|NE|E|SE|S|SW|W|NW|North|East|South|West)"

_STREET_PATTERN = re.compile(
    r"\b\d+\s+"                                 # street number
    + r"(?:" + _DIRECTION + r"\s+)?"             # optional leading direction
    + r"[A-Z][\w'.-]*"                           # first proper word
    + r"(?:\s+[A-Z][\w'.-]*)*"                   # …possibly more words
    + r"\s+" + _STREET_SUFFIX + r"\.?"            # suffix (St., Avenue, etc.)
    + r"(?:\s+" + _DIRECTION + r")?"             # optional trailing direction
    + _UNIT
    + r"\b",
    re.IGNORECASE,
)

_ZIP_AFTER_STATE_PATTERN = re.compile(
    r"\b[A-Z]{2}\s+\d{5}(?:-\d{4})?\b"
)


def _normalize(s: str) -> str:
    return re.sub(r"\s+", " ", s).strip().lower()


class AddressTagger:
    """Token prefix = `ADDR_`. State stored in
    `EntityDataModel.address.seen`."""

    TOKEN_PREFIX = "ADDR_"
    TOKEN_LEN = 15

    # Compiled patterns are exposed so tests can introspect them and
    # subclasses can swap them.
    STREET_PATTERN = _STREET_PATTERN
    ZIP_AFTER_STATE_PATTERN = _ZIP_AFTER_STATE_PATTERN

    def tag_text(
        self,
        text: str,
        model: EntityDataModel,
        sequence: int,
        seed: str,
    ) -> Tuple[bool, str]:
        if not text:
            return False, text

        spans: list[tuple[int, int, str]] = []  # (start, end, original)
        for m in self.STREET_PATTERN.finditer(text):
            spans.append((m.start(), m.end(), m.group(0)))
        for m in self.ZIP_AFTER_STATE_PATTERN.finditer(text):
            spans.append((m.start(), m.end(), m.group(0)))

        if not spans:
            return False, text

        # Resolve overlaps: when two patterns cover the same range, keep the
        # longer match (so "123 Main St, NY 10001" → one whole match if the
        # street pattern caught it, otherwise two adjacent matches).
        spans.sort(key=lambda s: (s[0], -(s[1] - s[0])))
        deduped: list[tuple[int, int, str]] = []
        last_end = -1
        for start, end, raw in spans:
            if start < last_end:
                continue
            deduped.append((start, end, raw))
            last_end = end

        replacements: list[tuple[int, int, str]] = []
        mutated = False
        for start, end, raw in deduped:
            key = _normalize(raw)
            code = model.address.seen.get(key)
            if code is None:
                code = self._mint_code(key, seed)
                model.address.seen[key] = code
                model.chat_decipher[code] = raw
                mutated = True
            else:
                prev = model.chat_decipher.get(code, "")
                if len(raw) > len(prev):
                    model.chat_decipher[code] = raw
                    mutated = True
            replacements.append((start, end, code))

        # Apply right-to-left.
        replacements.sort(key=lambda r: r[0], reverse=True)
        out = text
        for start, end, code in replacements:
            out = out[:start] + code + out[end:]

        logger.debug(
            "AddressTagger: replaced %d address(es) seq=%d", len(replacements), sequence
        )
        return mutated, out

    def _mint_code(self, normalized: str, seed: str) -> str:
        h = hashlib.sha256((seed + ":" + normalized).encode()).digest()
        return self.TOKEN_PREFIX + base64.urlsafe_b64encode(h)[: self.TOKEN_LEN].decode()
