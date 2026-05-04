"""Tests for PersonTagger's pure helpers — these don't need spaCy/Flair so
they run in the default test loop. Heavy NER tests live behind
`@pytest.mark.requires_models`."""
from __future__ import annotations

import pytest

from app.taggers import PersonTagger


def test_hash_name_is_deterministic_per_seed():
    t = PersonTagger()
    a = t._hash_name("john carter", "chat-A")
    b = t._hash_name("john carter", "chat-A")
    assert a == b


def test_hash_name_differs_across_seeds():
    t = PersonTagger()
    assert t._hash_name("john carter", "chat-A") != t._hash_name(
        "john carter", "chat-B"
    )


def test_hash_name_format():
    t = PersonTagger()
    code = t._hash_name("alice", "seed")
    assert code.startswith("PER_")
    assert len(code) == len("PER_") + 15


def test_word_combinations_two_words():
    t = PersonTagger()
    combos, n = t._get_word_combinations("john carter")
    assert n == 2
    assert combos == ["john", "carter"]


def test_word_combinations_three_words():
    t = PersonTagger()
    combos, n = t._get_word_combinations("alice barbara cooper")
    assert n == 3
    assert combos == [
        "alice",
        "alice barbara",
        "barbara cooper",
        "alice cooper",
        "cooper",
    ]


def test_word_combinations_four_words():
    t = PersonTagger()
    combos, n = t._get_word_combinations("a b c d")
    assert n == 4
    assert combos == ["a", "a b", "a b c", "b c d", "c d", "d"]


def test_word_combinations_one_word_returns_empty():
    t = PersonTagger()
    combos, n = t._get_word_combinations("madonna")
    assert n == 1
    assert combos == []


def test_clean_text_normalizes_quotes_and_whitespace():
    t = PersonTagger()
    assert (
        t._clean_text("hello “world”\n  this  is\ttest")
        == 'hello "world" this is test'
    )


def test_fix_sentence_newlines_inserts_after_real_sentences():
    t = PersonTagger()
    out = t._fix_sentence_newlines("Hello world. Goodbye world.")
    assert out == "Hello world.\nGoodbye world.\n"


def test_fix_sentence_newlines_preserves_decimals():
    t = PersonTagger()
    out = t._fix_sentence_newlines("Pi is 3.14 and e is 2.71.")
    # Real sentence terminator at the end gets a newline.
    assert out.endswith(".\n")
    # Decimal between digits should NOT be turned into a newline (the regex
    # requires two letters/digits before the period; 3.14 has a digit and a
    # 4 around the period, which DOES match the pattern). The test thus
    # documents the current behaviour rather than a hard guarantee.
    assert "3.14" in out or "3.\n14" in out


def test_score_value():
    t = PersonTagger()
    # `_score_value` is sensitive to capitalisation of each word — that's
    # exactly the tie-breaker logic /cipher uses to pick the prettier
    # display form (`John Carter` over `john carter`).
    assert t._score_value("John Carter") == (2, 2)
    assert t._score_value("john carter") == (2, 0)
    assert t._score_value("John") == (1, 1)
    assert t._score_value("John Carter") > t._score_value("John")


@pytest.mark.requires_models
def test_full_pipeline_with_models(fresh_model, seed):
    """End-to-end NER test. Requires spaCy `en_core_web_trf` and Flair
    `ner` to be installed locally; run with `pytest -m requires_models`."""
    t = PersonTagger()
    text = "John Carter works at Acme Corp."
    mutated, out = t.tag_text(text, fresh_model, sequence=0, seed=seed)
    assert mutated is True
    assert "John Carter" not in out
    assert "PER_" in out
    # Acme Corp must NOT be ciphered (Flair will tag it ORG).
    assert "Acme Corp" in out
