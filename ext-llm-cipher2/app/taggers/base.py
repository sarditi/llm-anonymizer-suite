"""
Tagger contract.

Every concrete tagger (person, credit card, address, …) implements
`tag_text(text, model, sequence, seed) -> (mutated, annotated_text)` so the
composite can pipeline arbitrarily many of them without knowing what they
detect. Match the original `PersonTagger.tag_file_persons` shape so existing
extension code keeps working.
"""
from __future__ import annotations

from typing import Protocol, Tuple

from app.data_model import EntityDataModel


class Tagger(Protocol):
    def tag_text(
        self,
        text: str,
        model: EntityDataModel,
        sequence: int,
        seed: str,
    ) -> Tuple[bool, str]:
        ...
