"""Tests for EntityDataModel serialization + robust parsing."""
from __future__ import annotations

from app.data_model import (
    AddressState,
    CreditCardState,
    EntityDataModel,
    Occurrence,
    PersonEntry,
    RelatedOrig,
)


def test_blank_model_round_trips_through_json():
    m = EntityDataModel()
    js = m.model_dump_json()
    m2 = EntityDataModel.from_json_string(js)
    assert m2.model_dump() == m.model_dump()


def test_populated_model_round_trips():
    m = EntityDataModel()
    m.chat_decipher["CC_xyz"] = "4111 1111 1111 1111"
    m.credit_card.seen["4111111111111111"] = "CC_xyz"
    m.address.seen["123 main st"] = "ADDR_abc"

    m.person.person_key_map["john carter"] = ["PER_jc"]
    m.person.fuzzy_check_array[1].append("john carter")
    m.person.key_person_all_permu_map["PER_jc"] = PersonEntry(
        related_orig=RelatedOrig(
            related_orig_sequance=0, related_orig_position_in_sequance=2
        ),
        occurances=[
            Occurrence(
                sequance=0, position_in_sequance=2, value="john carter", value_cs="John Carter"
            )
        ],
    )

    js = m.model_dump_json()
    m2 = EntityDataModel.from_json_string(js)
    assert m2.chat_decipher == m.chat_decipher
    assert m2.credit_card.seen == m.credit_card.seen
    assert m2.address.seen == m.address.seen
    assert m2.person.person_key_map == m.person.person_key_map
    assert (
        m2.person.key_person_all_permu_map["PER_jc"].occurances[0].value_cs
        == "John Carter"
    )


def test_from_json_string_handles_empty_input():
    assert isinstance(EntityDataModel.from_json_string(None), EntityDataModel)
    assert isinstance(EntityDataModel.from_json_string(""), EntityDataModel)
    assert isinstance(EntityDataModel.from_json_string("   "), EntityDataModel)


def test_from_json_string_handles_malformed_input():
    assert isinstance(
        EntityDataModel.from_json_string("not valid json {"), EntityDataModel
    )
    assert isinstance(
        EntityDataModel.from_json_string("[1,2,3]"), EntityDataModel
    )


def test_substate_defaults_are_isolated_per_instance():
    """Mutable defaults must not leak between instances."""
    a = EntityDataModel()
    b = EntityDataModel()
    a.credit_card.seen["x"] = "CC_a"
    a.address.seen["y"] = "ADDR_a"
    a.person.fuzzy_check_array[0].append("alice")
    a.chat_decipher["CC_a"] = "x"
    assert b.credit_card.seen == {}
    assert b.address.seen == {}
    assert b.person.fuzzy_check_array == [[], [], [], []]
    assert b.chat_decipher == {}


def test_substates_are_picky_about_fields():
    cc = CreditCardState(seen={"4111": "CC_a"})
    addr = AddressState(seen={"x": "ADDR_a"})
    assert cc.seen["4111"] == "CC_a"
    assert addr.seen["x"] == "ADDR_a"
