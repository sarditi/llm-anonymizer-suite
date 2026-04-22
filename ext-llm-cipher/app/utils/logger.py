import logging
import sys
from datetime import datetime, timezone

# Custom UTC Formatter
class UTCFormatter(logging.Formatter):
    def formatTime(self, record, datefmt=None):
        from datetime import datetime, timezone
        dt = datetime.fromtimestamp(record.created, tz=timezone.utc)
        return dt.strftime("%Y-%m-%d %H:%M:%S %z")

# Create a logger instance
logger = logging.getLogger("app_logger")
logger.setLevel(logging.DEBUG)  # adjust as needed

# Stream handler (console)
handler = logging.StreamHandler(sys.stdout)
formatter = UTCFormatter("[%(asctime)s] [%(process)d] [%(levelname)s] %(message)s")
handler.setFormatter(formatter)

# Avoid duplicate logs if imported multiple times
if not logger.handlers:
    logger.addHandler(handler)

# Optional: propagate to root logger
logger.propagate = False
