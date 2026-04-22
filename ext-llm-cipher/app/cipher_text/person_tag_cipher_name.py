import spacy
import re
import hashlib
import base64
from flair.data import Sentence
from flair.models import SequenceTagger
from rapidfuzz import process, fuzz
from app.person_model import Occurrence, RelatedOrig, PersonEntry

class PersonTagger:
    def __init__(self):
        # Initializing core models and constants
        self.nlp = spacy.load("en_core_web_trf")
        self.tagger = SequenceTagger.load("ner")
        self.FUZZY_THRESHOLD = 80

    def tag_file_persons(self, text, data_model, sequence, seed):
        """
        Public method to process a document and tag persons.
        """
        clean_text = self._clean_text(text)
        doc = self.nlp(clean_text)
        is_data_model_changed = False
        replacements = []
        
        # BATCH PREDICT
        flair_sentences = [Sentence(sent.text) for sent in doc.sents]    
        self.tagger.predict(flair_sentences, mini_batch_size=32) 
        
        for sent, flair_sent in zip(doc.sents, flair_sentences):
            # Identify non-person entities to avoid false positives
            org_mapping = set()
            for entity in flair_sent.get_spans('ner'):
                label = entity.get_label('ner').value
                if label in ("ORG", "LOC", "GPE"):
                    org_mapping.add(entity.text)

            # Filter spaCy entities for the current sentence
            persons_in_sent = [
                ent for ent in doc.ents 
                if ent.label_ == "PERSON" 
                and ent.start >= sent.start 
                and ent.end <= sent.end
            ]

            i = sent.start
            while i < sent.end:
                ent_here = next(
                    (ent for ent in persons_in_sent if ent.start == i and self._is_valid_person(ent)),
                    None
                )
                if ent_here:
                    name = ent_here.text
                    # Check if the name isn't actually an Organization
                    if not (" " in name and any(name in s for s in org_mapping)):
                        cipher = self._cipher_name(name, data_model, ent_here.start, sequence, seed)
                        if cipher:
                            if cipher in data_model.chat_decipher:
                                value = data_model.chat_decipher[cipher]
                                total_words_value, cap_words_value = self._score_value(value)
                                total_words_name, cap_words_name = self._score_value(name)
                                if total_words_value < total_words_name:
                                    data_model.chat_decipher[cipher] = name
                                elif total_words_value == total_words_name and cap_words_value < cap_words_name:
                                    data_model.chat_decipher[cipher] = name
                            else:
                                data_model.chat_decipher[cipher] = name
                            
                            replacements.append((ent_here.start_char, ent_here.end_char, cipher))

                            is_data_model_changed = True
                    i = ent_here.end
                else:
                    i += 1

            replacements.sort(key=lambda x: x[0], reverse=True)

            anonymized_text = clean_text
            for start_idx, end_idx, cipher in replacements:
                anonymized_text = anonymized_text[:start_idx] + cipher + anonymized_text[end_idx:]

        return is_data_model_changed, self._fix_sentence_newlines(anonymized_text)

    def _score_value(self, value: str) -> tuple[int, int]:

            words = value.split()
            word_count = len(words)
            cap_count = sum(1 for word in words if word and word[0].isupper())
            
            return word_count, cap_count

    def _fix_sentence_newlines(self, text):
        if not text:
            return ""
        
        pattern = r'(?<=[a-zA-Z0-9]{2})\.\s*'        
        return re.sub(pattern, '.\n', text)

    # --- Private Helper Methods ---
    def _find_fuzzy_match(self, name_lower, cipher_map):
        word_count = len(name_lower.split())
        bucket_index = word_count - 1
        
        if 0 <= bucket_index < len(cipher_map.fuzzy_check_array):
            target_bucket = cipher_map.fuzzy_check_array[bucket_index]
            if target_bucket:
                result = process.extractOne(name_lower, target_bucket, scorer=fuzz.ratio)
                if result:
                    match_val, score, _ = result
                    if score > self.FUZZY_THRESHOLD:
                        return match_val
        return None

    def _is_valid_person(self, ent):
        propn_count = sum(1 for t in ent if t.pos_ == "PROPN")
        return (propn_count / len(ent)) >= 0.5

    def _hash_name(self, name, seed):
        h = hashlib.sha256((seed + name).encode()).digest()
        return f"PER_{base64.urlsafe_b64encode(h)[:15].decode()}"

    def _clean_text(self, text):
        text = text.replace("\u201c", '"').replace("\u201d", '"')
        text = text.replace("\n", " ").replace("\r", " ")
        return re.sub(r"\s+", " ", text).strip()

    def _get_word_combinations(self, fullname):
        words = fullname.split()
        n = len(words)
        if n == 4:
            return [words[0], words[0] + " " + words[1],
                    words[0] + " " + words[1] + " " + words[2],
                    words[1] + " " + words[2] + " " + words[3],
                    words[2] + " " + words[3], words[3]], n
        elif n == 3:
            return [words[0], words[0] + " " + words[1],
                    words[1] + " " + words[2], words[0] + " " + words[2], words[2]], n
        elif n == 2:
            return [words[0], words[1]], n
        return [], n

    def _add_person_occurrence(self, code, name, name_lower, cipher_map, sequence, pos):
        person_entry = cipher_map.key_person_all_permu_map[code]
        new_occurrence = Occurrence(
            sequance=sequence,
            position_in_sequance=pos,
            value=name_lower,
            value_cs=name
        )  
        person_entry.occurances.append(new_occurrence)

    def _find_fit_occurance(self, name_lower, cipher_map, sequence, pos):
        codes = cipher_map.person_key_map.get(name_lower, [])
        closest_occurrence_sequance = 1000000
        closest_occurrence_position_in_sequance = -1
        curr_coord = (sequence * 100) + pos
        distance = float('inf')
        fit_code, fit_occurrence = None, None

        for code in codes:
            if code in cipher_map.key_person_all_permu_map:
                person_entry = cipher_map.key_person_all_permu_map[code]
                for occurrence in person_entry.occurances:
                    if occurrence.value == name_lower:
                        curr_distance = curr_coord - (occurrence.sequance * 100) + occurrence.position_in_sequance
                        if curr_distance < distance:
                            distance = curr_distance
                            closest_occurrence_sequance = sequence
                            closest_occurrence_position_in_sequance = pos
                            fit_code = code
                            fit_occurrence = occurrence
        return fit_code, closest_occurrence_sequance, closest_occurrence_position_in_sequance, fit_occurrence

    def _get_final_cipher_code(self, name, name_lower, cipher_map, sequence, pos):
        if len(name_lower.split()) > 1:
            code = cipher_map.person_key_map[name_lower][0]
            self._add_person_occurrence(code, name, name_lower, cipher_map, sequence, pos)        
            #return f"{code}_SEQ{sequence:02d}_POS{pos:02d}", code
            return code, code
        else:
            (fit_code, seq, p_in_s, fit_occ) = self._find_fit_occurance(name_lower, cipher_map, sequence, pos)
            fit_occ.value_cs = name
            fit_occ.sequance = seq
            fit_occ.position_in_sequance = p_in_s
            #return f"{fit_code}_SEQ{seq:02d}_POS{p_in_s:02d}", fit_code   
            return fit_code, fit_code


    def _cipher_name(self, name, cipher_map, pos, sequence, seed):
        name = name.removesuffix("'s")
        name_lower = name.lower()
        final_code_to_replace_name = None 

        if name_lower in cipher_map.person_key_map:
            final_code_to_replace_name, _ = self._get_final_cipher_code(name, name_lower, cipher_map, sequence, pos)                                                       
        else:
            name_lower_fuzzy_match = self._find_fuzzy_match(name_lower, cipher_map)
            if name_lower_fuzzy_match is not None:
                final_code, *_ = self._find_fit_occurance(name_lower_fuzzy_match, cipher_map, sequence, pos)
                self._add_person_occurrence(final_code, name, name_lower, cipher_map, sequence, pos)        
                cipher_map.person_key_map[name_lower] = [final_code]
            else:
                allowed_comb, n = self._get_word_combinations(name_lower)
                new_code = self._hash_name(name_lower, seed)
                cipher_map.person_key_map[name_lower] = [new_code]
                
                if 1 <= n <= 4:
                    bucket = cipher_map.fuzzy_check_array[n - 1]
                    if name_lower not in bucket: bucket.append(name_lower)

                cipher_map.key_person_all_permu_map[new_code] = PersonEntry(
                    related_orig=RelatedOrig(
                        related_orig_sequance=sequence,
                        related_orig_position_in_sequance=pos
                    )
                )
                
                self._add_person_occurrence(new_code, name, name_lower, cipher_map, sequence, pos)        
                for alias in allowed_comb:
                    existing_list = cipher_map.person_key_map.get(alias, [])
                    if new_code not in existing_list:
                        existing_list.append(new_code)
                        cipher_map.person_key_map[alias] = existing_list
                    
                    alias_n = alias.count(" ") + 1
                    if 1 <= alias_n <= 4:
                        alias_bucket = cipher_map.fuzzy_check_array[alias_n - 1]
                        if alias not in alias_bucket: alias_bucket.append(alias)

                    self._add_person_occurrence(new_code, alias, alias, cipher_map, 1000000, -1)
                
                #final_code_to_replace_name = f"{new_code}_SEQ{sequence:02d}_POS{pos:02d}"
                final_code_to_replace_name = new_code

        return final_code_to_replace_name