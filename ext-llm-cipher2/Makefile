.PHONY: build run test test-fast test-models lint clean

IMAGE ?= ext-llm-cipher2

build:
	docker build -t $(IMAGE) .

run:
	docker run --rm -p 8000:8000 $(IMAGE)

# Default: skip the heavy NER tests (no spaCy / Flair download required).
test: test-fast

test-fast:
	pytest -m "not requires_models"

# Run *only* the model-backed tests. Requires:
#   pip install -r requirements.txt
#   python -m spacy download en_core_web_trf
test-models:
	pytest -m requires_models

# Run everything.
test-all:
	pytest

clean:
	find . -type d -name __pycache__ -exec rm -rf {} +
	rm -rf .pytest_cache
