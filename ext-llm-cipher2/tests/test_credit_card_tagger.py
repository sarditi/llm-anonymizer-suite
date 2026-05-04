"""Unit tests for CreditCardTagger."""
from __future__ import annotations

from app.taggers import CreditCardTagger
from app.taggers.credit_card_tagger import _luhn_ok


# Well-known Luhn-valid sample numbers, none of them issued.
VISA = "4111 1111 1111 1111"
MASTERCARD = "5500 0000 0000 0004"
AMEX = "3400 000000 00009"
DISCOVER = "6011 0000 0000 0004"

INVALID_LUHN = "4111 1111 1111 1112"  # last digit flipped → checksum fails


def test_luhn_validator_accepts_known_good_numbers():
    for raw in (VISA, MASTERCARD, AMEX, DISCOVER):
        digits = "".join(c for c in raw if c.isdigit())
        assert _luhn_ok(digits), f"expected Luhn ok for {raw}"


def test_luhn_validator_rejects_bad_checksum():
    digits = "".join(c for c in INVALID_LUHN if c.isdigit())
    assert not _luhn_ok(digits)


def test_replaces_a_visa_card(fresh_model, seed):
    tagger = CreditCardTagger()
    text = f"My card is {VISA}, please charge it."
    mutated, out = tagger.tag_text(text, fresh_model, sequence=0, seed=seed)
    assert mutated is True
    assert VISA not in out
    assert out.startswith("My card is CC_")
    assert "please charge it." in out


def test_same_card_within_chat_gets_same_token(fresh_model, seed):
    tagger = CreditCardTagger()
    text = f"First {VISA}. Same again {VISA}."
    _, out = tagger.tag_text(text, fresh_model, sequence=0, seed=seed)
    # Two CC_ tokens, but they must be identical.
    tokens = [w for w in out.split() if w.startswith("CC_")]
    assert len(tokens) == 2
    # Trim trailing punctuation (".").
    tokens = [t.rstrip(".") for t in tokens]
    assert tokens[0] == tokens[1]


def test_different_chats_produce_different_tokens(fresh_model):
    tagger = CreditCardTagger()
    other = type(fresh_model)()
    _, out_a = tagger.tag_text(f"card {VISA}", fresh_model, 0, "chat-A")
    _, out_b = tagger.tag_text(f"card {VISA}", other, 0, "chat-B")
    a = next(w for w in out_a.split() if w.startswith("CC_"))
    b = next(w for w in out_b.split() if w.startswith("CC_"))
    assert a != b


def test_invalid_checksum_is_left_intact(fresh_model, seed):
    tagger = CreditCardTagger()
    text = f"This is not a card: {INVALID_LUHN}."
    mutated, out = tagger.tag_text(text, fresh_model, 0, seed)
    assert mutated is False
    assert INVALID_LUHN in out


def test_phone_number_is_not_tagged(fresh_model, seed):
    tagger = CreditCardTagger()
    text = "Call me at 555-1234 anytime."  # too short to even regex-match
    mutated, out = tagger.tag_text(text, fresh_model, 0, seed)
    assert mutated is False
    assert "555-1234" in out


def test_chat_decipher_records_original_spacing(fresh_model, seed):
    tagger = CreditCardTagger()
    _, out = tagger.tag_text(f"card {VISA}", fresh_model, 0, seed)
    cc_tokens = [w for w in out.split() if w.startswith("CC_")]
    assert len(cc_tokens) == 1
    code = cc_tokens[0]
    assert fresh_model.chat_decipher[code] == VISA


def test_empty_text_is_a_no_op(fresh_model, seed):
    tagger = CreditCardTagger()
    mutated, out = tagger.tag_text("", fresh_model, 0, seed)
    assert mutated is False
    assert out == ""
