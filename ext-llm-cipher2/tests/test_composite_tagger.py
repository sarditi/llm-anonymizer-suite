"""Tests for CompositeTagger using only the regex-based taggers (so the
suite runs without spaCy / Flair model downloads)."""
from __future__ import annotations

from app.redisacl import RedisCipherManager
from app.taggers import AddressTagger, CompositeTagger, CreditCardTagger


VISA = "4111 1111 1111 1111"


def test_pipeline_replaces_card_then_address(pure_composite, fresh_model, seed):
    text = f"Bill {VISA} and ship to 123 Main St."
    mutated, out = pure_composite.tag_text(text, fresh_model, 0, seed)
    assert mutated is True
    assert VISA not in out
    assert "123 Main St" not in out
    assert "CC_" in out
    assert "ADDR_" in out


def test_decipher_round_trip(pure_composite, fresh_model, seed):
    """End-to-end: cipher → decipher must round-trip the original strings.
    We exercise the same `_replace_keywords` helper that the production
    /decipher endpoint uses."""
    text = f"Bill {VISA} and ship to 123 Main St — confirm by NY 10001."
    _, ciphered = pure_composite.tag_text(text, fresh_model, 0, seed)
    deciphered = RedisCipherManager._replace_keywords(
        ciphered, fresh_model.chat_decipher
    )
    assert VISA in deciphered
    assert "123 Main St" in deciphered
    assert "NY 10001" in deciphered


def test_default_taggers_when_env_unset(monkeypatch):
    """With no CIPHER_TAGGERS env var the default order is
    [credit_card, address, person]. We can introspect the type list without
    actually invoking the person tagger (which would load spaCy)."""
    monkeypatch.delenv("CIPHER_TAGGERS", raising=False)
    composite = CompositeTagger()
    names = [type(t).__name__ for t in composite.taggers]
    assert names == ["CreditCardTagger", "AddressTagger", "PersonTagger"]


def test_env_filters_taggers(monkeypatch):
    monkeypatch.setenv("CIPHER_TAGGERS", "address,credit_card")
    composite = CompositeTagger()
    names = [type(t).__name__ for t in composite.taggers]
    assert names == ["AddressTagger", "CreditCardTagger"]


def test_env_with_unknown_tagger_is_skipped_with_warning(monkeypatch):
    monkeypatch.setenv("CIPHER_TAGGERS", "address,bogus,credit_card")
    composite = CompositeTagger()
    names = [type(t).__name__ for t in composite.taggers]
    assert names == ["AddressTagger", "CreditCardTagger"]


def test_explicit_tagger_list_wins_over_env(monkeypatch):
    monkeypatch.setenv("CIPHER_TAGGERS", "person")
    composite = CompositeTagger(taggers=[CreditCardTagger()])
    names = [type(t).__name__ for t in composite.taggers]
    assert names == ["CreditCardTagger"]


def test_empty_text_runs_each_tagger_without_crashing(pure_composite, fresh_model, seed):
    mutated, out = pure_composite.tag_text("", fresh_model, 0, seed)
    assert mutated is False
    assert out == ""


def test_aliased_method_name_works(pure_composite, fresh_model, seed):
    """`tag_file_persons` is kept as an alias for legacy callers."""
    text = f"card {VISA}"
    mutated, out = pure_composite.tag_file_persons(text, fresh_model, 0, seed)
    assert mutated is True
    assert "CC_" in out
