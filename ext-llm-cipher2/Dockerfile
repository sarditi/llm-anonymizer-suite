FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt gunicorn
ENV PYTHONPATH=/app
COPY app ./app
COPY tests ./tests
COPY pytest.ini ./pytest.ini

# Pre-bake the heavy NER models so the runtime image cold-starts in seconds
# instead of minutes. Flair downloads its NER model lazily on first use; the
# warm-up below forces that download into the image layer.
RUN python -m spacy download en_core_web_trf \
    && python -c "from flair.models import SequenceTagger; SequenceTagger.load('ner')"

EXPOSE 8000

CMD ["gunicorn", "-w", "4", "-k", "uvicorn.workers.UvicornWorker", "app.main:app", "--bind", "0.0.0.0:8000"]
