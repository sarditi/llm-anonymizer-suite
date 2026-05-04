"""
FastAPI entrypoint for ext-llm-cipher2.

Exposes the same `/cipher` and `/decipher` endpoints as the original
ext-llm-cipher service so the gateway dispatcher config can be repointed
without code changes. The wire contract (header + JSON body) is unchanged.
"""
from http import HTTPStatus
from typing import Annotated

from fastapi import FastAPI, Header, Request
from fastapi.responses import JSONResponse

from app.redisacl import RedisCipherManager
from app.schemas import ReqRedisAccessPermissions
from app.utils.logger import logger

app = FastAPI(title="Cipher/Decipher API v2")
cipher_manager = RedisCipherManager()


def _resolve_chat_id(
    request_hd: Request, x_user_id: str | None
) -> str | None:
    return x_user_id or request_hd.headers.get("X-User-ID")


@app.get("/healthz")
async def healthz():
    return {"status": "ok", "service": "ext-llm-cipher2"}


@app.post("/decipher")
async def decipher(
    request: ReqRedisAccessPermissions,
    request_hd: Request,
    x_user_id: Annotated[str | None, Header()] = None,
):
    logger.debug("/decipher was called...")
    chat_id = _resolve_chat_id(request_hd, x_user_id)
    if not chat_id:
        return JSONResponse(
            content="X-User-ID is missing or empty",
            status_code=HTTPStatus.BAD_REQUEST,
        )

    success, message = cipher_manager.decipher(request)
    if success:
        return {"message": f"chat_id: {chat_id} - Decipher completed successfully"}
    logger.error(f"chat_id: {chat_id} - {message}")
    return JSONResponse(
        content=f"chat_id: {chat_id} - {message}",
        status_code=HTTPStatus.INTERNAL_SERVER_ERROR,
    )


@app.post("/cipher")
async def cipher(
    request: ReqRedisAccessPermissions,
    request_hd: Request,
    x_user_id: Annotated[str | None, Header()] = None,
):
    logger.debug("/cipher was called...")
    chat_id = _resolve_chat_id(request_hd, x_user_id)
    if not chat_id:
        return JSONResponse(
            content="X-User-ID is missing or empty",
            status_code=HTTPStatus.BAD_REQUEST,
        )

    success, message = cipher_manager.cipher(request, chat_id)
    if success:
        logger.debug(f"Cipher succeeded chat_id: {chat_id} - {message}")
        return {"message": f"chat_id: {chat_id} - Cipher completed successfully"}
    logger.error(f"chat_id: {chat_id} - {message}")
    return JSONResponse(
        content=f"chat_id: {chat_id} - {message}",
        status_code=HTTPStatus.INTERNAL_SERVER_ERROR,
    )
