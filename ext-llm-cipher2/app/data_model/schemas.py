"""
EntityDataModel is the persistent per-chat state used by the cipher pipeline.

Compared to the original `PersonDataModel` in `ext-llm-cipher`, this version
separates state by entity type so each tagger owns its own slice of the model:

    EntityDataModel
    ├── person         (PersonState)        — original person-name maps
    ├── credit_card    (CreditCardState)    — normalized digits → cipher code
    ├── address        (AddressState)       — normalized address → cipher code
    └── chat_decipher  (Dict[str, str])     — universal code → display value

Every tagger writes its reverse-lookup entries into the shared
`chat_decipher` dict, so `/decipher` can substitute any kind of token without
knowing which tagger produced it. Token prefixes (`PER_`, `CC_`, `ADDR_`)
keep the namespaces from colliding.
"""
from __future__ import annotations

import json
from typing import Dict, List, Optional

from pydantic import BaseModel, ConfigDict, Field


# ---- Person -----------------------------------------------------------------


class RelatedOrig(BaseModel):
    related_orig_sequance: int = -1
    related_orig_position_in_sequance: int = -1


class Occurrence(BaseModel):
    sequance: int = -1
    position_in_sequance: int = -1
    value: str
    value_cs: Optional[str] = None


class PersonEntry(BaseModel):
    related_orig: RelatedOrig = Field(default_factory=RelatedOrig)
    occurances: List[Occurrence] = Field(default_factory=list)


class PersonState(BaseModel):
    """The original four maps from `PersonDataModel`, kept under one
    namespace so the schema can grow other entity types without colliding."""

    person_key_map: Dict[str, List[str]] = Field(default_factory=dict)
    key_person_all_permu_map: Dict[str, PersonEntry] = Field(default_factory=dict)
    fuzzy_check_array: List[List[str]] = Field(
        default_factory=lambda: [[], [], [], []]
    )


# ---- Credit cards / addresses ----------------------------------------------


class CreditCardState(BaseModel):
    """Maps a normalized PAN (digits only, no separators) → cipher code so a
    repeat mention of the same card produces the same token."""

    seen: Dict[str, str] = Field(default_factory=dict)


class AddressState(BaseModel):
    """Maps a normalized (lower-cased, whitespace-collapsed) address string
    → cipher code, for the same reason as `CreditCardState.seen`."""

    seen: Dict[str, str] = Field(default_factory=dict)


# ---- Root -------------------------------------------------------------------


class EntityDataModel(BaseModel):
    model_config = ConfigDict(arbitrary_types_allowed=True)

    person: PersonState = Field(default_factory=PersonState)
    credit_card: CreditCardState = Field(default_factory=CreditCardState)
    address: AddressState = Field(default_factory=AddressState)
    chat_decipher: Dict[str, str] = Field(default_factory=dict)

    @classmethod
    def from_json_string(cls, json_string: Optional[str] = None) -> "EntityDataModel":
        """Robust constructor: empty/missing/malformed input ⇒ fresh empty
        model. Mirrors `PersonDataModel.from_json_string` so the rest of the
        codebase (which calls this on cache miss) stays unchanged in shape."""

        if not json_string or not json_string.strip():
            return cls()
        try:
            data = json.loads(json_string)
            return cls.model_validate(data)
        except (json.JSONDecodeError, ValueError, Exception):
            return cls()
