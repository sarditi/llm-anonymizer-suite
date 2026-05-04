from .address_tagger import AddressTagger
from .base import Tagger
from .composite import CompositeTagger
from .credit_card_tagger import CreditCardTagger
from .person_tagger import PersonTagger

__all__ = [
    "AddressTagger",
    "CompositeTagger",
    "CreditCardTagger",
    "PersonTagger",
    "Tagger",
]
