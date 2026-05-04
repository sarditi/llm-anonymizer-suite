"""
PersonTagger — direct port of `ext-llm-cipher`'s tagger, adapted to read its
state out of `EntityDataModel.person` instead of the bare data-model root.

Two improvements over the original:
  * Lazy model loading (`_ensure_models`) so test runs that exercise pure
    helpers (`_hash_name`, `_get_word_combinations`, `_fix_sentence_newlines`)
    don't pay the ~30 s cold start of spaCy + Flair.
  * Public method renamed from `tag_file_persons` to `tag_text` to match
    the `Tagger` Protocol; the legacy name is kept as an alias for callers
    of the original module.

The detection logic is unchanged: spaCy `en_core_web_trf` for PERSON spans,
Flair `ner` for ORG/LOC/GPE cross-check, and the same alias / fuzzy-match
fallback as the original.
"""
from __future__ import annotations

import base64
import hashlib
import re
from typing import Tuple

from app.data_model import EntityDataModel, Occurrence, PersonEntry, RelatedOrig


class PersonTagger:
    FUZZY_THRESHOLD = 80
    TOKEN_PREFIX = "PER_"
    TOKEN_LEN = 15

    def __init__(self) -> None:
        self.nlp = None
        self.tagger = None

    # --- lazy model loading -------------------------------------------------

    def _ensure_models(self) -> None:
        """Load spaCy + Flair on first real call. Tests that only exercise
        pure helpers can avoid the model download entirely."""
        if self.nlp is not None and self.tagger is not None:
            return
        import spacy
        from flair.models import SequenceTagger

        if self.nlp is None:
            self.nlp = spacy.load("en_core_web_trf")
        if self.tagger is None:
            self.tagger = SequenceTagger.load("ner")

    # --- public API ---------------------------------------------------------

    def tag_text(
        self,
        text: str,
        model: EntityDataModel,
        sequence: int,
        seed: str,
    ) -> Tuple[bool, str]:
        if not text:
            return False, text

        from flair.data import Sentence

        self._ensure_models()
        clean_text = self._clean_text(text)
        doc = self.nlp(clean_text)
        is_changed = False
        replacements: list[tuple[int, int, str]] = []

        flair_sentences = [Sentence(sent.text) for sent in doc.sents]
        self.tagger.predict(flair_sentences, mini_batch_size=32)

        for sent, flair_sent in zip(doc.sents, flair_sentences):
            org_mapping = set()
            for entity in flair_sent.get_spans("ner"):
                label = entity.get_label("ner").value
                if label in ("ORG", "LOC", "GPE"):
                    org_mapping.add(entity.text)

            persons_in_sent = [
                ent
                for ent in doc.ents
                if ent.label_ == "PERSON"
                and ent.start >= sent.start
                and ent.end <= sent.end
            ]

            i = sent.start
            while i < sent.end:
                ent_here = next(
                    (
                        ent
                        for ent in persons_in_sent
                        if ent.start == i and self._is_valid_person(ent)
                    ),
                    None,
                )
                if ent_here:
                    name = ent_here.text
                    if not (
                        " " in name and any(name in s for s in org_mapping)
                    ):
                        cipher = self._cipher_name(
                            name, model, ent_here.start, sequence, seed
                        )
                        if cipher:
                            self._update_chat_decipher(model, cipher, name)
                            replacements.append(
                                (ent_here.start_char, ent_here.end_char, cipher)
                            )
                            is_changed = True
                    i = ent_here.end
                else:
                    i += 1

        replacements.sort(key=lambda r: r[0], reverse=True)
        anonymized = clean_text
        for start_idx, end_idx, cipher in replacements:
            anonymized = anonymized[:start_idx] + cipher + anonymized[end_idx:]

        return is_changed, self._fix_sentence_newlines(anonymized)

    # Backwards-compatible alias for callers using the old method name.
    tag_file_persons = tag_text

    # --- helpers ------------------------------------------------------------

    def _update_chat_decipher(self, model: EntityDataModel, cipher: str, name: str) -> None:
        existing = model.chat_decipher.get(cipher)
        if existing is None:
            model.chat_decipher[cipher] = name
            return
        ev_words, ev_caps = self._score_value(existing)
        nv_words, nv_caps = self._score_value(name)
        if (nv_words > ev_words) or (nv_words == ev_words and nv_caps > ev_caps):
            model.chat_decipher[cipher] = name

    @staticmethod
    def _score_value(value: str) -> tuple[int, int]:
        words = value.split()
        return len(words), sum(1 for w in words if w and w[0].isupper())

    @staticmethod
    def _fix_sentence_newlines(text: str) -> str:
        if not text:
            return ""
        return re.sub(r"(?<=[a-zA-Z0-9]{2})\.\s*", ".\n", text)

    def _find_fuzzy_match(self, name_lower: str, model: EntityDataModel):
        from rapidfuzz import fuzz, process

        word_count = len(name_lower.split())
        idx = word_count - 1
        if 0 <= idx < len(model.person.fuzzy_check_array):
            bucket = model.person.fuzzy_check_array[idx]
            if bucket:
                result = process.extractOne(name_lower, bucket, scorer=fuzz.ratio)
                if result:
                    match_val, score, _ = result
                    if score > self.FUZZY_THRESHOLD:
                        return match_val
        return None

    @staticmethod
    def _is_valid_person(ent) -> bool:
        propn_count = sum(1 for t in ent if t.pos_ == "PROPN")
        return (propn_count / len(ent)) >= 0.5

    def _hash_name(self, name: str, seed: str) -> str:
        h = hashlib.sha256((seed + name).encode()).digest()
        return self.TOKEN_PREFIX + base64.urlsafe_b64encode(h)[: self.TOKEN_LEN].decode()

    @staticmethod
    def _clean_text(text: str) -> str:
        text = text.replace("“", '"').replace("”", '"')
        text = text.replace("\n", " ").replace("\r", " ")
        return re.sub(r"\s+", " ", text).strip()

    @staticmethod
    def _get_word_combinations(fullname: str):
        words = fullname.split()
        n = len(words)
        if n == 4:
            return (
                [
                    words[0],
                    words[0] + " " + words[1],
                    words[0] + " " + words[1] + " " + words[2],
                    words[1] + " " + words[2] + " " + words[3],
                    words[2] + " " + words[3],
                    words[3],
                ],
                n,
            )
        if n == 3:
            return (
                [
                    words[0],
                    words[0] + " " + words[1],
                    words[1] + " " + words[2],
                    words[0] + " " + words[2],
                    words[2],
                ],
                n,
            )
        if n == 2:
            return [words[0], words[1]], n
        return [], n

    def _add_person_occurrence(
        self,
        code: str,
        name: str,
        name_lower: str,
        model: EntityDataModel,
        sequence: int,
        pos: int,
    ) -> None:
        entry = model.person.key_person_all_permu_map[code]
        entry.occurances.append(
            Occurrence(
                sequance=sequence,
                position_in_sequance=pos,
                value=name_lower,
                value_cs=name,
            )
        )

    def _find_fit_occurance(
        self, name_lower: str, model: EntityDataModel, sequence: int, pos: int
    ):
        codes = model.person.person_key_map.get(name_lower, [])
        closest_seq = 1_000_000
        closest_pos = -1
        curr = (sequence * 100) + pos
        distance = float("inf")
        fit_code, fit_occ = None, None

        for code in codes:
            entry = model.person.key_person_all_permu_map.get(code)
            if entry is None:
                continue
            for occ in entry.occurances:
                if occ.value == name_lower:
                    d = curr - (occ.sequance * 100) + occ.position_in_sequance
                    if d < distance:
                        distance = d
                        closest_seq = sequence
                        closest_pos = pos
                        fit_code = code
                        fit_occ = occ
        return fit_code, closest_seq, closest_pos, fit_occ

    def _get_final_cipher_code(
        self,
        name: str,
        name_lower: str,
        model: EntityDataModel,
        sequence: int,
        pos: int,
    ):
        if len(name_lower.split()) > 1:
            code = model.person.person_key_map[name_lower][0]
            self._add_person_occurrence(
                code, name, name_lower, model, sequence, pos
            )
            return code, code
        fit_code, seq, p_in_s, fit_occ = self._find_fit_occurance(
            name_lower, model, sequence, pos
        )
        if fit_occ is not None:
            fit_occ.value_cs = name
            fit_occ.sequance = seq
            fit_occ.position_in_sequance = p_in_s
        return fit_code, fit_code

    def _cipher_name(
        self,
        name: str,
        model: EntityDataModel,
        pos: int,
        sequence: int,
        seed: str,
    ):
        name = name.removesuffix("'s")
        name_lower = name.lower()
        final_code = None

        if name_lower in model.person.person_key_map:
            final_code, _ = self._get_final_cipher_code(
                name, name_lower, model, sequence, pos
            )
            return final_code

        fuzzy_hit = self._find_fuzzy_match(name_lower, model)
        if fuzzy_hit is not None:
            fc, *_ = self._find_fit_occurance(fuzzy_hit, model, sequence, pos)
            if fc is not None:
                self._add_person_occurrence(fc, name, name_lower, model, sequence, pos)
                model.person.person_key_map[name_lower] = [fc]
                return fc

        allowed_comb, n = self._get_word_combinations(name_lower)
        new_code = self._hash_name(name_lower, seed)
        model.person.person_key_map[name_lower] = [new_code]

        if 1 <= n <= 4:
            bucket = model.person.fuzzy_check_array[n - 1]
            if name_lower not in bucket:
                bucket.append(name_lower)

        model.person.key_person_all_permu_map[new_code] = PersonEntry(
            related_orig=RelatedOrig(
                related_orig_sequance=sequence,
                related_orig_position_in_sequance=pos,
            )
        )
        self._add_person_occurrence(new_code, name, name_lower, model, sequence, pos)

        for alias in allowed_comb:
            existing = model.person.person_key_map.get(alias, [])
            if new_code not in existing:
                existing.append(new_code)
                model.person.person_key_map[alias] = existing
            alias_n = alias.count(" ") + 1
            if 1 <= alias_n <= 4:
                alias_bucket = model.person.fuzzy_check_array[alias_n - 1]
                if alias not in alias_bucket:
                    alias_bucket.append(alias)
            self._add_person_occurrence(
                new_code, alias, alias, model, 1_000_000, -1
            )

        return new_code
