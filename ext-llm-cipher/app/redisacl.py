import redis
import re
from datetime import datetime, timezone, timedelta
from typing import Tuple, Annotated
from app.schemas import ReqRedisAccessPermissions, Content
from app.utils.logger import logger
from app.person_model import PersonDataModel
from app.cipher_text import PersonTagger
from cachetools import TTLCache

class RedisCipherManager:
    def __init__(self, cache_ttl_minutes: int = 5):
        self.logger = logger
        self.tagger = PersonTagger()
        self._model_cache = TTLCache(maxsize=10000, ttl=cache_ttl_minutes * 60)

    def _get_client(self, permissions: ReqRedisAccessPermissions) -> redis.Redis:
        """Internal helper to create a Redis client."""
        return redis.Redis.from_url(
            f"redis://{permissions.redis_url}",
            username=permissions.user_id,
            password=permissions.pwd,
            decode_responses=True
        )

    def decipher(self, permissions: ReqRedisAccessPermissions) -> Tuple[bool, str]:
        self.logger.info(f"Connecting to Redis: {permissions.redis_url}")
        
        try:
            if not permissions.keys_allowed_read_access or not permissions.keys_allowed_write_access:
                return False, "Missing read/write keys in permissions in decipher"

            r = self._get_client(permissions)

            # 1. Read Content
            read_key = permissions.keys_allowed_read_access[0]
            content_json = r.get(read_key)
            if not content_json:
                return False, f"Read key '{read_key}' not found in decipher"

            content = Content.model_validate_json(content_json)

            meta_key = permissions.read_content_meta
            if not meta_key:
                return False, "content_meta key is missing in decipher"

            if meta_key in self._model_cache:
                self.logger.debug(f"Decipher cache hit for {meta_key}")
                data_model = self._model_cache[meta_key]
            else:
                self.logger.debug(f"Decipher Cache miss for {meta_key}. Fetching from Redis...")
                meta_json = r.get(meta_key) or ""
                data_model = PersonDataModel.from_json_string(meta_json)
                # Store with expiration time
                self._model_cache[meta_key] = data_model
            
            deciphered_text = self._replace_keywords(content.input_text, data_model.chat_decipher)

            new_content = Content(input_text=deciphered_text, attachments=content.attachments)
            write_key = permissions.keys_allowed_write_access[-1]
            
            r.set(write_key, new_content.model_dump_json())

            return True, ""

        except Exception as e:
            self.logger.error(f"Redis processing failed: {str(e)} in decipher")
            return False, str(e)

    def cipher(self, permissions: ReqRedisAccessPermissions, chat_id: str) -> Tuple[bool, str]:
        self.logger.info(f"Connecting to Redis: {permissions.redis_url}")
        
        try:
            if not permissions.keys_allowed_read_access or not permissions.keys_allowed_write_access:
                return False, "Missing read/write keys in permissions"

            r = self._get_client(permissions)

            # 1. Read Content
            read_key = permissions.keys_allowed_read_access[0]
            content_json = r.get(read_key)
            if not content_json:
                return False, f"Read key '{read_key}' not found"

            content = Content.model_validate_json(content_json)

            meta_key = permissions.write_content_meta
            if not meta_key:
                return False, "content_meta key is missing"

            if meta_key in self._model_cache:
                self.logger.debug(f"Cache hit for {meta_key}")
                data_model = self._model_cache[meta_key]
            else:
                self.logger.debug(f"Cache miss for {meta_key}. Fetching from Redis...")
                meta_json = r.get(meta_key) or ""
                data_model = PersonDataModel.from_json_string(meta_json)
                # Store with expiration time
                self._model_cache[meta_key] = data_model

            seq_num = int(read_key[4:6]) 
            
            self.logger.debug(f"Tagging person for {chat_id}...")
            is_data_model_changed, ann_text = self.tagger.tag_file_persons(content.input_text, data_model, seq_num, chat_id)

            # 4. Save Results
            new_content = Content(input_text=ann_text, attachments=content.attachments)
            write_key = permissions.keys_allowed_write_access[-1]
            self.logger.debug(f"Updating Redis with {chat_id} content ...")
            
            r.set(write_key, new_content.model_dump_json())
            if is_data_model_changed:
                self._model_cache[meta_key] = data_model
                r.set(meta_key, data_model.model_dump_json())

            return True, ""

        except Exception as e:
            self.logger.error(f"Redis processing failed: {str(e)}")
            return False, str(e)


    def _replace_keywords(self, text: str, replacement_dict: dict[str, str]) -> str:
 
        if not replacement_dict:
            return text

        # Create a regex pattern that matches any of the keys
        # \b ensures word boundaries so we don't match substrings inside words
        #pattern = re.compile(r'\b(' + '|'.join(re.escape(key) for key in replacement_dict.keys()) + r')\b')
        pattern = re.compile(r'\b(' + '|'.join(re.escape(key) for key in replacement_dict.keys()) + r')(?!\w)')

        # Define the replacement logic
        def substitute(match):
            return replacement_dict[match.group(0)]

        # Perform the replacement
        return pattern.sub(substitute, text)

    # def _replace_keywords(self, text: str, replacement_dict: dict[str, str]) -> str:
    #     if not replacement_dict:
    #         return text

    #     # 1. Deduplicate the dictionary
    #     clean_map = {}
    #     for key, name in replacement_dict.items():
    #         # Extract the base ID (e.g., "PER_q9rBOL6Q0g4pfi4")
    #         # This splits by underscore and takes the first two parts
    #         parts = key.split('_')
    #         if len(parts) >= 2:
    #             base_id = f"{parts[0]}_{parts[1]}"
    #         else:
    #             base_id = key

    #         # Keep the longest name found for this ID (e.g., "John Carter" vs "John")
    #         if base_id not in clean_map or len(name) > len(clean_map[base_id]):
    #             clean_map[base_id] = name

    #     # The pattern matches: BASE_ID followed by optional (_SEQxx_POSxx)
    #     # \b ensures we don't catch partial IDs
    #     ids_pattern = '|'.join(re.escape(k) for k in clean_map.keys())
        
    #     # This regex looks for the ID and then greedily consumes any trailing 
    #     # alphanumeric chars/underscores (like _SEQ01_POS93)
    #     regex_string = r'\b(' + ids_pattern + r')(?:\w*)\b'
    #     pattern = re.compile(regex_string)

    #     def substitute(match):
    #         # match.group(1) is the base_id captured in the first parenthesis
    #         base_id = match.group(1)
    #         return clean_map.get(base_id, match.group(0))

    #     return pattern.sub(substitute, text)