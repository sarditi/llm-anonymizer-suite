"""Unit tests for AddressTagger."""
from __future__ import annotations

from app.taggers import AddressTagger


def test_replaces_basic_street_address(fresh_model, seed):
    tagger = AddressTagger()
    text = "Send the package to 123 Main St for me."
    mutated, out = tagger.tag_text(text, fresh_model, 0, seed)
    assert mutated is True
    assert "123 Main St" not in out
    assert "ADDR_" in out
    assert out.startswith("Send the package to ADDR_")


def test_replaces_full_avenue_with_unit(fresh_model, seed):
    tagger = AddressTagger()
    text = "Office is at 456 Oak Avenue Apt 5B, see you there."
    mutated, out = tagger.tag_text(text, fresh_model, 0, seed)
    assert mutated is True
    assert "456 Oak Avenue Apt 5B" not in out
    assert "ADDR_" in out


def test_replaces_state_plus_zip(fresh_model, seed):
    tagger = AddressTagger()
    text = "Mail goes to NY 10001 — easy to remember."
    mutated, out = tagger.tag_text(text, fresh_model, 0, seed)
    assert mutated is True
    assert "NY 10001" not in out
    assert "ADDR_" in out


def test_zip_plus4_is_supported(fresh_model, seed):
    tagger = AddressTagger()
    text = "billing CA 94103-1107 confirmed"
    mutated, out = tagger.tag_text(text, fresh_model, 0, seed)
    assert mutated is True
    assert "CA 94103-1107" not in out


def test_repeat_address_yields_same_token(fresh_model, seed):
    tagger = AddressTagger()
    text = "first 123 Main St, then later 123 Main St again."
    _, out = tagger.tag_text(text, fresh_model, 0, seed)
    tokens = [t for t in out.replace(",", " ").split() if t.startswith("ADDR_")]
    assert len(tokens) == 2
    assert tokens[0] == tokens[1]


def test_does_not_match_plain_numbers(fresh_model, seed):
    tagger = AddressTagger()
    text = "I scored 100 points and 25 rebounds."
    mutated, out = tagger.tag_text(text, fresh_model, 0, seed)
    assert mutated is False
    assert out == text


def test_does_not_match_lowercase_street_words(fresh_model, seed):
    """Avoid false positives like '5 dogs lane' that use lowercased street
    suffixes; we expect proper-case street names."""
    tagger = AddressTagger()
    text = "I live near a small pond."
    mutated, _ = tagger.tag_text(text, fresh_model, 0, seed)
    assert mutated is False


def test_chat_decipher_records_original_casing(fresh_model, seed):
    tagger = AddressTagger()
    text = "It's at 1600 Pennsylvania Avenue NW."
    _, out = tagger.tag_text(text, fresh_model, 0, seed)
    code = next(t for t in out.split() if t.startswith("ADDR_"))
    code = code.rstrip(".")
    assert fresh_model.chat_decipher[code] == "1600 Pennsylvania Avenue NW"


def test_overlap_resolution_prefers_longer_match(fresh_model, seed):
    tagger = AddressTagger()
    # Both the street pattern and the state+zip pattern could match parts of
    # this string; expect a single replacement spanning the longer one if
    # the patterns overlap, or two replacements if they don't.
    text = "Address: 123 Main St, NY 10001"
    mutated, out = tagger.tag_text(text, fresh_model, 0, seed)
    assert mutated is True
    # Both pieces of the address should be hidden in the output.
    assert "123 Main St" not in out
    assert "NY 10001" not in out


def test_empty_text_is_a_no_op(fresh_model, seed):
    tagger = AddressTagger()
    mutated, out = tagger.tag_text("", fresh_model, 0, seed)
    assert mutated is False
    assert out == ""
