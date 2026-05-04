"""Integration-flavoured tests for RedisCipherManager using fakeredis.

A `FakeTagger` is injected so we never need to load spaCy or Flair. The
tests cover the read → tag → write → cache pipeline end to end, plus the
two error paths (missing keys, missing meta).
"""
from __future__ import annotations

from unittest.mock import patch

import fakeredis
import pytest

from app.data_model import EntityDataModel
from app.redisacl import RedisCipherManager
from app.schemas import Content, ReqRedisAccessPermissions


class FakeTagger:
    """Replaces every literal occurrence of `secret` with `<REDACTED>` and
    records one entry in `chat_decipher` so the decipher path also has
    something to substitute back."""

    def tag_text(self, text, model: EntityDataModel, sequence: int, seed: str):
        if "secret" not in text:
            return False, text
        token = "PER_FAKE000000000"
        model.chat_decipher[token] = "secret"
        return True, text.replace("secret", token)


@pytest.fixture()
def fake_redis(monkeypatch):
    """Patch redis.Redis.from_url so RedisCipherManager talks to fakeredis."""
    fake = fakeredis.FakeStrictRedis(decode_responses=True)

    def _from_url(*_args, **_kwargs):
        return fake

    monkeypatch.setattr("redis.Redis.from_url", _from_url)
    return fake


@pytest.fixture()
def manager() -> RedisCipherManager:
    return RedisCipherManager(
        cache_ttl_minutes=1, cache_maxsize=16, tagger=FakeTagger()
    )


def _perms(read_key: str, write_key: str, meta: str) -> ReqRedisAccessPermissions:
    return ReqRedisAccessPermissions(
        user_id="u",
        pwd="p",
        redis_url="redis:6379",
        read_content_meta=meta,
        write_content_meta=meta,
        keys_allowed_read_access=[read_key],
        keys_allowed_write_access=[write_key],
    )


def test_cipher_writes_anonymized_content_and_persists_meta(
    manager, fake_redis
):
    read_key = "seq_00_workergroup_xx_wrkind_00_reqid_chatA"
    write_key = "seq_01_workergroup_xx_wrkind_00_reqid_chatA"
    meta_key = "meta_workergroup_xx_reqid_chatA"

    fake_redis.set(read_key, Content(input_text="this is a secret note").model_dump_json())

    ok, msg = manager.cipher(_perms(read_key, write_key, meta_key), "chatA")
    assert ok, msg

    written = Content.model_validate_json(fake_redis.get(write_key))
    assert "secret" not in written.input_text
    assert "PER_FAKE000000000" in written.input_text

    meta_json = fake_redis.get(meta_key)
    assert meta_json is not None
    persisted = EntityDataModel.from_json_string(meta_json)
    assert persisted.chat_decipher.get("PER_FAKE000000000") == "secret"


def test_decipher_substitutes_using_persisted_meta(manager, fake_redis):
    read_key = "seq_00_workergroup_xx_wrkind_00_reqid_chatB"
    write_key = "seq_01_workergroup_xx_wrkind_00_reqid_chatB"
    meta_key = "meta_workergroup_xx_reqid_chatB"

    # Pre-populate redis with a tokenized response and the meta map.
    fake_redis.set(
        read_key, Content(input_text="here is your PER_FAKE000000000 back").model_dump_json()
    )
    model = EntityDataModel()
    model.chat_decipher["PER_FAKE000000000"] = "secret"
    fake_redis.set(meta_key, model.model_dump_json())

    ok, msg = manager.decipher(_perms(read_key, write_key, meta_key))
    assert ok, msg

    written = Content.model_validate_json(fake_redis.get(write_key))
    assert "PER_FAKE" not in written.input_text
    assert "secret" in written.input_text


def test_cipher_short_circuits_when_keys_missing(manager, fake_redis):
    perms = ReqRedisAccessPermissions(
        user_id="u",
        pwd="p",
        redis_url="redis:6379",
        read_content_meta="x",
        write_content_meta="x",
        keys_allowed_read_access=None,
        keys_allowed_write_access=None,
    )
    ok, msg = manager.cipher(perms, "chatX")
    assert ok is False
    assert "Missing read/write keys" in msg


def test_cipher_short_circuits_when_meta_missing(manager, fake_redis):
    read_key = "seq_00_xx"
    write_key = "seq_01_xx"
    fake_redis.set(read_key, Content(input_text="ok").model_dump_json())

    perms = ReqRedisAccessPermissions(
        user_id="u",
        pwd="p",
        redis_url="redis:6379",
        read_content_meta=None,
        write_content_meta=None,
        keys_allowed_read_access=[read_key],
        keys_allowed_write_access=[write_key],
    )
    ok, msg = manager.cipher(perms, "chatX")
    assert ok is False
    assert "content_meta" in msg


def test_cipher_then_decipher_round_trips_via_cache(manager, fake_redis):
    """Two calls back-to-back: the second one should hit the in-process
    cache and not need to reread the meta key."""
    read_key = "seq_00_workergroup_xx_wrkind_00_reqid_chatC"
    write_key = "seq_01_workergroup_xx_wrkind_00_reqid_chatC"
    meta_key = "meta_workergroup_xx_reqid_chatC"

    fake_redis.set(read_key, Content(input_text="say secret softly").model_dump_json())
    ok, _ = manager.cipher(_perms(read_key, write_key, meta_key), "chatC")
    assert ok

    # Now pretend the LLM replied with the same token; we decipher it.
    decipher_read = "seq_02_workergroup_xx_wrkind_00_reqid_chatC"
    decipher_write = "seq_03_workergroup_xx_wrkind_00_reqid_chatC"
    fake_redis.set(
        decipher_read,
        Content(input_text="Echo: PER_FAKE000000000").model_dump_json(),
    )

    # Spy on r.get to confirm the meta key load is served from cache
    # the second time.
    seen_keys: list[str] = []
    real_get = fake_redis.get

    def tracking_get(k):
        seen_keys.append(k)
        return real_get(k)

    with patch.object(fake_redis, "get", side_effect=tracking_get):
        ok, _ = manager.decipher(_perms(decipher_read, decipher_write, meta_key))
        assert ok
        assert meta_key not in seen_keys, (
            "second meta load should be served from cache; saw keys=%s" % seen_keys
        )

    out = Content.model_validate_json(fake_redis.get(decipher_write))
    assert "secret" in out.input_text
