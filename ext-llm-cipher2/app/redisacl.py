"""
RedisCipherManager — bridges HTTP and the tagger pipeline.

Compared to `ext-llm-cipher`'s implementation:
  * `data_model` is now an `EntityDataModel` (composes person/credit-card/
    address state) instead of `PersonDataModel`.
  * The tagger is the `CompositeTagger`, configurable via `CIPHER_TAGGERS`
    so K8s can flip individual tagger types on/off without code changes.
  * `cache_ttl_minutes` and the cache size are env-overridable
    (`CIPHER_CACHE_TTL_MINUTES`, `CIPHER_CACHE_MAX`).
  * The optional `tagger=` constructor argument lets tests inject a fake
    tagger without monkey-patching the import.
"""
from __future__ import annotations

import os
import re
from typing import Optional, Tuple

import redis
from cachetools import TTLCache

from app.data_model import EntityDataModel
from app.schemas import Content, ReqRedisAccessPermissions
from app.taggers import CompositeTagger
from app.taggers.base import Tagger
from app.utils.logger import logger


def _env_int(name: str, default: int) -> int:
    try:
        v = os.getenv(name)
        return int(v) if v else default
    except (TypeError, ValueError):
        return default


class RedisCipherManager:
    def __init__(
        self,
        cache_ttl_minutes: Optional[int] = None,
        cache_maxsize: Optional[int] = None,
        tagger: Optional[Tagger] = None,
    ) -> None:
        self.logger = logger
        self.tagger: Tagger = tagger if tagger is not None else CompositeTagger()
        ttl_min = (
            cache_ttl_minutes
            if cache_ttl_minutes is not None
            else _env_int("CIPHER_CACHE_TTL_MINUTES", 5)
        )
        maxsize = (
            cache_maxsize
            if cache_maxsize is not None
            else _env_int("CIPHER_CACHE_MAX", 10000)
        )
        self._model_cache: TTLCache = TTLCache(maxsize=maxsize, ttl=ttl_min * 60)

    # ---- redis client ------------------------------------------------------

    def _get_client(self, permissions: ReqRedisAccessPermissions) -> redis.Redis:
        return redis.Redis.from_url(
            f"redis://{permissions.redis_url}",
            username=permissions.user_id,
            password=permissions.pwd,
            decode_responses=True,
        )

    # ---- public api --------------------------------------------------------

    def cipher(
        self, permissions: ReqRedisAccessPermissions, chat_id: str
    ) -> Tuple[bool, str]:
        self.logger.info("Connecting to Redis: %s", permissions.redis_url)
        try:
            if (
                not permissions.keys_allowed_read_access
                or not permissions.keys_allowed_write_access
            ):
                return False, "Missing read/write keys in permissions"

            r = self._get_client(permissions)

            read_key = permissions.keys_allowed_read_access[0]
            content_json = r.get(read_key)
            if not content_json:
                return False, f"Read key '{read_key}' not found"
            content = Content.model_validate_json(content_json)

            meta_key = permissions.write_content_meta
            if not meta_key:
                return False, "content_meta key is missing"

            data_model = self._load_model(r, meta_key)

            seq_num = self._extract_seq(read_key)

            self.logger.debug("Tagging entities for %s seq=%d", chat_id, seq_num)
            mutated, ann_text = self.tagger.tag_text(
                content.input_text, data_model, seq_num, chat_id
            )

            new_content = Content(
                input_text=ann_text, attachments=content.attachments
            )
            write_key = permissions.keys_allowed_write_access[-1]
            self.logger.debug("Writing %s (mutated=%s)", chat_id, mutated)
            r.set(write_key, new_content.model_dump_json())
            if mutated:
                self._model_cache[meta_key] = data_model
                r.set(meta_key, data_model.model_dump_json())

            return True, ""
        except Exception as e:  # noqa: BLE001 — surface upstream as 500
            self.logger.error("cipher failed: %s", str(e))
            return False, str(e)

    def decipher(
        self, permissions: ReqRedisAccessPermissions
    ) -> Tuple[bool, str]:
        self.logger.info("Connecting to Redis: %s", permissions.redis_url)
        try:
            if (
                not permissions.keys_allowed_read_access
                or not permissions.keys_allowed_write_access
            ):
                return False, "Missing read/write keys in permissions in decipher"

            r = self._get_client(permissions)

            read_key = permissions.keys_allowed_read_access[0]
            content_json = r.get(read_key)
            if not content_json:
                return False, f"Read key '{read_key}' not found in decipher"
            content = Content.model_validate_json(content_json)

            meta_key = permissions.read_content_meta
            if not meta_key:
                return False, "content_meta key is missing in decipher"

            data_model = self._load_model(r, meta_key)
            deciphered = self._replace_keywords(
                content.input_text, data_model.chat_decipher
            )
            new_content = Content(
                input_text=deciphered, attachments=content.attachments
            )
            write_key = permissions.keys_allowed_write_access[-1]
            r.set(write_key, new_content.model_dump_json())
            return True, ""
        except Exception as e:  # noqa: BLE001
            self.logger.error("decipher failed: %s", str(e))
            return False, str(e)

    # ---- helpers -----------------------------------------------------------

    def _load_model(self, r: redis.Redis, meta_key: str) -> EntityDataModel:
        if meta_key in self._model_cache:
            self.logger.debug("model cache hit %s", meta_key)
            return self._model_cache[meta_key]
        self.logger.debug("model cache miss %s — fetching", meta_key)
        meta_json = r.get(meta_key) or ""
        model = EntityDataModel.from_json_string(meta_json)
        self._model_cache[meta_key] = model
        return model

    @staticmethod
    def _extract_seq(read_key: str) -> int:
        """`seq_NN_workergroup_…` → NN. Falls back to 0 for non-conforming
        keys so a malformed key doesn't crash the cipher path; callers will
        see a tagger that records seq=0 occurrences rather than a 500."""
        try:
            return int(read_key[4:6])
        except (TypeError, ValueError):
            return 0

    @staticmethod
    def _replace_keywords(text: str, replacement_dict: dict[str, str]) -> str:
        if not replacement_dict:
            return text
        pattern = re.compile(
            r"\b("
            + "|".join(re.escape(k) for k in replacement_dict.keys())
            + r")(?!\w)"
        )

        def substitute(match: re.Match) -> str:
            return replacement_dict[match.group(0)]

        return pattern.sub(substitute, text)
