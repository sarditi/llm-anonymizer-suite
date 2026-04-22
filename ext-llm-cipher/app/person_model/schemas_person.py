from pydantic import BaseModel, Field, ConfigDict
from typing import Dict, List, Optional
import json

class RelatedOrig(BaseModel):
    related_orig_sequance: int = -1
    related_orig_position_in_sequance: int = -1

class Occurrence(BaseModel):
    sequance: int = -1
    position_in_sequance: int = -1
    value: str
    value_cs: Optional[str] = None

class PersonEntry(BaseModel):
    related_orig: RelatedOrig = Field(default_factory=RelatedOrig)
    occurances: List[Occurrence] = Field(default_factory=list)

class PersonDataModel(BaseModel):
    # This is excellent for Python 3.14/spaCy compatibility
    model_config = ConfigDict(arbitrary_types_allowed=True)
    
    person_key_map: Dict[str, List[str]] = Field(default_factory=dict)
    key_person_all_permu_map: Dict[str, PersonEntry] = Field(default_factory=dict)
    fuzzy_check_array: List[List[str]] = Field(default_factory=lambda: [[], [], [], []])
    chat_decipher: Dict[str, str] = Field(default_factory=dict)
    
    @classmethod
    def from_json_string(cls, json_string: str = None) -> "PersonDataModel":
        """Factory method to load from string or create fresh."""
        if not json_string or not json_string.strip():
            return cls()
        try:
            data = json.loads(json_string)
            # Use model_validate for better performance and V2 compliance
            return cls.model_validate(data)
        except (json.JSONDecodeError, ValueError, Exception):
            return cls()