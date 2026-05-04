"""Tests for the FastAPI surface itself — uses TestClient + an injected
RedisCipherManager so we don't need a real Redis or model downloads."""
from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

import app.main as main_mod


class StubManager:
    """Records every call and returns canned outcomes."""

    def __init__(self) -> None:
        self.cipher_calls = []
        self.decipher_calls = []
        self.cipher_result = (True, "")
        self.decipher_result = (True, "")

    def cipher(self, request, chat_id):
        self.cipher_calls.append((request, chat_id))
        return self.cipher_result

    def decipher(self, request):
        self.decipher_calls.append(request)
        return self.decipher_result


@pytest.fixture()
def stub_and_client(monkeypatch):
    stub = StubManager()
    monkeypatch.setattr(main_mod, "cipher_manager", stub)
    return stub, TestClient(main_mod.app)


PAYLOAD = {
    "user_id": "u",
    "pwd": "p",
    "redis_url": "redis:6379",
    "read_content_meta": "meta",
    "write_content_meta": "meta",
    "keys_allowed_read_access": ["seq_00_x"],
    "keys_allowed_write_access": ["seq_01_x"],
}


def test_healthz_returns_ok(stub_and_client):
    _, client = stub_and_client
    r = client.get("/healthz")
    assert r.status_code == 200
    assert r.json()["status"] == "ok"


def test_cipher_happy_path(stub_and_client):
    stub, client = stub_and_client
    r = client.post(
        "/cipher", json=PAYLOAD, headers={"X-User-ID": "chat-1"}
    )
    assert r.status_code == 200
    assert "Cipher completed successfully" in r.json()["message"]
    assert len(stub.cipher_calls) == 1
    _, chat_id = stub.cipher_calls[0]
    assert chat_id == "chat-1"


def test_cipher_missing_chat_id_returns_400(stub_and_client):
    _, client = stub_and_client
    r = client.post("/cipher", json=PAYLOAD)
    assert r.status_code == 400


def test_cipher_manager_failure_propagates_as_500(stub_and_client):
    stub, client = stub_and_client
    stub.cipher_result = (False, "redis exploded")
    r = client.post(
        "/cipher", json=PAYLOAD, headers={"X-User-ID": "chat-1"}
    )
    assert r.status_code == 500
    assert "redis exploded" in r.json()


def test_decipher_happy_path(stub_and_client):
    stub, client = stub_and_client
    r = client.post(
        "/decipher", json=PAYLOAD, headers={"X-User-ID": "chat-1"}
    )
    assert r.status_code == 200
    assert "Decipher completed successfully" in r.json()["message"]
    assert len(stub.decipher_calls) == 1


def test_decipher_missing_chat_id_returns_400(stub_and_client):
    _, client = stub_and_client
    r = client.post("/decipher", json=PAYLOAD)
    assert r.status_code == 400
