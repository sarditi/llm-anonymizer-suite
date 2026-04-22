
---

## **Makefile (optional for convenience)**

```makefile
.PHONY: build run test

build:
	docker build -t cipher-api .

run:
	docker run -p 8000:8000 cipher-api

test:
	python3 -m pytest tests

