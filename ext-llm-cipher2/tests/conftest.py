"""
Shared fixtures for the ext-llm-cipher2 test suite.

The PersonTagger pulls in spaCy + Flair + RapidFuzz models that are far too
heavy for a unit-test loop. The fixtures here provide:

  * `fresh_model`  — a blank `EntityDataModel`
  * `seed`         — a stable chat_id substitute used across the suite
  * `pure_composite` — a CompositeTagger composed only of the regex-based
                       taggers (credit_card + address), so tests can run
                       without ever loading spaCy or Flair.

Tests that genuinely want the full PersonTagger should mark themselves
`@pytest.mark.requires_models` and the user can opt into running them with
`pytest -m requires_models` after `python -m spacy download en_core_web_trf`.
"""
from __future__ import annotations

import pytest

from app.data_model import EntityDataModel
from app.taggers import AddressTagger, CompositeTagger, CreditCardTagger


@pytest.fixture()
def fresh_model() -> EntityDataModel:
    return EntityDataModel()


@pytest.fixture()
def seed() -> str:
    return "test-chat-0001"


@pytest.fixture()
def pure_composite() -> CompositeTagger:
    return CompositeTagger(taggers=[CreditCardTagger(), AddressTagger()])


def pytest_configure(config):
    config.addinivalue_line(
        "markers",
        "requires_models: tests that load spaCy / Flair models on first run",
    )
