import logging
import os
import sys
from datetime import datetime, timezone


class UTCFormatter(logging.Formatter):
    def formatTime(self, record, datefmt=None):
        dt = datetime.fromtimestamp(record.created, tz=timezone.utc)
        return dt.strftime("%Y-%m-%d %H:%M:%S %z")


def _level_from_env() -> int:
    """LOG_LEVEL=DEBUG|INFO|WARNING|ERROR — defaults to INFO, falls back to
    DEBUG when the value is unrecognised so misconfigurations don't silently
    drop diagnostics."""
    raw = os.getenv("LOG_LEVEL", "INFO").upper()
    return getattr(logging, raw, logging.DEBUG)


logger = logging.getLogger("ext_llm_cipher2")
logger.setLevel(_level_from_env())

if not logger.handlers:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(
        UTCFormatter("[%(asctime)s] [%(process)d] [%(levelname)s] %(message)s")
    )
    logger.addHandler(handler)

logger.propagate = False
