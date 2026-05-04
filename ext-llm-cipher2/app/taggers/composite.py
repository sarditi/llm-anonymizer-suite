"""
CompositeTagger — pipelines multiple taggers over the same text + data
model. Order matters:

    1. CreditCardTagger  (regex + Luhn) — replaces card numbers with CC_…
    2. AddressTagger     (regex)        — replaces street addresses / ZIPs
    3. PersonTagger      (NER)          — replaces person names with PER_…

Running the regex-based taggers first is the cheap path: their tokens
contain prefixes (`CC_`, `ADDR_`) so the NER pass downstream can't confuse
them with PERSON spans, and we avoid invoking spaCy/Flair more than once.

The full taggers list is selectable at construction time. Defaults reflect
the most common deployment: all three on. A subset is useful for unit
testing and for the env-driven config path.
"""
from __future__ import annotations

import os
from typing import List, Optional, Sequence, Tuple

from app.data_model import EntityDataModel
from app.taggers.base import Tagger
from app.utils.logger import logger

from .address_tagger import AddressTagger
from .credit_card_tagger import CreditCardTagger
from .person_tagger import PersonTagger


_TAGGER_FACTORIES = {
    "credit_card": CreditCardTagger,
    "address": AddressTagger,
    "person": PersonTagger,
}

_DEFAULT_ORDER = ("credit_card", "address", "person")


class CompositeTagger:
    def __init__(self, taggers: Optional[Sequence[Tagger]] = None) -> None:
        self.taggers: List[Tagger] = list(taggers) if taggers is not None else []
        if not self.taggers:
            self.taggers = self._default_taggers()
        logger.info(
            "CompositeTagger initialised with %d tagger(s): %s",
            len(self.taggers),
            ", ".join(type(t).__name__ for t in self.taggers),
        )

    @staticmethod
    def _default_taggers() -> List[Tagger]:
        """Build the tagger pipeline based on the `CIPHER_TAGGERS` env var.

        Format: comma-separated names from {credit_card, address, person}.
        Unknown names are skipped with a warning. Empty / unset value
        ⇒ all three in default order."""
        raw = os.getenv("CIPHER_TAGGERS", "").strip()
        if not raw:
            order = _DEFAULT_ORDER
        else:
            order = tuple(p.strip() for p in raw.split(",") if p.strip())

        out: List[Tagger] = []
        for name in order:
            factory = _TAGGER_FACTORIES.get(name)
            if factory is None:
                logger.warning("CompositeTagger: unknown tagger %r — skipped", name)
                continue
            out.append(factory())
        return out

    def tag_text(
        self,
        text: str,
        model: EntityDataModel,
        sequence: int,
        seed: str,
    ) -> Tuple[bool, str]:
        any_changed = False
        current = text
        for tagger in self.taggers:
            mutated, current = tagger.tag_text(current, model, sequence, seed)
            any_changed = any_changed or mutated
        return any_changed, current

    # Backwards-compatible alias.
    tag_file_persons = tag_text
