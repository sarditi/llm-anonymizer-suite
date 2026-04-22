#!/usr/bin/env python3
import requests
from datetime import datetime, timezone

# URL of your running FastAPI server
BASE_URL = "http://localhost:8211"  # change port if needed

sample_input = {
    "external_chat_id": "12345",
    "internal_chat_id": "reqid",
    "llm": "gpt4",
    "created_at": datetime.now(timezone.utc).isoformat(),
    "content": {
        "input_text": "Hello world",
        "attachments": []
    }
}

def test_decipher_response():
    url = f"{BASE_URL}/decipher_response"
    response = requests.post(url, json=sample_input)
    print(f"POST {url} -> Status: {response.status_code}")
    data = response.json()
    print("Response:", data)

    assert response.status_code == 200
    assert data["external_chat_id"] == "12345"
    assert data["internal_chat_id"] is not None
    assert data["content"]["input_text"] == "Hello world"
    print("test_decipher_response passed!")

def test_cipher_request():
    url = f"{BASE_URL}/cipher_request"
    response = requests.post(url, json=sample_input)
    print(f"POST {url} -> Status: {response.status_code}")
    data = response.json()
    print("Response:", data)

    assert response.status_code == 200
    assert data["external_chat_id"] == "12345"
    assert data["internal_chat_id"] is not None
    print("test_cipher_request passed!")

if __name__ == "__main__":
    test_decipher_response()
    test_cipher_request()


