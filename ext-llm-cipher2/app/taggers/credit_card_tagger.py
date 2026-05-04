"""
CreditCardTagger — regex-and-Luhn-based detection.

Strategy:
  1. Find every digit-rich substring that *could* be a card number
     (13-19 digits, optionally separated by single spaces or hyphens).
  2. Strip separators, validate length and Luhn checksum. Reject
     anything that fails so plain phone numbers / order IDs aren't
     ciphered.
  3. Reuse the same cipher code for the same canonical PAN within a
     chat (so a follow-up reference to the same card decodes to the
     identical replacement on /decipher).

The hash seed is the chat_id, mirroring PersonTagger so two different chats
that mention the same PAN get different tokens — the JIT ACL guarantees
cross-chat isolation in storage but isolating the tokens too prevents
leakage if a developer ever copy-pastes a token between chats while
debugging.
"""
from __future__ import annotations

import base64
import hashlib
import re
from typing import Tuple

from app.data_model import EntityDataModel
from app.utils.logger import logger

# Match runs of 13-19 digits separated only by single spaces or hyphens.
# Anchored with negative look-around to avoid eating arbitrary digit runs
# (so "9999999999999999999999" doesn't read as a valid card boundary).
_CC_PATTERN = re.compile(
    r"(?<![\w-])"
    r"(?:\d[ -]?){12,18}\d"
    r"(?![\w-])"
)


def _luhn_ok(digits: str) -> bool:
    """Standard Luhn checksum on a digits-only string."""
    if not digits.isdigit():
        return False
    total = 0
    parity = len(digits) % 2
    for i, ch in enumerate(digits):
        n = int(ch)
        if i % 2 == parity:
            n *= 2
            if n > 9:
                n -= 9
        total += n
    return total % 10 == 0


class CreditCardTagger:
    """Token prefix = `CC_`. State stored in
    `EntityDataModel.credit_card.seen`."""

    TOKEN_PREFIX = "CC_"
    TOKEN_LEN = 15

    def tag_text(
        self,
        text: str,
        model: EntityDataModel,
        sequence: int,
        seed: str,
    ) -> Tuple[bool, str]:
        if not text:
            return False, text

        replacements: list[tuple[int, int, str]] = []
        mutated = False

        for m in _CC_PATTERN.finditer(text):
            raw = m.group(0)
            digits = re.sub(r"[ -]", "", raw)
            if not (13 <= len(digits) <= 19):
                continue
            if not _luhn_ok(digits):
                continue

            code = model.credit_card.seen.get(digits)
            if code is None:
                code = self._mint_code(digits, seed)
                model.credit_card.seen[digits] = code
                model.chat_decipher[code] = raw  # display the original spacing
                mutated = True
            else:
                # Keep chat_decipher pointing to the longest / most-original
                # form we've seen so /decipher restores spacing faithfully.
                prev = model.chat_decipher.get(code, "")
                if len(raw) > len(prev):
                    model.chat_decipher[code] = raw
                    mutated = True

            replacements.append((m.start(), m.end(), code))

        if not replacements:
            return False, text

        # Apply right-to-left so earlier offsets stay valid.
        replacements.sort(key=lambda r: r[0], reverse=True)
        out = text
        for start, end, code in replacements:
            out = out[:start] + code + out[end:]

        logger.debug(
            "CreditCardTagger: replaced %d card(s) seq=%d", len(replacements), sequence
        )
        return mutated, out

    def _mint_code(self, digits: str, seed: str) -> str:
        h = hashlib.sha256((seed + ":" + digits).encode()).digest()
        return self.TOKEN_PREFIX + base64.urlsafe_b64encode(h)[: self.TOKEN_LEN].decode()
